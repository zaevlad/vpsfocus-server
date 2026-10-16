package mailhtml

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"regexp"
	"strings"
	"testing"
)

// Письмо о сбое собрано всеми приёмами сразу: приветствие, «Метка:»,
// заголовок со списком, многострочная подробность с пустой строкой внутри,
// подвал.
const siteDown = `Здравствуйте!

Сайт shop.test перестал открываться 9 октября в 14:44 UTC. Мы проверили его три раза подряд.

Причина: сервер не ответил за 10 секунд.

Что сделать:
1. Откройте shop.test в браузере.
2. Откройте приложение vpsFocus → «Доступность».

Техническая подробность (для того, кто будет чинить):
line one <script>alert(1)</script>

line three

—
Вы получили это письмо, потому что включили внешнюю проверку shop.test в vpsFocus.`

const siteDownDetails = "line one <script>alert(1)</script>\n\nline three"

func render(t *testing.T, letter Letter) string {
	t.Helper()
	page, err := Render(letter)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return page
}

func TestParseFindsEveryKindOfBlock(t *testing.T) {
	got := parse(siteDown, siteDownDetails, nil)

	kinds := []blockKind{}
	for _, part := range got.Body {
		kinds = append(kinds, part.Kind)
	}
	want := []blockKind{kindParagraph, kindParagraph, kindParagraph, kindList, kindDetails}
	if strings.Join(asStrings(kinds), ",") != strings.Join(asStrings(want), ",") {
		t.Fatalf("блоки: %v, ждали %v", kinds, want)
	}
	if got.Body[2].Lead != "Причина" {
		t.Errorf("метка абзаца: %q", got.Body[2].Lead)
	}
	if list := got.Body[3]; list.Heading != "Что сделать:" || len(list.Items) != 2 || list.Items[1].Marker != "2." {
		t.Errorf("список: %+v", list)
	}
	if details := got.Body[4]; details.Code != siteDownDetails || !strings.HasPrefix(details.Label, "Техническая") {
		t.Errorf("подробность: %+v", details)
	}
	if len(got.Footer) != 1 || !strings.HasPrefix(got.Footer[0], "Вы получили") {
		t.Errorf("подвал: %q", got.Footer)
	}
}

func asStrings(kinds []blockKind) []string {
	out := make([]string, len(kinds))
	for i, kind := range kinds {
		out[i] = string(kind)
	}
	return out
}

// Подробность берётся только целыми строками: строка, которая с неё лишь
// начинается, плашкой не становится.
func TestDetailsMatchWholeLinesOnly(t *testing.T) {
	text := "Что она сообщила:\nexit status 1\n\nexit status 1 — так задача сообщает об ошибке."
	got := parse(text, "exit status 1", nil)
	if len(got.Body) != 2 || got.Body[0].Kind != kindDetails || got.Body[1].Kind != kindParagraph {
		t.Fatalf("блоки: %+v", got.Body)
	}
	if got.Body[0].Label != "Что она сообщила:" {
		t.Errorf("подпись подробности: %q", got.Body[0].Label)
	}
	if lastWhole("a\nexit status 12", "exit status 1") != -1 {
		t.Error("начало строки принято за подробность")
	}
}

// Отчёт агента: строка проекта, под ней пункты с отступом и метками (Р20).
func TestParseReadsTheWeeklyReport(t *testing.T) {
	report := "Еженедельный отчёт о сервере web-1.\n\nshop.test\n  [!] диск занят на 96%\n  [·] память занята на 91%\n\nblog.test\n  всё в порядке"
	got := parse(report, "", nil)
	if len(got.Body) != 3 {
		t.Fatalf("блоков %d: %+v", len(got.Body), got.Body)
	}
	shop := got.Body[1]
	if shop.Kind != kindList || shop.Heading != "shop.test" || len(shop.Items) != 2 {
		t.Fatalf("проект: %+v", shop)
	}
	if shop.Items[0].Mark != "[!]" || shop.Items[0].MarkTone != Problem || shop.Items[1].MarkTone != Warning {
		t.Errorf("метки: %+v", shop.Items)
	}
	if blog := got.Body[2]; blog.Items[0].Mark != "" || blog.Items[0].Text != "всё в порядке" {
		t.Errorf("проект без находок: %+v", blog)
	}
}

func TestParsePairsAndIndentedBullets(t *testing.T) {
	text := "Тип: TXT\nИмя: _vpsfocus.shop.test\nЗначение: abc\n\n  • a.test — не отвечает\n  • b.test — не отвечает"
	got := parse(text, "", nil)
	if got.Body[0].Kind != kindPairs || len(got.Body[0].Pairs) != 3 || got.Body[0].Pairs[1].Key != "Имя" {
		t.Errorf("пары: %+v", got.Body[0])
	}
	if got.Body[1].Kind != kindList || got.Body[1].Items[0].Marker != "•" || got.Body[1].Heading != "" {
		t.Errorf("маркированный список: %+v", got.Body[1])
	}
}

