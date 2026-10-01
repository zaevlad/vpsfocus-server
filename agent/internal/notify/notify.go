// Package notify отправляет уведомления с сервера клиента.
//
// Именно с сервера, а не с десктопа: смысл агента в том, что он сообщает о
// беде, когда приложение закрыто. Настройки приезжают из `agent.env` с
// правами 600 и на backend разработчика не уходят никогда — это тот же
// секрет, что и пароль от базы.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"agent/internal/config"
)

// Адрес Bot API. Отдельной константой, потому что база уезжает в поле
// [Notifier]: сторожевому тесту нужен адрес, до которого нельзя достучаться.
const telegramAPI = "https://api.telegram.org"

// Notifier рассылает по всем настроенным каналам сразу.
//
// «Сразу» намеренно: если у агентства настроены и почта, и Telegram, значит
// они хотят оба. Выбирать за них, что важнее, мы не станем.
type Notifier struct {
	smtp     config.SMTP
	telegram config.Telegram
	// Адреса каналов «по адресу» (`hooks.go`). Пусто — канал не настроен;
	// негодный адрес сюда не попадает вовсе — см. [New].
	slack   string
	discord string
	webhook string
	http    *http.Client
	// Клиент каналов «по адресу»: со сторожем внутренних адресов и без
	// перенаправлений. Подменяется только в тестах.
	hooks *http.Client
	// Имя сервера для тела вебхука.
	server string
	// База адреса Bot API. Подменяется только в тестах.
	api string
	// Уведомления с этого сервера выключены человеком. Каналы при этом
	// настроены — они общие на все серверы, — а писать агент не должен.
	muted bool
}

func New(cfg config.Config) *Notifier {
	host := config.HostName(cfg.HostRoot)
	return &Notifier{
		smtp:     cfg.SMTP,
		telegram: cfg.Telegram,
		slack:    usableHook(ChannelSlack, cfg.Webhooks.Slack),
		discord:  usableHook(ChannelDiscord, cfg.Webhooks.Discord),
		webhook:  usableHook(ChannelWebhook, cfg.Webhooks.Custom),
		http:     &http.Client{Timeout: 20 * time.Second},
		hooks:    newHookClient(),
		server:   host,
		api:      telegramAPI,
		muted:    cfg.NotifyMuted,
	}
}

// usableHook отдаёт адрес канала, если по нему можно писать, и пустую строку
// иначе.
//
// Негодный адрес — это ненастроенный канал, а не канал, который отказывает
// на каждой тревоге: приложение такой адрес не сохранит, и сюда он попадает
// только из файла, правленного руками. В лог уходит причина без адреса.
func usableHook(channel, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if err := CheckHookURL(channel, raw); err != nil {
		log.Printf("канал %s выключен: %v", channel, err)
		return ""
	}
	return raw
}

// Muted — выключены ли уведомления с этого сервера человеком.
func (n *Notifier) Muted() bool {
	return n.muted
}

// Silent — писать некому или незачем: каналов нет или сервер выключен.
//
// Один вопрос на все круги агента. Спрашивай каждый «есть ли каналы» сам —
// выключенный сервер продолжил бы писать из того круга, где про выключатель
// забыли. Состояние тревог при этом не записывается, как и во время
// обслуживания: включат уведомления — незакрытая беда уйдёт как свежая.
func (n *Notifier) Silent() bool {
	return n.muted || len(n.Channels()) == 0
}

// Channels перечисляет настроенные каналы. Нужен приложению: экран
// уведомлений показывает, что именно сейчас работает.
func (n *Notifier) Channels() []string {
	// Пустой срез, а не nil: он уезжает в JSON, а null там означал бы
	// «неизвестно», тогда как мы точно знаем — каналов нет.
	channels := []string{}
	for _, channel := range n.routes(context.Background(), KindAlert, "", "") {
		if channel.configured {
			channels = append(channels, channel.name)
		}
	}
	return channels
}

// route — один канал: как называется, настроен ли и как в него отправить.
type route struct {
	name       string
	configured bool
	// Что сказать, когда проверяют ненастроенный канал.
	absent string
	send   func() error
}

