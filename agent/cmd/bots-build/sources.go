package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"agent/internal/bots"
)

// Источники имён — по закреплённым коммитам (Р30). Сменить коммит — значит
// прочитать, что поменялось у источника, и пересобрать; тогда же сверить
// метку лицензии.
type nameSource struct {
	id      string
	repo    string
	commit  string
	files   []string
	license string
	// Метка лицензии, без которой сборка отказывается (Р30).
	licenseMark func(text string) bool
}

const rawGitHub = "https://raw.githubusercontent.com/"

var nameSources = []nameSource{
	{
		id: "ai-robots-txt", repo: "ai-robots-txt/ai.robots.txt",
		commit: "be22abebcf663f7080766ad814829396dd19c659",
		files:  []string{"robots.json"}, license: "LICENSE",
		licenseMark: func(text string) bool { return strings.HasPrefix(strings.TrimSpace(text), "MIT License") },
	},
	{
		id: "botscent", repo: "nalinbhardwaj/botscent",
		commit: "7f9480037c60e2ebac7d655ede3f885bda27bde4",
		files:  []string{"registry/tokens.json", "registry/names.json"}, license: "LICENSE",
		licenseMark: func(text string) bool { return strings.HasPrefix(strings.TrimSpace(text), "MIT License") },
	},
	{
		// Файл определений лежит вне `ee/`, а всё вне `ee/` у PostHog — под
		// MIT Expat; путь проверяется отдельно, в postHogOutsideEE.
		id: "posthog", repo: "PostHog/posthog",
		commit: "eda7f6b41eb6fd33b4793f9b98de4d6c7badb0b3",
		files:  []string{postHogFile}, license: "LICENSE",
		licenseMark: func(text string) bool { return strings.Contains(text, `"MIT Expat" license`) },
	},
}

const postHogFile = "products/web_analytics/backend/hogql_queries/bot_definitions.py"

func (s nameSource) url(file string) string {
	return rawGitHub + s.repo + "/" + s.commit + "/" + file
}

func postHogOutsideEE() bool {
	return !strings.HasPrefix(postHogFile, "ee/") && !strings.Contains(postHogFile, "/ee/")
}

// candidate — одно имя одного источника до сведения.
type candidate struct {
	source string
	name   string
	// Роль; пусто — источник её не даёт или не даёт надёжно.
	role string
	// Старшинство роли (Р51): меньше — главнее.
	roleRank int
	match    []bots.Match
	// Только для нашей таблицы.
	family     string
	legacy     bool
	robotsOnly bool
}

// Старшинство источников (Р51). Имя берётся в том же порядке.
const (
	rankOwn = iota + 1
	rankAIRobotsPhrase
	rankPostHog
	rankAIRobotsText
	rankBotscent
)

func nameRank(source string) int {
	switch source {
	case "own":
		return 1
	case "ai-robots-txt":
		return 2
	case "posthog":
		return 3
	default:
		return 4
	}
}

// Категории ai-robots: начало поля `function`. Остальной текст — свободный,
// тогда робот «другой робот ИИ» с самым младшим старшинством ИИ (Р51).
var aiRobotsPhrases = []struct {
	prefix string
	role   string
}{
	{"AI Search Crawlers", bots.AISearch},
	{"AI Data Scrapers", bots.AITraining},
	{"AI Assistants", bots.AIUser},
	{"AI Agents", bots.AIUser},
	{"AI Coding Agents", bots.AIUser},
	{"AI Data Providers", bots.AICrawler},
	{"Undocumented AI Agents", bots.AICrawler},
}

// Оговорка ai-robots у записей, о которых он сам ничего не знает.
const aiRobotsUnsure = "not currently known to be artificially intelligent or AI-related"