// Метка — только короткая и только с пробелом после двоеточия: ни длинная
// фраза, ни адрес меткой не становятся.
func TestLabelIsNarrow(t *testing.T) {
	for _, line := range []string{
		"Откройте ссылку, чтобы подтвердить адрес почты:",
		"https://vpsfocus.xyz/verify?token=abc",
		"Проверка сертификата не прошла: x509: certificate has expired",
		"  Причина: с отступом",
	} {
		if key, _, ok := label(line); ok {
			t.Errorf("%q разобрано как метка %q", line, key)
		}
	}
	if key, value, ok := label("What this means: the schedule works"); !ok || key != "What this means" || value != "the schedule works" {
		t.Errorf("метка из трёх слов: %q %q %v", key, value, ok)
	}
}

func TestRenderEscapesAndKeepsEveryWord(t *testing.T) {
	page := render(t, Letter{
		Lang: "ru", Subject: "shop.test не открывается", Text: siteDown,
		Tone: Problem, Band: "Проблема", Details: siteDownDetails,
	})
	if strings.Contains(page, "<script>") {
		t.Error("разметка из текста ошибки ожила")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("текст ошибки пропал вместо экранирования")
	}
	if lost := Missing(siteDown, page); len(lost) > 0 {
		t.Errorf("HTML потерял слова: %q", lost)
	}
	for _, want := range []string{`lang="ru"`, "cid:logo@vpsfocus", "band-problem", "Проблема", "<b>Причина:</b>", "[if mso]"} {
		if !strings.Contains(page, want) {
			t.Errorf("нет %q", want)
		}
	}
	// Прехедер — первый содержательный абзац, а не «Здравствуйте!».
	if !strings.Contains(page, `opacity:0;">Сайт shop.test перестал`) {
		t.Error("прехедер не тот")
	}
}

// Письмо ничего не грузит снаружи (инвариант проекта, Р5): ни картинок, ни
// стилей, ни шрифтов — иначе открытие письма агента оставляло бы след на
// чужом сервере. Единственная ссылка — кнопка, и та только по нажатию.
func TestLetterLoadsNothingFromOutside(t *testing.T) {
	page := render(t, Letter{
		Lang: "ru", Subject: "s", Text: "https://vpsfocus.xyz/auth/verify?token=a\n\n" + siteDown, Tone: Problem, Band: "Проблема",
		Details: siteDownDetails, Action: &Action{Label: "Подтвердить", URL: "https://vpsfocus.xyz/auth/verify?token=a"},
	})
	sources := regexp.MustCompile(`\b(src|href|background)\s*=\s*"([^"]*)"`).FindAllStringSubmatch(page, -1)
	if len(sources) < 2 {
		t.Fatalf("не нашлись даже логотип и кнопка — сломан разбор: %q", sources)
	}
	for _, match := range sources {
		switch {
		case match[1] == "src" && match[2] == "cid:"+logoID:
		case match[1] == "href" && match[2] == "https://vpsfocus.xyz/auth/verify?token=a":
		default:
			t.Errorf("внешний ресурс %s=%q", match[1], match[2])
		}
	}
	for _, banned := range []string{"url(", "@import", "<link", "<script"} {
		if strings.Contains(page, banned) {
			t.Errorf("в письме %q", banned)
		}
	}
}

// Missing и правда видит потерю: иначе сторож Р24 зелёный всегда.
func TestMissingNoticesALostWord(t *testing.T) {
	page := render(t, Letter{Subject: "s", Text: "один два три", Tone: Info})
	if lost := Missing("один два три четыре", page); len(lost) != 1 || lost[0] != "четыре" {
		t.Errorf("потеря не замечена: %q", lost)
	}
}

func TestInfoLetterHasNoBandAndActionBecomesAButton(t *testing.T) {
	text := "Откройте ссылку, чтобы подтвердить адрес почты:\n\nhttps://vpsfocus.xyz/auth/verify?token=a&b=1\n\nСсылка действует 24 часа."
	page := render(t, Letter{
		Lang: "ru", Subject: "Подтвердите адрес", Text: text, Tone: Info,
		Action: &Action{Label: "Подтвердить адрес", URL: "https://vpsfocus.xyz/auth/verify?token=a&b=1", Fallback: "Если кнопка не нажимается:"},
	})
	if strings.Contains(page, "band-") && strings.Contains(page, `class="band-`) {
		t.Error("у письма аккаунта полоса состояния")
	}
	if !strings.Contains(page, `href="https://vpsfocus.xyz/auth/verify?token=a&amp;b=1"`) {
		t.Error("нет кнопки со ссылкой")
	}
	if lost := Missing(text, page); len(lost) > 0 {
		t.Errorf("HTML потерял слова: %q", lost)
	}
}

