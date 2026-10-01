package seo

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// auditOne разбирает одну страницу вместе с соседом, чтобы проверки
// «на всём обходе» (тупик) видели сайт больше одной страницы.
func auditOne(page Page) Page {
	neighbour := goodPage("https://site.test/neighbour")
	neighbour.Structure.Title = "Соседняя страница с собственным заголовком"
	neighbour.Structure.MetaDescription = strings.Repeat("соседнее описание ", 6)
	pages := []Page{page, neighbour}
	Audit(pages)
	return pages[0]
}

func TestHygieneCleanPageStaysClean(t *testing.T) {
	// Эталон молчит: иначе каждая проверка ниже доказывала бы только то,
	// что находки сыплются на всё подряд.
	page := auditOne(goodPage("https://site.test/catalog/shoes"))
	if len(page.Issues) != 0 {
		t.Fatalf("находки у чистой страницы: %s", codes(page))
	}
}

func TestHygieneRobotsDirectives(t *testing.T) {
	page := goodPage("https://site.test/a")
	page.Structure.MetaRobots = "nosnippet, noimageindex, noarchive"
	page = auditOne(page)

	for _, code := range []string{IssueRobotsNosnippet, IssueRobotsNoimageindex} {
		if _, ok := found(page, code); !ok {
			t.Errorf("нет находки %s: %s", code, codes(page))
		}
	}
	// noarchive ни на что не влияет: кеша Google больше нет.
	if strings.Contains(codes(page), "archive") {
		t.Fatalf("noarchive дал находку: %s", codes(page))
	}
}

func TestHygieneDirectiveFromHeaderCounts(t *testing.T) {
	page := goodPage("https://site.test/a")
	page.Structure.XRobotsTag = "googlebot: nosnippet"
	page = auditOne(page)

	if _, ok := found(page, IssueRobotsNosnippet); !ok {
		t.Fatalf("указание из заголовка не замечено: %s", codes(page))
	}
}

func TestHygieneNoindexWithCanonical(t *testing.T) {
	page := goodPage("https://site.test/copy")
	page.Structure.Noindex = true
	page.Structure.Canonical = "https://site.test/original"
	page.Structure.CanonicalSelf = false
	page = auditOne(page)

	issue, ok := found(page, IssueNoindexWithCanonical)
	if !ok || issue.Detail != "https://site.test/original" {
		t.Fatalf("находка: %+v, все: %s", issue, codes(page))
	}
}

func TestHygieneUnavailableAfter(t *testing.T) {
	defer func(previous func() time.Time) { now = previous }(now)
	now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

	cases := []struct {
		directive string
		passed    bool
	}{
		// RFC 850 — с запятой внутри даты.
		{"noodp, unavailable_after: Friday, 25-Jun-10 15:00:00 UTC", true},
		{"unavailable_after: 2025-01-01", true},
		{"unavailable_after: 2027-01-01T00:00:00Z", false},
		{"unavailable_after: 25 Jun 2030 15:00:00 PST, nosnippet", false},
		// Неразобранная дата — не повод говорить «страница выпала».
		{"unavailable_after: когда-нибудь", false},
	}
	for _, item := range cases {
		page := goodPage("https://site.test/a")
		page.Structure.MetaRobots = item.directive
		page = auditOne(page)

		_, ok := found(page, IssueUnavailableAfterPassed)
		if ok != item.passed {
			t.Errorf("%q: находка %v, ждали %v", item.directive, ok, item.passed)
		}
	}
}

