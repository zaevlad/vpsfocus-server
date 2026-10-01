package seo

import (
	"net/http"
	"net/url"
	"testing"
)

// testCrawler собирает краулер вручную: проверке входов не нужен ни обход,
// ни robots сайта клиента — она сама читает robots каждого входа.
func testCrawler(t *testing.T, base string) *crawler {
	t.Helper()

	target, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	return &crawler{
		// Тот же клиент, что у обхода: перенаправления не следуются
		// автоматически, иначе цепочку входа не увидеть.
		client: HTTPClient(testLimits().Timeout),
		limits: testLimits(),
		base:   target,
		seen:   map[string]bool{},
		hashes: map[string]string{},
	}
}

func TestSiblingHostAddsAndStripsWWW(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{base: "https://example.com/", want: "https://www.example.com/"},
		{base: "https://www.example.com/", want: "https://example.com/"},
		{base: "https://example.com:8443/", want: "https://www.example.com:8443/"},
		// У поддомена соседа не заводим: `www.shop.example.com` встречается
		// редко, и спрашивать его значит будить чужой сервер впустую.
		{base: "https://shop.example.com/", want: ""},
		// Адрес по IP соседа не имеет вовсе.
		{base: "http://127.0.0.1:8080/", want: ""},
	}

	for _, item := range cases {
		base, err := url.Parse(item.base)
		if err != nil {
			t.Fatal(err)
		}
		sibling := siblingHost(base)

		switch {
		case item.want == "" && sibling != nil:
			t.Errorf("%s: сосед появился там, где его быть не должно: %s",
				item.base, sibling)
		case item.want != "" && sibling == nil:
			t.Errorf("%s: сосед не построился, ждали %s", item.base, item.want)
		case item.want != "" && normalize(sibling) != item.want:
			t.Errorf("%s: сосед %s, ждали %s", item.base, normalize(sibling), item.want)
		}
	}
}

// Вход, отдающий содержимое сам, — это вторая копия сайта в индексе.
func TestEntryServingContentIsSeen(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(html5("копия сайта")))
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	entry := worker.checkEntry(t.Context(), target)
	if entry.Code != EntryServes {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntryServes)
	}
	if entry.Status != http.StatusOK {
		t.Errorf("код ответа %d", entry.Status)
	}
}

// Перенаправление на канонический адрес — это и есть настроенный сайт.
func TestEntryRedirectingToCanonicalIsFine(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			http.Redirect(w, r, "/дом", http.StatusMovedPermanently)
		default:
			_, _ = w.Write([]byte(html5("дом")))
		}
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	entry := worker.checkEntry(t.Context(), target)
	if entry.Code != EntryRedirects {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntryRedirects)
	}
}

// Объявленный canonical — законный способ развести две версии: содержимое
// отдаётся обеими, но поисковику сказано, какая главная.
func TestEntryDelegatingByCanonicalIsFine(t *testing.T) {
	var canonical string
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(
			`<!doctype html><html><head><title>t</title>` +
				`<link rel="canonical" href="` + canonical + `"></head><body>копия</body></html>`))
	})
	canonical = server.URL + "/"

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	entry := worker.checkEntry(t.Context(), target)
	if entry.Code != EntryDelegates {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntryDelegates)
	}
}

// У каждого входа свой robots.txt, и не отдавшийся файл означает «не
// ходить» — по тому же RFC 9309, по которому не начинается обход.
func TestEntryRespectsItsOwnRobots(t *testing.T) {
	var visited int
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		visited++
		_, _ = w.Write([]byte(html5("сюда ходить не должны")))
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	entry := worker.checkEntry(t.Context(), target)
	if entry.Code != EntrySkipped {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntrySkipped)
	}
	if visited != 0 {
		t.Errorf("постучались туда, куда robots.txt не пустил: %d раз", visited)
	}
}

// Закрытый корень — тоже «не ходить», и молчанием это не подменяется:
// «не смотрели» и «посмотрели, всё хорошо» — разные новости.
func TestEntryRespectsDisallowedRoot(t *testing.T) {
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		_, _ = w.Write([]byte(html5("сюда ходить не должны")))
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	if entry := worker.checkEntry(t.Context(), target); entry.Code != EntrySkipped {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntrySkipped)
	}
}

// Петля на входе — это сайт, которого нет ни для поисковика, ни для
// посетителя. Она обязана кончиться отказом, а не бесконечными запросами к
// чужому серверу.
func TestEntryLoopIsCaught(t *testing.T) {
	var hops int
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hops++
		http.Redirect(w, r, "/", http.StatusFound)
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	entry := worker.checkEntry(t.Context(), target)
	if entry.Code != EntryLoop {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntryLoop)
	}
	if hops > maxRedirects+1 {
		t.Errorf("сходили к чужому серверу %d раз при потолке %d", hops, maxRedirects+1)
	}
}

// Сайт, обойдённый по http, на переход к https не проверяется: об открытом
// канале говорит сертификат, а не мы, и вторая находка про то же самое —
// это шум. Соседа по www у адреса с IP тоже нет.
func TestEntriesAreNotInventedForIPOverHTTP(t *testing.T) {
	var visited int
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		visited++
		http.NotFound(w, r)
	})

	worker := testCrawler(t, server.URL+"/")
	report := worker.checkEntries(t.Context())

	if len(report.Entries) != 0 {
		t.Errorf("проверили входы, которых нет: %+v", report.Entries)
	}
	if len(report.Issues) != 0 {
		t.Errorf("нашли находки на пустом месте: %+v", report.Issues)
	}
	if visited != 0 {
		t.Errorf("сходили к серверу %d раз без всякого повода", visited)
	}
}

// Проверка входа не имеет права уйти на чужой хост.
//
// Перенаправление на партнёрский домен утащило бы нас за его robots.txt,
// которого мы не читали, и за его страницами, которые нас не касаются. Это
// то же правило, из-за которого проверка хоста стоит в цикле обхода, — и
// нарушить его здесь было бы тем легче, что цепочку мы проходим сами.
func TestEntryDoesNotFollowRedirectsOffsite(t *testing.T) {
	var foreign int
	partner := site(t, func(w http.ResponseWriter, r *http.Request) {
		foreign++
		_, _ = w.Write([]byte(html5("чужой сайт")))
	})

	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, partner.URL+"/", http.StatusMovedPermanently)
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	entry := worker.checkEntry(t.Context(), target)
	if entry.Code != EntryElsewhere {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntryElsewhere)
	}
	if foreign != 0 {
		t.Errorf("сходили на чужой хост %d раз — краулер ушёл с сайта клиента", foreign)
	}
	if entry.FinalURL == "" {
		t.Error("куда увело, человеку не сказали")
	}
}

// Перенаправление внутри своих проходится: `http://` → `https://` на том же
// имени — это как раз настроенный сайт, и обрывать цепочку на нём значило бы
// объявить бедой правильную настройку.
func TestEntryFollowsRedirectsAmongItsOwnHosts(t *testing.T) {
	var reached int
	server := site(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			http.Redirect(w, r, "/дом", http.StatusMovedPermanently)
		default:
			reached++
			_, _ = w.Write([]byte(html5("дом")))
		}
	})

	worker := testCrawler(t, server.URL+"/")
	target, _ := url.Parse(server.URL + "/")

	if entry := worker.checkEntry(t.Context(), target); entry.Code != EntryRedirects {
		t.Fatalf("исход %s, ждали %s", entry.Code, EntryRedirects)
	}
	if reached == 0 {
		t.Error("своё перенаправление не прошлось")
	}
}
