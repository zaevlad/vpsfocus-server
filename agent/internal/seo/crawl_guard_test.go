package seo

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// robots.txt, закрывший сайт целиком, — это отказ, а не «всё хорошо».
//
// Без кода экран показывал «0 из 0 страниц» и число закрытых адресов, ни
// словом не объясняя причину. Молчаливый ноль хуже отказа — то же решение и
// та же причина, что у проверки ссылок.
func TestCrawlRobotsForbidsEverything(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		_, _ = w.Write([]byte(html5("страница")))
	})

	scan, pages := Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", testLimits())

	if len(pages) != 0 {
		t.Fatalf("закрытый сайт всё же обошли: %d страниц", len(pages))
	}
	if scan.Error != ErrRobotsForbidden {
		t.Fatalf("код ошибки %q, ожидали %q", scan.Error, ErrRobotsForbidden)
	}
}

// Правило исключений, накрывшее главную, — тоже отказ, а не пустота.
//
// Пункт 2.1 спринта 25. Стартовый адрес отбрасывался в `accept` молча, и
// обход заканчивался с нулём страниц и пустым `run_error`. Дальше это
// расходилось в две стороны: состоявшимся такой обход не считался — и
// правильно, — но и в «последнюю неудачную попытку» он не попадал, потому
// что она отбирается по непустому `run_error`. Экран продолжал показывать
// вчерашний обход, ни словом не сказав, что сегодняшний не состоялся.
func TestCrawlExcludedStartPageIsAFailure(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(html5("страница")))
	})

	limits := testLimits()
	limits.Exclude = []*regexp.Regexp{regexp.MustCompile(".*")}

	scan, pages := Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", limits)

	if len(pages) != 0 {
		t.Fatalf("исключённый сайт всё же обошли: %d страниц", len(pages))
	}
	if scan.Error != ErrNoPages {
		t.Fatalf("код ошибки %q, ожидали %q", scan.Error, ErrNoPages)
	}
}

// `<base href>` пишет владелец страницы, и указывать он может куда угодно.
// Пойти по нему на чужой домен — значит обходить не сайт клиента, а тот, на
// который он сослался, да ещё и с чужим robots.txt.
func TestCrawlIgnoresForeignBaseHref(t *testing.T) {
	var foreign int
	outside := site(t, func(w http.ResponseWriter, _ *http.Request) {
		foreign++
		_, _ = w.Write([]byte(html5("чужая страница")))
	})

	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>t</title>` +
				`<base href="` + outside.URL + `/"></head><body>` +
				`<a href="stolen">туда</a></body></html>`))
		default:
			_, _ = w.Write([]byte(html5("своя страница")))
		}
	})

	_, pages := Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", testLimits())

	if foreign != 0 {
		t.Fatalf("краулер ушёл на чужой сайт: %d запросов", foreign)
	}
	for _, page := range pages {
		if !strings.HasPrefix(page.URL, server.URL) {
			t.Fatalf("в обходе чужой адрес: %s", page.URL)
		}
	}
}

// Остаток очереди — это ответ на вопрос «а это весь сайт?». В очереди лежат
// и виденные, и закрытые robots.txt адреса: они отсеиваются только при
// извлечении, и сырая её длина называет число больше настоящего.
func TestCrawlQueuedCountsOnlyWhatItWouldTake(t *testing.T) {
	// Главная ссылается на три закрытых адреса и один открытый. При
	// потолке в одну страницу в очереди остаются все четыре, а взяли бы мы
	// из них один.
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/":
			_, _ = w.Write([]byte(html5(
				`<a href="/private/a">a</a> <a href="/private/b">b</a>` +
					`<a href="/private/c">c</a> <a href="/open">open</a>`)))
		default:
			_, _ = w.Write([]byte(html5("страница")))
		}
	})

	limits := testLimits()
	limits.MaxPages = 1

	scan, pages := Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", limits)

	if len(pages) != 1 {
		t.Fatalf("страниц: %d", len(pages))
	}
	if scan.Stats.Queued != 1 {
		t.Fatalf("в остатке очереди %d, ожидали 1: закрытые адреса посчитаны как ждущие",
			scan.Stats.Queued)
	}
}
