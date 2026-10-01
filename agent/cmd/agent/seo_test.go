package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent/internal/config"
	"agent/internal/seo"
	"agent/internal/store"
)

func TestPickSeoSitesOneOrAll(t *testing.T) {
	sites := []config.Site{{Domain: "a.test"}, {Domain: "b.test"}}

	if all, ok := pickSeoSites(sites, ""); !ok || len(all) != 2 {
		t.Fatalf("без домена — все сайты: %v %v", all, ok)
	}
	if one, ok := pickSeoSites(sites, " B.test "); !ok || len(one) != 1 || one[0].Domain != "b.test" {
		t.Fatalf("один сайт: %v %v", one, ok)
	}
	// Чужой адрес агент не обходит, кто бы его ни прислал.
	if none, ok := pickSeoSites(sites, "evil.test"); ok || len(none) != 0 {
		t.Fatalf("чужой домен принят: %v", none)
	}
}

// seoAgent — агент со списком сайтов и выключенным обходом: запросы к
// настоящим сайтам тесту не нужны.
func seoAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	sitesPath := filepath.Join(dir, "sites.json")
	if err := os.WriteFile(sitesPath, []byte(`[{"domain":"a.test","name":"Магазин"},{"domain":"b.test"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Agent{db: db, rootCtx: t.Context(), cfg: config.Config{SitesPath: sitesPath}}
}

func TestRunSeoRefusesUnknownSite(t *testing.T) {
	answer, err := seoAgent(t).RunSeo("evil.test")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := answer.(map[string]any)
	if !ok || result["started"] != false || result["error"] != seoUnknownSite {
		t.Fatalf("ответ на чужой домен: %#v", answer)
	}
}

func TestSeoStateListsSitesWithoutScans(t *testing.T) {
	// Сайт, который ни разу не обходили, обязан быть на экране вкладкой.
	answer, err := seoAgent(t).Seo()
	if err != nil {
		t.Fatal(err)
	}
	state := answer.(SeoState)
	if len(state.Scans) != 0 {
		t.Fatalf("обходов быть не должно: %d", len(state.Scans))
	}
	if len(state.Sites) != 2 || state.Sites[0] != (SeoSite{Domain: "a.test", Name: "Магазин"}) ||
		state.Sites[1] != (SeoSite{Domain: "b.test", Name: "b.test"}) {
		t.Fatalf("сайты: %+v", state.Sites)
	}
	if state.RunningDomain != "" {
		t.Fatalf("обход не идёт, а сайт назван: %q", state.RunningDomain)
	}
}

func TestOneSiteScanKeepsTheOthers(t *testing.T) {
	// Уборка удалённых сайтов — только в полном круге. Обход одного сайта
	// знает про один сайт, и уборка по нему стёрла бы снимки остальных.
	agent := seoAgent(t)
	agent.cfg.Seo.Timeout = 2 * time.Second
	agent.cfg.Seo.MaxPages = 1

	if err := agent.db.SaveSeoScan(t.Context(), seo.Scan{
		Domain:    "b.test",
		CheckedAt: "2026-09-28T10:00:00Z",
	}, []seo.Page{{URL: "https://b.test/", Status: 200}}); err != nil {
		t.Fatal(err)
	}

	// a.test в сети не существует: обход кончится ошибкой, но дойдёт до
	// записи и до места, где раньше шла уборка.
	if err := agent.crawlSites(t.Context(), "a.test"); err != nil {
		t.Fatal(err)
	}

	scans, err := agent.db.SeoScans(t.Context(), map[string]bool{"a.test": true, "b.test": true})
	if err != nil {
		t.Fatal(err)
	}
	domains := map[string]bool{}
	for _, scan := range scans {
		domains[scan.Domain] = true
	}
	if !domains["b.test"] {
		t.Fatalf("обход одного сайта стёр снимок другого: %v", domains)
	}
	if !domains["a.test"] {
		t.Fatalf("обход выбранного сайта не записан: %v", domains)
	}
}
