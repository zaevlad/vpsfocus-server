package ignore

import (
	"errors"
	"strings"
	"testing"

	"agent/internal/links"
	"agent/internal/seo"
)

// Адрес назначения приводится к одному виду (Р26): схема и хост строчными,
// без фрагмента и порта по умолчанию; путь и строка запроса — как есть.
func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"HTTPS://Claude.AI/new", "https://claude.ai/new"},
		{"https://claude.ai:443/new#top", "https://claude.ai/new"},
		{"http://site.test:80/a", "http://site.test/a"},
		{"http://site.test:8080/a", "http://site.test:8080/a"},
		{"https://claude.ai", "https://claude.ai/"},
		// Строка запроса — часть адреса: иначе разные адреса склеятся.
		{"https://site.test/list?page=2", "https://site.test/list?page=2"},
		// Косая черта у адреса назначения — как есть: чужой сервер вправе
		// отвечать на них по-разному.
		{"https://site.test/ru/", "https://site.test/ru/"},
		{"https://site.test/ru", "https://site.test/ru"},
		// Путь регистр не меняет.
		{"https://site.test/About", "https://site.test/About"},
		{"https://[2001:DB8::1]:8443/x", "https://[2001:db8::1]:8443/x"},
		{"https://[2001:db8::1]:443/x", "https://[2001:db8::1]/x"},
	}
	for _, item := range cases {
		got, ok := NormalizeURL(item.in)
		if !ok || got != item.want {
			t.Errorf("NormalizeURL(%q) = %q, %v; ожидалось %q", item.in, got, ok, item.want)
		}
	}
	for _, bad := range []string{"", "/relative", "ftp://site.test/", "mailto:a@site.test", "https://user:pw@site.test/"} {
		if _, ok := NormalizeURL(bad); ok {
			t.Errorf("NormalizeURL(%q) принял негодный адрес", bad)
		}
	}
}

// «Где» — путь страницы: `/ru` и `/ru/` — одна страница сайта, строка
// запроса остаётся, хост отбрасывается.
func TestPagePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://site.test/ru/", "/ru"},
		{"https://site.test/ru", "/ru"},
		{"/ru/", "/ru"},
		{"https://site.test", "/"},
		{"https://site.test/", "/"},
		{"https://site.test/list/?page=2", "/list?page=2"},
		{"https://site.test/a#part", "/a"},
		{"", ""},
	}
	for _, item := range cases {
		if got := PagePath(item.in); got != item.want {
			t.Errorf("PagePath(%q) = %q; ожидалось %q", item.in, got, item.want)
		}
	}
}

func mustNormalize(t *testing.T, entry Entry) Entry {
	t.Helper()
	normalized, err := Normalize(entry)
	if err != nil {
		t.Fatalf("Normalize(%+v): %v", entry, err)
	}
	return normalized
}

