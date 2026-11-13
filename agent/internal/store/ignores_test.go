package store

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"agent/internal/ignore"
	"agent/internal/links"
	"agent/internal/seo"
)

func ignoreEntry(t *testing.T, entry ignore.Entry) ignore.Entry {
	t.Helper()
	normalized, err := ignore.Normalize(entry)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return normalized
}

// Повтор той же записи — «уже есть» с прежней записью, а не дубль (Р29);
// сверх предела — отказ (Р15).
func TestAddIgnoreRepeatAndLimit(t *testing.T) {
	db := openTestStore(t)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	first := ignoreEntry(t, ignore.Entry{Kind: ignore.KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ignore.ScopePrefix, At: "/account/", Note: "закрыто нарочно"})
	stored, existed, err := db.AddIgnore(t.Context(), first, now)
	if err != nil || existed || !stored.CreatedAt.Equal(now) {
		t.Fatalf("первая запись: %+v, %v, %v", stored, existed, err)
	}

	// Вторая копия приложения ставит то же — с другой заметкой и позже.
	again := first
	again.Note = "другая заметка"
	stored, existed, err = db.AddIgnore(t.Context(), again, now.Add(time.Hour))
	if err != nil || !existed || stored.Note != "закрыто нарочно" || !stored.CreatedAt.Equal(now) {
		t.Fatalf("повтор: %+v, %v, %v", stored, existed, err)
	}

	for index := 1; index < ignore.Limit; index++ {
		entry := ignoreEntry(t, ignore.Entry{Kind: ignore.KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ignore.ScopePage, At: fmt.Sprintf("/p%d", index)})
		if _, _, err := db.AddIgnore(t.Context(), entry, now); err != nil {
			t.Fatalf("запись %d: %v", index, err)
		}
	}
	over := ignoreEntry(t, ignore.Entry{Kind: ignore.KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ignore.ScopePage, At: "/one-more"})
	if _, _, err := db.AddIgnore(t.Context(), over, now); !errors.Is(err, ignore.ErrLimit) {
		t.Fatalf("1001-я запись: %v", err)
	}
	// Повтор уже записанного предел не задевает: это не новая запись.
	if _, existed, err := db.AddIgnore(t.Context(), first, now); err != nil || !existed {
		t.Fatalf("повтор на пределе: %v, %v", existed, err)
	}

	list, err := db.Ignores(t.Context())
	if err != nil || len(list) != ignore.Limit {
		t.Fatalf("записей: %d, %v", len(list), err)
	}

	removed, err := db.RemoveIgnore(t.Context(), first.Key())
	if err != nil || !removed {
		t.Fatalf("снятие: %v, %v", removed, err)
	}
	if removed, err := db.RemoveIgnore(t.Context(), first.Key()); err != nil || removed {
		t.Fatalf("повторное снятие — не ошибка и не «снято»: %v, %v", removed, err)
	}
}

