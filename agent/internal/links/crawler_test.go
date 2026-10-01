package links

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Сервер-миниатюра: главная, доступная страница, страница в подкаталоге
// и две битые ссылки. Один обработчик на все пути — как настоящий сайт,
// который отдаёт 404 по отсутствующим адресам сам.
func testSite(t *testing.T) *httptest.Server {
	t.Helper()

	pages := map[string]string{
		"/":            `<html><body><a href="/ok.html">ok</a><a href="/gone.html">gone</a><a href="/blog/post">post</a><a href="https://other.test/page">out</a><a href="mailto:hi@site.test">mail</a><a href="#anchor">anchor</a></body></html>`,
		"/ok.html":     `<html><body><a href="/">home</a></body></html>`,
		"/blog/post":   `<html><body><a href="/also-gone.html">also gone</a><a href="/deep/child/">child</a></body></html>`,
		"/deep/child/": `<html><body><a href="/ok.html">back</a></body></html>`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCrawlFollowsInternalLinks(t *testing.T) {
	server := testSite(t)

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 3,
		MaxPages: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"/", "/ok.html", "/gone.html", "/blog/post", "/also-gone.html", "/deep/child/"}
	for _, path := range want {
		found := false
		for _, page := range pages {
			if strings.HasSuffix(page, path) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("страница %s не обошлась, обошлись: %v", path, pages)
		}
	}
	// Внешний хост и якорь — не наши страницы. Несуществующие страницы
	// при этом обошлись правильно: их существование выяснит lychee.
	if len(pages) != len(want) {
		t.Errorf("обошлось лишнее: %v", pages)
	}
}

func TestCrawlRespectsDepthLimit(t *testing.T) {
	server := testSite(t)

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 1,
		MaxPages: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Глубина один: главная и то, что с неё видно. /deep/child/ виден
	// только со второй страницы и на этот раз не должен попасть.
	for _, page := range pages {
		if strings.HasSuffix(page, "/deep/child/") {
			t.Errorf("глубина нарушена: %v", pages)
		}
	}
}

func TestCrawlRespectsPageLimit(t *testing.T) {
	server := testSite(t)

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 5,
		MaxPages: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) > 2 {
		t.Errorf("потолок страниц пробит: %v", pages)
	}
}

func TestCrawlRespectsExcludes(t *testing.T) {
	server := testSite(t)
	exclude := CompileExcludes([]string{"/blog"})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 3,
		MaxPages: 100,
		Exclude:  exclude,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if strings.Contains(page, "/blog") || strings.Contains(page, "/deep") {
			t.Errorf("исключённый путь обошёлся: %v", pages)
		}
	}
}

// Недоступная страница обход не роняет: остальные страницы сайта всё равно
// надо проверить, а её собственные ссылки пометит lychee.
func TestCrawlSurvivesBrokenPage(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(`<a href="/broken">раз</a><a href="/ok">два</a>`))
		case "/broken":
			http.Error(w, "упало", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte("<p>тут пусто</p>"))
		}
	})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 2,
		MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("сломанная страница уронила обход: %v", err)
	}
	if len(pages) != 3 {
		t.Errorf("обошлось %d страниц, ожидали 3: %v", len(pages), pages)
	}
}

// Сервер, до которого не достучаться, обход не начинает.
//
// robots.txt не отдался — по RFC 9309 это «не ходить». Отдельная ошибка, а
// не общая «обход не удался»: для человека «сайт запретил» и «обход
// сломался» — разные новости, и в интерфейсе у них разные тексты.
func TestCrawlRefusesWhenRobotsUnavailable(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.Error(w, "не сегодня", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("<p>страница</p>"))
	})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 2,
		MaxPages: 10,
	})
	if !errors.Is(err, ErrRobotsUnavailable) {
		t.Fatalf("ошибка %v, ожидали ErrRobotsUnavailable", err)
	}
	if len(pages) != 0 {
		t.Errorf("после неотданного robots.txt обошлось %d страниц", len(pages))
	}
}

