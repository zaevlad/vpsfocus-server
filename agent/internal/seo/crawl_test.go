package seo

import (
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testLimits — пределы, при которых тест не ждёт: пауза между запросами
// нулевая, потолки щедрые.
func testLimits() Limits {
	return Limits{
		MaxDepth:   3,
		MaxPages:   50,
		MaxBytes:   1 << 20,
		Timeout:    5 * time.Second,
		UseSitemap: true,
	}
}

// site поднимает сервер с заданными страницами.
func site(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// pageByURL ищет страницу в результате обхода.
func pageByURL(pages []Page, address string) (Page, bool) {
	for _, page := range pages {
		if page.URL == address {
			return page, true
		}
	}
	return Page{}, false
}

func html5(body string) string {
	return "<!doctype html><html><head><title>t</title></head><body>" + body + "</body></html>"
}

func TestCrawlFollowsInternalLinks(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/a">a</a> <a href="/b">b</a> <a href="https://other.example/">чужой</a>`)))
		case "/a":
			_, _ = w.Write([]byte(html5(`<a href="/c">c</a>`)))
		case "/b", "/c":
			_, _ = w.Write([]byte(html5("конец")))
		default:
			http.NotFound(w, r)
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	if scan.Error != "" {
		t.Fatalf("обход не состоялся: %s", scan.Error)
	}
	if len(pages) != 4 {
		t.Fatalf("страниц обойдено %d: %v", len(pages), pages)
	}
	if scan.Stats.Fetched != 4 {
		t.Fatalf("разобрано %d", scan.Stats.Fetched)
	}
	if _, ok := pageByURL(pages, server.URL+"/c"); !ok {
		t.Fatal("до третьего уровня обход не дошёл")
	}
}

func TestCrawlRespectsRobots(t *testing.T) {
	// Строгое соблюдение robots.txt — решение спринта 19: агентство владеет
	// сайтом, но агент всё равно бот на живом сервере.
	visited := map[string]bool{}
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		visited[r.URL.Path] = true
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/private/secret">нельзя</a> <a href="/public">можно</a>`)))
		default:
			_, _ = w.Write([]byte(html5("страница")))
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	if visited["/private/secret"] {
		t.Fatal("краулер сходил туда, куда запрещает robots.txt")
	}
	if scan.Stats.BlockedByRobots != 1 {
		t.Fatalf("запрещённых адресов насчитано %d", scan.Stats.BlockedByRobots)
	}
	if _, ok := pageByURL(pages, server.URL+"/public"); !ok {
		t.Fatal("разрешённая страница не обойдена")
	}
}

func TestCrawlRobotsUnavailableStopsScan(t *testing.T) {
	// robots.txt, который не отдался, по RFC 9309 означает «не ходить».
	// Экран обязан объяснить, почему страниц нет, — поэтому отдельный код.
	requests := 0
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	if scan.Error != ErrRobotsUnavailable {
		t.Fatalf("код ошибки: %q", scan.Error)
	}
	if len(pages) != 0 {
		t.Fatalf("после недоступного robots.txt обойдено %d страниц", len(pages))
	}
	if requests != 1 {
		t.Fatalf("сделано запросов: %d — после отказа ходить больше некуда", requests)
	}
}

// robots.txt, отвечающий перенаправлением, — это не запрет.
//
// Переезд файла на www или на https встречается сплошь и рядом; пока
// загрузка жила здесь, а не в общем пакете, любой такой сайт объявлялся
// запретившим обход и не обходился вовсе. Перенаправления теперь проходятся
// вручную — клиент им не следует нарочно, цепочку редиректов страницы надо
// записывать целиком.
func TestCrawlFollowsRobotsRedirect(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.Redirect(w, r, "/robots-real.txt", http.StatusMovedPermanently)
		case "/robots-real.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/private/x">нельзя</a><a href="/public">можно</a>`)))
		default:
			_, _ = w.Write([]byte(html5("<p>страница</p>")))
		}
	})

	limits := testLimits()
	limits.UseSitemap = false

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)

	if scan.Error != "" {
		t.Fatalf("перенаправленный robots.txt не должен запрещать обход: %q", scan.Error)
	}
	if _, ok := pageByURL(pages, server.URL+"/private/x"); ok {
		t.Error("правила из перенаправленного файла не применились")
	}
	if _, ok := pageByURL(pages, server.URL+"/public"); !ok {
		t.Error("разрешённая страница не обойдена")
	}
}

func TestCrawlRecordsRedirectChain(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/old">старый адрес</a>`)))
		case "/old":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		case "/new":
			_, _ = w.Write([]byte(html5("новая страница")))
		default:
			http.NotFound(w, r)
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	page, ok := pageByURL(pages, server.URL+"/old")
	if !ok {
		t.Fatal("страница с перенаправлением не записана")
	}
	if len(page.Redirects) != 1 || page.Redirects[0] != server.URL+"/new" {
		t.Fatalf("цепочка: %v", page.Redirects)
	}
	if page.FinalURL != server.URL+"/new" {
		t.Fatalf("конечный адрес: %q", page.FinalURL)
	}
	if page.Structure == nil {
		t.Fatal("страница за перенаправлением не разобрана")
	}
	if scan.Stats.Redirected != 1 {
		t.Fatalf("перенаправлений насчитано %d", scan.Stats.Redirected)
	}
	// Цель перенаправления не должна попасть в обход второй раз своей
	// строкой: это одна и та же страница.
	if _, duplicated := pageByURL(pages, server.URL+"/new"); duplicated {
		t.Fatal("цель перенаправления обойдена повторно")
	}
}

