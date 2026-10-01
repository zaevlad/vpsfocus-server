package links

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Форма ответа снята с живого lychee v0.24.2, а не придумана: поля code
// есть не у всех вариантов статуса, и разбор обязан это переживать.
const fixtureOutput = `{
  "total": 6,
  "unique": 6,
  "successful": 3,
  "unknown": 0,
  "unsupported": 0,
  "timeouts": 0,
  "redirects": 0,
  "remaps": 0,
  "excludes": 1,
  "errors": 2,
  "cached": 0,
  "success_map": {},
  "error_map": {
    "http://site.test/": [
      {
        "url": "http://site.test/missing.html",
        "status": {
          "text": "Rejected status code: 404 Not Found",
          "code": 404
        },
        "span": { "line": 3, "column": 10 },
        "duration": { "secs": 0, "nanos": 21663900 }
      }
    ],
    "http://site.test/blog/post.html": [
      {
        "url": "http://site.test/missing.html",
        "status": {
          "text": "Rejected status code: 404 Not Found",
          "code": 404
        },
        "span": { "line": 1, "column": 69 },
        "duration": { "secs": 0, "nanos": 61801000 }
      }
    ]
  },
  "timeout_map": {
    "http://site.test/slow.html": [
      {
        "url": "https://external.test/api",
        "status": { "text": "Timeout", "details": "Request timed out" }
      }
    ]
  },
  "suggestion_map": {},
  "redirect_map": {},
  "excluded_map": {},
  "duration": { "secs": 5, "nanos": 382532000 },
  "detailed_stats": false
}`

// stubRunner подменяет запуск бинарника фикстурой. Код возврата lychee
// здесь не нужен: он отличает «битые ссылки есть» от «нет», а обе ситуации
// для нас — нормальный результат.
func stubRunner(t *testing.T, output string) *bool {
	t.Helper()
	called := false
	runLycheeFunc = func(ctx context.Context, opts Options, pages []string) (*lycheeOutput, error) {
		called = true
		parsed, err := parseLychee(output)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	}
	t.Cleanup(func() { runLycheeFunc = runLychee })
	return &called
}

func TestParseFixture(t *testing.T) {
	output, err := parseLychee(fixtureOutput)
	if err != nil {
		t.Fatalf("фикстура не разобралась: %v", err)
	}
	if output.Total != 6 || output.Errors != 2 {
		t.Fatalf("счётчики не те: total=%d errors=%d", output.Total, output.Errors)
	}
	if output.Duration.Seconds != 5 {
		t.Fatalf("длительность не распознана: %+v", output.Duration)
	}

	// Пустой вывод — не пустой отчёт, а ошибка: проверки не было.
	if _, err := parseLychee(""); err == nil {
		t.Fatal("пустой вывод разобрался")
	}
	if _, err := parseLychee("это не json"); err == nil {
		t.Fatal("мусор разобрался в отчёт")
	}
}

func TestCheckGroupsFailuresAcrossSources(t *testing.T) {
	stubRunner(t, fixtureOutput)

	report, err := Check(testContext(t), Options{}, "site.test",
		[]string{"http://site.test/", "http://site.test/blog/post.html"})
	if err != nil {
		t.Fatalf("запуск не удался: %v", err)
	}

	if report.Broken != 2 || len(report.Failures) != 2 {
		t.Fatalf("битых ссылок должно быть две: %+v", report.Failures)
	}
	if report.TotalLinks != 6 {
		t.Fatalf("всего ссылок: %d", report.TotalLinks)
	}
	if report.DurationSeconds != 5 {
		t.Fatalf("длительность круга: %d", report.DurationSeconds)
	}

	var missing *Failure
	for i := range report.Failures {
		if report.Failures[i].URL == "http://site.test/missing.html" {
			missing = &report.Failures[i]
		}
	}
	if missing == nil {
		t.Fatal("missing.html потерялся")
	}
	// Одна ссылка, найденная на двух страницах, приходит одной записью
	// со списком источников, а не двумя половинками.
	if len(missing.Sources) != 2 {
		t.Fatalf("источники не собрались: %+v", missing.Sources)
	}
	if missing.Kind != "httpStatus" || missing.Code != 404 {
		t.Fatalf("классификация сломалась: %+v", missing)
	}

	var timeout *Failure
	for i := range report.Failures {
		if report.Failures[i].URL == "https://external.test/api" {
			timeout = &report.Failures[i]
		}
	}
	if timeout == nil || timeout.Kind != "timeout" {
		t.Fatalf("таймаут не распознан: %+v", timeout)
	}

	// Худшее — выше: HTTP-ошибки раньше таймаута.
	if report.Failures[0].Kind != "httpStatus" {
		t.Fatalf("порядок сортировки сломался: %+v", report.Failures)
	}
}

func TestCheckEmptyPagesSkipsBinary(t *testing.T) {
	called := stubRunner(t, "{}")

	report, err := Check(testContext(t), Options{}, "site.test", nil)
	if err != nil {
		t.Fatalf("пустой список страниц: %v", err)
	}
	if *called {
		t.Fatal("без страниц бинарник звать незачем")
	}
	if report.Broken != 0 || len(report.Failures) != 0 {
		t.Fatalf("от пустого списка ждём пустой отчёт: %+v", report)
	}
}

func TestCheckMissingBinary(t *testing.T) {
	opts := Options{Binary: "нет такого"}
	if _, err := runLychee(testContext(t), opts, []string{"http://site.test/"}); err == nil {
		t.Fatal("нет бинарника — должна быть ошибка")
	}
}

// ── Настоящий lychee ─────────────────────────────────────────────────────
//
// Гоняется только когда бинарник передан окружением: тест не должен
// зависеть от того, скачан ли lychee на машину разработчика.
//
//	powershell: $env:VPSFOCUS_LYCHEE = "...lychee.exe"; go test ./internal/links -run Real
func TestRealLycheeAgainstTestSite(t *testing.T) {
	binary := os.Getenv("VPSFOCUS_LYCHEE")
	if binary == "" {
		t.Skip("VPSFOCUS_LYCHEE не задан: настоящий запуск пропущен")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><body><a href="/ok.html">ok</a><a href="/gone.html">gone</a></body></html>`)
		case "/ok.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><body><a href="/">home</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output, err := runLychee(testContext(t), Options{Binary: binary, Concurrency: 2, TimeoutSeconds: 10},
		[]string{server.URL + "/", server.URL + "/ok.html"})
	if err != nil {
		t.Fatalf("настоящий lychee не отработал: %v", err)
	}
	t.Logf("сырой вывод: total=%d errors=%d error_map=%+v", output.Total, output.Errors, output.ErrorMap)

	report, err := Check(testContext(t), Options{Binary: binary, Concurrency: 2, TimeoutSeconds: 10},
		"127.0.0.1", []string{server.URL + "/", server.URL + "/ok.html"})
	if err != nil {
		t.Fatalf("настоящий lychee не отработал: %v", err)
	}
	t.Logf("отчёт: total=%d broken=%d failures=%+v", report.TotalLinks, report.Broken, report.Failures)
	if len(report.Failures) == 0 {
		t.Fatal("битая ссылка не найдена")
	}
	if report.Failures[0].Code != 404 || report.Failures[0].Kind != "httpStatus" {
		t.Fatalf("классификация сломалась: %+v", report.Failures[0])
	}
	if len(report.Failures[0].Sources) != 1 {
		t.Fatalf("источник потерялся: %+v", report.Failures[0])
	}
}
