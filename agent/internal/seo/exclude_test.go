package seo

import (
	"reflect"
	"strings"
	"testing"
)

// excludePages — обход vpsfocus.xyz в миниатюре: `noindex` и нет
// `canonical` на закрытых страницах, длинный заголовок на одной открытой.
func excludePages() map[string][]string {
	return map[string][]string{
		"https://site.test/":                 {},
		"https://site.test/pricing":          {IssueTitleLong},
		"https://site.test/account/":         {IssueNoindex, IssueCanonicalMissing},
		"https://site.test/account/settings": {IssueNoindex, IssueCanonicalMissing},
		"https://site.test/accounts":         {IssueNoindex},
	}
}

// excludeStats — сводка того же обхода, посчитанная `audit`-ом.
func excludeStats() Stats {
	stats := checkedStats()
	stats.Pages, stats.Fetched = 5, 5
	pages := []Page{}
	for url, codes := range excludePages() {
		page := Page{URL: url}
		for _, code := range codes {
			page.Issues = append(page.Issues, Issue{Code: code, Severity: SeverityOf(code)})
		}
		pages = append(pages, page)
	}
	stats.Issues = summarize(pagesCodes(pages), Skip{})
	stats.Checks = BuildChecks(stats)
	return stats
}

func pagesCodes(pages []Page) map[string][]string {
	out := map[string][]string{}
	for _, page := range pages {
		out[page.URL] = Codes(page.Issues)
	}
	return out
}

func noindexUnder(prefix string) Skip {
	return Skip{
		Finding: func(code, url string) bool {
			return code == IssueNoindex && strings.Contains(url, prefix+"/") || code == IssueNoindex && strings.HasSuffix(url, prefix)
		},
		PageScoped: true,
	}
}

// Пересчёт сводки по страницам — тем же счётом, что у `audit`: без
// пропусков он обязан дать ровно ту же сводку. Иначе «не учитывать» ничего
// не поставив меняло бы числа.
func TestSummarizeMatchesAudit(t *testing.T) {
	pages := []Page{
		{URL: "https://site.test/", Issues: []Issue{{Code: IssueTitleMissing, Severity: SeverityOf(IssueTitleMissing)}}},
		{URL: "https://site.test/a", Issues: []Issue{
			{Code: IssueImagesWithoutAlt, Severity: SeverityOf(IssueImagesWithoutAlt)},
			{Code: IssueTitleLong, Severity: SeverityOf(IssueTitleLong)},
		}},
		{URL: "https://site.test/b"},
	}
	var direct IssueSummary
	counts := map[string]int{}
	for _, page := range pages {
		if len(page.Issues) == 0 {
			continue
		}
		direct.Pages++
		for _, issue := range page.Issues {
			counts[issue.Code]++
			switch issue.Severity {
			case SeverityCritical:
				direct.Critical++
			case SeverityWarning:
				direct.Warning++
			default:
				direct.Notice++
			}
		}
	}
	direct.ByCode = rankCodes(counts)

	if got := summarize(pagesCodes(pages), Skip{}); !reflect.DeepEqual(got, direct) {
		t.Fatalf("пересчёт разошёлся со счётом audit:\n%+v\n%+v", got, direct)
	}
}

// Неучтённая на части страниц проверка остаётся проверкой, но эти страницы
// в её счёт не входят (Р6).
func TestWithoutOnSomePages(t *testing.T) {
	stats := excludeStats()
	before := checkOf(t, stats.Checks, IssueNoindex)
	if before.State != CheckFailed || before.Pages != 3 {
		t.Fatalf("до пропуска: %+v", before)
	}

	filtered, ok := stats.Without(excludePages(), noindexUnder("/account"))
	if !ok {
		t.Fatal("страницы есть, а пересчёт не состоялся")
	}
	after := checkOf(t, filtered.Checks, IssueNoindex)
	// /accounts — другой раздел: граница сегмента.
	if after.State != CheckFailed || after.Pages != 1 || after.IgnoredPages != 2 {
		t.Fatalf("после пропуска: %+v", after)
	}
	if filtered.Issues.PagesWithCode(IssueNoindex) != 1 {
		t.Fatalf("разрез по кодам: %+v", filtered.Issues.ByCode)
	}
	// Находок на /account/settings осталось одна — страница с находками
	// по-прежнему считается.
	if filtered.Issues.Pages != stats.Issues.Pages {
		t.Fatalf("страниц с находками: %d, было %d", filtered.Issues.Pages, stats.Issues.Pages)
	}
	if filtered.Checks.Tally.Total != stats.Checks.Tally.Total || filtered.Checks.Tally.Ignored != 0 {
		t.Fatalf("счёт перечня: %+v", filtered.Checks.Tally)
	}
	// Сводка обхода в базе не меняется: вычитается копия (Р18).
	if checkOf(t, stats.Checks, IssueNoindex).Pages != 3 {
		t.Fatal("пересчёт испортил исходную сводку")
	}
}