func TestCrawlOffsiteRedirect(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/away">прочь</a>`)))
		case "/away":
			http.Redirect(w, r, "https://other.example/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})

	_, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	page, ok := pageByURL(pages, server.URL+"/away")
	if !ok {
		t.Fatal("страница с уходом на чужой сайт не записана")
	}
	if page.Error != PageErrOffsite {
		t.Fatalf("код: %q", page.Error)
	}
	if page.FinalURL != "https://other.example/" {
		t.Fatalf("конечный адрес: %q", page.FinalURL)
	}
}

func TestCrawlDetectsDuplicateContent(t *testing.T) {
	same := html5("одно и то же содержимое")
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/one">1</a> <a href="/two">2</a>`)))
		case "/one", "/two":
			_, _ = w.Write([]byte(same))
		default:
			http.NotFound(w, r)
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	if scan.Stats.Duplicates != 1 {
		t.Fatalf("копий насчитано %d", scan.Stats.Duplicates)
	}
	second, _ := pageByURL(pages, server.URL+"/two")
	if second.DuplicateOf != server.URL+"/one" {
		t.Fatalf("копия указывает на %q", second.DuplicateOf)
	}
	first, _ := pageByURL(pages, server.URL+"/one")
	if first.Hash == "" || first.Hash != second.Hash {
		t.Fatal("отпечатки одинаковых страниц разошлись")
	}
}

func TestCrawlHonorsPageAndDepthLimits(t *testing.T) {
	// Бесконечный сайт: каждая страница ссылается на следующую.
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(html5(`<a href="` + r.URL.Path + `x">дальше</a>`)))
	})

	limits := testLimits()
	limits.MaxPages = 3
	limits.MaxDepth = 10

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)

	if len(pages) != 3 {
		t.Fatalf("страниц: %d", len(pages))
	}
	// Очередь не пуста — значит, сайт больше показанного, и врать «это
	// весь сайт» нельзя.
	if scan.Stats.Queued == 0 {
		t.Fatal("остаток очереди не записан")
	}

	limits.MaxPages = 50
	limits.MaxDepth = 1
	_, shallow := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)
	if len(shallow) != 2 {
		t.Fatalf("при глубине 1 обойдено %d страниц", len(shallow))
	}
}

func TestCrawlSkipsNonPagesAndRecordsStatus(t *testing.T) {
	asked := map[string]bool{}
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		asked[r.URL.Path] = true
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(
				`<a href="/picture.png">картинка</a> <a href="/feed">лента</a> <a href="/gone">нет</a>`)))
		case "/feed":
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte("<rss/>"))
		default:
			http.NotFound(w, r)
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	if asked["/picture.png"] {
		t.Fatal("краулер сходил за картинкой — лишний запрос на чужой сервер")
	}

	feed, ok := pageByURL(pages, server.URL+"/feed")
	if !ok || feed.Error != PageErrNotHTML {
		t.Fatalf("не-HTML: %+v", feed)
	}
	if feed.Structure != nil {
		t.Fatal("у не-HTML появилась структура")
	}

	gone, ok := pageByURL(pages, server.URL+"/gone")
	if !ok || gone.Status != http.StatusNotFound || gone.Error != PageErrStatus {
		t.Fatalf("страница с 404: %+v", gone)
	}
	if scan.Stats.Failed != 1 {
		t.Fatalf("неудач насчитано %d — не-HTML не является неудачей", scan.Stats.Failed)
	}
}