// routes перечисляет все каналы, которые агент знает. Один список на
// [Notifier.Channels], рассылку и проверку одного канала: новый канал,
// добавленный в одно место из трёх, молчал бы в двух других.
func (n *Notifier) routes(ctx context.Context, kind, subject, body string) []route {
	return []route{
		{ChannelSMTP, n.smtp.Configured(), "почта не настроена",
			func() error { return n.sendMail(subject, body) }},
		{ChannelTelegram, n.telegram.Configured(), "telegram не настроен",
			func() error { return n.sendTelegram(ctx, subject, body) }},
		{ChannelSlack, n.slack != "", "slack не настроен",
			func() error { return n.sendSlack(ctx, subject, body) }},
		{ChannelDiscord, n.discord != "", "discord не настроен",
			func() error { return n.sendDiscord(ctx, subject, body) }},
		{ChannelWebhook, n.webhook != "", "вебхук не настроен",
			func() error { return n.sendWebhook(ctx, kind, subject, body) }},
	}
}

// Send рассылает тревогу. Ошибка одного канала не отменяет второй: письмо
// могло не уйти из-за почтового сервера, а Telegram при этом работает.
func (n *Notifier) Send(ctx context.Context, subject, body string) error {
	return n.deliverAll(ctx, KindAlert, subject, body)
}

// SendReport рассылает еженедельный отчёт — теми же каналами, что и тревогу.
// Отдельным именем только ради поля `kind` в теле вебхука.
func (n *Notifier) SendReport(ctx context.Context, subject, body string) error {
	return n.deliverAll(ctx, KindReport, subject, body)
}

func (n *Notifier) deliverAll(ctx context.Context, kind, subject, body string) error {
	// Вторая линия: круги спрашивают [Notifier.Silent] сами, но письмо с
	// выключенного сервера не должно уйти, даже если новый круг про это
	// забудет.
	if n.muted {
		return ErrMuted
	}

	return n.sendAll(ctx, kind, subject, body)
}

// sendAll шлёт во все настроенные каналы, не спрашивая про выключатель.
func (n *Notifier) sendAll(ctx context.Context, kind, subject, body string) error {
	var failures []string
	configured := 0

	for _, channel := range n.routes(ctx, kind, subject, body) {
		if !channel.configured {
			continue
		}
		configured++
		if err := channel.send(); err != nil {
			failures = append(failures, channel.name+": "+err.Error())
		}
	}

	if configured == 0 {
		return fmt.Errorf("не настроен ни один канал уведомлений")
	}
	if len(failures) == configured {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

// Имена каналов — те же, что отдаёт [Notifier.Channels] и что приходит в
// `POST /notify/test?channel=`. Каналы «по адресу» названы в `hooks.go`.
const (
	ChannelSMTP     = "smtp"
	ChannelTelegram = "telegram"
)

// ErrUnknownChannel — канала с таким именем нет. Отдельной ошибкой, потому
// что это отказ запроса, а не отказ доставки.
var ErrUnknownChannel = errors.New("неизвестный канал уведомлений")

// ErrMuted — уведомления с этого сервера выключены человеком.
var ErrMuted = errors.New("уведомления с этого сервера выключены")

// SendTo отправляет сообщение в один канал и отвечает, куда оно ушло.
//
// Нужен проверочному сообщению: человек настраивает бота в своём блоке
// экрана и проверяет именно его — письмо на почту при этом было бы ответом
// не на тот вопрос. Настоящие тревоги идут через [Notifier.Send], во все
// каналы. Пустое имя — все настроенные каналы, как у старых версий.
//
// В отличие от [Notifier.Send], отказ одного канала здесь не прячется за
// удачей второго: проверяли его, и сказать надо про него.
//
// Выключенный сервер проверочное всё равно шлёт: человек нажал кнопку сам и
// проверяет, доходит ли отсюда, — а молчат тревоги и отчёт.
func (n *Notifier) SendTo(ctx context.Context, channel, subject, body string) ([]string, error) {
	if channel == "" {
		return n.Channels(), n.sendAll(ctx, KindTest, subject, body)
	}

	for _, known := range n.routes(ctx, KindTest, subject, body) {
		if known.name != channel {
			continue
		}
		if !known.configured {
			return nil, errors.New(known.absent)
		}
		if err := known.send(); err != nil {
			return nil, fmt.Errorf("%s: %w", channel, err)
		}
		return []string{channel}, nil
	}
	return nil, ErrUnknownChannel
}

// sendMail отправляет письмо.
//
// STARTTLS обязателен везде, кроме порта 465, где TLS начинается сразу.
// Открытым текстом пароль не уходит: почтовый сервер клиента может стоять и
// в чужой сети.
func (n *Notifier) sendMail(subject, body string) error {
	address := net.JoinHostPort(n.smtp.Host, fmt.Sprint(n.smtp.Port))
	message := buildMessage(n.smtp.From, n.smtp.To, subject, body)

	if n.smtp.Port == 465 {
		conn, err := tls.Dial("tcp", address, &tls.Config{ServerName: n.smtp.Host})
		if err != nil {
			return err
		}
		defer conn.Close()

		client, err := smtp.NewClient(conn, n.smtp.Host)
		if err != nil {
			return err
		}
		defer client.Quit()
		return n.deliver(client, message)
	}

	client, err := smtp.Dial(address)
	if err != nil {
		return err
	}
	defer client.Quit()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: n.smtp.Host}); err != nil {
			return err
		}
	}

	return n.deliver(client, message)
}