// Совпадение битой ссылки по всем видам охвата (Р3, Р4, Р22).
func TestCoversLink(t *testing.T) {
	exact := mustNormalize(t, Entry{Kind: KindLink, Domain: "Site.Test", What: "HTTPS://claude.ai/new#x", Response: "403", Scope: ScopeSite})
	prefix := mustNormalize(t, Entry{Kind: KindLink, Domain: "site.test", What: "https://claude.ai", Match: MatchPrefix, Response: "403", Scope: ScopeSite})
	page := mustNormalize(t, Entry{Kind: KindLink, Domain: "site.test", What: "https://claude.ai/new", Response: "403", Scope: ScopePage, At: "https://site.test/download/"})
	under := mustNormalize(t, Entry{Kind: KindLink, Domain: "site.test", What: "https://claude.ai/new", Response: "403", Scope: ScopePrefix, At: "/ru/"})
	timeout := mustNormalize(t, Entry{Kind: KindLink, Domain: "site.test", What: "https://slow.test/", Response: "Timeout", Scope: ScopeSite})

	if exact.Domain != "site.test" || exact.What != "https://claude.ai/new" {
		t.Fatalf("запись не приведена: %+v", exact)
	}
	if under.At != "/ru" || page.At != "/download" {
		t.Fatalf("«где» не приведено: %q, %q", under.At, page.At)
	}

	cases := []struct {
		name     string
		entry    Entry
		target   string
		response string
		source   string
		want     bool
	}{
		{"адрес целиком", exact, "https://claude.ai/new", "403", "https://site.test/", true},
		{"регистр хоста назначения", exact, "https://CLAUDE.ai/new", "403", "https://site.test/", true},
		{"другой адрес", exact, "https://claude.ai/old", "403", "https://site.test/", false},
		// Изменился ответ — это уже другая беда (Р3).
		{"другой ответ", exact, "https://claude.ai/new", "404", "https://site.test/", false},
		{"тайм-аут вместо 403", exact, "https://claude.ai/new", "timeout", "https://site.test/", false},
		{"начало адреса", prefix, "https://claude.ai/chat/1", "403", "https://site.test/", true},
		{"начало адреса и чужой ответ", prefix, "https://claude.ai/chat/1", "404", "https://site.test/", false},
		// Начало «https://claude.ai» — до косой черты: соседний хост не
		// покрывается.
		{"соседний хост", prefix, "https://claude.ai.example/", "403", "https://site.test/", false},
		{"одна страница", page, "https://claude.ai/new", "403", "https://site.test/download", true},
		{"та же страница с косой чертой", page, "https://claude.ai/new", "403", "https://site.test/download/", true},
		{"другая страница", page, "https://claude.ai/new", "403", "https://site.test/", false},
		{"начало пути", under, "https://claude.ai/new", "403", "https://site.test/ru/download", true},
		{"начало пути — сама страница", under, "https://claude.ai/new", "403", "https://site.test/ru", true},
		// Граница сегмента: `/ru` не берёт `/rus`.
		{"граница сегмента", under, "https://claude.ai/new", "403", "https://site.test/rus", false},
		{"вид сбоя без кода", timeout, "https://slow.test", "timeout", "https://site.test/", true},
	}
	for _, item := range cases {
		if got := item.entry.CoversLink(item.target, item.response, item.source); got != item.want {
			t.Errorf("%s: CoversLink = %v, ожидалось %v", item.name, got, item.want)
		}
	}
}

// SEO: весь сайт, одна страница, начало адреса; находка о сайте целиком —
// только записью «весь сайт».
func TestCoversSeo(t *testing.T) {
	site := mustNormalize(t, Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ScopeSite})
	account := mustNormalize(t, Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ScopePrefix, At: "/account/"})
	pricing := mustNormalize(t, Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueTitleLong, Scope: ScopePage, At: "/pricing"})

	cases := []struct {
		name  string
		entry Entry
		code  string
		page  string
		want  bool
	}{
		{"весь сайт", site, seo.IssueNoindex, "https://site.test/any", true},
		{"весь сайт, другая проверка", site, seo.IssueTitleLong, "https://site.test/any", false},
		{"начало адреса", account, seo.IssueNoindex, "https://site.test/account/settings", true},
		{"начало адреса, другой раздел", account, seo.IssueNoindex, "https://site.test/accounts", false},
		{"начало адреса, находка о сайте", account, seo.IssueNoindex, "", false},
		{"одна страница", pricing, seo.IssueTitleLong, "https://site.test/pricing/", true},
		{"одна страница, со строкой запроса", pricing, seo.IssueTitleLong, "https://site.test/pricing?x=1", false},
	}
	for _, item := range cases {
		if got := item.entry.CoversSeo(item.code, item.page); got != item.want {
			t.Errorf("%s: CoversSeo = %v, ожидалось %v", item.name, got, item.want)
		}
	}
}