func TestCrawlSeedsFromSitemap(t *testing.T) {
	// Страница из карты сайта, на которую не ведёт ни одна ссылка, — самая
	// частая находка аудита: раздел выложили, из меню убрали.
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("Sitemap: " + serverURL(r) + "/sitemap.xml\n"))
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
			<urlset><url><loc>` + serverURL(r) + `/orphan</loc></url></urlset>`))
		case "/":
			_, _ = w.Write([]byte(html5("главная без ссылок")))
		case "/orphan":
			_, _ = w.Write([]byte(html5("сирота")))
		default:
			http.NotFound(w, r)
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	orphan, ok := pageByURL(pages, server.URL+"/orphan")
	if !ok {
		t.Fatalf("страница из карты сайта не обойдена: %v", pages)
	}
	if !orphan.FromSitemap {
		t.Fatal("происхождение страницы из карты сайта потеряно")
	}
	if scan.Stats.FromSitemap != 1 {
		t.Fatalf("из карты сайта насчитано %d", scan.Stats.FromSitemap)
	}
}

func TestCrawlSitemapDoesNotOverrideRobots(t *testing.T) {
	// Спор двух файлов владельца сайта решается в пользу запрета.
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /closed\nSitemap: " + serverURL(r) + "/sitemap.xml\n"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<urlset><url><loc>` + serverURL(r) + `/closed</loc></url></urlset>`))
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			t.Errorf("запрос к запрещённому адресу %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	_, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", testLimits())

	if _, ok := pageByURL(pages, server.URL+"/closed"); ok {
		t.Fatal("адрес из карты сайта обошёл запрет robots.txt")
	}
}

func TestCrawlKeepsDelayBetweenRequests(t *testing.T) {
	// Пауза — единственное, что отделяет обход от нагрузочного теста
	// чужого прода.
	var stamps []time.Time
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		stamps = append(stamps, time.Now())
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		// Каждая страница ведёт на свою следующую: иначе обход кончится на
		// второй, и пауз будет меньше, чем проверяет тест.
		_, _ = w.Write([]byte(html5(`<a href="` + r.URL.Path + `x">дальше</a>`)))
	})

	limits := testLimits()
	limits.MaxPages = 3
	limits.Delay = 120 * time.Millisecond
	limits.UseSitemap = false

	started := time.Now()
	scan, _ := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)
	elapsed := time.Since(started)

	if len(stamps) != 4 {
		t.Fatalf("запросов сделано %d, ожидалось 4 (robots.txt и три страницы)", len(stamps))
	}
	// Три паузы между четырьмя запросами (robots.txt плюс три страницы).
	if elapsed < 300*time.Millisecond {
		t.Fatalf("обход уложился в %v — паузы не выдерживались", elapsed)
	}
	if scan.Stats.DelayMS != 120 {
		t.Fatalf("пауза в сводке: %d мс", scan.Stats.DelayMS)
	}
}

func TestCrawlCrawlDelayOverridesSetting(t *testing.T) {
	// Сайт попросил ходить реже, чем настроено, — слушаемся его.
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0.2\n"))
			return
		}
		_, _ = w.Write([]byte(html5("страница")))
	})

	limits := testLimits()
	limits.Delay = 10 * time.Millisecond
	limits.UseSitemap = false

	scan, _ := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)

	if scan.Stats.DelayMS != 200 {
		t.Fatalf("crawl-delay сайта не победил настройку: %d мс", scan.Stats.DelayMS)
	}
}

func TestCrawlExcludesByPattern(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/cart/add">корзина</a> <a href="/about">о нас</a>`)))
		default:
			_, _ = w.Write([]byte(html5("страница")))
		}
	})

	limits := testLimits()
	limits.Exclude = CompileExcludes([]string{"/cart/"})

	_, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)

	if _, ok := pageByURL(pages, server.URL+"/cart/add"); ok {
		t.Fatal("исключённый адрес обойдён")
	}
	if _, ok := pageByURL(pages, server.URL+"/about"); !ok {
		t.Fatal("исключение задело лишнее")
	}
}

func TestCrawlTruncatesHugePage(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(html5("<p>" + strings.Repeat("текст ", 20000) + "</p>")))
	})

	limits := testLimits()
	limits.MaxBytes = 2048
	limits.UseSitemap = false

	_, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)

	if len(pages) != 1 {
		t.Fatalf("страниц: %d", len(pages))
	}
	if pages[0].Error != PageErrTooLarge {
		t.Fatalf("обрезка не отмечена: %q", pages[0].Error)
	}
	// Структура всё равно снята — по началу страницы, и об этом сказано
	// кодом ошибки, а не молчанием.
	if pages[0].Structure == nil {
		t.Fatal("у обрезанной страницы нет структуры")
	}
	if pages[0].Bytes > 2048 {
		t.Fatalf("прочитано %d байт сверх потолка", pages[0].Bytes)
	}
}