func parseAIRobots(raw []byte) ([]candidate, error) {
	var entries map[string]struct {
		Function    string `json:"function"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("ai-robots-txt: %w", err)
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []candidate{}
	for _, key := range keys {
		// `__usage_ai-training` и подобные — знаки политики, а не роботы.
		if strings.HasPrefix(key, "__") {
			continue
		}
		// «iaskspider/2.0», «MistralAI-User/1.0»: в строке робота стоит имя с
		// версией, совпадение — по имени.
		name, _, _ := strings.Cut(key, "/")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("ai-robots-txt: пустое имя в ключе %q", key)
		}
		item := candidate{source: "ai-robots-txt", name: name, role: bots.AICrawler, roleRank: rankAIRobotsText,
			match: []bots.Match{{Text: name, Rule: bots.RuleToken}}}
		// Сам список пишет, что не знает, связан ли робот с ИИ: тогда его
		// категория — не довод, и роль дают другие источники или наша
		// таблица (Awario — сервис упоминаний, а не обучение нейросетей).
		if _, skip := aiRobotsSkipped[name]; skip {
			continue
		}
		if strings.Contains(entries[key].Description, aiRobotsUnsure) {
			item.role, item.roleRank = "", 0
			out = append(out, item)
			continue
		}
		function := strings.TrimSpace(entries[key].Function)
		for _, phrase := range aiRobotsPhrases {
			if strings.HasPrefix(function, phrase.prefix) {
				item.role, item.roleRank = phrase.role, rankAIRobotsPhrase
				break
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// Шаблон записи словаря PostHog (сверка 270): группы — сырая ли строка,
// шаблон, имя, категория, тип, владелец.
var postHogEntry = regexp.MustCompile(`(?m)^\s{4}(r?)"([^"]+)":\s*BotDefinition\(\s*"([^"]*)",\s*"([^"]+)",\s*"([^"]+)",\s*"([^"]*)"`)

var postHogCategories = map[string]string{
	"search_crawler":   bots.SearchEngine,
	"ai_search":        bots.AISearch,
	"ai_assistant":     bots.AIUser,
	"ai_crawler":       bots.AICrawler,
	"seo_crawler":      bots.SEOTool,
	"social_crawler":   bots.LinkPreview,
	"monitoring":       bots.Monitoring,
	"http_client":      bots.Script,
	"headless_browser": bots.Script,
}

// Знаки регулярного выражения. Точка — отдельно: в буквальных шаблонах
// PostHog («seo4ajax.com») она значит точку, и мы читаем её так же — строже
// регулярки, а не шире.
const regexMeta = `\^$|?*+()[]{}`

