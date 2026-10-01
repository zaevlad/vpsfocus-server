package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent/internal/config"
)

const (
	testSlackURL   = "https://hooks.slack.com/services/T000/B000/secret-slack-path"
	testDiscordURL = "https://discord.com/api/webhooks/123456/secret-discord-token"
	testWebhookURL = "https://hooks.example.com/notify/secret-webhook-path?auth=secret-query"
)

// transport, который никуда не ходит: запоминает запросы и отвечает кодом.
type recorder struct {
	status   int
	location string
	requests []*http.Request
	bodies   []string
}

func (r *recorder) RoundTrip(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	r.requests = append(r.requests, request)
	r.bodies = append(r.bodies, string(body))

	header := http.Header{}
	if r.location != "" {
		header.Set("Location", r.location)
	}
	return &http.Response{
		StatusCode: r.status,
		Status:     http.StatusText(r.status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("тело чужого сервера")),
		Request:    request,
	}, nil
}

func hookNotifier(transport http.RoundTripper) *Notifier {
	client := newHookClient()
	client.Transport = transport
	return &Notifier{
		slack:   testSlackURL,
		discord: testDiscordURL,
		webhook: testWebhookURL,
		hooks:   client,
		server:  "vps-1",
	}
}

// Три канала «по адресу» получают каждый своё тело: Slack — `text`, Discord —
// `content` без упоминаний, вебхук — поля для чужого скрипта.
func TestHooksSendTheirOwnPayloads(t *testing.T) {
	transport := &recorder{status: http.StatusOK}
	notifier := hookNotifier(transport)

	if err := notifier.Send(context.Background(), "Диск заполнен", "Занято 91%"); err != nil {
		t.Fatalf("рассылка: %v", err)
	}
	if len(transport.requests) != 3 {
		t.Fatalf("запросов %d, ждали три", len(transport.requests))
	}

	byHost := map[string]map[string]any{}
	for index, request := range transport.requests {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("запрос к %s: %s %q", request.URL.Host, request.Method, request.Header.Get("Content-Type"))
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(transport.bodies[index]), &payload); err != nil {
			t.Fatalf("тело запроса к %s: %v", request.URL.Host, err)
		}
		byHost[request.URL.Host] = payload
	}

	if text := byHost["hooks.slack.com"]["text"]; text != "Диск заполнен\n\nЗанято 91%" {
		t.Fatalf("Slack: %v", text)
	}
	discord := byHost["discord.com"]
	if discord["content"] != "Диск заполнен\n\nЗанято 91%" {
		t.Fatalf("Discord: %v", discord["content"])
	}
	if _, ok := discord["allowed_mentions"]; !ok {
		t.Fatal("Discord: упоминания не выключены")
	}
	hook := byHost["hooks.example.com"]
	for field, want := range map[string]string{
		"source": "vpsfocus", "kind": KindAlert, "server": "vps-1",
		"subject": "Диск заполнен", "body": "Занято 91%",
	} {
		if hook[field] != want {
			t.Fatalf("вебхук: поле %s = %v, ждали %q", field, hook[field], want)
		}
	}
	if sent, _ := hook["sentAt"].(string); sent == "" {
		t.Fatal("вебхук: нет времени отправки")
	}
}

// Вид сообщения в теле вебхука: тревога, отчёт и проверка по кнопке — три
// разных значения, иначе чужой скрипт будил бы человека недельным отчётом.
func TestWebhookTellsAlertReportAndTestApart(t *testing.T) {
	transport := &recorder{status: http.StatusNoContent}
	notifier := hookNotifier(transport)
	notifier.slack, notifier.discord = "", ""

	_ = notifier.Send(context.Background(), "т", "т")
	_ = notifier.SendReport(context.Background(), "т", "т")
	if _, err := notifier.SendTo(context.Background(), ChannelWebhook, "т", "т"); err != nil {
		t.Fatalf("проверка вебхука: %v", err)
	}

	var kinds []string
	for _, body := range transport.bodies {
		var message hookMessage
		if err := json.Unmarshal([]byte(body), &message); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, message.Kind)
	}
	if strings.Join(kinds, ",") != "alert,report,test" {
		t.Fatalf("виды сообщений: %v", kinds)
	}
}

