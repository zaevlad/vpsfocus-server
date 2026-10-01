package store

import (
	"testing"

	"agent/internal/seo"
)

// Неудачный обход не выкидывает удачный, а два подряд не стирают его снимок.
//
// Главная проверка пункта 1.2 спринта 24. «Последним обходом» считался
// просто последний по id, и следствий было три разом: экран показывал
// «обход не сохранил ни одной страницы», хотя вчерашний снимок лежал в той
// же базе; сравнение объявляло пропавшими все страницы; а ротация,
// оставлявшая снимки двум последним обходам безотносительно успеха, после
// двух неудач подряд удаляла последний удачный насовсем — и восстановить
// его было нечем.
func TestSeoFailedScansDoNotEvictTheGoodOne(t *testing.T) {
	db := openTestStore(t)

	good := seoScan("site.test", "2026-09-01T10:00:00Z", 2)
	pages := []seo.Page{seoPage("https://site.test/", 0), seoPage("https://site.test/a", 1)}
	if err := db.SaveSeoScan(t.Context(), good, pages); err != nil {
		t.Fatalf("запись удачного обхода: %v", err)
	}

	// Один перенаправленный robots.txt — и обход не приносит ничего.
	// Раньше двух таких хватало, чтобы снимок исчез.
	for _, when := range []string{"2026-09-02T10:00:00Z", "2026-09-03T10:00:00Z"} {
		failed := seoScan("site.test", when, 0)
		failed.Stats = seo.Stats{}
		failed.Error = seo.ErrRobotsUnavailable
		if err := db.SaveSeoScan(t.Context(), failed, nil); err != nil {
			t.Fatalf("запись неудачного обхода: %v", err)
		}
	}

	stored, total, err := db.SeoPages(t.Context(), "site.test", "", "", 10, 0)
	if err != nil {
		t.Fatalf("чтение страниц: %v", err)
	}
	if total != 2 || len(stored) != 2 {
		t.Fatalf("страниц удачного обхода осталось %d (total %d): снимок стёрт", len(stored), total)
	}

	// Сравнение тоже смотрит на состоявшиеся обходы: иначе все страницы
	// разом объявляются пропавшими, хотя сайт не менялся.
	current, previous, err := db.SeoScanCodes(t.Context(), "site.test")
	if err != nil {
		t.Fatalf("чтение кодов: %v", err)
	}
	if len(current.Pages) != 2 {
		t.Fatalf("в текущем срезе %d страниц: взят неудачный обход", len(current.Pages))
	}
	if len(previous.Pages) != 0 {
		t.Fatalf("предыдущий срез не пуст: %d страниц", len(previous.Pages))
	}

	// А экран показывает удачный обход и рядом — что последняя попытка
	// провалилась и когда. Молчать об этом нельзя: человек принял бы
	// позавчерашний снимок за сегодняшний.
	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatalf("чтение обходов: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("обходов: %d", len(scans))
	}
	if scans[0].CheckedAt != "2026-09-01T10:00:00Z" {
		t.Fatalf("показан обход от %s, ожидали удачный", scans[0].CheckedAt)
	}
	if scans[0].Stats.Pages != 2 {
		t.Fatalf("сводка от неудачного обхода: %+v", scans[0].Stats)
	}
	if scans[0].FailedError != seo.ErrRobotsUnavailable {
		t.Fatalf("о неудачной попытке не сказано: %q", scans[0].FailedError)
	}
	if scans[0].FailedAt != "2026-09-03T10:00:00Z" {
		t.Fatalf("время неудачной попытки %q, ожидали самую свежую", scans[0].FailedAt)
	}
}

// Пока состоявшихся обходов не было вовсе, показывается неудачная попытка с
// её причиной: сказать о сайте больше нечего, а молчать — значит показать
// пустой экран без объяснения.
func TestSeoFirstScanFailedIsShownWithReason(t *testing.T) {
	db := openTestStore(t)

	failed := seoScan("site.test", "2026-09-01T10:00:00Z", 0)
	failed.Stats = seo.Stats{}
	failed.Error = seo.ErrRobotsForbidden
	if err := db.SaveSeoScan(t.Context(), failed, nil); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatalf("чтение обходов: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("обходов: %d", len(scans))
	}
	if scans[0].Error != seo.ErrRobotsForbidden {
		t.Fatalf("причина потеряна: %q", scans[0].Error)
	}
	if scans[0].FailedError != "" {
		t.Fatalf("та же попытка названа ещё и отдельной неудачей: %q", scans[0].FailedError)
	}
}