// Записи сайта, которого больше нет, уходят (Р30) — но только по непустому
// списку (Р37).
func TestForgetIgnores(t *testing.T) {
	db := openTestStore(t)
	now := time.Now()
	for _, domain := range []string{"alive.test", "gone.test"} {
		entry := ignoreEntry(t, ignore.Entry{Kind: ignore.KindSeo, Domain: domain, What: seo.IssueNoindex, Scope: ignore.ScopeSite})
		if _, _, err := db.AddIgnore(t.Context(), entry, now); err != nil {
			t.Fatal(err)
		}
	}

	// Пустой список сайтов — не прочитанный или наполовину записанный файл.
	if err := db.ForgetIgnores(t.Context(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.Ignores(t.Context()); len(list) != 2 {
		t.Fatalf("пустой список сайтов стёр записи: осталось %d", len(list))
	}

	if err := db.ForgetIgnores(t.Context(), map[string]bool{"alive.test": true}); err != nil {
		t.Fatal(err)
	}
	list, _ := db.Ignores(t.Context())
	if len(list) != 1 || list[0].Domain != "alive.test" {
		t.Fatalf("после чистки: %+v", list)
	}
}

// vpsfocusFailures — восемь «битых» ссылок, как на vpsfocus.xyz
// 2026-11-12: шесть на claude.ai с ответом 403 и две настоящие.
func vpsfocusFailures(claudeCode int) []links.Failure {
	var out []links.Failure
	for _, lang := range []string{"", "ru/", "es/", "de/", "fr/", "pt/"} {
		out = append(out, links.Failure{
			URL: "https://claude.ai/new?lang=" + lang, Kind: "httpStatus", Code: claudeCode,
			Sources: []string{"https://site.test/" + lang},
		})
	}
	return append(out,
		links.Failure{URL: "https://gone.test/a", Kind: "httpStatus", Code: 404, Sources: []string{"https://site.test/"}},
		links.Failure{URL: "https://slow.test/", Kind: "timeout", Sources: []string{"https://site.test/blog"}},
	)
}

// На сохранённых отчётах: запись «начало https://claude.ai/, 403» убирает
// шесть ссылок, broken = 2, ignored = 6 — и в истории тоже (Р19); сменился
// ответ — ссылка вернулась (Р3).
func TestLinkReportsWithoutIgnored(t *testing.T) {
	db := openTestStore(t)
	for day := 1; day <= 3; day++ {
		saved := report("site.test", vpsfocusFailures(403)...)
		saved.CheckedAt = fmt.Sprintf("2026-11-%02dT10:00:00Z", day)
		if err := db.SaveLinkReport(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}
	list := ignore.List{ignoreEntry(t, ignore.Entry{
		Kind: ignore.KindLink, Domain: "site.test", What: "https://claude.ai/",
		Match: ignore.MatchPrefix, Response: "403", Scope: ignore.ScopeSite,
	})}

	reports, err := db.LinkReports(t.Context(), map[string]bool{"site.test": true}, list)
	if err != nil || len(reports) != 1 {
		t.Fatalf("отчёты: %v, %d", err, len(reports))
	}
	got := reports[0]
	if got.Broken != 2 || got.Ignored != 6 || len(got.Failures) != 2 || len(got.IgnoredFailures) != 6 {
		t.Fatalf("broken %d, ignored %d, failures %d, ignoredFailures %d",
			got.Broken, got.Ignored, len(got.Failures), len(got.IgnoredFailures))
	}
	if len(got.History) != 2 {
		t.Fatalf("история: %+v", got.History)
	}
	for _, point := range got.History {
		if point.Broken != 2 {
			t.Fatalf("история не пересчитана: %+v", got.History)
		}
	}

	// Без записей — всё как было.
	raw, _ := db.LinkReports(t.Context(), map[string]bool{"site.test": true}, nil)
	if raw[0].Broken != 8 || raw[0].Ignored != 0 || raw[0].History[0].Broken != 8 {
		t.Fatalf("сырой отчёт: broken %d, history %+v", raw[0].Broken, raw[0].History)
	}

	// claude.ai стал отвечать 404 — это уже другая беда.
	changed := report("site.test", vpsfocusFailures(404)...)
	changed.CheckedAt = "2026-11-04T10:00:00Z"
	if err := db.SaveLinkReport(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	reports, _ = db.LinkReports(t.Context(), map[string]bool{"site.test": true}, list)
	if reports[0].Broken != 8 || reports[0].Ignored != 0 {
		t.Fatalf("сменился ответ, а ссылки не вернулись: broken %d, ignored %d", reports[0].Broken, reports[0].Ignored)
	}
}

// seoScanWith — обход с находками на страницах и сводкой, посчитанной по
// ним же.
func seoScanWith(domain, when string, pages []seo.Page) seo.Scan {
	scan := seoScan(domain, when, len(pages))
	scan.Stats.Site = &seo.SiteReport{Robots: seo.RobotsFound, Sitemap: &seo.SitemapReport{Found: true, Complete: true}}
	scan.Stats.Canonical = &seo.CanonicalReport{Canonical: "https://" + domain + "/"}
	counts := map[string]int{}
	var order []string
	for _, page := range pages {
		if len(page.Issues) > 0 {
			scan.Stats.Issues.Pages++
		}
		for _, issue := range page.Issues {
			if counts[issue.Code] == 0 {
				order = append(order, issue.Code)
			}
			counts[issue.Code]++
			switch issue.Severity {
			case seo.SeverityCritical:
				scan.Stats.Issues.Critical++
			case seo.SeverityWarning:
				scan.Stats.Issues.Warning++
			default:
				scan.Stats.Issues.Notice++
			}
		}
	}
	for _, code := range order {
		scan.Stats.Issues.ByCode = append(scan.Stats.Issues.ByCode,
			seo.CodeCount{Code: code, Severity: seo.SeverityOf(code), Pages: counts[code]})
	}
	scan.Stats.Checks = seo.BuildChecks(scan.Stats)
	return scan
}

func withIssues(address string, codes ...string) seo.Page {
	page := seoPage(address, 1)
	for _, code := range codes {
		page.Issues = append(page.Issues, seo.Issue{Code: code, Severity: seo.SeverityOf(code)})
	}
	return page
}

// SEO: `noindex` по началу `/account/` выпадает из счёта, но не в
// «пройдено» и не в «исправлено» (Р6, Р7); история пересчитана по
// seo_findings (Р34), а обход без них подписан (Р19).
func TestSeoScansWithoutIgnored(t *testing.T) {
	db := openTestStore(t)
	pages := []seo.Page{
		withIssues("https://site.test/", seo.IssueTitleLong),
		withIssues("https://site.test/account/", seo.IssueNoindex),
		withIssues("https://site.test/account/settings", seo.IssueNoindex),
	}
	for day := 1; day <= 4; day++ {
		scan := seoScanWith("site.test", fmt.Sprintf("2026-11-%02dT10:00:00Z", day), pages)
		if err := db.SaveSeoScan(t.Context(), scan, pages); err != nil {
			t.Fatal(err)
		}
	}
	// Самый старый обход — как у агента старше Р34: находок по страницам
	// нет, снимков страниц тоже (их хранят только два последних обхода).
	var oldest int64
	if err := db.db.QueryRow(`select min(id) from seo_scans`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`delete from seo_finding_sets where scan_id = ?`, oldest); err != nil {
		t.Fatal(err)
	}

	list := ignore.List{ignoreEntry(t, ignore.Entry{
		Kind: ignore.KindSeo, Domain: "site.test", What: seo.IssueNoindex, Scope: ignore.ScopePrefix, At: "/account/",
	})}
	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true}, list)
	if err != nil || len(scans) != 1 {
		t.Fatalf("обходы: %v, %d", err, len(scans))
	}
	scan := scans[0]

	var noindex *seo.Check
	for index := range scan.Stats.Checks.Items {
		if scan.Stats.Checks.Items[index].Code == seo.IssueNoindex {
			noindex = &scan.Stats.Checks.Items[index]
		}
	}
	// Все страницы с находкой неучтены — проверка пройдена, а страницы
	// названы числом.
	if noindex == nil || noindex.State != seo.CheckPassed || noindex.IgnoredPages != 2 {
		t.Fatalf("noindex: %+v", noindex)
	}
	if scan.Stats.Issues.PagesWithCode(seo.IssueNoindex) != 0 || scan.Stats.Issues.Pages != 1 {
		t.Fatalf("сводка находок: %+v", scan.Stats.Issues)
	}
	// Прошлый обход пересчитан тем же списком: «исправлено» пусто.
	if scan.Progress == nil || len(scan.Progress.Fixed) != 0 || len(scan.Progress.Appeared) != 0 {
		t.Fatalf("«было → стало»: %+v", scan.Progress)
	}

	if len(scan.History) != 3 {
		t.Fatalf("история: %d точек", len(scan.History))
	}
	// Самая старая точка без находок по страницам — подписана, остальные
	// пересчитаны: предупреждений `noindex` в них нет.
	if !scan.History[0].Unfiltered {
		t.Fatalf("старая точка не подписана: %+v", scan.History[0])
	}
	for _, point := range scan.History[1:] {
		if point.Unfiltered || point.Checks == nil || point.Checks.Passed != scan.Stats.Checks.Tally.Passed {
			t.Fatalf("точка не пересчитана: %+v", point)
		}
	}

	// Окно страниц: находка ушла в IgnoredIssues, отбор по ней пуст.
	window, total, err := db.SeoPages(t.Context(), "site.test", "", seo.IssueNoindex, 10, 0, ignore.Seo(list, "site.test"))
	if err != nil || total != 0 || len(window) != 0 {
		t.Fatalf("страницы с неучтённой находкой: %d, %v", total, err)
	}
	all, total, err := db.SeoPages(t.Context(), "site.test", "", "", 10, 0, ignore.Seo(list, "site.test"))
	if err != nil || total != 3 {
		t.Fatalf("все страницы: %d, %v", total, err)
	}
	for _, page := range all {
		if page.URL == "https://site.test/account/" && (len(page.Issues) != 0 || len(page.IgnoredIssues) != 1) {
			t.Fatalf("страница: %+v", page)
		}
	}
	// По уровню: страниц с предупреждением и хуже — одна, /account/
	// предупреждением больше не считается.
	if _, total, _ := db.SeoPages(t.Context(), "site.test", seo.SeverityOf(seo.IssueNoindex), "", 10, 0, ignore.Seo(list, "site.test")); total != countAtLeast(pages, seo.SeverityOf(seo.IssueNoindex)) {
		t.Fatalf("отбор по уровню: %d", total)
	}

	// Без записей — прежний счёт.
	raw, _ := db.SeoScans(t.Context(), map[string]bool{"site.test": true}, nil)
	for _, check := range raw[0].Stats.Checks.Items {
		if check.Code == seo.IssueNoindex && (check.State != seo.CheckFailed || check.Pages != 2) {
			t.Fatalf("сырой обход изменился: %+v", check)
		}
	}
}

// countAtLeast — сколько страниц, кроме /account/*, с находкой уровня
// `severity` и тяжелее.
func countAtLeast(pages []seo.Page, severity string) int {
	levels := map[string]bool{}
	for _, level := range seo.AtLeast(severity) {
		levels[level] = true
	}
	count := 0
	for _, page := range pages {
		var kept []seo.Issue
		for _, issue := range page.Issues {
			if issue.Code != seo.IssueNoindex {
				kept = append(kept, issue)
			}
		}
		if levels[seo.Worst(kept)] {
			count++
		}
	}
	return count
}

// Находки по страницам живут столько же, сколько сводки обходов (Р34), — не
// два обхода, как снимки страниц, и не вечно.
func TestSeoFindingsRotateWithScans(t *testing.T) {
	db := openTestStore(t)
	pages := []seo.Page{withIssues("https://site.test/a", seo.IssueTitleLong)}
	for day := 0; day < keepSeoScans+3; day++ {
		when := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC).AddDate(0, 0, day).Format(time.RFC3339)
		if err := db.SaveSeoScan(t.Context(), seoScanWith("site.test", when, pages), pages); err != nil {
			t.Fatal(err)
		}
	}
	var sets, findings int
	if err := db.db.QueryRow(`select count(*) from seo_finding_sets`).Scan(&sets); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`select count(*) from seo_findings`).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if sets != keepSeoScans || findings != keepSeoScans {
		t.Fatalf("находок по страницам: наборов %d, строк %d; ожидалось по %d", sets, findings, keepSeoScans)
	}

	if err := db.ForgetSeoScans(t.Context(), map[string]bool{"other.test": true}); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`select count(*) from seo_findings`).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if findings != 0 {
		t.Fatalf("сайт удалён, а находки остались: %d", findings)
	}
}