// Не осталось страниц — проверка пройдена; но не «исправлена» (Р6, Р7).
func TestWithoutAllPagesPassesButIsNotFixed(t *testing.T) {
	stats := excludeStats()
	skip := Skip{
		Finding:    func(code, url string) bool { return code == IssueTitleLong },
		PageScoped: true,
	}
	filtered, _ := stats.Without(excludePages(), skip)
	check := checkOf(t, filtered.Checks, IssueTitleLong)
	if check.State != CheckPassed || check.IgnoredPages != 1 {
		t.Fatalf("проверка без учтённых страниц: %+v", check)
	}

	// Тот же список, применённый к прошлому обходу, — сравнение молчит.
	previous, _ := stats.Without(excludePages(), skip)
	progress := CompareChecks(filtered.Checks, previous.Checks, "2026-11-11T10:00:00Z")
	if len(progress.Fixed) != 0 || len(progress.Appeared) != 0 {
		t.Fatalf("«не учитывать» выглядит переменой: %+v", progress)
	}
	// А прошлый обход, не пересчитанный по списку, сравнивать по этой
	// проверке нельзя — её убирает Progress.Without.
	raw := CompareChecks(filtered.Checks, stats.Checks, "2026-11-11T10:00:00Z")
	if len(raw.Fixed) != 1 {
		t.Fatalf("без пересчёта прошлого должно было выйти «исправлено»: %+v", raw)
	}
	if dropped := raw.Without(func(code string) bool { return code == IssueTitleLong }); len(dropped.Fixed) != 0 {
		t.Fatalf("Without не убрал проверку: %+v", dropped)
	}
}

// Неучтённая на всём сайте проверка уходит из перечня: не «пройдена» и не в
// общем числе (Р6); её исход без пропуска — в IgnoredChecks.
func TestWithoutWholeCheck(t *testing.T) {
	stats := excludeStats()
	skip := Skip{Whole: func(code string) bool { return code == IssueNoindex || code == IssueRobotsMissing }}
	stats.Site.Issues = []Issue{{Code: IssueRobotsMissing, Severity: SeverityOf(IssueRobotsMissing)}}
	stats.Checks = BuildChecks(stats)

	filtered, ok := stats.Without(nil, skip)
	if !ok {
		t.Fatal("запись на всём сайте применяется и без страниц")
	}
	for _, check := range filtered.Checks.Items {
		if check.Code == IssueNoindex || check.Code == IssueRobotsMissing {
			t.Fatalf("неучтённая проверка осталась в перечне: %+v", check)
		}
	}
	if len(filtered.Checks.IgnoredChecks) != 2 || filtered.Checks.Tally.Ignored != 2 {
		t.Fatalf("неучтённые: %+v", filtered.Checks.IgnoredChecks)
	}
	for _, check := range filtered.Checks.IgnoredChecks {
		if check.State != CheckFailed {
			t.Fatalf("исход неучтённой проверки подменён: %+v", check)
		}
	}
	if filtered.Checks.Tally.Total != stats.Checks.Tally.Total-2 {
		t.Fatalf("всего проверок: %d, было %d", filtered.Checks.Tally.Total, stats.Checks.Tally.Total)
	}
	if filtered.Checks.Tally.Passed != stats.Checks.Tally.Passed {
		t.Fatalf("пройденных стало больше: %d, было %d", filtered.Checks.Tally.Passed, stats.Checks.Tally.Passed)
	}
	sum := filtered.Checks.Tally.Passed + filtered.Checks.Tally.Critical + filtered.Checks.Tally.Warning +
		filtered.Checks.Tally.Notice + filtered.Checks.Tally.Unknown
	if sum != filtered.Checks.Tally.Total {
		t.Fatalf("сумма счёта %d не равна всему %d", sum, filtered.Checks.Tally.Total)
	}
	// Без страниц сводка находок вычитается по разрезу: три страницы с
	// `noindex` уходят из счёта своего уровня.
	issues := func(summary IssueSummary) int { return summary.Critical + summary.Warning + summary.Notice }
	if filtered.Issues.PagesWithCode(IssueNoindex) != 0 || issues(filtered.Issues) != issues(stats.Issues)-3 {
		t.Fatalf("сводка находок: %+v, была %+v", filtered.Issues, stats.Issues)
	}
	if len(filtered.Site.Issues) != 0 || len(stats.Site.Issues) != 1 {
		t.Fatalf("находка о сайте: %+v / исходная %+v", filtered.Site.Issues, stats.Site.Issues)
	}
}

// Без страниц записи «на странице» применить нечем: сводка как есть и ok =
// false — точка истории подписывается, а не выдаётся за пересчитанную.
func TestWithoutNoPages(t *testing.T) {
	stats := excludeStats()
	same, ok := stats.Without(nil, noindexUnder("/account"))
	if ok || !reflect.DeepEqual(same, stats) {
		t.Fatalf("без страниц: ok = %v", ok)
	}
}

// Страница: неучтённые находки — отдельно, в учтённых их нет.
func TestPageSplit(t *testing.T) {
	page := Page{URL: "https://site.test/account/", Issues: []Issue{{Code: IssueNoindex}, {Code: IssueCanonicalMissing}}}
	split := page.Split(noindexUnder("/account"))
	if len(split.Issues) != 1 || split.Issues[0].Code != IssueCanonicalMissing ||
		len(split.IgnoredIssues) != 1 || split.IgnoredIssues[0].Code != IssueNoindex {
		t.Fatalf("страница: %+v", split)
	}
	codes := ScanCodes{CheckedAt: "x", Pages: map[string][]string{page.URL: Codes(page.Issues)}}.Without(noindexUnder("/account"))
	if got := codes.Pages[page.URL]; len(got) != 1 || got[0] != IssueCanonicalMissing {
		t.Fatalf("коды для сравнения: %v", got)
	}
}
