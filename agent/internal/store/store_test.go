package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"agent/internal/links"
	"agent/internal/logs"
	"agent/internal/seo"
	"agent/internal/vitals"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("база не открылась: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func report(domain string, broken ...links.Failure) links.Report {
	return links.Report{
		Domain:     domain,
		CheckedAt:  "2026-08-25T12:00:00Z",
		Pages:      10,
		TotalLinks: 100,
		Broken:     len(broken),
		Failures:   broken,
	}
}

func TestLinkReportRoundtrip(t *testing.T) {
	db := openTestStore(t)

	saved := report("site.test", links.Failure{
		URL:     "http://site.test/gone",
		Kind:    "httpStatus",
		Code:    404,
		Detail:  "Rejected status code: 404 Not Found",
		Sources: []string{"http://site.test/", "http://site.test/blog"},
	})
	if err := db.SaveLinkReport(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	// Второй отчёт по тому же сайту — должен стать последним.
	newer := report("site.test")
	newer.CheckedAt = "2026-08-25T13:00:00Z"
	newer.TotalLinks = 90
	if err := db.SaveLinkReport(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	// Отчёт другого сайта — не мешает первому.
	other := report("other.test")
	if err := db.SaveLinkReport(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	reports, err := db.LinkReports(t.Context(), map[string]bool{"site.test": true, "other.test": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("отчётов: %d", len(reports))
	}

	var site *links.Report
	for i := range reports {
		if reports[i].Domain == "site.test" {
			site = &reports[i]
		}
	}
	if site == nil {
		t.Fatal("site.test потерялся")
	}
	// Последний отчёт без битых ссылок: детали прошлого не прилипают.
	if site.Broken != 0 || len(site.Failures) != 0 || site.TotalLinks != 90 {
		t.Fatalf("последний отчёт неверен: %+v", site)
	}
	// История: один прошлый круг с одной битой ссылкой.
	if len(site.History) != 1 || site.History[0].Broken != 1 {
		t.Fatalf("история неверна: %+v", site.History)
	}
}

func TestLinkFailuresRegroupByURL(t *testing.T) {
	db := openTestStore(t)

	saved := report("site.test", links.Failure{
		URL:     "http://site.test/gone",
		Kind:    "httpStatus",
		Code:    404,
		Sources: []string{"http://site.test/a", "http://site.test/b"},
	})
	if err := db.SaveLinkReport(t.Context(), saved); err != nil {
		t.Fatal(err)
	}

	reports, err := db.LinkReports(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || len(reports[0].Failures) != 1 {
		t.Fatalf("не тот разрез: %+v", reports)
	}
	got := reports[0].Failures[0]
	if got.Code != 404 || len(got.Sources) != 2 {
		t.Fatalf("детали потерялись: %+v", got)
	}
}

func TestLinkReportsOfDeletedSiteAreInvisibleAndForgotten(t *testing.T) {
	db := openTestStore(t)

	if err := db.SaveLinkReport(t.Context(), report("gone.test")); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLinkReport(t.Context(), report("alive.test")); err != nil {
		t.Fatal(err)
	}

	reports, err := db.LinkReports(t.Context(), map[string]bool{"alive.test": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Domain != "alive.test" {
		t.Fatalf("чужой сайт виден в списке: %+v", reports)
	}

	// Удалённый сайт вычищается целиком, вместе с деталями.
	if err := db.ForgetLinkReports(t.Context(), map[string]bool{"alive.test": true}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`select count(*) from link_failures`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("осиротевшие детали остались: %d", count)
	}
}

// Ротация обязана держать потолок истории: база живёт на сервере клиента,
// и расти вечно на нём нельзя.
func TestLinkHistoryRotates(t *testing.T) {
	db := openTestStore(t)

	for i := 0; i < keepLinkReports+5; i++ {
		r := report("site.test")
		if err := db.SaveLinkReport(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := db.db.QueryRow(`select count(*) from link_reports where domain = 'site.test'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count > keepLinkReports {
		t.Fatalf("история не ротируется: %d отчётов", count)
	}
	var orphans int
	if err := db.db.QueryRow(`select count(*) from link_failures`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("осиротевшие детали остались: %d", orphans)
	}
}

func measurement(domain, path string, at string, score int) vitals.Report {
	return vitals.Report{
		Domain:    domain,
		Path:      path,
		URL:       vitals.PageURL(domain, path),
		Strategy:  vitals.StrategyMobile,
		CheckedAt: at,
		Lab:       &vitals.Lab{Score: score, ScoreRating: vitals.RateScore(score)},
	}
}

func TestVitalsReportRoundtrip(t *testing.T) {
	db := openTestStore(t)
	alive := map[string]bool{
		VitalsKey("site.test", "/", vitals.StrategyMobile):        true,
		VitalsKey("site.test", "/catalog", vitals.StrategyMobile): true,
	}

	for _, saved := range []vitals.Report{
		measurement("site.test", "/", "2026-09-01T10:00:00Z", 70),
		measurement("site.test", "/", "2026-09-02T10:00:00Z", 80),
		measurement("site.test", "/", "2026-09-03T10:00:00Z", 90),
		measurement("site.test", "/catalog", "2026-09-03T10:05:00Z", 55),
	} {
		if err := db.SaveVitalsReport(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}

	reports, err := db.VitalsReports(t.Context(), alive)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("страниц в ответе %d, ждали 2", len(reports))
	}

	home := reports[0]
	if home.Path != "/" || home.Lab == nil || home.Lab.Score != 90 {
		t.Fatalf("последним замером главной должен быть свежий: %+v", home)
	}
	// История — прошлые замеры от старых к новым, без текущего: он и так
	// показан целиком.
	if len(home.History) != 2 {
		t.Fatalf("история главной: %+v", home.History)
	}
	if home.History[0].Score == nil || *home.History[0].Score != 70 {
		t.Errorf("история идёт не от старых к новым: %+v", home.History)
	}
	if home.History[1].Score == nil || *home.History[1].Score != 80 {
		t.Errorf("история главной: %+v", home.History)
	}
}

// Страницу убрали из списка или выключили настольные замеры — её цифры
// больше не показываются и не занимают место на чужом сервере.
func TestVitalsReportsOfRemovedPageAreForgotten(t *testing.T) {
	db := openTestStore(t)

	for _, saved := range []vitals.Report{
		measurement("site.test", "/", "2026-09-03T10:00:00Z", 90),
		measurement("site.test", "/gone", "2026-09-03T10:05:00Z", 40),
	} {
		if err := db.SaveVitalsReport(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}

	alive := map[string]bool{VitalsKey("site.test", "/", vitals.StrategyMobile): true}

	reports, err := db.VitalsReports(t.Context(), alive)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Path != "/" {
		t.Fatalf("удалённая страница осталась видна: %+v", reports)
	}

	if err := db.ForgetVitalsReports(t.Context(), alive); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.db.QueryRow(`select count(*) from vitals_reports where path = '/gone'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("после уборки осталось строк: %d", left)
	}
}

func TestVitalsHistoryRotates(t *testing.T) {
	db := openTestStore(t)

	for i := 0; i < keepVitalsReports+5; i++ {
		saved := measurement("site.test", "/",
			time.Unix(int64(1_760_000_000+i*3600), 0).UTC().Format(time.RFC3339), 50+i)
		if err := db.SaveVitalsReport(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}

	var left int
	if err := db.db.QueryRow(`select count(*) from vitals_reports`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != keepVitalsReports {
		t.Fatalf("замеров осталось %d, ждали %d", left, keepVitalsReports)
	}
}

// Квота считается по суткам UTC — по ним же её сбрасывает Google.
func TestVitalsQuotaCounts(t *testing.T) {
	db := openTestStore(t)
	day := Today()

	used, err := db.VitalsSpent(t.Context(), day)
	if err != nil || used != 0 {
		t.Fatalf("на пустой базе расход должен быть нулевым: %d, %v", used, err)
	}

	for _, requests := range []int{3, 2, 0} {
		if err := db.AddVitalsSpent(t.Context(), day, requests); err != nil {
			t.Fatal(err)
		}
	}

	if used, err = db.VitalsSpent(t.Context(), day); err != nil || used != 5 {
		t.Fatalf("расход: %d, ждали 5 (%v)", used, err)
	}

	// Позавчерашний счёт не мешает сегодняшнему.
	old := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	if err := db.AddVitalsSpent(t.Context(), old, 100); err != nil {
		t.Fatal(err)
	}
	if used, err = db.VitalsSpent(t.Context(), day); err != nil || used != 5 {
		t.Fatalf("чужие сутки попали в сегодняшний расход: %d (%v)", used, err)
	}
}

// Прошлый замер нужен на старте: перезапуск агента после правки настроек не
// повод мерить всё заново и тратить квоту.
func TestLatestVitalsAt(t *testing.T) {
	db := openTestStore(t)

	if _, ok, err := db.LatestVitalsAt(t.Context()); err != nil || ok {
		t.Fatalf("на пустой базе замеров быть не должно: %v, %v", ok, err)
	}

	if err := db.SaveVitalsReport(t.Context(),
		measurement("site.test", "/", "2026-09-03T10:00:00Z", 90)); err != nil {
		t.Fatal(err)
	}

	at, ok, err := db.LatestVitalsAt(t.Context())
	if err != nil || !ok {
		t.Fatalf("замер не нашёлся: %v, %v", ok, err)
	}
	if at.Format(time.RFC3339) != "2026-09-03T10:00:00Z" {
		t.Errorf("время замера: %s", at.Format(time.RFC3339))
	}
}

// ── Логи веб-сервера ─────────────────────────────────────────────────────

func serverError(at time.Time, status int, path string) logs.Counted {
	return logs.Counted{
		Event: logs.Event{
			Kind: logs.KindAccess, At: at, Status: status,
			Method: "GET", Path: path,
		},
		Count:  1,
		LastAt: at,
	}
}

// Счётчики складываются, а не заменяются: тот же ключ приходит и из
// прошлого прохода, и из этого, а «insert or replace» потерял бы прошлый
// счёт — график ошибок показывал бы только последнюю минуту.
func TestLogGroupsAccumulate(t *testing.T) {
	db := openTestStore(t)
	at := time.Now().UTC().Add(-10 * time.Minute)

	first := serverError(at, 500, "/checkout")
	first.Count = 3
	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/access.log", []logs.Counted{first}); err != nil {
		t.Fatal(err)
	}

	second := serverError(at.Add(time.Minute), 500, "/checkout")
	second.Count = 2
	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/access.log", []logs.Counted{second}); err != nil {
		t.Fatal(err)
	}

	groups, err := db.LogGroups(t.Context(), at.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("групп: %d (%+v)", len(groups), groups)
	}
	if groups[0].Count != 5 {
		t.Errorf("счёт: %d, ждали 5", groups[0].Count)
	}
	if groups[0].Path != "/checkout" || groups[0].Status != 500 {
		t.Errorf("группа: %+v", groups[0])
	}
}

// Разные коды — разные беды: 500 у приложения и 502 у апстрима лечат
// по-разному, и в одну строку экрана они сливаться не должны.
func TestLogGroupsSeparateByStatus(t *testing.T) {
	db := openTestStore(t)
	at := time.Now().UTC().Add(-10 * time.Minute)

	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/access.log", []logs.Counted{
		serverError(at, 500, "/checkout"),
		serverError(at, 502, "/checkout"),
	}); err != nil {
		t.Fatal(err)
	}

	groups, err := db.LogGroups(t.Context(), at.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("ждали две группы, получили %d: %+v", len(groups), groups)
	}
}

// График рисуется по тем же окнам, что и агрегация, и два вида логов в нём
// разделены: пятисотки — что сломалось, error-лог — почему.
func TestLogSeriesSplitsKinds(t *testing.T) {
	db := openTestStore(t)
	at := time.Now().UTC().Add(-10 * time.Minute)

	fatal := logs.Counted{
		Event: logs.Event{
			Kind: logs.KindError, At: at, Level: "error",
			Signature: "PHP Fatal error", Sample: "PHP Fatal error: что-то",
		},
		Count:  4,
		LastAt: at,
	}
	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/error.log", []logs.Counted{fatal}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/access.log",
		[]logs.Counted{serverError(at, 500, "/checkout")}); err != nil {
		t.Fatal(err)
	}

	series, err := db.LogSeries(t.Context(), at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("окон: %d (%+v)", len(series), series)
	}
	if series[0].Access != 1 || series[0].Errors != 4 {
		t.Errorf("окно: %+v", series[0])
	}

	// Тревога считается только по строкам access-лога: записи error-лога
	// описывают ту же беду и удвоили бы счёт, а порог человек задаёт в
	// пятисотках.
	spikes, err := db.LogSpikes(t.Context(), at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	errors, notFound := 0, 0
	for _, spike := range spikes {
		errors += spike.ServerErrors
		notFound += spike.NotFound
	}
	if errors != 1 || notFound != 0 {
		t.Errorf("всплеск: 5xx %d, 404 %d — ждали 1 и 0", errors, notFound)
	}
}

// Пятисотки и «страницы нет» считаются порознь: пороги у них разные, и
// сложи их в одно число, всплеск от ботов объявил бы аварией работающий
// сайт.
func TestLogSpikesSeparateNotFoundFromServerErrors(t *testing.T) {
	db := openTestStore(t)
	at := time.Now().UTC().Add(-10 * time.Minute)

	groups := []logs.Counted{
		serverError(at, 500, "/checkout"),
		serverError(at, 404, "/пропало"),
		serverError(at, 404, "/тоже-пропало"),
	}
	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/access.log", groups); err != nil {
		t.Fatal(err)
	}

	spikes, err := db.LogSpikes(t.Context(), at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(spikes) != 1 {
		t.Fatalf("разрезов: %d (%+v)", len(spikes), spikes)
	}
	if spikes[0].ServerErrors != 1 || spikes[0].NotFound != 2 {
		t.Errorf("всплеск: %+v", spikes[0])
	}
}

func TestLogEventsRotate(t *testing.T) {
	db := openTestStore(t)
	old := time.Now().UTC().AddDate(0, 0, -30)
	fresh := time.Now().UTC().Add(-time.Hour)

	if err := db.AddLogGroups(t.Context(), "/var/log/nginx/access.log", []logs.Counted{
		serverError(old, 500, "/старое"),
		serverError(fresh, 500, "/свежее"),
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := db.RotateLogEvents(t.Context(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("удалено групп: %d, ждали 1", removed)
	}

	groups, err := db.LogGroups(t.Context(), time.Now().UTC().AddDate(0, 0, -60), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Path != "/свежее" {
		t.Errorf("после ротации осталось: %+v", groups)
	}
}

// Позиция в файле переживает перезапуск агента: без неё каждый запуск
// перечитывал бы лог заново и считал одни и те же ошибки по второму разу.
func TestLogPositionRoundtrip(t *testing.T) {
	db := openTestStore(t)
	path := "/var/log/nginx/access.log"

	if err := db.SaveLogPosition(t.Context(), path,
		logs.Position{Inode: 42, Size: 1000, Offset: 900}, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLogPosition(t.Context(), "/var/log/nginx/error.log",
		logs.Position{}, logs.ErrNoAccess); err != nil {
		t.Fatal(err)
	}

	positions, err := db.LogPositions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if positions[path].Offset != 900 || positions[path].Inode != 42 {
		t.Errorf("позиция: %+v", positions[path])
	}
	if positions["/var/log/nginx/error.log"].Error != logs.ErrNoAccess {
		t.Errorf("код беды не сохранился: %+v", positions["/var/log/nginx/error.log"])
	}

	// Файл убрали из списка — его позиция больше не наша забота.
	if err := db.ForgetLogPositions(t.Context(), map[string]bool{path: true}); err != nil {
		t.Fatal(err)
	}
	positions, err = db.LogPositions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 {
		t.Errorf("после уборки осталось: %+v", positions)
	}
}

// ── Обход сайта ──────────────────────────────────────────────────────────

func seoScan(domain, when string, pages int) seo.Scan {
	return seo.Scan{
		Domain:          domain,
		CheckedAt:       when,
		DurationSeconds: 42,
		Stats:           seo.Stats{Pages: pages, Fetched: pages, DelayMS: 1000},
	}
}

func seoPage(address string, depth int) seo.Page {
	return seo.Page{
		URL:    address,
		Status: 200,
		Depth:  depth,
		Hash:   "hash-" + address,
		Structure: &seo.Structure{
			Title:     "Заголовок " + address,
			WordCount: 100,
		},
	}
}

func TestSeoScanRoundtrip(t *testing.T) {
	db := openTestStore(t)

	pages := []seo.Page{seoPage("https://site.test/", 0), seoPage("https://site.test/a", 1)}
	if err := db.SaveSeoScan(t.Context(), seoScan("site.test", "2026-09-04T10:00:00Z", 2), pages); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatalf("чтение обходов: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("обходов: %d", len(scans))
	}
	if scans[0].Stats.Pages != 2 || scans[0].Stats.DelayMS != 1000 {
		t.Fatalf("сводка не восстановилась: %+v", scans[0].Stats)
	}

	stored, total, err := db.SeoPages(t.Context(), "site.test", "", "", 10, 0)
	if err != nil {
		t.Fatalf("чтение страниц: %v", err)
	}
	if total != 2 || len(stored) != 2 {
		t.Fatalf("страниц: %d из %d", len(stored), total)
	}
	// Структура — то единственное, ради чего затевался обход: HTML мы не
	// храним, и потерять её значит потерять всё.
	if stored[0].Structure == nil || stored[0].Structure.Title == "" {
		t.Fatalf("структура страницы потеряна: %+v", stored[0])
	}
}

func TestSeoPagesWindow(t *testing.T) {
	db := openTestStore(t)

	var pages []seo.Page
	for index := 0; index < 5; index++ {
		pages = append(pages, seoPage(fmt.Sprintf("https://site.test/%d", index), 1))
	}
	if err := db.SaveSeoScan(t.Context(), seoScan("site.test", "2026-09-04T10:00:00Z", 5), pages); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	window, total, err := db.SeoPages(t.Context(), "site.test", "", "", 2, 2)
	if err != nil {
		t.Fatalf("чтение окна: %v", err)
	}
	if total != 5 {
		t.Fatalf("всего страниц: %d", total)
	}
	if len(window) != 2 {
		t.Fatalf("в окне страниц: %d", len(window))
	}
	// Порядок устойчивый — иначе листание показывало бы одни и те же
	// страницы дважды, а другие не показывало бы вовсе.
	if window[0].URL != "https://site.test/2" || window[1].URL != "https://site.test/3" {
		t.Fatalf("окно: %v %v", window[0].URL, window[1].URL)
	}
}

func TestSeoPageSnapshotsRotateButHistoryStays(t *testing.T) {
	// Снимки страниц хранятся у двух последних обходов: их сотни, и каждый
	// — килобайт на чужом диске. Сводки живут дольше: график «страниц было
	// — страниц стало» без них не построить.
	db := openTestStore(t)

	for day := 1; day <= 4; day++ {
		when := fmt.Sprintf("2026-09-0%dT10:00:00Z", day)
		pages := []seo.Page{seoPage(fmt.Sprintf("https://site.test/day%d", day), 0)}
		if err := db.SaveSeoScan(t.Context(), seoScan("site.test", when, 1), pages); err != nil {
			t.Fatalf("запись обхода: %v", err)
		}
	}

	var scanRows, pageRows int
	if err := db.db.QueryRow(`select count(*) from seo_scans`).Scan(&scanRows); err != nil {
		t.Fatalf("счёт обходов: %v", err)
	}
	if err := db.db.QueryRow(`select count(*) from seo_pages`).Scan(&pageRows); err != nil {
		t.Fatalf("счёт страниц: %v", err)
	}

	if scanRows != 4 {
		t.Fatalf("сводок осталось %d — история потерялась", scanRows)
	}
	if pageRows != keepSeoPageSets {
		t.Fatalf("снимков страниц осталось %d, ожидалось %d", pageRows, keepSeoPageSets)
	}

	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if len(scans[0].History) != 3 {
		t.Fatalf("точек истории: %d", len(scans[0].History))
	}
	// История идёт от старых к новым, без последнего обхода: он показан
	// целиком.
	if scans[0].History[0].CheckedAt > scans[0].History[2].CheckedAt {
		t.Fatal("история отдана в обратном порядке")
	}
}

func TestSeoScansOfDeletedSiteAreInvisibleAndForgotten(t *testing.T) {
	db := openTestStore(t)

	for _, domain := range []string{"live.test", "gone.test"} {
		if err := db.SaveSeoScan(t.Context(),
			seoScan(domain, "2026-09-04T10:00:00Z", 1),
			[]seo.Page{seoPage("https://"+domain+"/", 0)}); err != nil {
			t.Fatalf("запись обхода: %v", err)
		}
	}

	alive := map[string]bool{"live.test": true}

	scans, err := db.SeoScans(t.Context(), alive)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if len(scans) != 1 || scans[0].Domain != "live.test" {
		t.Fatalf("удалённый сайт виден: %v", scans)
	}

	if err := db.ForgetSeoScans(t.Context(), alive); err != nil {
		t.Fatalf("уборка: %v", err)
	}

	var rows int
	if err := db.db.QueryRow(`select count(*) from seo_pages`).Scan(&rows); err != nil {
		t.Fatalf("счёт страниц: %v", err)
	}
	if rows != 1 {
		t.Fatalf("снимки удалённого сайта остались: строк %d", rows)
	}
}

func TestLatestSeoAt(t *testing.T) {
	db := openTestStore(t)

	if _, ok, err := db.LatestSeoAt(t.Context()); err != nil || ok {
		t.Fatalf("до первого обхода: ok=%v err=%v", ok, err)
	}

	if err := db.SaveSeoScan(t.Context(),
		seoScan("site.test", "2026-09-04T10:00:00Z", 1),
		[]seo.Page{seoPage("https://site.test/", 0)}); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	latest, ok, err := db.LatestSeoAt(t.Context())
	if err != nil || !ok {
		t.Fatalf("после обхода: ok=%v err=%v", ok, err)
	}
	if latest.Format(time.RFC3339) != "2026-09-04T10:00:00Z" {
		t.Fatalf("время: %s", latest.Format(time.RFC3339))
	}
}

func TestSeoIssuesSurviveAndReachHistory(t *testing.T) {
	// Находки лежат в том же JSON, что и структура страницы, а числа по
	// уровням — в сводке обхода: без них история не покажет динамику, ради
	// которой её и держат.
	db := openTestStore(t)

	// Порядок записи важен: последним обходом считается последний
	// записанный, а не самый поздний по дате.
	for _, item := range []struct {
		day      string
		critical int
	}{{"2026-09-01", 5}, {"2026-09-02", 2}} {
		day, critical := item.day, item.critical
		scan := seoScan("site.test", day+"T10:00:00Z", 1)
		scan.Stats.Issues = seo.IssueSummary{
			Critical: critical,
			Warning:  1,
			Pages:    1,
			ByCode: []seo.CodeCount{{
				Code:     seo.IssueTitleMissing,
				Severity: seo.SeverityCritical,
				Pages:    critical,
			}},
		}
		page := seoPage("https://site.test/", 0)
		page.Issues = []seo.Issue{{
			Code:     seo.IssueTitleMissing,
			Severity: seo.SeverityCritical,
		}}
		if err := db.SaveSeoScan(t.Context(), scan, []seo.Page{page}); err != nil {
			t.Fatalf("запись обхода: %v", err)
		}
	}

	scans, err := db.SeoScans(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if scans[0].Stats.Issues.PagesWithCode(seo.IssueTitleMissing) == 0 {
		t.Fatalf("разрез по кодам потерян: %+v", scans[0].Stats.Issues)
	}
	if len(scans[0].History) != 1 {
		t.Fatalf("точек истории: %d", len(scans[0].History))
	}
	if scans[0].History[0].Critical != 5 {
		t.Fatalf("история без чисел находок: %+v", scans[0].History[0])
	}

	pages, _, err := db.SeoPages(t.Context(), "site.test", "", "", 10, 0)
	if err != nil {
		t.Fatalf("чтение страниц: %v", err)
	}
	if len(pages[0].Issues) != 1 || pages[0].Issues[0].Code != seo.IssueTitleMissing {
		t.Fatalf("находки страницы потеряны: %+v", pages[0].Issues)
	}
}

func TestSeoPagesFilterBySeverity(t *testing.T) {
	// Отбор в базе, а не на экране: у обхода сотни страниц, и присылать их
	// все, чтобы показать двадцать, значит гонять весь обход через туннель.
	db := openTestStore(t)

	critical := seoPage("https://site.test/a", 0)
	critical.Issues = []seo.Issue{{Code: seo.IssueTitleMissing, Severity: seo.SeverityCritical}}
	notice := seoPage("https://site.test/b", 1)
	notice.Issues = []seo.Issue{{Code: seo.IssueLangMissing, Severity: seo.SeverityNotice}}
	clean := seoPage("https://site.test/c", 1)

	if err := db.SaveSeoScan(t.Context(),
		seoScan("site.test", "2026-09-04T10:00:00Z", 3),
		[]seo.Page{critical, notice, clean}); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	all, total, err := db.SeoPages(t.Context(), "site.test", "", "", 10, 0)
	if err != nil || len(all) != 3 || total != 3 {
		t.Fatalf("без отбора: %d из %d, err=%v", len(all), total, err)
	}

	worst, total, err := db.SeoPages(t.Context(), "site.test", seo.SeverityCritical, "", 10, 0)
	if err != nil {
		t.Fatalf("отбор: %v", err)
	}
	if len(worst) != 1 || worst[0].URL != "https://site.test/a" {
		t.Fatalf("критичные: %+v", worst)
	}
	// Общее число считается по тому же условию: «показано 1 из 3», когда
	// критичная одна, — это неверный ответ.
	if total != 1 {
		t.Fatalf("всего при отборе: %d", total)
	}

	loose, total, err := db.SeoPages(t.Context(), "site.test", seo.SeverityNotice, "", 10, 0)
	if err != nil || len(loose) != 2 || total != 2 {
		t.Fatalf("уровень «и хуже» не сработал: %d из %d, err=%v", len(loose), total, err)
	}
}

func TestSeoPagesFilterByCode(t *testing.T) {
	// Отбор по конкретной находке: «покажи страницы без описания» — это
	// один вопрос, а не чтение всего обхода на экране.
	db := openTestStore(t)

	noTitle := seoPage("https://site.test/a", 0)
	noTitle.Issues = []seo.Issue{{
		Code:     seo.IssueTitleMissing,
		Severity: seo.SeverityOf(seo.IssueTitleMissing),
	}}
	noDescription := seoPage("https://site.test/b", 1)
	noDescription.Issues = []seo.Issue{{
		Code:     seo.IssueDescriptionMissing,
		Severity: seo.SeverityOf(seo.IssueDescriptionMissing),
	}}

	if err := db.SaveSeoScan(t.Context(),
		seoScan("site.test", "2026-09-04T10:00:00Z", 2),
		[]seo.Page{noTitle, noDescription}); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	pages, total, err := db.SeoPages(t.Context(), "site.test", "", seo.IssueTitleMissing, 10, 0)
	if err != nil {
		t.Fatalf("отбор: %v", err)
	}
	if len(pages) != 1 || total != 1 || pages[0].URL != "https://site.test/a" {
		t.Fatalf("отобрано: %+v (всего %d)", pages, total)
	}

	// Незнакомый код отбора не делает: он не из нашего интерфейса.
	all, total, err := db.SeoPages(t.Context(), "site.test", "", "чужая строка", 10, 0)
	if err != nil || len(all) != 2 || total != 2 {
		t.Fatalf("незнакомый код отфильтровал: %d из %d, err=%v", len(all), total, err)
	}
}

func TestSeoScanCodesGivesTwoLastScans(t *testing.T) {
	db := openTestStore(t)

	for _, item := range []struct {
		day  string
		code string
	}{
		{"2026-09-02", seo.IssueTitleMissing},
		{"2026-09-03", seo.IssueDescriptionMissing},
		{"2026-09-04", seo.IssueH1Missing},
	} {
		page := seoPage("https://site.test/", 0)
		page.Issues = []seo.Issue{{Code: item.code, Severity: seo.SeverityOf(item.code)}}
		if err := db.SaveSeoScan(t.Context(),
			seoScan("site.test", item.day+"T10:00:00Z", 1),
			[]seo.Page{page}); err != nil {
			t.Fatalf("запись обхода: %v", err)
		}
	}

	current, previous, err := db.SeoScanCodes(t.Context(), "site.test")
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if current.CheckedAt != "2026-09-04T10:00:00Z" {
		t.Fatalf("последний обход: %s", current.CheckedAt)
	}
	if previous.CheckedAt != "2026-09-03T10:00:00Z" {
		t.Fatalf("предыдущий обход: %s", previous.CheckedAt)
	}
	// Снимки страниц хранятся у двух последних обходов — ровно столько и
	// сравнивается; у самого старого страниц уже нет.
	if len(current.Pages["https://site.test/"]) != 1 ||
		current.Pages["https://site.test/"][0] != seo.IssueH1Missing {
		t.Fatalf("коды последнего обхода: %v", current.Pages)
	}

	diff := seo.Compare("site.test", current, previous)
	if !diff.Comparable {
		t.Fatal("сравнение невозможно при двух сохранённых обходах")
	}
	if len(diff.Appeared) != 1 || diff.Appeared[0].Code != seo.IssueH1Missing {
		t.Fatalf("появившееся: %+v", diff.Appeared)
	}
	if len(diff.Fixed) != 1 || diff.Fixed[0].Code != seo.IssueDescriptionMissing {
		t.Fatalf("исправленное: %+v", diff.Fixed)
	}
}

func TestSeoScanCodesWithoutPreviousScan(t *testing.T) {
	db := openTestStore(t)

	if err := db.SaveSeoScan(t.Context(),
		seoScan("site.test", "2026-09-04T10:00:00Z", 1),
		[]seo.Page{seoPage("https://site.test/", 0)}); err != nil {
		t.Fatalf("запись обхода: %v", err)
	}

	current, previous, err := db.SeoScanCodes(t.Context(), "site.test")
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if current.CheckedAt == "" || previous.CheckedAt != "" {
		t.Fatalf("после одного обхода: current=%q previous=%q", current.CheckedAt, previous.CheckedAt)
	}
}

// ── Приглушение находок ──────────────────────────────────────────────────

// Приглушение переживает круг чтения и снимается.
func TestSuppressionRoundtrip(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := db.Suppress(ctx, "healthCertExpired", "example.com",
		now.Add(48*time.Hour), "меняю сертификат"); err != nil {
		t.Fatal(err)
	}

	list, err := db.Suppressions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("приглушение не сохранилось: %+v", list)
	}
	if list[0].Note != "меняю сертификат" {
		t.Errorf("заметка потеряна: %+v", list[0])
	}

	if err := db.Unsuppress(ctx, "healthCertExpired", "example.com"); err != nil {
		t.Fatal(err)
	}
	if list, err = db.Suppressions(ctx, now); err != nil || len(list) != 0 {
		t.Fatalf("приглушение не снялось: %+v, %v", list, err)
	}
}

// Истёкшее приглушение убирается при чтении, а не ждёт круга ротации.
//
// Истёкшее приглушение — это находка, которая снова должна попасть в свод,
// и откладывать её возвращение значило бы молчать о ней лишние сутки.
func TestExpiredSuppressionIsSweptOnRead(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := db.Suppress(ctx, "healthDiskFilling", "", now.Add(-time.Minute), ""); err != nil {
		t.Fatal(err)
	}

	list, err := db.Suppressions(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("истёкшее приглушение осталось: %+v", list)
	}
}

// Бессрочное приглушение не истекает никогда.
func TestForeverSuppressionSurvives(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()

	if err := db.Suppress(context.Background(), "healthMemoryTight", "", time.Time{}, ""); err != nil {
		t.Fatal(err)
	}

	list, err := db.Suppressions(ctx, time.Now().AddDate(10, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("бессрочное приглушение исчезло: %+v", list)
	}
	if !list[0].Until.IsZero() {
		t.Errorf("у бессрочного приглушения появился срок: %+v", list[0])
	}
}

// ── Окно обслуживания ────────────────────────────────────────────────────

// Окно открывается, читается и закрывается досрочно.
func TestMaintenanceWindowRoundtrip(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, active, err := db.MaintenanceWindow(ctx, now); err != nil || active {
		t.Fatalf("окно есть до того, как его открыли: %v, %v", active, err)
	}

	if err := db.StartMaintenance(ctx, now.Add(time.Hour), "обновляю стек"); err != nil {
		t.Fatal(err)
	}
	window, active, err := db.MaintenanceWindow(ctx, now)
	if err != nil || !active {
		t.Fatalf("окно не открылось: %v, %v", active, err)
	}
	if window.Note != "обновляю стек" {
		t.Errorf("заметка потеряна: %+v", window)
	}

	if err := db.EndMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, active, err = db.MaintenanceWindow(ctx, now); err != nil || active {
		t.Fatalf("окно не закрылось: %v, %v", active, err)
	}
}

// Истёкшее окно перестаёт действовать само.
//
// «Обслуживание кончилось» должно означать «письма снова ходят», а не
// «ходят со следующего круга уборки»: забытое окно — это выключенный
// мониторинг, о котором никто не помнит.
func TestExpiredMaintenanceEndsItself(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := db.StartMaintenance(ctx, now.Add(time.Minute), ""); err != nil {
		t.Fatal(err)
	}

	if _, active, err := db.MaintenanceWindow(ctx, now.Add(2*time.Minute)); err != nil || active {
		t.Fatalf("истёкшее окно всё ещё действует: %v, %v", active, err)
	}
	// И оно действительно удалено, а не просто не отдано.
	if _, active, err := db.MaintenanceWindow(ctx, now); err != nil || active {
		t.Fatalf("окно вернулось: %v, %v", active, err)
	}
}
