package main

import (
	"strings"
	"testing"
	"time"

	"agent/internal/bots"
)

// Тесты на образцах форматов, без сети: сборщик ходит в интернет только
// при запуске руками.

func TestAIRobotsRolesAndKeys(t *testing.T) {
	raw := []byte(`{
		"__usage_ai-training": {"function": "AI Data Scrapers", "description": "policy"},
		"GPTBot": {"function": "AI Data Scrapers", "description": "x"},
		"iaskspider/2.0": {"function": "AI Search Crawlers", "description": "x"},
		"Some Agent": {"function": "AI Agents", "description": "x"},
		"FreeText": {"function": "Retrieves data for something", "description": "x"},
		"Unsure": {"function": "AI Data Scrapers", "description": "It's not currently known to be artificially intelligent or AI-related."},
		"WARDBot": {"function": "AI Data Scrapers", "description": "It's not currently known to be artificially intelligent or AI-related."}
	}`)
	got, err := parseAIRobots(raw)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]candidate{}
	for _, item := range got {
		byName[item.name] = item
	}
	if _, ok := byName["__usage_ai-training"]; ok {
		t.Error("знак политики попал в роботы")
	}
	if _, ok := byName["WARDBot"]; ok {
		t.Error("пропущенное имя попало в роботы")
	}
	want := map[string]struct {
		role string
		rank int
	}{
		"GPTBot":     {bots.AITraining, rankAIRobotsPhrase},
		"iaskspider": {bots.AISearch, rankAIRobotsPhrase},
		"Some Agent": {bots.AIUser, rankAIRobotsPhrase},
		"FreeText":   {bots.AICrawler, rankAIRobotsText},
		"Unsure":     {"", 0},
	}
	for name, w := range want {
		item, ok := byName[name]
		if !ok {
			t.Errorf("%s потерян", name)
			continue
		}
		if item.role != w.role || item.roleRank != w.rank {
			t.Errorf("%s: роль %q/%d, ждали %q/%d", name, item.role, item.roleRank, w.role, w.rank)
		}
	}
	if m := byName["iaskspider"].match[0]; m.Text != "iaskspider" || m.Rule != bots.RuleToken {
		t.Errorf("версия в ключе не отрезана: %+v", m)
	}
}

const postHogSample = `class BotDefinition:
    name: str

BOT_DEFINITIONS: dict[str, BotDefinition] = {
    "GPTBot": BotDefinition(
        "GPTBot", "ai_crawler", "AI Agent", "OpenAI", documentation_url="https://bots.fyi/d/gptbot"
    ),
    "ExaSearchBot/": BotDefinition("Exa", "ai_search", "Bot", "Exa"),
    "seo4ajax.com": BotDefinition("seo4ajax", "seo_crawler", "Bot", "Prerender.io"),
    "^Cursor/[0-9.]+ \\(": BotDefinition("Cursor", "ai_assistant", "AI Agent", "Anysphere"),
    r"archive\.org_bot": BotDefinition("Internet Archive", "search_crawler", "Bot", "Internet Archive"),
    r"Safari/537\.3$": BotDefinition("Truncated WebKit UA", "headless_browser", "Automation", "Unknown"),
}

SOMETHING_ELSE = ("BotDefinition(",)
`

func TestPostHogParse(t *testing.T) {
	got, err := parsePostHog([]byte(postHogSample))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bots.Match{
		"GPTBot":           {Text: "GPTBot", Rule: bots.RuleWord},
		"Exa":              {Text: "ExaSearchBot", Rule: bots.RuleVersioned},
		"seo4ajax":         {Text: "seo4ajax.com", Rule: bots.RuleWord},
		"Cursor":           {Text: `^Cursor/[0-9.]+ \(`, Rule: bots.RuleRegex},
		"Internet Archive": {Text: "archive.org_bot", Rule: bots.RuleWord},
	}
	if len(got) != len(want) {
		t.Fatalf("записей %d, ждали %d (примета браузера пропускается)", len(got), len(want))
	}
	for _, item := range got {
		if w, ok := want[item.name]; !ok || item.match[0] != w {
			t.Errorf("%s: %+v, ждали %+v", item.name, item.match[0], w)
		}
	}
}

