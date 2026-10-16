package checks

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"agent/internal/mailhtml"
)

var verbs = regexp.MustCompile(`%[ds]`)

// filled — шаблон fmt с правдоподобными подстановками: числа — 91, строки —
// домен.
func filled(template string) string {
	var args []any
	for _, verb := range verbs.FindAllString(template, -1) {
		if verb == "%d" {
			args = append(args, 91)
		} else {
			args = append(args, "shop.example.com")
		}
	}
	return fmt.Sprintf(template, args...)
}

// Каждое письмо агента обоих языков оформляется без потерь (трек AC, Р24):
// в HTML есть все слова текста и подвала по порядку. Поля берутся
// отражением, а не перечнем: новый текст попадёт под сторож сам.
func TestEveryAlertKeepsItsTextInHTML(t *testing.T) {
	for _, locale := range []string{"ru", "en"} {
		words := WordsFor(locale)
		value := reflect.ValueOf(words)
		footer := filled(words.MailFooter)

		for i := 0; i < value.NumField(); i++ {
			name := value.Type().Field(i).Name
			if !strings.HasSuffix(name, "Body") {
				continue
			}
			subject := value.FieldByName(strings.TrimSuffix(name, "Body") + "Subject")
			if !subject.IsValid() {
				t.Fatalf("у %s нет темы", name)
			}

			text := filled(value.Field(i).String()) + "\n\n—\n" + footer
			page, err := mailhtml.Render(mailhtml.Letter{
				Lang: locale, Subject: filled(subject.String()), Text: text,
				Tone: mailhtml.Warning, Band: words.MailBandWarning, Source: filled(words.MailSource),
			})
			if err != nil {
				t.Fatalf("%s %s: %v", locale, name, err)
			}
			if lost := mailhtml.Missing(text, page); len(lost) > 0 {
				t.Errorf("%s %s: HTML потерял слова %q", locale, name, lost)
			}
			if !strings.Contains(page, `class="t-rule"`) {
				t.Errorf("%s %s: подвал не стал подвалом", locale, name)
			}
		}
	}
}

// В текстах агента нет слов, которых человек без навыков админа не знает
// (правило проекта): «агент» и «стек» — внутренние имена мониторинга.
func TestAlertWordsAvoidJargon(t *testing.T) {
	// \b в Go знает только латиницу — у русских слов граница своя.
	jargon := regexp.MustCompile(`(?i)(^|[^\p{L}])(агент|стек)\p{L}*|\b(agent|stack)\b|сайт клиента`)
	for _, sample := range []string{"от агента на сервере", "Стек упал", "the agent on", "и сайт клиента"} {
		if !jargon.MatchString(sample) {
			t.Fatalf("сторож не видит жаргон в %q", sample)
		}
	}
	for _, locale := range []string{"ru", "en"} {
		value := reflect.ValueOf(WordsFor(locale))
		for i := 0; i < value.NumField(); i++ {
			if found := jargon.FindString(value.Field(i).String()); found != "" {
				t.Errorf("%s %s: «%s»", locale, value.Type().Field(i).Name, found)
			}
		}
	}
}
