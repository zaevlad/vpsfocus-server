package seo

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// siteCodes — коды находок о сайте, по порядку.
func siteCodes(scan Scan) []string {
	if scan.Stats.Site == nil {
		return nil
	}
	var list []string
	for _, issue := range scan.Stats.Site.Issues {
		list = append(list, issue.Code)
	}
	return list
}

// siteIssue — находка о сайте по коду.
func siteIssue(scan Scan, code string) (Issue, bool) {
	if scan.Stats.Site == nil {
		return Issue{}, false
	}
	for _, issue := range scan.Stats.Site.Issues {
		if issue.Code == code {
			return issue, true
		}
	}
	return Issue{}, false
}

// sitemapCodes — находки страницы, выведенные из карты сайта.
func sitemapCodes(page Page) []string {
	var list []string
	for _, issue := range page.Issues {
		switch issue.Code {
		case IssueSitemapBroken, IssueSitemapRedirect, IssueSitemapNoindex,
			IssueSitemapCanonicalised, IssueNotInSitemap:
			list = append(list, issue.Code)
		}
	}
	sort.Strings(list)
	return list
}

// urlset — карта сайта из путей.
func urlset(base string, paths ...string) string {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset>`)
	for _, path := range paths {
		body.WriteString("<url><loc>" + base + path + "</loc></url>")
	}
	body.WriteString("</urlset>")
	return body.String()
}

func crawlSite(t *testing.T, limits Limits, handler http.HandlerFunc) (Scan, []Page) {
	t.Helper()
	server := site(t, handler)
	return Crawl(context.Background(), HTTPClient(5*time.Second), server.URL+"/", limits)
}

func TestSiteSitemapDisagreementsAreFound(t *testing.T) {
	// Весь набор сразу: карта из двух файлов, в ней адрес-404,
	// адрес-перенаправление, закрытая страница и страница-копия; на сайте
	// есть страница, которой в карте нет. GPTBot закрыт целиком, админка
	// закрыта для всех. Ждём ровно эти находки и ни одной сверх.
	scan, pages := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		base := serverURL(r)
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /admin/\n\nUser-agent: GPTBot\nDisallow: /\n\nSitemap: " + base + "/sitemap-index.xml\n"))
		case "/sitemap-index.xml":
			_, _ = w.Write([]byte(`<sitemapindex><sitemap><loc>` + base + `/sitemap-1.xml</loc></sitemap><sitemap><loc>` + base + `/sitemap-2.xml</loc></sitemap></sitemapindex>`))
		case "/sitemap-1.xml":
			_, _ = w.Write([]byte(urlset(base, "/", "/ok", "/gone")))
		case "/sitemap-2.xml":
			_, _ = w.Write([]byte(urlset(base, "/moved", "/closed", "/copy")))
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/ok">ok</a> <a href="/extra">extra</a> <a href="/admin/">админка</a>`)))
		case "/ok", "/extra", "/final":
			_, _ = w.Write([]byte(html5("страница")))
		case "/moved":
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
		case "/closed":
			_, _ = w.Write([]byte(`<html><head><title>t</title><meta name="robots" content="noindex"></head><body>закрыта</body></html>`))
		case "/copy":
			_, _ = w.Write([]byte(`<html><head><title>t</title><link rel="canonical" href="` + base + `/ok"></head><body>копия</body></html>`))
		default:
			http.NotFound(w, r)
		}
	})

	if scan.Error != "" {
		t.Fatalf("обход не состоялся: %s", scan.Error)
	}
	if got := strings.Join(siteCodes(scan), ","); got != IssueAITrainingBlocked {
		t.Fatalf("находки о сайте: %q", got)
	}
	if issue, _ := siteIssue(scan, IssueAITrainingBlocked); issue.Detail != "GPTBot" {
		t.Fatalf("закрытые боты обучения: %q", issue.Detail)
	}

	sitemap := scan.Stats.Site.Sitemap
	if sitemap == nil || !sitemap.Declared || !sitemap.Found || !sitemap.Complete {
		t.Fatalf("карта сайта: %+v", sitemap)
	}
	if sitemap.Addresses != 6 {
		t.Fatalf("адресов в карте: %d", sitemap.Addresses)
	}

	base := strings.TrimSuffix(scan.Stats.Site.Sitemap.Address, "/sitemap-index.xml")
	want := map[string]string{
		"/":       "",
		"/ok":     "",
		"/gone":   IssueSitemapBroken,
		"/moved":  IssueSitemapRedirect,
		"/closed": IssueSitemapNoindex,
		"/copy":   IssueSitemapCanonicalised,
		"/extra":  IssueNotInSitemap,
	}
	for path, code := range want {
		page, ok := pageByURL(pages, base+path)
		if !ok {
			t.Fatalf("страница %s не обойдена", path)
		}
		if got := strings.Join(sitemapCodes(page), ","); got != code {
			t.Errorf("%s: находки карты %q, ждали %q", path, got, code)
		}
	}
}