func TestCrawlNormalizesAddresses(t *testing.T) {
	// Один и тот же адрес в разных написаниях — это одна страница, а не
	// три: иначе потолок страниц уйдёт на якоря.
	requests := 0
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		requests++
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(html5(
				`<a href="/page">раз</a> <a href="/page#top">два</a> <a href="/page">три</a>`)))
			return
		}
		_, _ = w.Write([]byte(html5("страница")))
	})

	limits := testLimits()
	limits.UseSitemap = false

	_, pages := Crawl(context.Background(), HTTPClient(time.Second*5), server.URL+"/", limits)

	if len(pages) != 2 {
		t.Fatalf("страниц: %d — якорь создал лишнюю", len(pages))
	}
	if requests != 2 {
		t.Fatalf("запросов к сайту: %d", requests)
	}
}

// serverURL собирает адрес тестового сервера из запроса: httptest не
// сообщает его обработчику, а карте сайта нужны абсолютные адреса.
func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

// gzipped отдаёт тело сжатым, если клиент об этом попросил, — как nginx и
// Caddy с включённым сжатием, то есть почти любой живой сайт.
func gzipped(w http.ResponseWriter, r *http.Request, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		_, _ = w.Write([]byte(body))
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	writer := gzip.NewWriter(w)
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
}

func TestCrawlReadsCompressedPagesAndSitemap(t *testing.T) {
	// Со спринта 19 обход сам ставил `Accept-Encoding: gzip`, а транспорт Go
	// распаковывает ответ, только если попросил сжатие сам. На сайте со
	// сжатием аудит разбирал сжатые байты как HTML — и видел пустые
	// страницы без title, а карта сайта не читалась вовсе. Нашлось живым
	// обходом vpsfocus.xyz в спринте 94.
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			gzipped(w, r, "text/plain", "User-agent: *\nDisallow:\nSitemap: "+serverURL(r)+"/sitemap.xml\n")
		case "/sitemap.xml":
			gzipped(w, r, "application/xml", urlset(serverURL(r), "/"))
		case "/":
			gzipped(w, r, "text/html; charset=utf-8",
				"<!doctype html><html lang=\"ru\"><head><title>Сжатая страница</title></head><body><h1>Заголовок</h1></body></html>")
		default:
			http.NotFound(w, r)
		}
	})

	scan, pages := Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", testLimits())

	home, ok := pageByURL(pages, server.URL+"/")
	if !ok || home.Structure == nil {
		t.Fatalf("главная не разобрана: %+v", pages)
	}
	if home.Structure.Title != "Сжатая страница" || len(home.Structure.H1) != 1 {
		t.Fatalf("сжатая страница разобрана как пустая: title %q, h1 %v", home.Structure.Title, home.Structure.H1)
	}
	if sitemap := scan.Stats.Site.Sitemap; sitemap == nil || !sitemap.Found || sitemap.Failed != "" {
		t.Fatalf("сжатая карта сайта не прочитана: %+v", sitemap)
	}
}

func TestCrawlReadsGzipSitemapFileEitherWay(t *testing.T) {
	// `.xml.gz` бывает файлом (application/gzip) и файлом, который сервер
	// ещё и отдал с Content-Encoding — транспорт тогда распакует его сам.
	for _, doubleEncoded := range []bool{false, true} {
		server := site(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/robots.txt":
				_, _ = w.Write([]byte("Sitemap: " + serverURL(r) + "/sitemap.xml.gz\n"))
			case "/sitemap.xml.gz":
				var packed strings.Builder
				writer := gzip.NewWriter(&packed)
				_, _ = writer.Write([]byte(urlset(serverURL(r), "/")))
				_ = writer.Close()
				if doubleEncoded {
					w.Header().Set("Content-Encoding", "gzip")
				} else {
					w.Header().Set("Content-Type", "application/gzip")
				}
				_, _ = w.Write([]byte(packed.String()))
			case "/":
				_, _ = w.Write([]byte(html5("главная")))
			default:
				http.NotFound(w, r)
			}
		})

		scan, _ := Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", testLimits())
		if sitemap := scan.Stats.Site.Sitemap; sitemap == nil || !sitemap.Found || sitemap.Addresses != 1 {
			t.Errorf("Content-Encoding=%v: карта не прочитана: %+v", doubleEncoded, sitemap)
		}
	}
}