func TestHygieneSoft404Title(t *testing.T) {
	for _, title := range []string{"Страница не найдена", "404 — нет такой страницы", "Page Not Found | Shop"} {
		page := goodPage("https://site.test/a")
		page.Structure.Title = title
		page = auditOne(page)

		issue, ok := found(page, IssueSoft404Title)
		if !ok || issue.Detail != title {
			t.Errorf("%q: %s", title, codes(page))
		}
	}

	// «404» внутри слова — не ошибка, а модель товара.
	page := goodPage("https://site.test/a")
	page.Structure.Title = "Кроссовки X404 для бега по пересечённой местности"
	if _, ok := found(auditOne(page), IssueSoft404Title); ok {
		t.Fatal("модель X404 принята за страницу ошибки")
	}

	// Закрытая от индексации страница ошибки поиску не вредит.
	page = goodPage("https://site.test/a")
	page.Structure.Title = "Страница не найдена"
	page.Structure.Noindex = true
	if _, ok := found(auditOne(page), IssueSoft404Title); ok {
		t.Fatal("закрытая от индексации страница ошибки дала находку")
	}
}

func TestHygieneSlowResponse(t *testing.T) {
	page := goodPage("https://site.test/a")
	page.LoadMS = 3400
	page = auditOne(page)
	if issue, ok := found(page, IssueSlowResponse); !ok || issue.Detail != "3400" {
		t.Fatalf("медленный ответ: %s", codes(page))
	}

	page = goodPage("https://site.test/a")
	page.LoadMS = 2999
	if _, ok := found(auditOne(page), IssueSlowResponse); ok {
		t.Fatal("ответ быстрее порога назван медленным")
	}
}

func TestHygieneH1(t *testing.T) {
	long := goodPage("https://site.test/a")
	long.Structure.H1 = []string{strings.Repeat("слово ", 15)}
	if _, ok := found(auditOne(long), IssueH1Long); !ok {
		t.Fatal("длинный H1 не замечен")
	}

	first := goodPage("https://site.test/a")
	second := goodPage("https://site.test/b")
	first.Structure.H1 = []string{"Один и тот же"}
	second.Structure.H1 = []string{"Один и тот же"}
	// Закрытая копия с тем же H1 законна и в дубли не идёт.
	closed := goodPage("https://site.test/c")
	closed.Structure.H1 = []string{"Один и тот же"}
	closed.Structure.Noindex = true

	pages := []Page{first, second, closed}
	Audit(pages)
	issue, ok := found(pages[0], IssueH1Duplicate)
	if !ok || issue.Detail != "https://site.test/b" {
		t.Fatalf("дубль H1: %+v", issue)
	}
	if _, ok := found(pages[2], IssueH1Duplicate); ok {
		t.Fatal("закрытая страница попала в дубли H1")
	}
}

func TestHygieneLinks(t *testing.T) {
	dead := goodPage("https://site.test/a")
	dead.Structure.Links = LinkCounts{External: 3}
	if _, ok := found(auditOne(dead), IssueDeadEnd); !ok {
		t.Fatal("тупик не замечен")
	}

	// Одностраничному сайту ссылаться некуда.
	single := []Page{dead}
	single[0].Issues = nil
	Audit(single)
	if _, ok := found(single[0], IssueDeadEnd); ok {
		t.Fatal("одностраничный сайт назван тупиком")
	}

	crowded := goodPage("https://site.test/a")
	crowded.Structure.Links = LinkCounts{Internal: 250, External: 60}
	if issue, ok := found(auditOne(crowded), IssueTooManyLinks); !ok || issue.Detail != "310" {
		t.Fatalf("много ссылок: %+v", issue)
	}
}

func TestHygieneURLShape(t *testing.T) {
	cases := map[string]string{
		"https://site.test/Catalog/Shoes":               "A-Z",
		"https://site.test/big_shoes":                   "_",
		"https://site.test/big%20shoes":                 "%20",
		"https://site.test/catalog//shoes":              "//",
		"https://site.test/" + strings.Repeat("a", 120): ">115",
		"https://site.test/Big_Shoes":                   "A-Z _",
	}
	for address, want := range cases {
		issue, ok := found(auditOne(goodPage(address)), IssueURLShape)
		if !ok || issue.Detail != want {
			t.Errorf("%s: %q, ждали %q", address, issue.Detail, want)
		}
	}

	// Кириллица в адресе закодирована заглавными `%D0%9F`, но заглавных
	// букв в пути нет.
	if _, ok := found(auditOne(goodPage("https://site.test/%D0%BE%D0%B1%D1%83%D0%B2%D1%8C")), IssueURLShape); ok {
		t.Fatal("закодированная кириллица принята за заглавные буквы")
	}
}