// Запрет в robots.txt соблюдается строго и выключателя не имеет.
//
// Решение 2026-09-04: правило «агент остаётся ботом на живом сервере» общее,
// и краулер проверки ссылок ему подчиняется наравне с SEO-обходом. Цена —
// закрытые разделы в отчёт о битых ссылках не попадают.
func TestCrawlObeysRobots(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/":
			_, _ = w.Write([]byte(`<a href="/private/secret">нельзя</a><a href="/public">можно</a>`))
		default:
			_, _ = w.Write([]byte("<p>страница</p>"))
		}
	})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 3,
		MaxPages: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if strings.Contains(page, "/private/") {
			t.Errorf("краулер сходил туда, куда запрещает robots.txt: %v", pages)
		}
	}
	if len(pages) != 2 {
		t.Errorf("обошлось %d страниц, ожидали 2 (главная и /public): %v", len(pages), pages)
	}
}

// robots.txt, отвечающий перенаправлением, — это не запрет.
//
// Переезд файла на www или на https встречается сплошь и рядом, и считать
// его отказом означало бы не обходить такие сайты вовсе.
func TestCrawlFollowsRobotsRedirect(t *testing.T) {
	var asked []string
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/robots.txt":
			http.Redirect(w, r, "/robots-real.txt", http.StatusMovedPermanently)
		case "/robots-real.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/":
			_, _ = w.Write([]byte(`<a href="/private/x">нельзя</a><a href="/public">можно</a>`))
		default:
			_, _ = w.Write([]byte("<p>страница</p>"))
		}
	})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 3,
		MaxPages: 50,
	})
	if err != nil {
		t.Fatalf("перенаправленный robots.txt не должен запрещать обход: %v", err)
	}
	for _, page := range pages {
		if strings.Contains(page, "/private/") {
			t.Errorf("правила из перенаправленного файла не применились: %v", pages)
		}
	}
	if len(asked) < 2 || asked[1] != "/robots-real.txt" {
		t.Errorf("за перенаправлением не пошли: %v", asked)
	}
}

// Пауза между запросами — единственное, что отделяет обход от нагрузочного
// теста чужого прода.
func TestCrawlKeepsDelayBetweenRequests(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		// Каждая страница ведёт на следующую: иначе обход кончится раньше,
		// чем наберётся проверяемое число пауз.
		_, _ = w.Write([]byte(`<a href="` + r.URL.Path + `x">дальше</a>`))
	})

	started := time.Now()
	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 10,
		MaxPages: 3,
		Delay:    120 * time.Millisecond,
	})
	elapsed := time.Since(started)

	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("обошлось %d страниц, ожидали 3", len(pages))
	}
	// Четыре запроса — robots.txt и три страницы, — значит три паузы.
	if elapsed < 300*time.Millisecond {
		t.Fatalf("обход уложился в %v — паузы не выдерживались", elapsed)
	}
}

// Crawl-delay сайта сильнее нашей настройки: это его сервер.
func TestCrawlDelayFromRobotsWins(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0.15\n"))
			return
		}
		_, _ = w.Write([]byte(`<a href="` + r.URL.Path + `x">дальше</a>`))
	})

	started := time.Now()
	if _, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 10,
		MaxPages: 2,
		Delay:    time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	// Две страницы после robots.txt — две паузы по 150 мс.
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
		t.Fatalf("обход уложился в %v: Crawl-delay сайта проигнорирован", elapsed)
	}
}

// site — сервер из одного обработчика.
func site(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

// robots.txt, запрещающий главную, обход не начинает — и говорит об этом.
//
// Без отдельной ошибки обход вернул бы пустой список без единого признака
// беды, `Check` — пустой отчёт, а экран показал бы зелёное «битых ссылок
// нет» по сайту, на который мы не заходили ни разу.
func TestCrawlRefusesWhenRobotsForbidsEverything(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		_, _ = w.Write([]byte("<p>страница</p>"))
	})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 2,
		MaxPages: 10,
	})
	if !errors.Is(err, ErrRobotsForbidden) {
		t.Fatalf("ошибка %v, ожидали ErrRobotsForbidden", err)
	}
	if len(pages) != 0 {
		t.Errorf("обойдено %d страниц вопреки запрету", len(pages))
	}
}