// Кнопкой становится только адрес http(s): иное html/template гасит.
func TestActionRefusesAStrangeScheme(t *testing.T) {
	page := render(t, Letter{Subject: "s", Text: "javascript:alert(1)", Tone: Info,
		Action: &Action{Label: "x", URL: "javascript:alert(1)"}})
	if strings.Contains(page, `href="javascript:`) {
		t.Error("кнопка со ссылкой javascript:")
	}
}

// Длинный отчёт не доходит до 100 КБ: дальше Gmail обрезает письмо.
func TestLongLetterStaysUnderGmailClip(t *testing.T) {
	var text strings.Builder
	text.WriteString("Еженедельный отчёт.\n")
	for i := 0; i < 40; i++ {
		text.WriteString("\nshop.test\n")
		for j := 0; j < 5; j++ {
			text.WriteString("  [!] диск занят на 96% — сервер начнёт ломаться сам\n")
		}
	}
	page := render(t, Letter{Subject: "Отчёт", Text: text.String(), Tone: Warning, Band: "Стоит посмотреть"})
	if len(page) > 100*1024 {
		t.Errorf("HTML %d байт", len(page))
	}
}

func TestAssembleBuildsTextHTMLAndLogo(t *testing.T) {
	letter := Letter{Lang: "ru", Subject: "shop.test не открывается", Text: siteDown,
		Tone: Problem, Band: "Проблема", Details: siteDownDetails}
	contentType, body, err := Assemble(letter)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, line := range strings.Split(string(body), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("строка длиннее 998 байт: %d", len(line))
		}
	}

	raw := "Content-Type: " + contentType + "\r\n\r\n" + string(body)
	message, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("письмо не разбирается: %v", err)
	}
	media, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if media != "multipart/alternative" {
		t.Fatalf("тип письма %q", media)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])

	text, err := parts.NextPart()
	if err != nil || text.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("первая часть не текст: %v %v", err, text.Header)
	}
	if got := decoded(t, text); got != siteDown {
		t.Errorf("текстовая часть изменилась:\n%s", got)
	}

	related, err := parts.NextPart()
	if err != nil {
		t.Fatalf("вторая часть: %v", err)
	}
	media, params, _ = mime.ParseMediaType(related.Header.Get("Content-Type"))
	if media != "multipart/related" {
		t.Fatalf("вторая часть %q", media)
	}
	inner := multipart.NewReader(related, params["boundary"])
	html, err := inner.NextPart()
	if err != nil || !strings.HasPrefix(html.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("нет HTML: %v", err)
	}
	if !strings.Contains(decoded(t, html), "cid:logo@vpsfocus") {
		t.Error("HTML не ссылается на логотип")
	}
	image, err := inner.NextPart()
	if err != nil {
		t.Fatalf("нет картинки: %v", err)
	}
	if image.Header.Get("Content-Id") != "<logo@vpsfocus>" || !strings.HasPrefix(image.Header.Get("Content-Disposition"), "inline") {
		t.Errorf("заголовки картинки: %v", image.Header)
	}
	if png := decoded(t, image); !strings.HasPrefix(png, "\x89PNG") {
		t.Error("картинка не PNG")
	}
	if _, err := parts.NextPart(); err != io.EOF {
		t.Errorf("лишняя часть: %v", err)
	}
}

func decoded(t *testing.T, part *multipart.Part) string {
	t.Helper()
	raw, err := io.ReadAll(part)
	if err != nil {
		t.Fatalf("чтение части: %v", err)
	}
	out, err := base64.StdEncoding.DecodeString(string(bytes.ReplaceAll(raw, []byte("\r\n"), nil)))
	if err != nil {
		t.Fatalf("часть не base64: %v", err)
	}
	return string(out)
}

func TestMessageIDUsesTheSenderDomain(t *testing.T) {
	first, second := MessageID("vpsFocus <noreply@vpsfocus.xyz>"), MessageID("noreply@vpsfocus.xyz")
	if !strings.HasSuffix(first, "@vpsfocus.xyz>") || !strings.HasPrefix(first, "<") || first == second {
		t.Errorf("Message-ID: %q, %q", first, second)
	}
	if got := MessageID("не адрес"); !strings.HasSuffix(got, "@localhost>") {
		t.Errorf("Message-ID без адреса: %q", got)
	}
}
