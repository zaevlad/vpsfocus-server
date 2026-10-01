package store

import (
	"testing"

	"agent/internal/seo"
)

// Перечень проверок достраивается обходам, сохранённым без него, счёт
// попадает в историю, а «исправлено / появилось» считается против прошлого
// состоявшегося обхода (трек U).
func TestSeoChecksReachHistoryAndProgress(t *testing.T) {
	db := openTestStore(t)
	pages := []seo.Page{seoPage("https://site.test/", 0), seoPage("https://site.test/a", 1)}

	// Сводка в том виде, в каком её писал агент 0.16.0–0.17.0: отчёт о
	// сайте есть, перечня проверок нет.
	withSite := func(when string, codes ...seo.CodeCount) seo.Scan {
		scan := seoScan("site.test", when, 2)
		scan.Stats.Site = &seo.SiteReport{
			Robots:  seo.RobotsFound,
			Sitemap: &seo.SitemapReport{Found: true, Complete: true},
		}
		scan.Stats.Canonical = &seo.CanonicalReport{Canonical: "https://site.test/"}
		scan.Stats.Issues.ByCode = codes
		return scan
	}

	// Совсем старый обход: отчёта о сайте нет — перечня быть не должно.
	if err := db.SaveSeoScan(t.Context(), seoScan("site.test", "2026-09-01T10:00:00Z", 2), pages); err != nil {
		t.Fatalf("запись: %v", err)
	}
	older := withSite("2026-09-02T10:00:00Z",
		seo.CodeCount{Code: seo.IssueTitleMissing, Severity: seo.SeverityCritical, Pages: 2},
		seo.CodeCount{Code: seo.IssueH1Missing, Severity: seo.SeverityWarning, Pages: 1})
	if err := db.SaveSeoScan(t.Context(), older, pages); err != nil {
		t.Fatalf("запись: %v", err)
	}
	latest := withSite("2026-09-03T10:00:00Z",
		seo.CodeCount{Code: seo.IssueH1Missing, Severity: seo.SeverityWarning, Pages: 1},
		seo.CodeCount{Code: seo.IssueRedirectChain, Severity: seo.SeverityWarning, Pages: 1})
	if err := db.SaveSeoScan(t.Context(), latest, pages); err != nil {
		t.Fatalf("запись: %v", err)
	}
	// Неудачная попытка после показанного обхода прошлым обходом не считается.
	failed := seoScan("site.test", "2026-09-04T10:00:00Z", 0)
	failed.Stats = seo.Stats{}
	failed.Error = seo.ErrRobotsUnavailable
	if err := db.SaveSeoScan(t.Context(), failed, nil); err != nil {
		t.Fatalf("запись: %v", err)
	}

	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true})
	if err != nil || len(scans) != 1 {
		t.Fatalf("чтение обходов: %v, %d", err, len(scans))
	}
	scan := scans[0]

	if scan.Stats.Checks == nil {
		t.Fatal("перечень не достроен обходу, сохранённому без него")
	}
	if tally := scan.Stats.Checks.Tally; tally.Warning != 2 || tally.Critical != 0 || tally.Passed != tally.Total-2 {
		t.Fatalf("счёт показанного обхода: %+v", tally)
	}

	if len(scan.History) != 3 {
		t.Fatalf("в истории %d точек", len(scan.History))
	}
	if scan.History[0].Checks != nil {
		t.Errorf("обходу без отчёта о сайте выдуман счёт: %+v", scan.History[0].Checks)
	}
	if got := scan.History[1].Checks; got == nil || got.Critical != 1 || got.Warning != 1 {
		t.Errorf("счёт прошлого обхода: %+v", got)
	}
	if scan.History[2].Checks != nil {
		t.Errorf("неудачной попытке выдуман счёт: %+v", scan.History[2].Checks)
	}

	progress := scan.Progress
	if progress == nil {
		t.Fatal("сравнения с прошлым обходом нет")
	}
	if progress.PreviousAt != "2026-09-02T10:00:00Z" {
		t.Errorf("сравнили с обходом от %s", progress.PreviousAt)
	}
	if len(progress.Fixed) != 1 || progress.Fixed[0] != seo.IssueTitleMissing {
		t.Errorf("исправлено: %v", progress.Fixed)
	}
	if len(progress.Appeared) != 1 || progress.Appeared[0] != seo.IssueRedirectChain {
		t.Errorf("появилось: %v", progress.Appeared)
	}
}
