package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"agent/internal/bots"
	"agent/internal/config"
	"agent/internal/logs"
	"agent/internal/store"
)

// Ответ `GET /robots` после трека AI: прежние поля — в прежнем виде для
// старого приложения 1.6.1 (Р17), новое — новыми полями. Заодно гоняет SQL
// новых таблиц на настоящей SQLite.
func TestRobotsAnswerKeepsTheOldShape(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.Config{}
	cfg.Logs.RetentionDays = 14
	agent := &Agent{db: db, rootCtx: t.Context(), cfg: cfg}
	ctx := t.Context()

	now := time.Now().UTC()
	day := now.Unix() - now.Unix()%86_400
	host := "shop.example"
	if err := db.AddRobotCounts(ctx, []logs.RobotCount{
		{Day: day, Host: host, Name: logs.MetaLines, Count: 100, LastAt: now},
		{Day: day, Host: host, Name: "GPTBot", Count: 20, LastAt: now},
		{Day: day, Host: host, Name: "Ahrefs", Count: 5, LastAt: now},
		{Day: day, Host: host, Name: "vpsFocus", Count: 7, LastAt: now},
		{Day: day, Host: host, Name: bots.Other, Count: 3, LastAt: now},
		{Day: day, Host: host, Name: logs.MetaAddrCheckable, Count: 25, LastAt: now},
		{Day: day, Host: host, Name: logs.MetaAddrProxy, Count: 4, LastAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddRobotChecks(ctx, []logs.RobotCheck{
		{Day: day, Host: host, Name: "GPTBot", Verified: 12, Unverified: 6, Unknown: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSiteTraffic(ctx, []logs.SiteTraffic{
		{Day: day, Host: host, Kind: logs.TrafficAll, Requests: 100},
		{Day: day, Host: host, Kind: bots.AITraining, Requests: 20},
		{Day: day, Host: host, Kind: bots.SEOTool, Requests: 5},
		{Day: day, Host: host, Kind: bots.Self, Requests: 7},
		{Day: day, Host: host, Kind: logs.TrafficOther, Requests: 3},
	}); err != nil {
		t.Fatal(err)
	}
	refusal := logs.RobotRefusal{Day: day, Host: host, Name: "GPTBot", Status: 403, Count: 4}
	if err := db.AddRobotRefusals(ctx, []logs.RobotRefusal{refusal}); err != nil {
		t.Fatal(err)
	}
	refusal.Count = 4
	if err := db.AddImpostorRefusals(ctx, []logs.RobotRefusal{refusal}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddRobotViolations(ctx, []logs.RobotViolation{
		{Day: day, Host: host, Name: "GPTBot", Count: 5, Example: "/private"},
		{Day: day, Host: host, Name: "ClaudeBot", Count: 2, Example: "/private"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddImpostorViolations(ctx, []logs.RobotViolation{
		{Day: day, Host: host, Name: "GPTBot", Count: 2},
		{Day: day, Host: host, Name: "ClaudeBot", Count: 2},
	}); err != nil {
		t.Fatal(err)
	}

	answer, err := agent.Robots(7)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var state RobotsState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}

	if len(state.Bots) != 14 || !state.NamesUnverified || !state.NamesChecked || state.ListDate == "" || state.ListSince == "" {
		t.Fatalf("шапка ответа: %+v", state)
	}
	if state.ChecksSince != time.Unix(day, 0).UTC().Format("2006-01-02") {
		t.Errorf("checksSince %q", state.ChecksSince)
	}
	if len(state.Hosts) != 1 {
		t.Fatalf("сайтов %d", len(state.Hosts))
	}
	site := state.Hosts[0]

	// Прежние поля: в visits — только прежние имена, новые роботы — в other,
	// наши обходы — ни там, ни там.
	if len(site.Visits) != 1 || site.Visits[0].Name != "GPTBot" || site.Visits[0].Count != 20 {
		t.Errorf("visits: %+v", site.Visits)
	}
	if site.Other != 3+5 {
		t.Errorf("other %d, ждали 8 (прочие и Ahrefs)", site.Other)
	}
	old := map[string]int64{}
	for _, kind := range site.Traffic.Kinds {
		old[kind.Kind] = kind.Requests
	}
	if len(old) != 2 || old[bots.AITraining] != 20 || old[logs.TrafficOther] != 3+5+7 {
		t.Errorf("прежние доли нагрузки: %v", old)
	}

	// Новые поля.
	roles := map[string]int64{}
	for _, kind := range site.Traffic.Roles {
		roles[kind.Kind] = kind.Requests
	}
	if roles[bots.Self] != 7 || roles[bots.SEOTool] != 5 {
		t.Errorf("роли нагрузки: %v", roles)
	}
	seen := map[string]RobotSeen{}
	for _, robot := range site.Robots {
		seen[robot.Name] = robot
	}
	gpt := seen["GPTBot"]
	if gpt.Role != bots.AITraining || gpt.Check == nil || gpt.Check.Unverified != 6 || !gpt.Checkable {
		t.Errorf("GPTBot в robots: %+v", gpt)
	}
	if ahrefs := seen["Ahrefs"]; ahrefs.Role != bots.SEOTool || ahrefs.Check != nil {
		t.Errorf("Ahrefs в robots: %+v", ahrefs)
	}
	if seen["vpsFocus"].Role != bots.Self {
		t.Errorf("наши обходы: %+v", seen["vpsFocus"])
	}
	if site.Addresses == nil || site.Addresses.Checkable != 25 || site.Addresses.Proxy != 4 {
		t.Errorf("причины адресов: %+v", site.Addresses)
	}
	if len(site.ImpostorRefusals) != 1 || site.ImpostorRefusals[0].Count != 4 {
		t.Errorf("отказы самозванцам: %+v", site.ImpostorRefusals)
	}
	// Все отказы GPTBot — самозванцам: вывода «отказывают» нет (Р9).
	for _, verdict := range site.Verdicts {
		if verdict.Name == "GPTBot" && verdict.Verdict == logs.VerdictRefused {
			t.Errorf("отказ самозванцам стал выводом: %+v", verdict)
		}
	}
	// Заходы в закрытое без самозванцев (Р24); у ClaudeBot все — чужие.
	if len(site.Violations) != 1 || site.Violations[0].Name != "GPTBot" ||
		site.Violations[0].Count != 3 || site.Violations[0].Impostors != 2 {
		t.Errorf("заходы в закрытое: %+v", site.Violations)
	}
}

// Что читали ИИ по просьбе людей (Р11, Р38, Р42): сначала «не нашёл»,
// потом по числу заходов; строка — страница с кодом и именами роботов.
func TestRobotsAnswerListsAIPages(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := config.Config{}
	cfg.Logs.RetentionDays = 14
	agent := &Agent{db: db, rootCtx: t.Context(), cfg: cfg}

	now := time.Now().UTC()
	day := now.Unix() - now.Unix()%86_400
	pages := []logs.RobotPage{
		{Day: day, Host: "a.example", Name: "Claude-User", Path: "/pricing", Status: 200, Count: 9},
		{Day: day, Host: "a.example", Name: "ChatGPT-User", Path: "/pricing", Status: 200, Count: 3},
		{Day: day, Host: "a.example", Name: "ChatGPT-User", Path: "/old", Status: 404, Count: 1},
		{Day: day, Host: "a.example", Name: "ChatGPT-User", Path: logs.OtherPath, Count: 5},
	}
	for i := 0; i < robotPagesShown+2; i++ {
		// Два робота: у каждого не больше суточного потолка, вместе — больше.
		name := "Claude-User"
		if i%2 == 1 {
			name = "ChatGPT-User"
		}
		pages = append(pages, logs.RobotPage{Day: day, Host: "b.example", Name: name,
			Path: "/p/" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Status: 200, Count: 1})
	}
	if err := db.AddRobotPages(t.Context(), pages); err != nil {
		t.Fatal(err)
	}
	answer, err := agent.Robots(7)
	if err != nil {
		t.Fatal(err)
	}
	state := answer.(RobotsState)
	byHost := map[string]RobotHost{}
	for _, host := range state.Hosts {
		byHost[host.Host] = host
	}
	a := byHost["a.example"]
	if len(a.Pages) != 2 || a.Pages[0].Path != "/old" || a.Pages[0].Outcome != logs.PageNotFound {
		t.Fatalf("порядок: %+v", a.Pages)
	}
	if a.Pages[1].Count != 12 || len(a.Pages[1].Names) != 2 || a.PagesOther != 5 {
		t.Fatalf("страница двух роботов: %+v, остальные %d", a.Pages[1], a.PagesOther)
	}
	b := byHost["b.example"]
	if len(b.Pages) != robotPagesShown || b.PagesMore != 2 {
		t.Fatalf("предел ответа: %d и ещё %d", len(b.Pages), b.PagesMore)
	}
}

// Потолок путей — на сутки, а не на проход: второй проход с новыми путями
// сверх потолка пишет их в «остальные», а уже известный путь — своей строкой.
func TestRobotPagesCapHoldsAcrossPasses(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	day := int64(1_763_510_400)
	pass := func(paths ...string) {
		pages := []logs.RobotPage{}
		for _, path := range paths {
			pages = append(pages, logs.RobotPage{Day: day, Host: "a.example", Name: "Claude-User", Path: path, Status: 200, Count: 1})
		}
		if err := db.AddRobotPages(t.Context(), pages); err != nil {
			t.Fatal(err)
		}
	}
	first := []string{}
	for i := 0; i < logs.PagesPerRobotDay; i++ {
		first = append(first, "/p"+string(rune('A'+i/26))+string(rune('a'+i%26)))
	}
	pass(first...)
	pass("/new-1", "/new-2", first[0])

	rows, err := db.RobotPages(t.Context(), time.Unix(day, 0))
	if err != nil {
		t.Fatal(err)
	}
	paths, other := 0, 0
	for _, row := range rows {
		switch row.Path {
		case logs.OtherPath:
			other = row.Count
		case first[0]:
			if row.Count != 2 {
				t.Errorf("известный путь: %d", row.Count)
			}
			paths++
		default:
			paths++
		}
	}
	if paths != logs.PagesPerRobotDay || other != 2 {
		t.Fatalf("путей %d, остальных %d", paths, other)
	}
}