// Сторож Р50: запись, которую шаблон не узнал, роняет сборку, а не
// пропадает молча.
func TestPostHogUnparsedEntryFails(t *testing.T) {
	broken := strings.Replace(postHogSample, `"ExaSearchBot/": BotDefinition("Exa"`, `"ExaSearchBot/": BotDefinition(name="Exa"`, 1)
	if _, err := parsePostHog([]byte(broken)); err == nil {
		t.Fatal("нераспознанная запись не уронила сборку")
	}
}

func TestPostHogNewBrowserHeuristicFails(t *testing.T) {
	added := strings.Replace(postHogSample, "}\n\nSOMETHING", `    r"Chrome/99$": BotDefinition("Odd UA", "headless_browser", "Automation", "Unknown"),
}

SOMETHING`, 1)
	if _, err := parsePostHog([]byte(added)); err == nil || !strings.Contains(err.Error(), "postHogSkipped") {
		t.Fatalf("новая примета браузера прошла без решения: %v", err)
	}
}

func TestPostHogUnknownCategoryFails(t *testing.T) {
	bad := strings.Replace(postHogSample, `"seo_crawler"`, `"brand_new"`, 1)
	if _, err := parsePostHog([]byte(bad)); err == nil {
		t.Fatal("незнакомая категория прошла")
	}
}

func TestBotscentParse(t *testing.T) {
	tokens := []byte(`{"tokens": [
		{"token": "curl", "name": "curl", "match": "versioned"},
		{"token": "node", "name": "node", "match": "exact"},
		{"token": "Yeti", "name": "naver", "match": "versioned"},
		{"token": "TelegramBot", "name": "telegram"}
	]}`)
	names := []byte(`{"names": {
		"curl": {"display": "curl", "kind": "client"},
		"node": {"display": "Node", "kind": "client"},
		"naver": {"display": "Naver", "kind": "crawler"},
		"telegram": {"display": "Telegram", "kind": "previewer"}
	}}`)
	got, err := parseBotscent(tokens, names)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]candidate{
		"curl":     {role: bots.Script, match: []bots.Match{{Text: "curl", Rule: bots.RuleVersioned}}},
		"Node":     {role: bots.Script, match: []bots.Match{{Text: "node", Rule: bots.RuleExact}}},
		"Naver":    {role: "", match: []bots.Match{{Text: "Yeti", Rule: bots.RuleVersioned}}},
		"Telegram": {role: bots.LinkPreview, match: []bots.Match{{Text: "TelegramBot", Rule: bots.RuleToken}}},
	}
	for _, item := range got {
		w := want[item.name]
		if item.role != w.role || item.match[0] != w.match[0] {
			t.Errorf("%s: %q %+v, ждали %q %+v", item.name, item.role, item.match[0], w.role, w.match[0])
		}
	}
	bad := []byte(`{"tokens": [{"token": "x", "name": "missing"}]}`)
	if _, err := parseBotscent(bad, names); err == nil {
		t.Error("токен с неизвестным именем прошёл")
	}
}