func parsePostHog(raw []byte) ([]candidate, error) {
	text := string(raw)
	start := strings.Index(text, "BOT_DEFINITIONS: dict[str, BotDefinition] = {")
	if start < 0 {
		return nil, fmt.Errorf("posthog: словарь BOT_DEFINITIONS не найден")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		return nil, fmt.Errorf("posthog: конец словаря не найден")
	}
	body := text[start : start+end]
	// Сторож разбора (Р50): записей столько же, сколько `BotDefinition(`.
	// Не сошлось — какую-то запись шаблон не узнал, и она пропала бы молча.
	want := strings.Count(body, "BotDefinition(")
	found := postHogEntry.FindAllStringSubmatch(body, -1)
	if len(found) != want {
		return nil, fmt.Errorf("posthog: разобрано %d записей из %d", len(found), want)
	}

	out := []candidate{}
	for _, entry := range found {
		rawString, key, name, category, operator := entry[1] == "r", entry[2], entry[3], entry[4], entry[6]
		pattern := key
		if !rawString {
			// В обычной строке Python `\\` — одна обратная черта; других
			// экранирований в словаре нет, а появятся — отказ, а не догадка.
			unescaped := strings.ReplaceAll(key, `\\`, "\x00")
			if strings.Contains(unescaped, `\`) {
				return nil, fmt.Errorf("posthog: незнакомое экранирование в %q", key)
			}
			pattern = strings.ReplaceAll(unescaped, "\x00", `\`)
		}
		if _, skip := postHogSkipped[pattern]; skip {
			continue
		}
		role, ok := postHogCategories[category]
		if !ok {
			return nil, fmt.Errorf("posthog: незнакомая категория %q у %q", category, key)
		}
		match, err := postHogMatch(pattern)
		if err != nil {
			return nil, fmt.Errorf("posthog: %q: %w", key, err)
		}
		if match.Rule == bots.RuleRegex && category == "headless_browser" && operator == "Unknown" {
			// Новая регулярка-примета «странного браузера» без имени: решить
			// руками, брать ли её (postHogSkipped), — ложный робот хуже
			// пропущенного. Буквальное слово («Mozlila/») — имя, его берём.
			return nil, fmt.Errorf("posthog: новая примета браузера без имени %q (%s) — решить в postHogSkipped", pattern, name)
		}
		out = append(out, candidate{source: "posthog", name: strings.TrimSpace(name), role: role, roleRank: rankPostHog,
			match: []bots.Match{match}})
	}
	return out, nil
}

// postHogMatch — буквальный шаблон становится словом или «имя/версия»,
// настоящий — регуляркой, которую обязан понять RE2 (Р50).
func postHogMatch(pattern string) (bots.Match, error) {
	literal := strings.ReplaceAll(pattern, `\.`, ".")
	if !strings.ContainsAny(literal, regexMeta) {
		if name, ok := strings.CutSuffix(literal, "/"); ok && !strings.Contains(name, "/") {
			return bots.Match{Text: name, Rule: bots.RuleVersioned}, nil
		}
		return bots.Match{Text: literal, Rule: bots.RuleWord}, nil
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return bots.Match{}, fmt.Errorf("RE2 не понимает: %w", err)
	}
	return bots.Match{Text: pattern, Rule: bots.RuleRegex}, nil
}

var botscentKinds = map[string]string{
	"computer-use-agent": bots.AIUser,
	"previewer":          bots.LinkPreview,
	"client":             bots.Script,
	"automation":         bots.Script,
	// `crawler` и `fetcher` — кто угодно, от поисковика до SEO-сервиса: роль
	// только ручной строкой нашей таблицы (Р51).
	"crawler": "",
	"fetcher": "",
}

func parseBotscent(tokensRaw, namesRaw []byte) ([]candidate, error) {
	var tokens struct {
		Tokens []struct {
			Token string `json:"token"`
			Name  string `json:"name"`
			Match string `json:"match"`
		} `json:"tokens"`
	}
	var names struct {
		Names map[string]struct {
			Display string `json:"display"`
			Kind    string `json:"kind"`
		} `json:"names"`
	}
	if err := json.Unmarshal(tokensRaw, &tokens); err != nil {
		return nil, fmt.Errorf("botscent tokens: %w", err)
	}
	if err := json.Unmarshal(namesRaw, &names); err != nil {
		return nil, fmt.Errorf("botscent names: %w", err)
	}
	if len(tokens.Tokens) == 0 || len(names.Names) == 0 {
		return nil, fmt.Errorf("botscent: пустой список")
	}
	out := []candidate{}
	for _, token := range tokens.Tokens {
		info, ok := names.Names[token.Name]
		if !ok {
			return nil, fmt.Errorf("botscent: токен %q ссылается на неизвестное имя %q", token.Token, token.Name)
		}
		role, ok := botscentKinds[info.Kind]
		if !ok {
			return nil, fmt.Errorf("botscent: незнакомый вид %q у %q", info.Kind, token.Name)
		}
		rule := bots.RuleToken
		switch token.Match {
		case "", "token":
		case "versioned":
			rule = bots.RuleVersioned
		case "exact":
			rule = bots.RuleExact
		default:
			return nil, fmt.Errorf("botscent: незнакомое правило %q у %q", token.Match, token.Token)
		}
		name := strings.TrimSpace(info.Display)
		if name == "" {
			name = token.Name
		}
		item := candidate{source: "botscent", name: name, role: role, roleRank: rankBotscent,
			match: []bots.Match{{Text: token.Token, Rule: rule}}}
		if role == "" {
			item.roleRank = 0
		}
		out = append(out, item)
	}
	return out, nil
}