func TestHygieneURLTrackingAndRepeatsAndSearch(t *testing.T) {
	tracked := auditOne(goodPage("https://site.test/a?utm_source=menu&yclid=1&page=2"))
	if issue, ok := found(tracked, IssueURLTracking); !ok || issue.Detail != "utm_source, yclid" {
		t.Fatalf("метки: %+v", issue)
	}

	repeated := auditOne(goodPage("https://site.test/team/about/team/"))
	if issue, ok := found(repeated, IssueURLRepeatingSegment); !ok || issue.Detail != "team" {
		t.Fatalf("повтор сегмента: %+v", issue)
	}

	for _, address := range []string{"https://site.test/?s=обувь", "https://site.test/search/?q=1"} {
		if _, ok := found(auditOne(goodPage(address)), IssueInternalSearchCrawled); !ok {
			t.Errorf("%s: поиск по сайту не замечен", address)
		}
	}
	closed := goodPage("https://site.test/?s=обувь")
	closed.Structure.Noindex = true
	if _, ok := found(auditOne(closed), IssueInternalSearchCrawled); ok {
		t.Fatal("закрытая от индексации страница поиска дала находку")
	}
}

func TestHygieneAddressTwins(t *testing.T) {
	pages := []Page{
		goodPage("https://site.test/Shoes"),
		goodPage("https://site.test/shoes"),
		goodPage("https://site.test/bags/"),
		goodPage("https://site.test/bags"),
	}
	// Перенаправление на один вариант — правильная настройка.
	redirected := goodPage("https://site.test/hats/")
	redirected.Redirects = []string{"https://site.test/hats"}
	pages = append(pages, redirected, goodPage("https://site.test/hats"))
	// Как и canonical на один вариант.
	delegating := goodPage("https://site.test/Coats")
	delegating.Structure.Canonical = "https://site.test/coats"
	delegating.Structure.CanonicalSelf = false
	pages = append(pages, delegating, goodPage("https://site.test/coats"))

	Audit(pages)

	if issue, ok := found(pages[1], IssueURLCaseDuplicate); !ok || issue.Detail != "https://site.test/Shoes" {
		t.Fatalf("регистр: %+v", issue)
	}
	if issue, ok := found(pages[2], IssueURLSlashDuplicate); !ok || issue.Detail != "https://site.test/bags" {
		t.Fatalf("косая черта: %+v", issue)
	}
	for _, index := range []int{4, 5, 6, 7} {
		for _, code := range []string{IssueURLCaseDuplicate, IssueURLSlashDuplicate} {
			if _, ok := found(pages[index], code); ok {
				t.Errorf("%s: %s при правильной настройке", pages[index].URL, code)
			}
		}
	}
}

func TestCrawlLoadTimeExcludesOurPause(t *testing.T) {
	// Пауза — наша вежливость. С ней в времени ответа каждая страница
	// сайта с Crawl-delay выглядела бы медленной.
	scan, pages := crawlSite(t, func() Limits {
		limits := testLimits()
		limits.Delay = 400 * time.Millisecond
		limits.UseSitemap = false
		return limits
	}(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/a">a</a>`)))
		default:
			_, _ = w.Write([]byte(html5("страница")))
		}
	})

	if scan.Error != "" || len(pages) != 2 {
		t.Fatalf("обход: %s, страниц %d", scan.Error, len(pages))
	}
	for _, page := range pages {
		if page.LoadMS >= 300 {
			t.Errorf("%s отвечала %d мс — в них сидит наша пауза", page.URL, page.LoadMS)
		}
	}
}
