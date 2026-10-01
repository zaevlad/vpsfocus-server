package seo

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// checkOf — проверка перечня по коду.
func checkOf(t *testing.T, report *ChecksReport, code string) Check {
	t.Helper()
	if report == nil {
		t.Fatalf("перечня нет, а спрашивают %s", code)
	}
	for _, check := range report.Items {
		if check.Code == code {
			return check
		}
	}
	t.Fatalf("в перечне нет проверки %s", code)
	return Check{}
}

// checkedStats — сводка состоявшегося обхода без единой находки: карта
// сайта прочитана целиком, входы проверены.
func checkedStats() Stats {
	return Stats{
		Pages:   3,
		Fetched: 3,
		Site: &SiteReport{
			Robots:  RobotsFound,
			Sitemap: &SitemapReport{Declared: true, Found: true, Complete: true, Addresses: 3},
		},
		Canonical: &CanonicalReport{Canonical: "https://site.test/"},
	}
}

// Каждая находка стоит ровно в одной группе, и в группах нет лишнего.
//
// Перечень строится по группам: находка без группы не считалась бы ни
// пройденной, ни проваленной, а «всего проверок» разошлось бы с числом
// находок, которые умеет агент.
func TestEveryCodeHasExactlyOneGroup(t *testing.T) {
	seen := map[string]string{}
	for _, group := range checkGroups {
		for _, code := range group.Codes {
			if other, twice := seen[code]; twice {
				t.Errorf("%s стоит в двух группах: %s и %s", code, other, group.ID)
			}
			seen[code] = group.ID
			if _, known := severityByCode[code]; !known {
				t.Errorf("в группе %s код %s, которого нет в severityByCode", group.ID, code)
			}
		}
	}

	var missing []string
	for code := range severityByCode {
		if seen[code] == "" {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("находки без группы — в перечень проверок они не попадут: %s", strings.Join(missing, ", "))
	}
}

// Проверки о сайте целиком и те, которым нужна карта, — настоящие коды.
func TestCheckListsNameKnownCodes(t *testing.T) {
	var listed []string
	for code := range siteWideChecks {
		listed = append(listed, code)
	}
	listed = append(listed, entryChecks...)
	listed = append(listed, sitemapDependentChecks...)

	for _, code := range listed {
		if _, known := severityByCode[code]; !known {
			t.Errorf("в списках перечня код %s, которого нет в severityByCode", code)
		}
	}
}

// Обход без находок: пройдено всё, и счёт сходится с числом находок агента.
func TestChecksAllPassOnCleanScan(t *testing.T) {
	report := BuildChecks(checkedStats())
	if report == nil {
		t.Fatal("перечня нет у состоявшегося обхода")
	}

	if report.Tally.Total != len(severityByCode) || len(report.Items) != len(severityByCode) {
		t.Fatalf("проверок %d (в списке %d), а находок у агента %d",
			report.Tally.Total, len(report.Items), len(severityByCode))
	}
	if report.Tally.Passed != report.Tally.Total {
		t.Fatalf("без находок пройдено не всё: %+v", report.Tally)
	}
	for _, check := range report.Items {
		if check.State != CheckPassed || check.Reason != "" || check.Pages != 0 {
			t.Errorf("%s: %+v", check.Code, check)
		}
	}
}

// Находки страниц, сайта и входов становятся непройденными проверками, и
// счёт раскладывает их по важности.
func TestChecksCountFailuresBySeverity(t *testing.T) {
	stats := checkedStats()
	stats.Issues = IssueSummary{
		Critical: 2, Notice: 18, Pages: 18,
		ByCode: []CodeCount{
			{Code: IssueTitleMissing, Severity: SeverityCritical, Pages: 2},
			{Code: IssueImagesWithoutAlt, Severity: SeverityNotice, Pages: 18},
		},
	}
	stats.Site.Issues = []Issue{{Code: IssueAISearchBlocked, Severity: SeverityWarning, Detail: "PerplexityBot"}}
	stats.Canonical.Issues = []Issue{{Code: IssueCanonicalNoHTTPS, Severity: SeverityCritical}}

	report := BuildChecks(stats)

	want := CheckTally{Total: len(severityByCode), Passed: len(severityByCode) - 4, Critical: 2, Warning: 1, Notice: 1}
	if report.Tally != want {
		t.Fatalf("счёт %+v, ожидали %+v", report.Tally, want)
	}

	title := checkOf(t, report, IssueTitleMissing)
	if title.State != CheckFailed || title.Pages != 2 || title.Group != GroupTitles {
		t.Errorf("titleMissing: %+v", title)
	}
	// У проверки о сайте целиком числа страниц нет.
	if bot := checkOf(t, report, IssueAISearchBlocked); bot.State != CheckFailed || bot.Pages != 0 {
		t.Errorf("aiSearchBlocked: %+v", bot)
	}
	if entry := checkOf(t, report, IssueCanonicalNoHTTPS); entry.State != CheckFailed {
		t.Errorf("canonicalNoHTTPS: %+v", entry)
	}
}

// «Не знаем» — не «в порядке»: без прочитанной карты её проверки не
// считаются пройденными.
func TestChecksUnknownWithoutSitemap(t *testing.T) {
	t.Run("сверка выключена", func(t *testing.T) {
		stats := checkedStats()
		stats.Site.Sitemap = nil
		report := BuildChecks(stats)

		if report.Tally.Unknown != 8 {
			t.Fatalf("не проверено %d, ожидали все восемь про карту", report.Tally.Unknown)
		}
		if check := checkOf(t, report, IssueSitemapMissing); check.State != CheckUnknown || check.Reason != CheckReasonSitemapOff {
			t.Errorf("sitemapMissing: %+v", check)
		}
	})

	t.Run("карты нет", func(t *testing.T) {
		stats := checkedStats()
		stats.Site.Sitemap = &SitemapReport{}
		stats.Site.Issues = []Issue{{Code: IssueSitemapMissing, Severity: SeverityWarning}}
		report := BuildChecks(stats)

		if check := checkOf(t, report, IssueSitemapMissing); check.State != CheckFailed {
			t.Errorf("sitemapMissing: %+v", check)
		}
		for _, code := range append([]string{IssueSitemapUnavailable}, sitemapDependentChecks...) {
			if check := checkOf(t, report, code); check.State != CheckUnknown || check.Reason != CheckReasonNoSitemap {
				t.Errorf("%s: %+v", code, check)
			}
		}
	})

	t.Run("карта объявлена и не читается", func(t *testing.T) {
		stats := checkedStats()
		stats.Site.Sitemap = &SitemapReport{Declared: true, Failed: "https://site.test/sitemap.xml"}
		stats.Site.Issues = []Issue{{Code: IssueSitemapUnavailable, Severity: SeverityWarning}}
		report := BuildChecks(stats)

		// Находка сильнее причины: проверку провели, и она провалена.
		if check := checkOf(t, report, IssueSitemapUnavailable); check.State != CheckFailed || check.Reason != "" {
			t.Errorf("sitemapUnavailable: %+v", check)
		}
		if check := checkOf(t, report, IssueNotInSitemap); check.State != CheckUnknown {
			t.Errorf("notInSitemap: %+v", check)
		}
	})

	t.Run("карта прочитана не целиком", func(t *testing.T) {
		stats := checkedStats()
		stats.Site.Sitemap.Complete = false
		report := BuildChecks(stats)

		if report.Tally.Unknown != 1 {
			t.Fatalf("не проверено %d, ожидали одну", report.Tally.Unknown)
		}
		if check := checkOf(t, report, IssueNotInSitemap); check.Reason != CheckReasonSitemapPartial {
			t.Errorf("notInSitemap: %+v", check)
		}
	})
}

// Входы не проверялись (обход оборван по времени) — три проверки серые.
func TestChecksUnknownWithoutEntries(t *testing.T) {
	stats := checkedStats()
	stats.Canonical = nil
	report := BuildChecks(stats)

	if report.Tally.Unknown != len(entryChecks) {
		t.Fatalf("не проверено %d", report.Tally.Unknown)
	}
	for _, code := range entryChecks {
		if check := checkOf(t, report, code); check.Reason != CheckReasonEntriesSkipped {
			t.Errorf("%s: %+v", code, check)
		}
	}
}

// Ни одной разобранной страницы: постраничные проверки не пройдены и не
// провалены, кроме той, что и сказала «страницы недоступны».
func TestChecksUnknownWithoutParsedPages(t *testing.T) {
	stats := checkedStats()
	stats.Fetched = 0
	stats.Canonical = nil
	stats.Issues = IssueSummary{
		Critical: 3, Pages: 3,
		ByCode: []CodeCount{{Code: IssuePageUnavailable, Severity: SeverityCritical, Pages: 3}},
	}
	report := BuildChecks(stats)

	if check := checkOf(t, report, IssuePageUnavailable); check.State != CheckFailed || check.Pages != 3 {
		t.Errorf("pageUnavailable: %+v", check)
	}
	if check := checkOf(t, report, IssueTitleMissing); check.State != CheckUnknown || check.Reason != CheckReasonNoPages {
		t.Errorf("titleMissing: %+v", check)
	}
	// Про сайт целиком robots.txt ответил — эти проверки состоялись.
	if check := checkOf(t, report, IssueSearchEnginesBlocked); check.State != CheckPassed {
		t.Errorf("searchEnginesBlocked: %+v", check)
	}
}

// Перечня нет там, где сказать нечего: обход без страниц и обход агента
// старше 0.16.0 (без отчёта о сайте).
func TestChecksAbsentWhenNothingToSay(t *testing.T) {
	empty := checkedStats()
	empty.Pages = 0
	if BuildChecks(empty) != nil {
		t.Error("перечень у обхода без страниц")
	}

	old := checkedStats()
	old.Site = nil
	if BuildChecks(old) != nil {
		t.Error("перечень у обхода без отчёта о сайте: тогда проверок было вдвое меньше")
	}

	// И достраивание его не выдумывает.
	old.EnsureChecks()
	if old.Checks != nil {
		t.Error("EnsureChecks выдумал перечень")
	}
}

// Достраивание не трогает перечень, сохранённый при обходе.
func TestEnsureChecksKeepsStoredReport(t *testing.T) {
	stats := checkedStats()
	stored := &ChecksReport{Tally: CheckTally{Total: 1, Passed: 1}, Items: []Check{{Code: IssueNoindex, State: CheckPassed}}}
	stats.Checks = stored
	stats.EnsureChecks()
	if stats.Checks != stored {
		t.Fatal("сохранённый перечень пересчитан нынешним списком проверок")
	}
}

// Сравнение перечней: исправлено и появилось; «не удалось проверить» в нём
// не участвует.
func TestCompareChecks(t *testing.T) {
	previous := checkedStats()
	previous.Site.Sitemap.Complete = false
	previous.Issues.ByCode = []CodeCount{
		{Code: IssueTitleMissing, Severity: SeverityCritical, Pages: 2},
		{Code: IssueH1Missing, Severity: SeverityWarning, Pages: 1},
	}

	current := checkedStats()
	current.Canonical = nil
	current.Issues.ByCode = []CodeCount{
		{Code: IssueH1Missing, Severity: SeverityWarning, Pages: 1},
		{Code: IssueRedirectChain, Severity: SeverityWarning, Pages: 3},
	}

	progress := CompareChecks(BuildChecks(current), BuildChecks(previous), "2026-09-22T10:00:00Z")
	if progress == nil {
		t.Fatal("сравнения нет")
	}
	if got := strings.Join(progress.Fixed, ","); got != IssueTitleMissing {
		t.Errorf("исправлено: %q", got)
	}
	if got := strings.Join(progress.Appeared, ","); got != IssueRedirectChain {
		t.Errorf("появилось: %q", got)
	}
	if progress.PreviousAt != "2026-09-22T10:00:00Z" {
		t.Errorf("время прошлого обхода: %q", progress.PreviousAt)
	}

	// Сравнивать не с чем — сравнения нет, а не «ничего не изменилось».
	if CompareChecks(BuildChecks(current), nil, "") != nil {
		t.Error("сравнение с отсутствующим перечнем")
	}

	// Пустые списки уезжают списками, а не null.
	same := CompareChecks(BuildChecks(current), BuildChecks(current), "x")
	if same.Fixed == nil || same.Appeared == nil {
		t.Error("пустой список сериализуется в null")
	}
}

// Настоящий обход кладёт перечень в сводку, и он сходится с находками.
func TestCrawlBuildsChecks(t *testing.T) {
	scan, _ := crawlSite(t, testLimits(), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(html5(`<a href="/a">a</a>`)))
		case "/a":
			_, _ = w.Write([]byte(html5("a")))
		default:
			http.NotFound(w, r)
		}
	})

	report := scan.Stats.Checks
	if report == nil {
		t.Fatal("обход не положил перечень в сводку")
	}
	if report.Tally.Total != len(severityByCode) {
		t.Fatalf("проверок %d из %d", report.Tally.Total, len(severityByCode))
	}
	tally := report.Tally
	if tally.Passed+tally.Critical+tally.Warning+tally.Notice+tally.Unknown != tally.Total {
		t.Fatalf("счёт не сходится: %+v", tally)
	}

	// robots.txt и карты у сайта нет: обе находки — непройденные проверки.
	for _, code := range []string{IssueRobotsMissing, IssueSitemapMissing} {
		if check := checkOf(t, report, code); check.State != CheckFailed {
			t.Errorf("%s: %+v", code, check)
		}
	}
	// А каждая находка страниц — непройденная проверка с тем же числом страниц.
	for _, item := range scan.Stats.Issues.ByCode {
		if check := checkOf(t, report, item.Code); check.State != CheckFailed || check.Pages != item.Pages {
			t.Errorf("%s: %+v, в сводке страниц %d", item.Code, check, item.Pages)
		}
	}
}