// Замер пересчёта истории (риск трека): 30 кругов по 2000 битых ссылок с
// тремя источниками и запись по началу адреса. Пишется в лог теста, цифра —
// в журнал.
func TestLinkHistoryRecountTiming(t *testing.T) {
	// Замер идёт десятки секунд (запись 180 тысяч строк) — только по
	// просьбе: AGENT_TIMING=1 go test -run RecountTiming -v ./internal/store/
	if os.Getenv("AGENT_TIMING") == "" {
		t.Skip("замер: AGENT_TIMING=1")
	}
	db := openTestStore(t)
	for day := 0; day < keepLinkReports; day++ {
		var failures []links.Failure
		for index := 0; index < 2000; index++ {
			failures = append(failures, links.Failure{
				URL: fmt.Sprintf("https://ext%d.test/page/%d", index%50, index), Kind: "httpStatus", Code: 403,
				Sources: []string{"https://site.test/a", "https://site.test/b", fmt.Sprintf("https://site.test/p/%d", index)},
			})
		}
		saved := report("site.test", failures...)
		saved.CheckedAt = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC).AddDate(0, 0, day).Format(time.RFC3339)
		if err := db.SaveLinkReport(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}
	list := ignore.List{ignoreEntry(t, ignore.Entry{
		Kind: ignore.KindLink, Domain: "site.test", What: "https://ext1.test/", Match: ignore.MatchPrefix,
		Response: "403", Scope: ignore.ScopePrefix, At: "/p",
	})}

	started := time.Now()
	reports, err := db.LinkReports(t.Context(), map[string]bool{"site.test": true}, list)
	elapsed := time.Since(started)
	if err != nil || len(reports) != 1 || reports[0].Ignored != 40 {
		t.Fatalf("отчёт: %v, ignored %d", err, reports[0].Ignored)
	}
	started = time.Now()
	if _, err := db.LinkReports(t.Context(), map[string]bool{"site.test": true}, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("30 кругов × 2000 ссылок × 3 источника: с записью %v, без записей %v", elapsed, time.Since(started))
}
