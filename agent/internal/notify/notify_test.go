package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent/internal/config"
)

const testToken = "1234567890:AAH-secret-bot-token-do-not-leak"

// notifier с адресом, до которого нельзя достучаться: нужен именно отказ
// транспорта — он и приносил токен в текст ошибки.
func deadNotifier() *Notifier {
	return &Notifier{
		telegram: config.Telegram{BotToken: testToken, ChatID: "42"},
		http:     &http.Client{Timeout: 2 * time.Second},
		// Порт 1 на петле: соединение отвергается сразу, тест не ждёт.
		api: "http://127.0.0.1:1",
	}
}

// Токен бота стоит в пути запроса, а `*url.Error` печатает адрес целиком.
// Отсюда он уходил в лог агента на сервере клиента, в текст ошибки `Send` и
// на экран уведомлений — то есть за пределы машины агентства.
func TestTelegramTransportFailureHidesToken(t *testing.T) {
	err := deadNotifier().sendTelegram(context.Background(), "тема", "текст")
	if err == nil {
		t.Fatal("отказ транспорта обязан быть ошибкой")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("токен бота в тексте ошибки: %v", err)
	}
	// Причина отказа обязана остаться: ради неё человек и нажал «проверить».
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("ошибка без причины бесполезна на экране")
	}
}

// Тот же путь, но через `Send`: когда Telegram — единственный настроенный
// канал, склейка отказов уходит наружу целиком.
func TestSendFailureHidesToken(t *testing.T) {
	err := deadNotifier().Send(context.Background(), "тема", "текст")
	if err == nil {
		t.Fatal("единственный отказавший канал обязан быть ошибкой")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("токен бота в тексте ошибки Send: %v", err)
	}
}

// Проверка одного канала не трогает второй: человек проверяет бота, и
// письмо на почту было бы ответом не на тот вопрос. Почта здесь настроена на
// адрес, до которого не достучаться, — тронь её `SendTo`, тест бы упал.
func TestSendToOneChannelLeavesTheOtherAlone(t *testing.T) {
	var hits int
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer bot.Close()

	notifier := deadNotifier()
	notifier.api = bot.URL
	notifier.smtp = config.SMTP{Host: "127.0.0.1", Port: 1, From: "a@example.com", To: []string{"b@example.com"}}
	if !notifier.smtp.Configured() {
		t.Fatal("почта в тесте обязана считаться настроенной")
	}

	channels, err := notifier.SendTo(context.Background(), ChannelTelegram, "тема", "текст")
	if err != nil {
		t.Fatalf("проверка Telegram: %v", err)
	}
	if len(channels) != 1 || channels[0] != ChannelTelegram || hits != 1 {
		t.Fatalf("ушло не туда: каналы %v, обращений к боту %d", channels, hits)
	}

	// А проверка почты говорит про почту — и бота не трогает.
	if _, err := notifier.SendTo(context.Background(), ChannelSMTP, "тема", "текст"); err == nil {
		t.Fatal("недоступная почта обязана быть ошибкой, даже когда Telegram работает")
	}
	if hits != 1 {
		t.Fatalf("проверка почты сходила к боту: обращений %d", hits)
	}
}

// Сервер, на котором уведомления выключены, не пишет сам — ни тревог, ни
// отчёта, — но проверочное по кнопке шлёт: человек проверяет, доходит ли
// отсюда, и молчание в ответ на нажатие было бы неправдой.
func TestMutedServerStaysSilentButStillTests(t *testing.T) {
	var hits int
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer bot.Close()

	notifier := deadNotifier()
	notifier.api = bot.URL
	notifier.muted = true

	if !notifier.Silent() {
		t.Fatal("выключенный сервер обязан считаться молчащим")
	}
	// Каналы при этом настроены — они общие, и экран это показывает.
	if len(notifier.Channels()) != 1 {
		t.Fatalf("каналы выключенного сервера: %v", notifier.Channels())
	}
	if err := notifier.Send(context.Background(), "тревога", "текст"); !errors.Is(err, ErrMuted) {
		t.Fatalf("тревога с выключенного сервера: %v", err)
	}
	if hits != 0 {
		t.Fatalf("выключенный сервер написал в Telegram: обращений %d", hits)
	}

	if _, err := notifier.SendTo(context.Background(), ChannelTelegram, "проверка", "текст"); err != nil {
		t.Fatalf("проверочное с выключенного сервера: %v", err)
	}
	if _, err := notifier.SendTo(context.Background(), "", "проверка", "текст"); err != nil {
		t.Fatalf("проверочное во все каналы с выключенного сервера: %v", err)
	}
	if hits != 2 {
		t.Fatalf("проверочных ушло %d, ждали 2", hits)
	}
}

// Ненастроенный канал и неизвестное имя — отказ, а не молчаливое «ушло».
func TestSendToRefusesWhatItCannotSend(t *testing.T) {
	notifier := deadNotifier()

	if _, err := notifier.SendTo(context.Background(), ChannelSMTP, "тема", "текст"); err == nil {
		t.Fatal("ненастроенная почта обязана быть ошибкой")
	}
	_, err := notifier.SendTo(context.Background(), "sms", "тема", "текст")
	if !errors.Is(err, ErrUnknownChannel) {
		t.Fatalf("неизвестный канал: %v", err)
	}
	// Отказ одного канала несёт ту же защиту токена, что и `Send`.
	_, err = notifier.SendTo(context.Background(), ChannelTelegram, "тема", "текст")
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("отказ Telegram: %v", err)
	}
}

// Негодный адрес ловится ещё на сборке запроса — и там ошибка тоже несёт
// адрес целиком.
func TestTelegramBadEndpointHidesToken(t *testing.T) {
	notifier := deadNotifier()
	notifier.api = "http://127.0.0.1:1/\x7f"

	err := notifier.sendTelegram(context.Background(), "тема", "текст")
	if err == nil {
		t.Fatal("негодный адрес обязан быть ошибкой")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("токен бота в тексте ошибки разбора адреса: %v", err)
	}
}