// Новые каналы видны там же, где старые: в списке каналов и в проверке
// одного канала. Проверка одного не трогает остальные.
func TestHookChannelsAreListedAndTestedOneByOne(t *testing.T) {
	transport := &recorder{status: http.StatusOK}
	notifier := hookNotifier(transport)

	if got := strings.Join(notifier.Channels(), ","); got != "slack,discord,webhook" {
		t.Fatalf("каналы: %s", got)
	}

	channels, err := notifier.SendTo(context.Background(), ChannelDiscord, "тема", "текст")
	if err != nil || len(channels) != 1 || channels[0] != ChannelDiscord {
		t.Fatalf("проверка Discord: %v, %v", channels, err)
	}
	if len(transport.requests) != 1 || transport.requests[0].URL.Host != "discord.com" {
		t.Fatalf("проверка Discord ушла не туда: %d запросов", len(transport.requests))
	}

	notifier.slack = ""
	if _, err := notifier.SendTo(context.Background(), ChannelSlack, "тема", "текст"); err == nil {
		t.Fatal("ненастроенный Slack обязан быть ошибкой")
	}
}

// Отказ и перенаправление — ошибка, и в её тексте нет ни адреса, ни тела
// ответа чужого сервера: адрес — секрет, а текст уходит в лог на сервере
// клиента и на экран.
func TestHookFailureCarriesNeitherAddressNorBody(t *testing.T) {
	for _, transport := range []*recorder{
		{status: http.StatusNotFound},
		{status: http.StatusFound, location: "http://127.0.0.1/admin"},
	} {
		notifier := hookNotifier(transport)
		for _, channel := range []string{ChannelSlack, ChannelDiscord, ChannelWebhook} {
			_, err := notifier.SendTo(context.Background(), channel, "тема", "текст")
			if err == nil {
				t.Fatalf("%s: ответ %d обязан быть ошибкой", channel, transport.status)
			}
			for _, secret := range []string{"secret-", "тело чужого сервера", "127.0.0.1/admin"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("%s: в ошибке %q: %v", channel, secret, err)
				}
			}
		}
		// Перенаправление не выполняется: запрос к каждому каналу один.
		if len(transport.requests) != 3 {
			t.Fatalf("ответ %d: запросов %d, ждали три", transport.status, len(transport.requests))
		}
	}
}

// Отказ транспорта приносит `*url.Error` с адресом целиком — ровно та беда,
// от которой у Telegram стоит `hideToken`.
func TestHookTransportFailureHidesAddress(t *testing.T) {
	notifier := hookNotifier(failing{errors.New("connection refused")})

	err := notifier.Send(context.Background(), "тема", "текст")
	if err == nil {
		t.Fatal("отказ всех каналов обязан быть ошибкой")
	}
	if strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), "hooks.example.com/notify") {
		t.Fatalf("адрес канала в тексте ошибки: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("причина отказа потеряна: %v", err)
	}
}

type failing struct{ err error }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// Настоящий клиент каналов не соединяется с самим сервером: настройки общие
// на команду, и вебхук не должен становиться ходом во внутреннюю сеть
// сервера клиента. Сервер теста стоит на петле — запрос до него дойти не
// вправе.
func TestHookClientRefusesInternalAddresses(t *testing.T) {
	var hits int
	local := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	notifier := &Notifier{webhook: local.URL, hooks: newHookClient(), server: "vps-1"}
	_, err := notifier.SendTo(context.Background(), ChannelWebhook, "тема", "текст")
	if !errors.Is(err, ErrInternalAddress) {
		t.Fatalf("соединение с петлёй: %v", err)
	}
	if hits != 0 {
		t.Fatalf("запрос дошёл до сервера на петле: %d", hits)
	}
}

