package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent/internal/config"
	"agent/internal/health"
	"agent/internal/ignore"
	"agent/internal/links"
	"agent/internal/notify"
	"agent/internal/seo"
	"agent/internal/store"
	"agent/internal/vitals"
)

// ignoreAgent — агент с двумя сайтами и восемью «битыми» ссылками на
// a.test: шесть на claude.ai с ответом 403 и две настоящие.
func ignoreAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		SitesPath:    filepath.Join(dir, "sites.json"),
		ProjectsPath: filepath.Join(dir, "projects.json"),
	}
	if err := os.WriteFile(cfg.SitesPath, []byte(`[{"domain":"a.test"},{"domain":"b.test"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	var failures []links.Failure
	for _, path := range []string{"/", "/ru/", "/es/", "/de/", "/fr/", "/pt/"} {
		failures = append(failures, links.Failure{
			URL: "https://claude.ai/new" + path, Kind: "httpStatus", Code: 403, Sources: []string{"https://a.test" + path},
		})
	}
	failures = append(failures,
		links.Failure{URL: "https://gone.test/", Kind: "httpStatus", Code: 404, Sources: []string{"https://a.test/"}},
		links.Failure{URL: "https://slow.test/", Kind: "timeout", Sources: []string{"https://a.test/blog"}},
	)
	if err := db.SaveLinkReport(t.Context(), links.Report{
		Domain: "a.test", CheckedAt: "2026-11-12T10:00:00Z", Pages: 30, TotalLinks: 400,
		Broken: len(failures), Failures: failures,
	}); err != nil {
		t.Fatal(err)
	}
	return &Agent{db: db, rootCtx: t.Context(), cfg: cfg, notifier: notify.New(cfg)}
}

func claudeEntry() ignore.Entry {
	return ignore.Entry{
		Kind: ignore.KindLink, Domain: "A.test", What: "https://claude.ai/",
		Match: ignore.MatchPrefix, Response: "403", Scope: ignore.ScopeSite, Note: "робота не пускают",
	}
}

func linksReason(t *testing.T, snapshot health.Snapshot) (count int, found bool) {
	t.Helper()
	for _, project := range snapshot.Projects {
		for _, reason := range project.Reasons {
			if reason.Code == health.CodeLinksBroken {
				return reason.Count, true
			}
		}
	}
	return 0, false
}

// «Не учитывать» действует сразу (Р18): экран ссылок, свод и показатель
// проекта считают учтённое, «учитывать снова» возвращает всё как было.
func TestIgnoreReachesLinksAndHealth(t *testing.T) {
	agent := ignoreAgent(t)

	before, err := agent.snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count, ok := linksReason(t, before); !ok || count != 8 {
		t.Fatalf("до записи: повод %d, %v", count, ok)
	}

	// Предпросмотр называет охват и ничего не пишет.
	preview, err := agent.PreviewIgnore(claudeEntry())
	if err != nil || preview.(IgnoreAdded).Entry.Covers != 6 {
		t.Fatalf("предпросмотр: %+v, %v", preview, err)
	}
	if list, _ := agent.db.Ignores(t.Context()); len(list) != 0 {
		t.Fatal("предпросмотр записал запись")
	}

	answer, err := agent.AddIgnore(claudeEntry())
	if err != nil {
		t.Fatal(err)
	}
	added := answer.(IgnoreAdded)
	if added.Existed || added.Entry.Covers != 6 || added.Entry.Domain != "a.test" {
		t.Fatalf("ответ записи: %+v", added)
	}

	state, err := agent.Links()
	if err != nil {
		t.Fatal(err)
	}
	report := state.(LinksState).Reports[0]
	if report.Broken != 2 || report.Ignored != 6 {
		t.Fatalf("экран ссылок: broken %d, ignored %d", report.Broken, report.Ignored)
	}

	after, err := agent.snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count, ok := linksReason(t, after); !ok || count != 2 {
		t.Fatalf("повод после записи: %d, %v", count, ok)
	}
	checked := false
	for _, project := range after.Projects {
		for _, figure := range project.Figures {
			// У b.test ссылки не проверялись — его показатель «неизвестно».
			if figure.Area != health.AreaLinks || figure.Domain != "a.test" {
				continue
			}
			checked = true
			if figure.Value == nil || *figure.Value != 2 {
				t.Fatalf("показатель ссылок: %+v", figure)
			}
		}
	}
	if !checked {
		t.Fatal("показателя ссылок a.test нет")
	}

	// Повтор от второй копии — «уже есть».
	again, err := agent.AddIgnore(claudeEntry())
	if err != nil || !again.(IgnoreAdded).Existed {
		t.Fatalf("повтор: %+v, %v", again, err)
	}

	list, err := agent.Ignores()
	if err != nil {
		t.Fatal(err)
	}
	items := list.(IgnoresState)
	if len(items.Ignores) != 1 || items.Ignores[0].Covers != 6 || items.Limit != ignore.Limit {
		t.Fatalf("список: %+v", items)
	}

	removed, err := agent.RemoveIgnore(claudeEntry())
	if err != nil || !removed.(IgnoreRemoved).Removed {
		t.Fatalf("снятие: %+v, %v", removed, err)
	}
	back, _ := agent.snapshot(t.Context())
	if count, _ := linksReason(t, back); count != 8 {
		t.Fatalf("после «учитывать снова»: повод %d", count)
	}
}

// Сайта нет в списке мониторинга — отказ, а не запись, которую никто не
// увидит; негодная запись — отказ ignoreInvalid.
func TestAddIgnoreRefuses(t *testing.T) {
	agent := ignoreAgent(t)

	foreign := claudeEntry()
	foreign.Domain = "evil.test"
	if _, err := agent.AddIgnore(foreign); !errors.Is(err, ignore.ErrUnknownSite) {
		t.Fatalf("чужой сайт: %v", err)
	}
	broken := claudeEntry()
	broken.Response = ""
	if _, err := agent.AddIgnore(broken); !errors.Is(err, ignore.ErrInvalid) {
		t.Fatalf("без ответа: %v", err)
	}
}

// Список сайтов не прочитался — записи не трогаются (Р37); сайт убрали —
// его записи уходят (Р30).
func TestForgetIgnoresOnlyByReadableSites(t *testing.T) {
	agent := ignoreAgent(t)
	entry := claudeEntry()
	entry.Domain = "b.test"
	if _, err := agent.AddIgnore(entry); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		list, err := agent.db.Ignores(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}

	for name, content := range map[string]string{"битый файл": `[{"domain":`, "пустой список": `[]`} {
		if err := os.WriteFile(agent.cfg.SitesPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = agent.Links()
		_, _ = agent.Ignores()
		if count() != 1 {
			t.Fatalf("%s: записи стёрты", name)
		}
	}
	if err := os.Remove(agent.cfg.SitesPath); err != nil {
		t.Fatal(err)
	}
	_, _ = agent.Ignores()
	if count() != 1 {
		t.Fatal("файла нет: записи стёрты")
	}

	if err := os.WriteFile(agent.cfg.SitesPath, []byte(`[{"domain":"a.test"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = agent.Ignores()
	if count() != 0 {
		t.Fatal("сайт убран, а его запись осталась")
	}
}

// Совет Lighthouse — одной записью на обе стратегии (Р23): уходит в
// ignoredAdvice и в мобильном, и в компьютерном замере, оценки не тронуты
// (Р24), охват — число страниц.
func TestIgnoreAdviceBothStrategies(t *testing.T) {
	agent := ignoreAgent(t)
	agent.cfg.Vitals.Strategies = []string{"mobile", "desktop"}
	agent.cfg.Vitals.MaxPages = 5
	agent.cfg.Vitals.PagesPath = filepath.Join(t.TempDir(), "vitals.json")

	for _, strategy := range agent.cfg.Vitals.Strategies {
		if err := agent.db.SaveVitalsReport(t.Context(), vitals.Report{
			Domain: "a.test", Path: "/", URL: "https://a.test/", Strategy: strategy,
			CheckedAt: "2026-11-12T10:00:00Z",
			Lab: &vitals.Lab{
				Score:      61,
				Categories: []vitals.Category{{ID: "performance", Score: 61}, {ID: "accessibility", Score: 88}},
				Advice: []vitals.Advice{
					{ID: "unused-javascript", Category: "performance"},
					{ID: "color-contrast", Category: "accessibility"},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	answer, err := agent.AddIgnore(ignore.Entry{Kind: ignore.KindAdvice, Domain: "a.test", What: "unused-javascript", Scope: ignore.ScopeSite})
	if err != nil {
		t.Fatal(err)
	}
	if covers := answer.(IgnoreAdded).Entry.Covers; covers != 1 {
		t.Fatalf("охват совета: %d страниц, ожидалась одна", covers)
	}

	state, err := agent.Vitals()
	if err != nil {
		t.Fatal(err)
	}
	reports := state.(VitalsState).Reports
	if len(reports) != 2 {
		t.Fatalf("замеров: %d", len(reports))
	}
	for _, report := range reports {
		lab := report.Lab
		if len(lab.IgnoredAdvice) != 1 || lab.IgnoredAdvice[0].ID != "unused-javascript" ||
			len(lab.Advice) != 1 || lab.Advice[0].ID != "color-contrast" {
			t.Fatalf("%s: советы %+v, неучтённые %+v", report.Strategy, lab.Advice, lab.IgnoredAdvice)
		}
		if lab.Score != 61 || lab.Categories[0].Score != 61 || lab.Categories[1].Score != 88 {
			t.Fatalf("%s: оценки изменились: %+v", report.Strategy, lab.Categories)
		}
	}
}

// Ответы агента с записями «не учитывать» — образцом для приложения
// (трек AG, 251): Rust пересобирает ответ, а serde молча выбрасывает
// незнакомое поле, поэтому сторож приложения читает настоящий JSON агента,
// а не выдуманный. Образец сверяется здесь с живым ответом; обновить —
// AGENT_GOLDEN=1 go test -run IgnoreAnswersGolden ./cmd/agent/
func TestIgnoreAnswersGolden(t *testing.T) {
	agent := ignoreAgent(t)
	agent.cfg.Vitals.Strategies = []string{"mobile"}
	agent.cfg.Vitals.MaxPages = 5
	agent.cfg.Vitals.PagesPath = filepath.Join(t.TempDir(), "vitals.json")

	if err := agent.db.SaveVitalsReport(t.Context(), vitals.Report{
		Domain: "a.test", Path: "/", URL: "https://a.test/", Strategy: "mobile", CheckedAt: "2026-11-12T10:00:00Z",
		Lab: &vitals.Lab{Score: 61, Advice: []vitals.Advice{
			{ID: "unused-javascript", Category: "performance", Title: "Unused JavaScript"},
			{ID: "color-contrast", Category: "accessibility", Title: "Contrast"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	pages := []seo.Page{
		{URL: "https://a.test/", Status: 200, Issues: []seo.Issue{{Code: seo.IssueTitleLong, Severity: seo.SeverityOf(seo.IssueTitleLong)}}},
		{URL: "https://a.test/account/", Status: 200, Issues: []seo.Issue{{Code: seo.IssueNoindex, Severity: seo.SeverityOf(seo.IssueNoindex)}}},
	}
	for _, when := range []string{"2026-11-11T10:00:00Z", "2026-11-12T10:00:00Z"} {
		stats := seo.Stats{
			Pages: 2, Fetched: 2,
			Site:      &seo.SiteReport{Robots: seo.RobotsFound, Sitemap: &seo.SitemapReport{Found: true, Complete: true}},
			Canonical: &seo.CanonicalReport{Canonical: "https://a.test/"},
			Issues: seo.IssueSummary{Warning: 1, Notice: 1, Pages: 2, ByCode: []seo.CodeCount{
				{Code: seo.IssueNoindex, Severity: seo.SeverityOf(seo.IssueNoindex), Pages: 1},
				{Code: seo.IssueTitleLong, Severity: seo.SeverityOf(seo.IssueTitleLong), Pages: 1},
			}},
		}
		stats.Issues.Warning, stats.Issues.Notice = 0, 0
		for _, item := range stats.Issues.ByCode {
			switch item.Severity {
			case seo.SeverityWarning:
				stats.Issues.Warning++
			case seo.SeverityNotice:
				stats.Issues.Notice++
			}
		}
		stats.Checks = seo.BuildChecks(stats)
		if err := agent.db.SaveSeoScan(t.Context(), seo.Scan{Domain: "a.test", CheckedAt: when, Stats: stats}, pages); err != nil {
			t.Fatal(err)
		}
	}

	created := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, entry := range []ignore.Entry{
		claudeEntry(),
		{Kind: ignore.KindSeo, Domain: "a.test", What: seo.IssueNoindex, Scope: ignore.ScopePrefix, At: "/account/"},
		{Kind: ignore.KindSeo, Domain: "a.test", What: seo.IssueRobotsMissing, Scope: ignore.ScopeSite},
		{Kind: ignore.KindAdvice, Domain: "a.test", What: "unused-javascript", Scope: ignore.ScopeSite},
	} {
		normalized, err := ignore.Normalize(entry)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := agent.db.AddIgnore(t.Context(), normalized, created); err != nil {
			t.Fatal(err)
		}
	}

	answers := map[string]any{}
	for name, read := range map[string]func() (any, error){
		"links": agent.Links, "seo": agent.Seo, "vitals": agent.Vitals, "ignores": agent.Ignores,
	} {
		answer, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		answers[name] = answer
	}
	pagesAnswer, err := agent.SeoPages("a.test", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	answers["seoPages"] = pagesAnswer

	got, err := json.MarshalIndent(answers, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// День квоты замеров — сегодняшний: без замены образец устаревал бы в
	// полночь, и тест падал бы на следующий день без единой правки.
	got = []byte(strings.ReplaceAll(string(got), `"day": "`+store.Today()+`"`, `"day": "TODAY"`))
	golden := filepath.Join("testdata", "ignore-answers.json")
	if os.Getenv("AGENT_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("образца нет: %v (AGENT_GOLDEN=1)", err)
	}
	if string(want) != string(got)+"\n" {
		t.Fatalf("ответ агента разошёлся с образцом %s — обновить: AGENT_GOLDEN=1", golden)
	}
}