func TestSiteSearchEnginesBlockedIsCritical(t *testing.T) {
	// Сайт закрыт для всех, кроме нас: обход состоится, а Google, Bing и
	// Яндекс сайта не увидят.
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n\nUser-agent: vpsfocus-seo\nAllow: /\n"))
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			http.NotFound(w, r)
		}
	})

	issue, ok := siteIssue(scan, IssueSearchEnginesBlocked)
	if !ok {
		t.Fatalf("закрытый от поисковиков сайт не замечен: %v", siteCodes(scan))
	}
	if issue.Severity != SeverityCritical {
		t.Fatalf("уровень: %s", issue.Severity)
	}
	if issue.Detail != "Googlebot, Bingbot, YandexBot" {
		t.Fatalf("закрытые поисковики: %q", issue.Detail)
	}
	if siteCodes(scan)[0] != IssueSearchEnginesBlocked {
		t.Fatalf("критичная находка не первая: %v", siteCodes(scan))
	}
	for _, code := range []string{IssueAISearchBlocked, IssueAITrainingBlocked} {
		if _, ok := siteIssue(scan, code); !ok {
			t.Errorf("нет находки %s", code)
		}
	}
	for _, bot := range scan.Stats.Site.Bots {
		if bot.Allowed {
			t.Errorf("бот %s числится открытым", bot.Name)
		}
	}
}

func TestSiteYandexGroupIsFoundByPrefix(t *testing.T) {
	// Правило для Яндекса пишут на «Yandex», а бот зовётся YandexBot.
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: Yandex\nDisallow: /\n"))
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			http.NotFound(w, r)
		}
	})

	if issue, _ := siteIssue(scan, IssueSearchEnginesBlocked); issue.Detail != "YandexBot" {
		t.Fatalf("закрытый Яндекс: %q, находки %v", issue.Detail, siteCodes(scan))
	}
}

func TestSitePartialDisallowIsNotBlocking(t *testing.T) {
	// Закрытая админка — норма, а не «сайт закрыт от Google».
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /admin/\nDisallow: /cart\n"))
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			http.NotFound(w, r)
		}
	})

	for _, code := range []string{IssueSearchEnginesBlocked, IssueAISearchBlocked, IssueAITrainingBlocked} {
		if _, ok := siteIssue(scan, code); ok {
			t.Errorf("частичный запрет дал находку %s", code)
		}
	}
	if len(scan.Stats.Site.Bots) != len(knownBots) {
		t.Fatalf("ботов в отчёте %d — открытые тоже показываются", len(scan.Stats.Site.Bots))
	}
}

func TestSiteMissingRobotsAndSitemap(t *testing.T) {
	scan, pages := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/a">a</a>`)))
		case "/a":
			_, _ = w.Write([]byte(html5("a")))
		default:
			http.NotFound(w, r)
		}
	})

	if got := strings.Join(siteCodes(scan), ","); got != IssueSitemapMissing+","+IssueRobotsMissing {
		t.Fatalf("находки о сайте: %q", got)
	}
	if scan.Stats.Site.Robots != RobotsAbsent {
		t.Fatalf("robots.txt: %s", scan.Stats.Site.Robots)
	}
	// Карты нет — сверять не с чем, и «страницы нет в карте» не говорится.
	for _, page := range pages {
		if codes := sitemapCodes(page); len(codes) > 0 {
			t.Errorf("%s: %v", page.URL, codes)
		}
	}
}

func TestSiteHTMLInsteadOfSitemapIsMissing(t *testing.T) {
	// Сайт на одной странице отвечает главной на любой адрес, в том числе
	// на /sitemap.xml. Это не карта.
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nDisallow:\n"))
			return
		}
		_, _ = w.Write([]byte(html5("приложение")))
	})

	if got := strings.Join(siteCodes(scan), ","); got != IssueSitemapMissing {
		t.Fatalf("находки о сайте: %q", got)
	}
	if scan.Stats.Site.Sitemap.Found {
		t.Fatal("HTML-страница принята за карту сайта")
	}
}

func TestSiteDeclaredSitemapFailing(t *testing.T) {
	scan, pages := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("Sitemap: " + serverURL(r) + "/sitemap.xml\n"))
		case "/sitemap.xml":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			http.NotFound(w, r)
		}
	})

	issue, ok := siteIssue(scan, IssueSitemapUnavailable)
	if !ok || !strings.HasSuffix(issue.Detail, "/sitemap.xml") {
		t.Fatalf("сломанная карта: %v %q", siteCodes(scan), issue.Detail)
	}
	if _, ok := siteIssue(scan, IssueSitemapMissing); ok {
		t.Fatal("объявленная, но сломанная карта названа отсутствующей")
	}
	for _, page := range pages {
		if codes := sitemapCodes(page); len(codes) > 0 {
			t.Errorf("%s: %v", page.URL, codes)
		}
	}
}