func (n *Notifier) deliver(client *smtp.Client, message []byte) error {
	if n.smtp.User != "" {
		auth := smtp.PlainAuth("", n.smtp.User, n.smtp.Password, n.smtp.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}

	if err := client.Mail(n.smtp.From); err != nil {
		return err
	}
	for _, to := range n.smtp.To {
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}

	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		return err
	}
	return writer.Close()
}

// buildMessage собирает письмо.
//
// Тема кодируется по RFC 2047: она приходит на русском, а заголовки писем
// — семибитные, и без кодирования почтовые клиенты покажут мусор.
func buildMessage(from string, to []string, subject, body string) []byte {
	var message bytes.Buffer

	fmt.Fprintf(&message, "From: %s\r\n", from)
	fmt.Fprintf(&message, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&message, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&message, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	message.WriteString("\r\n")
	message.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))

	return message.Bytes()
}

// sendTelegram отправляет сообщение ботом.
//
// Ни одна ошибка отсюда не выходит как есть: токен стоит в пути запроса, а
// `*url.Error` печатает адрес целиком — см. [hideToken].
func (n *Notifier) sendTelegram(ctx context.Context, subject, body string) error {
	base := n.api
	if base == "" {
		base = telegramAPI
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimSuffix(base, "/"), n.telegram.BotToken)

	form := url.Values{}
	form.Set("chat_id", n.telegram.ChatID)
	form.Set("text", subject+"\n\n"+body)
	// Без разметки: в текст попадают домены и пути, а любой символ разметки
	// в них превратил бы сообщение в ошибку разбора на стороне Telegram.
	form.Set("disable_web_page_preview", "true")

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return hideToken(err, n.telegram.BotToken)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := n.http.Do(request)
	if err != nil {
		return hideToken(err, n.telegram.BotToken)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram ответил %s", response.Status)
	}
	return nil
}

// hideToken убирает из ошибки адрес запроса, в котором стоит токен бота.
//
// Заголовком токен не передать — так устроен Bot API, — поэтому он всегда
// лежит в пути: `/bot<TOKEN>/sendMessage`. Отказ транспорта Go возвращает
// как `*url.Error`, а тот печатает адрес целиком, и дальше эта строка идёт
// тремя путями сразу: в `log.Printf` (то есть в `docker logs` **на сервере
// клиента**), в склейку `Send`, когда отказали все каналы, и телом ответа
// `SendTest` — на экран уведомлений.
//
// Токен один на всё агентство: он приезжает на каждый сервер из общих
// настроек. Это ровно та граница, ради которой `agent.env` кладётся с
// правами 600, а секреты не уходят на backend разработчика никогда, — только
// здесь она проходит не «к нам», а «наружу с сервера клиента».
//
// Снимается обёртка, а не подставляется своя строка: причина отказа («no
// such host», «connection refused») — это то, ради чего человек и нажал
// «проверить». Замена самого токена — вторая линия на случай, если он
// окажется в ошибке ещё каким-нибудь путём.
func hideToken(err error, token string) error {
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		err = wrapped.Err
	}
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, "<токен>")
	}
	return errors.New(message)
}