// Запрет на раздел главной не касается: там обход идёт как обычно.
func TestCrawlPartialDisallowIsNotRefusal(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/":
			_, _ = w.Write([]byte(`<a href="/private/x">нельзя</a>`))
		default:
			_, _ = w.Write([]byte("<p>страница</p>"))
		}
	})

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 2,
		MaxPages: 10,
	})
	if err != nil {
		t.Fatalf("частичный запрет обход не отменяет: %v", err)
	}
	if len(pages) != 1 {
		t.Errorf("обойдено %d страниц, ожидали одну главную: %v", len(pages), pages)
	}
}

// Хост фильтруется при постановке в очередь, но перенаправление случается
// после отбора: без проверки в клиенте краулер скачивал и разбирал страницу
// чужого сайта, куда его увели. Правило «краулер не уходит с сайта клиента»
// писалось про SEO-обход, а вежливость у двух краулеров общая.
func TestCrawlDoesNotFollowRedirectOffsite(t *testing.T) {
	var visited []string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visited = append(visited, r.URL.Path)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><a href="/чужая">ссылка</a></body></html>`))
	}))
	t.Cleanup(foreign.Close)

	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><body><a href="/увод">туда</a></body></html>`))
		case "/увод":
			http.Redirect(w, r, foreign.URL+"/страница", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(own.Close)

	pages, err := Crawl(testContext(t), HTTPClient(0), own.URL+"/", Limits{MaxDepth: 3, MaxPages: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) != 0 {
		t.Fatalf("краулер ушёл на чужой сайт: %v", visited)
	}
	// Сама страница из списка не пропадает: её проверит lychee, и «ссылка
	// уводит на чужой сайт» — это его ответ, а не наш молчаливый ноль.
	for _, page := range pages {
		if strings.Contains(page, foreign.URL) {
			t.Fatalf("чужой адрес попал в список страниц: %v", pages)
		}
	}
}

// Перенаправление всего сайта с голого домена на www (и обратно) — обычная
// настройка, и запрет на него оставил бы такой сайт вовсе без обхода.
func TestSiblingWwwHostIsTheSameSite(t *testing.T) {
	for _, pair := range [][2]string{
		{"www.site.test", "site.test"},
		{"site.test", "www.site.test"},
		{"site.test:8080", "site.test:8080"},
		{"SITE.test", "site.test"},
	} {
		if !sameSite(pair[0], pair[1]) {
			t.Errorf("%q и %q сочтены разными сайтами", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{
		{"site.test.evil.io", "site.test"},
		{"m.site.test", "site.test"},
		{"shop.site.test", "site.test"},
		{"othersite.test", "site.test"},
	} {
		if sameSite(pair[0], pair[1]) {
			t.Errorf("%q сочтён тем же сайтом, что и %q", pair[0], pair[1])
		}
	}
}

// Тело страницы читается с потолком: агент живёт на сервере клиента, и
// память там не наша. До этого размер не держало ничто, кроме таймаута.
func TestCrawlStopsReadingHugePage(t *testing.T) {
	const limit = 4 << 10

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>"))
		// Ссылка за потолком: если потолок работает, до неё не дочитаем.
		_, _ = w.Write([]byte(strings.Repeat("<p>дно</p>", limit)))
		_, _ = w.Write([]byte(`<a href="/за-потолком">поздно</a></body></html>`))
	}))
	t.Cleanup(server.Close)

	pages, err := Crawl(testContext(t), HTTPClient(0), server.URL+"/", Limits{
		MaxDepth: 2,
		MaxPages: 100,
		MaxBytes: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if strings.Contains(page, "за-потолком") {
			t.Fatalf("страница прочитана за потолком байт: %v", pages)
		}
	}
}
