package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Каналы «по адресу»: Slack, Discord и вебхук (агент 0.21.0).
//
// Все три устроены одинаково: сервис выдаёт человеку секретный адрес
// («входящий вебхук»), и сообщение — это POST с JSON на него. Бота заводить
// не надо, ключей, кроме самого адреса, нет. Отсюда и главное правило этого
// файла: **адрес — секрет** того же разряда, что токен бота Telegram. Он
// приезжает из `agent.env` (права 600), в лог и в текст ошибки не попадает
// никогда — см. [hideHook].
const (
	ChannelSlack   = "slack"
	ChannelDiscord = "discord"
	ChannelWebhook = "webhook"
)

// Хосты, на которые вправе указывать адрес Slack и Discord. Списком, а не
// «любой https»: блок на экране называется «Slack», и сообщение из него
// обязано уходить в Slack, а не туда, куда показала опечатка.
//
// Те же имена зашиты в приложении (`notifications.rs`), и тест
// `hook_rules_match_the_agent` сверяет их с этим файлом.
var (
	slackHosts   = []string{"hooks.slack.com"}
	discordHosts = []string{"discord.com", "discordapp.com"}
)

// Путь входящего вебхука Discord. У Slack он бывает разным (`/services/`,
// `/triggers/`), и сверять его незачем: хост принадлежит только вебхукам.
const discordPathPrefix = "/api/webhooks/"

// HookURLForbidden — знаки, которых в адресе быть не может.
//
// Адрес лежит строкой в `agent.env`, а файл читает docker compose: пробел
// обрывает значение, кавычки он снимает, `$` подставляет, `#` после пробела
// считает комментарием. В настоящем адресе ни одного из них нет — всё это
// кодируется процентами. Управляющие знаки отбираются сверх списка.
const HookURLForbidden = " \"'`$\\#"

// HookURLMaxLen — потолок длины адреса.
const HookURLMaxLen = 2048

// ErrInternalAddress — адрес ведёт на сам сервер или во внутреннюю сеть.
//
// Настройки уведомлений общие на команду, а агент стоит на чужом проде:
// без этого отказа один человек из команды заставил бы его стучаться в
// базу, панель хостера или служебный адрес облака (169.254.169.254) сервера
// клиента. Цена — свой ntfy на том же сервере подключается только по
// внешнему имени.
var ErrInternalAddress = errors.New("адрес ведёт на сам сервер или во внутреннюю сеть")

// CheckHookURL говорит, годится ли адрес для канала. Текст ошибки адреса не
// содержит: он уходит в лог агента на сервере клиента.
func CheckHookURL(channel, raw string) error {
	if len(raw) > HookURLMaxLen {
		return errors.New("адрес слишком длинный")
	}
	if strings.ContainsAny(raw, HookURLForbidden) || strings.ContainsFunc(raw, isControl) {
		return errors.New("в адресе недопустимые знаки")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("адрес не разбирается")
	}
	// Только https: по http адрес-секрет и текст тревоги с доменами клиента
	// ехали бы открытым текстом.
	if parsed.Scheme != "https" {
		return errors.New("адрес обязан начинаться с https://")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return errors.New("в адресе нет имени сервера")
	}
	// Имя проверяется при соединении — по адресу, в который оно
	// разрешилось. Здесь отсекается только то, что видно без сети.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ErrInternalAddress
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return ErrInternalAddress
	}

	switch channel {
	case ChannelSlack:
		if !contains(slackHosts, host) {
			return errors.New("это не адрес входящего вебхука Slack")
		}
	case ChannelDiscord:
		if !contains(discordHosts, host) || !strings.HasPrefix(parsed.Path, discordPathPrefix) {
			return errors.New("это не адрес вебхука Discord")
		}
	case ChannelWebhook:
	default:
		return ErrUnknownChannel
	}
	return nil
}

func isControl(symbol rune) bool {
	return symbol < 0x20 || symbol == 0x7f
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Сети, которые не интернет, но и не «частные» по мнению стандартной
// библиотеки: адреса операторского NAT, служебные и тестовые диапазоны.
var reservedNets = mustNets(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"240.0.0.0/4",
	"64:ff9b::/96",
)

func mustNets(blocks ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(blocks))
	for _, block := range blocks {
		_, parsed, err := net.ParseCIDR(block)
		if err != nil {
			panic(err)
		}
		nets = append(nets, parsed)
	}
	return nets
}