// Запись, которую нельзя принять, — отказ ignoreInvalid, а не молча
// сохранённая чепуха.
func TestNormalizeRefuses(t *testing.T) {
	cases := []struct {
		name  string
		entry Entry
	}{
		{"нет домена", Entry{Kind: KindSeo, What: seo.IssueNoindex, Scope: ScopeSite}},
		{"незнакомый вид", Entry{Kind: "probe", Domain: "site.test", What: "x", Scope: ScopeSite}},
		{"незнакомая проверка", Entry{Kind: KindSeo, Domain: "site.test", What: "noSuchCheck", Scope: ScopeSite}},
		// Проверки о сайте целиком страниц не имеют (Р5).
		{"robots.txt на странице", Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueRobotsMissing, Scope: ScopePage, At: "/a"}},
		{"ссылка без ответа", Entry{Kind: KindLink, Domain: "site.test", What: "https://a.test/", Scope: ScopeSite}},
		{"ссылка не http", Entry{Kind: KindLink, Domain: "site.test", What: "mailto:a@b.test", Response: "403", Scope: ScopeSite}},
		{"ответ чепухой", Entry{Kind: KindLink, Domain: "site.test", What: "https://a.test/", Response: "4 03", Scope: ScopeSite}},
		{"незнакомое сравнение", Entry{Kind: KindLink, Domain: "site.test", What: "https://a.test/", Response: "403", Match: "regex", Scope: ScopeSite}},
		// Начало «/» — это весь сайт, у него свой охват.
		{"начало адреса — корень", Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ScopePrefix, At: "/"}},
		{"страница без пути", Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ScopePage}},
		{"страница не путём", Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ScopePage, At: "pricing"}},
		{"весь сайт со страницей", Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ScopeSite, At: "/a"}},
		{"незнакомый охват", Entry{Kind: KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: "everywhere"}},
		{"совет не id", Entry{Kind: KindAdvice, Domain: "site.test", What: "Render Blocking", Scope: ScopeSite}},
	}
	for _, item := range cases {
		if _, err := Normalize(item.entry); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: ожидался отказ ignoreInvalid, получено %v", item.name, err)
		}
	}

	// Длинная заметка обрезается по знакам, а не по байтам.
	long := mustNormalize(t, Entry{Kind: KindAdvice, Domain: "site.test", What: "unused-javascript", Scope: ScopeSite, Note: strings.Repeat("я", 600)})
	if len([]rune(long.Note)) != noteMax {
		t.Fatalf("заметка: %d знаков", len([]rune(long.Note)))
	}
}

// Пример с vpsfocus.xyz (2026-11-12): восемь «битых» ссылок, шесть из них —
// claude.ai с ответом 403. Запись «начало https://claude.ai/, 403» убирает
// шесть; ссылка, которая на части страниц учтена, делится.
func TestLinksSplit(t *testing.T) {
	failures := []links.Failure{
		{URL: "https://claude.ai/new", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/", "https://site.test/download"}},
		{URL: "https://claude.ai/login", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/ru/"}},
		{URL: "https://claude.ai/a", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/de/"}},
		{URL: "https://claude.ai/b", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/fr/"}},
		{URL: "https://claude.ai/c", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/es/"}},
		{URL: "https://claude.ai/d", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/pt/"}},
		{URL: "https://leginfo.legislature.ca.gov/x", Kind: "httpStatus", Code: 403, Sources: []string{"https://site.test/compliance", "https://site.test/pricing"}},
		{URL: "https://gone.test/", Kind: "httpStatus", Code: 404, Sources: []string{"https://site.test/"}},
	}
	list := List{
		mustNormalize(t, Entry{Kind: KindLink, Domain: "site.test", What: "https://claude.ai/", Match: MatchPrefix, Response: "403", Scope: ScopeSite}),
		mustNormalize(t, Entry{Kind: KindLink, Domain: "site.test", What: "https://leginfo.legislature.ca.gov/x", Response: "403", Scope: ScopePrefix, At: "/compliance"}),
		// Запись другого сайта сюда не относится.
		mustNormalize(t, Entry{Kind: KindLink, Domain: "other.test", What: "https://gone.test/", Response: "404", Scope: ScopeSite}),
	}

	report := Links(links.Report{Domain: "site.test", Broken: 8, Failures: failures}, list)
	if report.Broken != 2 || report.Ignored != 7 {
		t.Fatalf("broken = %d, ignored = %d; ожидалось 2 и 7", report.Broken, report.Ignored)
	}
	// leginfo учтена со страницы /pricing и не учтена с /compliance.
	var kept, ignored []string
	for _, failure := range report.Failures {
		kept = append(kept, failure.URL+" "+strings.Join(failure.Sources, ","))
	}
	for _, failure := range report.IgnoredFailures {
		if failure.URL == "https://leginfo.legislature.ca.gov/x" {
			ignored = append(ignored, strings.Join(failure.Sources, ","))
		}
	}
	if strings.Join(kept, "|") != "https://leginfo.legislature.ca.gov/x https://site.test/pricing|https://gone.test/ https://site.test/" {
		t.Fatalf("учтённые: %v", kept)
	}
	if strings.Join(ignored, "|") != "https://site.test/compliance" {
		t.Fatalf("неучтённая часть leginfo: %v", ignored)
	}

	// Без записей сайта отчёт прежний, а список неучтённых — пустой, не null.
	plain := Links(links.Report{Domain: "plain.test", Broken: 1, Failures: failures[:1]}, list)
	if plain.Broken != 1 || plain.Ignored != 0 || plain.IgnoredFailures == nil || len(plain.Failures) != 1 {
		t.Fatalf("отчёт без записей изменился: %+v", plain)
	}
}