func TestMergeOrderAndUnion(t *testing.T) {
	all := []candidate{
		{source: "own", name: "GPTBot", role: bots.AITraining, roleRank: rankOwn, legacy: true,
			match: []bots.Match{{Text: "GPTBot", Rule: bots.RuleToken}}},
		{source: "ai-robots-txt", name: "GPTBot", role: bots.AICrawler, roleRank: rankAIRobotsText,
			match: []bots.Match{{Text: "GPTBot", Rule: bots.RuleToken}}},
		{source: "posthog", name: "GPT Bot", role: bots.AICrawler, roleRank: rankPostHog,
			match: []bots.Match{{Text: "gptbot", Rule: bots.RuleWord}}},
		// Роль у PostHog, имя — у ai-robots: имя по старшинству имён,
		// роль — по старшинству ролей.
		{source: "ai-robots-txt", name: "Naver", role: bots.AICrawler, roleRank: rankAIRobotsText,
			match: []bots.Match{{Text: "Yeti", Rule: bots.RuleToken}}},
		{source: "posthog", name: "Naver Yeti", role: bots.SearchEngine, roleRank: rankPostHog,
			match: []bots.Match{{Text: "Yeti", Rule: bots.RuleVersioned}}},
		{source: "botscent", name: "Yeti", role: "", match: []bots.Match{{Text: "yeti", Rule: bots.RuleVersioned}}},
		// Пропущенный текст источника выпадает.
		{source: "ai-robots-txt", name: "Code", role: bots.AIUser, roleRank: rankAIRobotsPhrase,
			match: []bots.Match{{Text: "Code", Rule: bots.RuleToken}}},
	}
	robots, err := mergeNoCheck(all)
	if err != nil {
		t.Fatal(err)
	}
	if len(robots) != 2 {
		t.Fatalf("роботов %d: %+v", len(robots), robots)
	}
	gpt, naver := robots[0], robots[1]
	if gpt.Name != "GPTBot" || gpt.Role != bots.AITraining || gpt.RoleFrom != "own" || !gpt.Legacy {
		t.Errorf("GPTBot: %+v", gpt)
	}
	if len(gpt.Match) != 2 {
		t.Errorf("правила GPTBot не слиты: %+v", gpt.Match)
	}
	if naver.Name != "Naver" || naver.Role != bots.SearchEngine || naver.RoleFrom != "posthog" {
		t.Errorf("Naver: %+v", naver)
	}
}

func TestMergeWithoutRoleFails(t *testing.T) {
	all := []candidate{{source: "botscent", name: "Mystery", match: []bots.Match{{Text: "Mystery", Rule: bots.RuleToken}}}}
	if _, err := mergeNoCheck(all); err == nil || !strings.Contains(err.Error(), "Mystery") {
		t.Fatalf("робот без роли прошёл: %v", err)
	}
}

func TestMergeNameClashFails(t *testing.T) {
	all := []candidate{
		{source: "ai-robots-txt", name: "Awario", role: bots.AITraining, roleRank: rankAIRobotsPhrase,
			match: []bots.Match{{Text: "Awario", Rule: bots.RuleToken}}},
		{source: "posthog", name: "Awario", role: bots.SearchEngine, roleRank: rankPostHog,
			match: []bots.Match{{Text: "AwarioBot", Rule: bots.RuleWord}}},
	}
	if _, err := mergeNoCheck(all); err == nil {
		t.Fatal("одно имя с двумя ролями прошло")
	}
}

// Наша таблица сама по себе проходит сторожа: 14 прежних, семейства,
// роли из перечня.
func TestOwnRowsPassChecks(t *testing.T) {
	if _, err := merge(ownCandidates()); err != nil {
		t.Fatal(err)
	}
	for _, row := range ownRows {
		if row.why == "" {
			t.Errorf("%s: строка нашей таблицы без причины", row.name)
		}
	}
}

func TestSAIL(t *testing.T) {
	raw := []byte(`schema        = "draft-kelley-webbotauth-sail-00"
modified      = 2026-11-14T02:20:00Z
expires_after = 3600  # seconds

[[prefix_sets]]
name          = "agentic-browser-use"
exclusive     = true
ipv4_prefixes = [
  "136.71.21.147",
  "34.31.25.14",
]
ipv6_prefixes = []

[[prefix_sets]]
name          = "other"
ipv4_prefixes = ["10.0.0.0/8"]
ipv6_prefixes = []

[[services]]
name        = "Claude-Agent"
exhaustive  = true
prefix_sets = ["agentic-browser-use"]
`)
	modified, items, err := parseSAIL(raw, "Claude-Agent")
	if err != nil {
		t.Fatal(err)
	}
	if modified != "2026-11-14T02:20:00Z" || strings.Join(items, ",") != "136.71.21.147,34.31.25.14" {
		t.Fatalf("%q %v", modified, items)
	}
	if _, _, err := parseSAIL(raw, "Nobody"); err == nil {
		t.Error("нет сервиса — а ошибки нет")
	}
	if _, _, err := parseSAIL(append(raw, []byte("[weird]\n")...), "Claude-Agent"); err == nil {
		t.Error("незнакомый блок прошёл")
	}
}