// publicIP — адрес в интернете, а не петля, частная сеть или служебный
// диапазон. Неразобранный адрес публичным не считается.
func publicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, block := range reservedNets {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// guardDial отказывает соединению с непубличным адресом.
//
// Стоит на самом соединении, а не на разборе адреса: имя `hook.example.com`
// вправе разрешиться в 127.0.0.1, и сменить ответ DNS между проверкой и
// соединением ничего не стоит. Сюда приходит уже адрес, в который имя
// разрешилось.
func guardDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if !publicIP(net.ParseIP(host)) {
		return ErrInternalAddress
	}
	return nil
}

// newHookClient — клиент каналов «по адресу».
//
// Отдельный от клиента Telegram по трём причинам: соединение проходит
// [guardDial]; перенаправления не выполняются — иначе публичный адрес увёл
// бы запрос во внутреннюю сеть вторым шагом, а настоящий вебхук никуда не
// перенаправляет; прокси из окружения не читается — с ним сторож видел бы
// адрес прокси, а не адресата.
func newHookClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: guardDial}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Вид сообщения — поле `kind` в теле вебхука: чужому скрипту надо отличать
// тревогу от еженедельного отчёта и от проверки по кнопке.
const (
	KindAlert  = "alert"
	KindReport = "report"
	KindTest   = "test"
)

// Потолки длины текста. Discord отказывает сообщению длиннее 2000 знаков,
// Slack — длиннее 40 000; еженедельный отчёт большого сервера первый потолок
// проходит легко. Обрезанное сообщение лучше недоставленного.
const (
	discordTextLimit = 1900
	slackTextLimit   = 30000
)

// clip обрезает текст по знакам, а не по байтам: половина русской буквы —
// это отказ разбора JSON на той стороне.
func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// hookMessage — тело вебхука. Поля названы так, чтобы их разобрал чужой
// скрипт без документации; уровня и сайта отдельными полями нет — агент
// получает от кругов только заголовок и текст, и придумывать поля разбором
// текста значило бы обещать больше, чем известно.
type hookMessage struct {
	Source  string `json:"source"`
	Kind    string `json:"kind"`
	Server  string `json:"server"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	SentAt  string `json:"sentAt"`
}

func (n *Notifier) sendSlack(ctx context.Context, subject, body string) error {
	return n.postHook(ctx, n.slack, map[string]string{
		"text": clip(subject+"\n\n"+body, slackTextLimit),
	})
}

func (n *Notifier) sendDiscord(ctx context.Context, subject, body string) error {
	return n.postHook(ctx, n.discord, map[string]any{
		"content": clip(subject+"\n\n"+body, discordTextLimit),
		// В тексте стоят домены и пути с сайта клиента: строка вида
		// `@everyone` в адресе страницы не должна будить весь сервер Discord.
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
}

func (n *Notifier) sendWebhook(ctx context.Context, kind, subject, body string) error {
	return n.postHook(ctx, n.webhook, hookMessage{
		Source:  "vpsfocus",
		Kind:    kind,
		Server:  n.server,
		Subject: subject,
		Body:    body,
		SentAt:  time.Now().UTC().Format(time.RFC3339),
	})
}

// postHook отправляет JSON на адрес канала. От ответа берётся только код:
// тело чужого сервера агенту незачем, а на экран оно попасть не должно.
func (n *Notifier) postHook(ctx context.Context, address string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(encoded))
	if err != nil {
		return hideHook(err, address)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "vpsfocus-agent")

	client := n.hooks
	if client == nil {
		client = newHookClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return hideHook(err, address)
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return fmt.Errorf("адрес перенаправляет (%s) — перенаправления не выполняются", response.Status)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("адрес ответил %s", response.Status)
	}
	return nil
}

// hideHook убирает из ошибки адрес канала — та же защита, что [hideToken] у
// Telegram, и по той же причине: `*url.Error` печатает адрес целиком, а
// секрет здесь — весь его путь.
func hideHook(err error, address string) error {
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		// Отказ сторожа остаётся узнаваемым: по нему экран говорит «адрес
		// ведёт во внутреннюю сеть», а не «не удалось соединиться».
		if errors.Is(wrapped.Err, ErrInternalAddress) {
			return ErrInternalAddress
		}
		err = wrapped.Err
	}
	message := err.Error()
	if address != "" {
		message = strings.ReplaceAll(message, address, "<адрес>")
		if parsed, parseErr := url.Parse(address); parseErr == nil {
			if secret := parsed.RequestURI(); len(secret) > 1 {
				message = strings.ReplaceAll(message, secret, "<адрес>")
			}
		}
	}
	return errors.New(message)
}
