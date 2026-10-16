package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	netmail "net/mail"
	"strings"
	"testing"

	"agent/internal/mailhtml"
)

var testMail = Mail{
	Lang:   "ru",
	Source: "сервер web-1",
	Footer: "Это письмо прислал мониторинг vpsFocus с сервера web-1.",
}

// Подвал «кто прислал и где выключить» — у тревоги и проверочного письма,
// но не у отчёта: у отчёта он свой (трек AC, Р12, Р20, Р21).
func TestFooterGoesToAlertsAndTestButNotToTheReport(t *testing.T) {
	n := &Notifier{mail: testMail}
	for kind, want := range map[string]bool{KindAlert: true, KindTest: true, KindReport: false} {
		letter := n.letter(kind, Look{}, "тема", "текст")
		if got := strings.Contains(letter.Text, "\n\n—\n"+testMail.Footer); got != want {
			t.Errorf("%s: подвал %v, ждали %v:\n%s", kind, got, want, letter.Text)
		}
		if !strings.HasPrefix(letter.Text, "текст") || letter.Lang != "ru" || letter.Source != testMail.Source {
			t.Errorf("%s: письмо %+v", kind, letter)
		}
	}
}

// Slack, Discord и вебхук получают тот же текст, что и до трека AC (Р11):
// ни подвала, ни слова полосы.
func TestHooksGetThePlainTextWithoutMailDecorations(t *testing.T) {
	transport := &recorder{status: http.StatusOK}
	notifier := hookNotifier(transport)
	notifier.SetMail(testMail)

	look := Look{Tone: mailhtml.Problem, Band: "Проблема"}
	if err := notifier.Send(context.Background(), look, "Диск заполнен", "Занято 91%"); err != nil {
		t.Fatalf("рассылка: %v", err)
	}
	for index, body := range transport.bodies {
		if strings.Contains(body, "vpsFocus с сервера") || strings.Contains(body, "Проблема") {
			t.Errorf("запрос к %s получил оформление письма: %s", transport.requests[index].URL.Host, body)
		}
		if !strings.Contains(body, "Занято 91%") {
			t.Errorf("запрос к %s без текста: %s", transport.requests[index].URL.Host, body)
		}
	}
}

// Письмо — две части в base64, текст ровно тот, что написан, HTML с полосой,
// источником и подвалом, свой Message-ID (Р2, Р15, Р16).
func TestBuildMessageHasTextAndHTML(t *testing.T) {
	n := &Notifier{mail: testMail}
	letter := n.letter(KindAlert, Look{Tone: mailhtml.Warning, Band: "Предупреждение"},
		"Диск сервера занят на 91%", "Занято 91% дискового пространства.\n\nКогда место кончится, встанет сайт.")
	raw := buildMessage("vpsFocus <bot@example.com>", []string{"ops@example.com"}, letter)

	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("строка длиннее 998 байт: %d", len(line))
		}
	}
	message, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("письмо не разбирается: %v", err)
	}
	if id := message.Header.Get("Message-Id"); !strings.HasSuffix(id, "@example.com>") {
		t.Errorf("Message-ID: %q", id)
	}
	media, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || media != "multipart/alternative" {
		t.Fatalf("тип письма %q: %v", media, err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])

	text, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if got := decodePart(t, text); got != letter.Text {
		t.Errorf("текстовая часть:\n%s", got)
	}

	related, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ = mime.ParseMediaType(related.Header.Get("Content-Type"))
	html, err := multipart.NewReader(related, params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	page := decodePart(t, html)
	for _, want := range []string{"band-warning", "Предупреждение", "· сервер web-1", testMail.Footer} {
		if !strings.Contains(page, want) {
			t.Errorf("в HTML нет %q", want)
		}
	}
	if lost := mailhtml.Missing(letter.Text, page); len(lost) > 0 {
		t.Errorf("HTML потерял слова %q", lost)
	}
}

func decodePart(t *testing.T, part *multipart.Part) string {
	t.Helper()
	encoded, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(encoded), "\r\n", ""))
	if err != nil {
		t.Fatalf("часть не base64: %v", err)
	}
	return string(decoded)
}