func TestNormalizeFloorAndOrder(t *testing.T) {
	v4, v6, err := normalize([]string{"20.0.0.5/24", "1.2.3.4", "20.0.0.0/24", "2001:db8::/48"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(v4, ",") != "1.2.3.4/32,20.0.0.0/24" || strings.Join(v6, ",") != "2001:db8::/48" {
		t.Fatalf("%v %v", v4, v6)
	}
	for _, wide := range []string{"10.0.0.0/8", "2001::/16"} {
		if _, _, err := normalize([]string{wide}, true); err == nil {
			t.Errorf("%s прошёл пол ширины", wide)
		}
	}
	if _, _, err := normalize([]string{"104.16.0.0/13"}, false); err != nil {
		t.Errorf("у прокси пола нет: %v", err)
	}
	if _, _, err := normalize([]string{"not-an-address"}, true); err == nil {
		t.Error("мусор прошёл")
	}
}

func TestBuildSetDates(t *testing.T) {
	built := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	source := rangeSource{id: "x", format: formatPrefixes}

	fresh, err := buildSet(source, []byte(`{"creationTime": "2026-11-18T15:45:52.000000", "prefixes": [{"ipv4Prefix": "1.2.3.0/24"}]}`), built)
	if err != nil || fresh.Stale || fresh.Created != "2026-11-18" || fresh.CreatedFrom != "file" {
		t.Errorf("свежий: %+v %v", fresh, err)
	}
	// Р54: файл Bing от 2024-01-03 — «не проверить».
	stale, err := buildSet(source, []byte(`{"creationTime": "2024-01-03T10:00:00.121331", "prefixes": [{"ipv4Prefix": "1.2.3.0/24"}]}`), built)
	if err != nil || !stale.Stale {
		t.Errorf("старый: %+v %v", stale, err)
	}
	// Ahrefs даты не пишет — день скачивания.
	nodate, err := buildSet(source, []byte(`{"prefixes": [{"ipv6Prefix": "2001:db8::/48"}]}`), built)
	if err != nil || nodate.Created != "2026-11-19" || nodate.CreatedFrom != "download" {
		t.Errorf("без даты: %+v %v", nodate, err)
	}
	if _, err := buildSet(source, []byte(`{"creationTime": "2026-11-18", "prefixes": []}`), built); err == nil {
		t.Error("пустой набор прошёл")
	}
	if _, err := buildSet(source, []byte(`{"creationTime": "2026-11-18"}`), built); err == nil {
		t.Error("файл без prefixes прошёл")
	}
}

func TestShrinkGuard(t *testing.T) {
	previous := &bots.Ranges{Sets: []bots.Set{{ID: "a", V4: make([]string, 10)}}}
	now := []bots.Set{{ID: "a", Names: []string{"A"}, V4: make([]string, 4)}}
	if err := checkSets(now, previous, nil); err == nil {
		t.Fatal("набор сжался с 10 до 4 — а сборка прошла")
	}
	if err := checkSets(now, previous, map[string]bool{"a": true}); err != nil {
		t.Fatalf("разрешённое сжатие: %v", err)
	}
	now[0].V4 = make([]string, 5)
	if err := checkSets(now, previous, nil); err != nil {
		t.Fatalf("ровно половина — не урезан: %v", err)
	}
	twice := []bots.Set{{ID: "a", Names: []string{"A"}}, {ID: "b", Names: []string{"A"}}}
	if err := checkSets(twice, nil, nil); err == nil {
		t.Error("имя в двух наборах прошло")
	}
}

func TestResolveNamesByToken(t *testing.T) {
	robots := []bots.Robot{
		{Name: "DuckDuckGo", Match: []bots.Match{{Text: "DuckDuckBot", Rule: bots.RuleWord}}},
		{Name: "Google-Extended", RobotsOnly: true, Match: []bots.Match{{Text: "Google-Extended", Rule: bots.RuleToken}}},
	}
	names, err := resolveNames([]string{"duckduckbot"}, robots)
	if err != nil || len(names) != 1 || names[0] != "DuckDuckGo" {
		t.Fatalf("%v %v", names, err)
	}
	if _, err := resolveNames([]string{"Google-Extended"}, robots); err == nil {
		t.Error("правило только для robots.txt получило диапазоны")
	}
	if _, err := resolveNames([]string{"Typo-Bot"}, robots); err == nil {
		t.Error("опечатка в токене прошла")
	}
}