func TestSiteSitemapBeyondBudgetStillCounts(t *testing.T) {
	// В очередь из карты идёт половина потолка страниц, а карта читается
	// целиком: страница из карты сверх бюджета, найденная по ссылке, в
	// карте есть.
	limits := testLimits()
	limits.MaxPages = 4
	scan, pages := crawlSite(t, limits, func(w http.ResponseWriter, r *http.Request) {
		base := serverURL(r)
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("Sitemap: " + base + "/sitemap.xml\n"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(urlset(base, "/", "/a", "/b", "/c", "/d", "/e")))
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/e">e</a>`)))
		default:
			_, _ = w.Write([]byte(html5("страница")))
		}
	})

	sitemap := scan.Stats.Site.Sitemap
	if sitemap.Addresses != 6 || !sitemap.Complete {
		t.Fatalf("карта: %+v", sitemap)
	}
	for _, page := range pages {
		if !page.InSitemap {
			t.Errorf("%s числится вне карты", page.URL)
		}
		if codes := sitemapCodes(page); len(codes) > 0 {
			t.Errorf("%s: %v", page.URL, codes)
		}
	}
}

func TestSiteIncompleteSitemapSaysNothingAboutAbsence(t *testing.T) {
	// Список из одиннадцати карт: читаем десять файлов и упираемся в потолок.
	// Страницу, которой нет в прочитанном, «вне карты» не объявляем — мы
	// просто не дочитали.
	scan, pages := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		base := serverURL(r)
		switch {
		case r.URL.Path == "/robots.txt":
			_, _ = w.Write([]byte("Sitemap: " + base + "/sitemap-index.xml\n"))
		case r.URL.Path == "/sitemap-index.xml":
			var body strings.Builder
			body.WriteString("<sitemapindex>")
			for index := 1; index <= 11; index++ {
				body.WriteString(fmt.Sprintf("<sitemap><loc>%s/part-%d.xml</loc></sitemap>", base, index))
			}
			body.WriteString("</sitemapindex>")
			_, _ = w.Write([]byte(body.String()))
		case strings.HasPrefix(r.URL.Path, "/part-"):
			_, _ = w.Write([]byte(urlset(base, "/")))
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(html5(`<a href="/unlisted">u</a>`)))
		default:
			_, _ = w.Write([]byte(html5("страница")))
		}
	})

	if scan.Stats.Site.Sitemap.Complete {
		t.Fatal("карта сверх потолка файлов названа прочитанной целиком")
	}
	for _, page := range pages {
		if _, ok := found(page, IssueNotInSitemap); ok {
			t.Errorf("%s объявлена вне недочитанной карты", page.URL)
		}
	}
}

func TestSiteSitemapDisabledIsNotMissing(t *testing.T) {
	limits := testLimits()
	limits.UseSitemap = false
	scan, _ := crawlSite(t, limits, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow:\n"))
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			http.NotFound(w, r)
		}
	})

	if scan.Stats.Site.Sitemap != nil {
		t.Fatal("выключенная сверка с картой всё равно дала отчёт о карте")
	}
	if codes := siteCodes(scan); len(codes) > 0 {
		t.Fatalf("находки о сайте при выключенной карте: %v", codes)
	}
}

func TestSiteSitemapBlockedByRobots(t *testing.T) {
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		base := serverURL(r)
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /closed\nSitemap: " + base + "/sitemap.xml\n"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(urlset(base, "/", "/closed", "/closed-too")))
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			t.Errorf("запрос к запрещённому адресу %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	issue, ok := siteIssue(scan, IssueSitemapBlockedByRobots)
	if !ok || issue.Detail != "2" {
		t.Fatalf("закрытые адреса карты: %v %q", siteCodes(scan), issue.Detail)
	}
}

func TestAuditSitemapBrokenAccompaniesUnavailable(t *testing.T) {
	// Правки разные: страницу чинят, адрес из карты убирают.
	page := Page{URL: "https://site.test/gone", Status: 404, Error: PageErrStatus, InSitemap: true}
	pages := []Page{page}
	Audit(pages)

	if got := codes(pages[0]); got != IssuePageUnavailable+","+IssueSitemapBroken {
		t.Fatalf("находки: %s", got)
	}
}

func TestSiteSummaryLeavesSiteIssuesOut(t *testing.T) {
	// Находки о сайте живут в своём отчёте: в разрез «сколько страниц» они
	// не входят — страниц у них нет.
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(html5("главная")))
		default:
			http.NotFound(w, r)
		}
	})

	for _, item := range scan.Stats.Issues.ByCode {
		if item.Code == IssueRobotsMissing || item.Code == IssueSitemapMissing {
			t.Fatalf("находка о сайте в разрезе страниц: %s", item.Code)
		}
	}
}