func TestPublicIP(t *testing.T) {
	internal := []string{
		"127.0.0.1", "10.0.0.5", "172.16.3.4", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "198.18.0.1", "224.0.0.1", "255.255.255.255",
		"::1", "fe80::1", "fc00::1", "fd12:3456::1", "::", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
	}
	for _, address := range internal {
		if publicIP(net.ParseIP(address)) {
			t.Errorf("%s принят за адрес в интернете", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "1.1.1.1", "162.159.135.232", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(address)) {
			t.Errorf("%s не принят за адрес в интернете", address)
		}
	}
	if publicIP(nil) {
		t.Error("неразобранный адрес принят за адрес в интернете")
	}
}

func TestCheckHookURL(t *testing.T) {
	good := map[string][]string{
		ChannelSlack:   {testSlackURL, "https://hooks.slack.com/triggers/T0/1/abc"},
		ChannelDiscord: {testDiscordURL, "https://discordapp.com/api/webhooks/1/abc", "https://DISCORD.com/api/webhooks/1/abc"},
		ChannelWebhook: {testWebhookURL, "https://ntfy.sh/my-topic", "https://8.8.8.8/hook"},
	}
	for channel, addresses := range good {
		for _, address := range addresses {
			if err := CheckHookURL(channel, address); err != nil {
				t.Errorf("%s: %s отвергнут: %v", channel, address, err)
			}
		}
	}

	bad := map[string][]string{
		ChannelSlack: {
			"https://hooks.slack.com.evil.example/services/x",
			"https://example.com/services/x",
			"http://hooks.slack.com/services/x",
		},
		ChannelDiscord: {
			"https://discord.com/channels/1/2",
			"https://discord.com.evil.example/api/webhooks/1/abc",
		},
		ChannelWebhook: {
			"http://hooks.example.com/x",
			"https://localhost/x",
			"https://app.localhost/x",
			"https://127.0.0.1/x",
			"https://10.0.0.8:8443/x",
			"https://[::1]/x",
			"https://169.254.169.254/latest/meta-data",
			"https://hooks.example.com/x y",
			"https://hooks.example.com/$HOME",
			"https://hooks.example.com/x#frag",
			"https://hooks.example.com/x\nWEBHOOK_URL=other",
			"https://hooks.example.com/'x'",
			"hooks.example.com/x",
			"https:///x",
			"https://hooks.example.com/" + strings.Repeat("a", HookURLMaxLen),
		},
		"sms": {testWebhookURL},
	}
	for channel, addresses := range bad {
		for _, address := range addresses {
			err := CheckHookURL(channel, address)
			if err == nil {
				t.Errorf("%s: %q принят", channel, address)
				continue
			}
			// Причина уходит в лог агента: адреса в ней быть не должно.
			if len(address) > 12 && strings.Contains(err.Error(), address) {
				t.Errorf("%s: адрес в тексте отказа: %v", channel, err)
			}
		}
	}
}

// Негодный адрес из `agent.env` — это выключенный канал, а не канал, который
// отказывает на каждой тревоге.
func TestNewTurnsABadAddressIntoAnAbsentChannel(t *testing.T) {
	notifier := New(config.Config{Webhooks: config.Webhooks{
		Slack:   "https://example.com/not-slack",
		Discord: " " + testDiscordURL + " ",
		Custom:  "http://10.0.0.1/hook",
	}})

	if got := strings.Join(notifier.Channels(), ","); got != ChannelDiscord {
		t.Fatalf("каналы: %q, ждали только discord", got)
	}
}

// Discord отказывает сообщению длиннее 2000 знаков — отчёт большого сервера
// обрезается, а не теряется. Режется по знакам: половина буквы ломает JSON.
func TestDiscordTextIsClippedByRunes(t *testing.T) {
	transport := &recorder{status: http.StatusNoContent}
	notifier := hookNotifier(transport)

	long := strings.Repeat("я", 5000)
	if _, err := notifier.SendTo(context.Background(), ChannelDiscord, "тема", long); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(transport.bodies[0]), &payload); err != nil {
		t.Fatalf("тело после обрезки не разбирается: %v", err)
	}
	if count := len([]rune(payload.Content)); count > 2000 || count < discordTextLimit {
		t.Fatalf("знаков в сообщении: %d", count)
	}
}
