// mail-preview пишет письма агента на обоих языках в папку — смотреть
// оформление глазами (трек AC, спринт 179):
//
//	go run ./cmd/mail-preview -out <папка>
//
// Каждая тревога (с подстановками-примерами) в трёх тонах не нужна: тон
// берётся по смыслу письма — тема с «снова» и «OK» показывается отбоем.
// Ещё отчёт за неделю и проверочное письмо. В образ агента не входит:
// Dockerfile собирает только cmd/agent.
package main

import (
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"agent/internal/checks"
	"agent/internal/digest"
	"agent/internal/health"
	"agent/internal/mailhtml"
)

func main() {
	out := flag.String("out", "mail-preview", "куда писать письма")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "mail-preview:", err)
		os.Exit(1)
	}
}

var verbs = regexp.MustCompile(`%[ds]`)

func filled(template string) string {
	var args []any
	for _, verb := range verbs.FindAllString(template, -1) {
		if verb == "%d" {
			args = append(args, 91)
		} else {
			args = append(args, "myweb.com")
		}
	}
	return fmt.Sprintf(template, args...)
}

type entry struct{ Locale, Name, Subject, File string }

func run(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var entries []entry
	write := func(locale, name string, letter mailhtml.Letter) error {
		page, err := mailhtml.PreviewHTML(letter)
		if err != nil {
			return fmt.Errorf("%s %s: %w", locale, name, err)
		}
		file := locale + "-" + name
		entries = append(entries, entry{locale, name, letter.Subject, file})
		return os.WriteFile(filepath.Join(out, file+".html"), []byte(page), 0o644)
	}

	for _, locale := range []string{"ru", "en"} {
		words := checks.WordsFor(locale)
		source := fmt.Sprintf(words.MailSource, "web-1")
		footer := "\n\n—\n" + fmt.Sprintf(words.MailFooter, "web-1")
		value := reflect.ValueOf(words)

		for i := 0; i < value.NumField(); i++ {
			name := value.Type().Field(i).Name
			if !strings.HasSuffix(name, "Body") {
				continue
			}
			base := strings.TrimSuffix(name, "Body")
			// Отбой — у полей ...OK, тревога — у остальных; проверочное
			// письмо — без полосы.
			tone, band := mailhtml.Warning, words.MailBandWarning
			switch {
			case base == "Test":
				tone, band = mailhtml.Info, ""
			case strings.HasSuffix(base, "OK") || strings.HasSuffix(base, "Renewed"):
				tone, band = mailhtml.OK, words.MailBandOK
			case strings.Contains(base, "Problem") || base == "Backup":
				tone, band = mailhtml.Problem, words.MailBandProblem
			}
			err := write(locale, base, mailhtml.Letter{
				Lang: locale, Subject: filled(value.FieldByName(base + "Subject").String()),
				Text: filled(value.Field(i).String()) + footer, Tone: tone, Band: band, Source: source,
			})
			if err != nil {
				return err
			}
		}

		report := digest.Build(locale, "web-1", sample())
		if err := write(locale, "WeeklyReport", mailhtml.Letter{
			Lang: locale, Subject: report.Subject, Text: report.Body, Tone: report.Tone, Band: report.Band, Source: source,
		}); err != nil {
			return err
		}
	}

	file, err := os.Create(filepath.Join(out, "index.html"))
	if err != nil {
		return err
	}
	defer file.Close()
	if err := index.Execute(file, entries); err != nil {
		return err
	}
	fmt.Printf("%d писем: %s\n", len(entries), filepath.Join(out, "index.html"))
	return nil
}

// sample — неделя с находками на сервере и в одном из проектов.
func sample() health.Snapshot {
	return health.Snapshot{
		Server: health.ServerState{Level: health.LevelAttention, Reasons: []health.Reason{
			{Code: health.CodeDiskFilling, Level: health.LevelAttention, Count: 88},
			{Code: health.CodeMemoryTight, Level: health.LevelAttention, Count: 92},
		}},
		Projects: []health.ProjectState{
			{Name: "myweb.com", Level: health.LevelProblem, Reasons: []health.Reason{
				{Code: health.CodeErrorSpike, Level: health.LevelProblem, Count: 140, Domain: "myweb.com"},
			}},
			{Name: "blog.myweb.com", Level: health.LevelOK},
		},
	}
}

var index = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><title>Письма агента vpsFocus</title>
<style>body{font:15px/1.6 system-ui,sans-serif;max-width:760px;margin:32px auto;padding:0 16px}td{padding:2px 12px 2px 0}</style>
</head><body><h1>Письма агента</h1><table>
{{range .}}<tr><td>{{.Locale}}</td><td><a href="{{.File}}.html">{{.Name}}</a></td><td>{{.Subject}}</td></tr>
{{end}}</table></body></html>
`))
