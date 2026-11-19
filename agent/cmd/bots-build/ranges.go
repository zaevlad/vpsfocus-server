package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"agent/internal/bots"
)

// Файлы диапазонов — у владельцев роботов. Коммитов у них нет: берётся
// текущий файл, а дата — его собственная (Р54).
type rangeSource struct {
	id     string
	url    string
	format string // prefixes | sail
	// Сервис в файле SAIL.
	service string
	// Токены из строки User-agent: имя робота в списке находится по ним, а
	// не по отображаемому имени, которое выбирает сведение источников.
	tokens []string
}

const (
	formatPrefixes = "prefixes"
	formatSAIL     = "sail"
)

const googleRanges = "https://developers.google.com/static/crawling/ipranges/"

var rangeSources = []rangeSource{
	{id: "openai-chatgpt-user", url: "https://openai.com/chatgpt-user.json", format: formatPrefixes, tokens: []string{"ChatGPT-User"}},
	{id: "openai-searchbot", url: "https://openai.com/searchbot.json", format: formatPrefixes, tokens: []string{"OAI-SearchBot"}},
	{id: "openai-gptbot", url: "https://openai.com/gptbot.json", format: formatPrefixes, tokens: []string{"GPTBot"}},
	{id: "perplexity-user", url: "https://www.perplexity.ai/perplexity-user.json", format: formatPrefixes, tokens: []string{"Perplexity-User"}},
	{id: "perplexitybot", url: "https://www.perplexity.ai/perplexitybot.json", format: formatPrefixes, tokens: []string{"PerplexityBot"}},
	{id: "google-common", url: googleRanges + "common-crawlers.json", format: formatPrefixes, tokens: googleCommon},
	{id: "google-special", url: googleRanges + "special-crawlers.json", format: formatPrefixes, tokens: googleSpecial},
	{id: "google-user-fetchers", url: googleRanges + "user-triggered-fetchers-google.json", format: formatPrefixes, tokens: googleFetchers},
	{id: "google-user-agents", url: googleRanges + "user-triggered-agents.json", format: formatPrefixes, tokens: googleAgents},
	{id: "bingbot", url: "https://www.bing.com/toolbox/bingbot.json", format: formatPrefixes, tokens: []string{"Bingbot"}},
	{id: "applebot", url: "https://search.developer.apple.com/applebot.json", format: formatPrefixes, tokens: []string{"Applebot"}},
	{id: "duckduckbot", url: "https://duckduckgo.com/duckduckbot.json", format: formatPrefixes, tokens: []string{"DuckDuckBot"}},
	{id: "ccbot", url: "https://index.commoncrawl.org/ccbot.json", format: formatPrefixes, tokens: []string{"CCBot"}},
	{id: "ahrefs", url: "https://api.ahrefs.com/v3/public/crawler-ip-ranges", format: formatPrefixes, tokens: []string{"AhrefsBot", "AhrefsSiteAudit"}},
	{id: "anthropic-sail", url: "https://claude.com/.well-known/sail", format: formatSAIL, service: "Claude-Agent", tokens: []string{"Claude-Agent"}},
}

// Службы Google по их файлам (developers.google.com/crawling/docs). Токен
// обязан найтись в списке роботов — иначе отказ: опечатка здесь молча
// оставила бы робота без проверки.
var (
	googleCommon   = []string{"Googlebot", "Googlebot-Image", "Googlebot-Video", "Googlebot-News", "GoogleOther", "GoogleOther-Image", "GoogleOther-Video", "Google-InspectionTool", "Google-CloudVertexBot", "Storebot-Google"}
	googleSpecial  = []string{"AdsBot-Google", "AdsBot-Google-Mobile", "Mediapartners-Google", "APIs-Google", "Google-Safety"}
	googleFetchers = []string{"FeedFetcher-Google", "Google-Site-Verification", "Google-Read-Aloud", "Google-CWS", "Google-Pinpoint", "GoogleProducer", "GoogleMessages"}
	googleAgents   = []string{"Google-Agent"}
)

var cloudflareURLs = []string{"https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"}

// Пол ширины (Р32): шире — ошибка источника, а не робот. Такой диапазон
// сделал бы «настоящим» кого угодно из половины провайдера.
const (
	floorV4 = 16
	floorV6 = 32
)

// Файл старше этого на день сборки — «не проверить» (Р54).
const staleAfter = 365 * 24 * time.Hour

// Урезанный файл (Р32): меньше половины префиксов прошлой сборки — отказ,
// пока человек не посмотрит и не разрешит флагом -allow-shrink. Ноль —
// отказ всегда: пустой набор сделал бы всех «назвался».
func shrunk(previous, now int) bool {
	return previous >= 2 && now*2 < previous
}

type prefixFile struct {
	CreationTime *string `json:"creationTime"`
	Prefixes     []struct {
		V4 string `json:"ipv4Prefix"`
		V6 string `json:"ipv6Prefix"`
	} `json:"prefixes"`
}

// parsePrefixes — формат Google, OpenAI, Perplexity, Bing, Apple и др.
// Возвращает дату файла (пусто — поля нет) и префиксы.
func parsePrefixes(raw []byte) (string, []string, error) {
	var file prefixFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&file); err != nil {
		return "", nil, err
	}
	if file.Prefixes == nil {
		return "", nil, fmt.Errorf("нет поля prefixes")
	}
	out := []string{}
	for _, item := range file.Prefixes {
		switch {
		case item.V4 != "" && item.V6 == "":
			out = append(out, item.V4)
		case item.V6 != "" && item.V4 == "":
			out = append(out, item.V6)
		default:
			return "", nil, fmt.Errorf("запись без префикса или с двумя")
		}
	}
	created := ""
	if file.CreationTime != nil {
		created = *file.CreationTime
	}
	return created, out, nil
}

// SAIL (черновик IETF, похож на TOML) разбирается узко (Р55): `modified`,
// блоки `[[prefix_sets]]` с `name` и списками, блоки `[[services]]` с
// `name` и `prefix_sets`. Всё прочее в блоках — пропускается, незнакомый
// блок или строка, которую не понять, — отказ.
var (
	sailKey    = regexp.MustCompile(`^([a-z0-9_]+)\s*=\s*(.*)$`)
	sailString = regexp.MustCompile(`^"([^"]*)"$`)
)

func parseSAIL(raw []byte, service string) (string, []string, error) {
	type block struct {
		kind  string
		name  string
		lists map[string][]string
	}
	var (
		blocks   []*block
		current  *block
		modified string
		openList string
		listVals []string
	)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if i := strings.Index(text, "#"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if openList != "" {
			for _, part := range strings.Split(text, ",") {
				part = strings.TrimSpace(part)
				if part == "]" || part == "" {
					continue
				}
				part = strings.TrimSuffix(part, "]")
				m := sailString.FindStringSubmatch(strings.TrimSpace(part))
				if m == nil {
					return "", nil, fmt.Errorf("sail, строка %d: не строка в списке", line)
				}
				listVals = append(listVals, m[1])
			}
			if strings.HasSuffix(text, "]") {
				current.lists[openList] = listVals
				openList, listVals = "", nil
			}
			continue
		}
		switch text {
		case "[[prefix_sets]]", "[[services]]":
			current = &block{kind: strings.Trim(text, "[]"), lists: map[string][]string{}}
			blocks = append(blocks, current)
			continue
		}
		if strings.HasPrefix(text, "[") {
			return "", nil, fmt.Errorf("sail, строка %d: незнакомый блок %s", line, text)
		}
		m := sailKey.FindStringSubmatch(text)
		if m == nil {
			return "", nil, fmt.Errorf("sail, строка %d: не понять", line)
		}
		key, value := m[1], strings.TrimSpace(m[2])
		if current == nil {
			if key == "modified" {
				modified = value
			}
			continue
		}
		if strings.HasPrefix(value, "[") {
			inner := strings.TrimPrefix(value, "[")
			current.lists[key] = nil
			if strings.HasSuffix(inner, "]") {
				vals := []string{}
				for _, part := range strings.Split(strings.TrimSuffix(inner, "]"), ",") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					s := sailString.FindStringSubmatch(part)
					if s == nil {
						return "", nil, fmt.Errorf("sail, строка %d: не строка в списке", line)
					}
					vals = append(vals, s[1])
				}
				current.lists[key] = vals
				continue
			}
			if strings.TrimSpace(inner) != "" {
				return "", nil, fmt.Errorf("sail, строка %d: список с первым значением в строке ключа", line)
			}
			openList, listVals = key, []string{}
			continue
		}
		if key == "name" {
			s := sailString.FindStringSubmatch(value)
			if s == nil {
				return "", nil, fmt.Errorf("sail, строка %d: имя не строкой", line)
			}
			current.name = s[1]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, err
	}
	if openList != "" {
		return "", nil, fmt.Errorf("sail: незакрытый список %s", openList)
	}

	sets := map[string]*block{}
	var svc *block
	for _, b := range blocks {
		switch b.kind {
		case "prefix_sets":
			sets[b.name] = b
		case "services":
			if b.name == service {
				svc = b
			}
		}
	}
	if svc == nil {
		return "", nil, fmt.Errorf("sail: нет сервиса %q", service)
	}
	out := []string{}
	for _, name := range svc.lists["prefix_sets"] {
		set, ok := sets[name]
		if !ok {
			return "", nil, fmt.Errorf("sail: сервис ссылается на неизвестный набор %q", name)
		}
		out = append(out, set.lists["ipv4_prefixes"]...)
		out = append(out, set.lists["ipv6_prefixes"]...)
	}
	return strings.Trim(modified, `"`), out, nil
}

// normalize — префиксы по порядку, без повторов, с проверкой пола ширины.
// Голый адрес (так пишет SAIL) — префикс из одного адреса.
func normalize(items []string, floor bool) ([]string, []string, error) {
	var v4, v6 []netip.Prefix
	for _, item := range items {
		item = strings.TrimSpace(item)
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			addr, addrErr := netip.ParseAddr(item)
			if addrErr != nil {
				return nil, nil, fmt.Errorf("не адрес и не префикс: %q", item)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4() {
			if floor && prefix.Bits() < floorV4 {
				return nil, nil, fmt.Errorf("%s шире /%d", prefix, floorV4)
			}
			v4 = append(v4, prefix)
		} else {
			if prefix.Addr().Is4In6() {
				return nil, nil, fmt.Errorf("%s: IPv4 в записи IPv6", prefix)
			}
			if floor && prefix.Bits() < floorV6 {
				return nil, nil, fmt.Errorf("%s шире /%d", prefix, floorV6)
			}
			v6 = append(v6, prefix)
		}
	}
	return sortPrefixes(v4), sortPrefixes(v6), nil
}

func sortPrefixes(in []netip.Prefix) []string {
	sort.Slice(in, func(i, j int) bool {
		if c := in[i].Addr().Compare(in[j].Addr()); c != 0 {
			return c < 0
		}
		return in[i].Bits() < in[j].Bits()
	})
	out := []string{}
	for i, prefix := range in {
		if i > 0 && prefix == in[i-1] {
			continue
		}
		out = append(out, prefix.String())
	}
	return out
}

// day — дата файла днём, `2006-01-02`.
func day(stamp string) (time.Time, error) {
	if len(stamp) < 10 {
		return time.Time{}, fmt.Errorf("дата %q не понять", stamp)
	}
	return time.Parse("2006-01-02", stamp[:10])
}

// buildSet — набор из скачанного файла.
func buildSet(source rangeSource, raw []byte, built time.Time) (bots.Set, error) {
	var (
		created string
		items   []string
		err     error
	)
	switch source.format {
	case formatPrefixes:
		created, items, err = parsePrefixes(raw)
	case formatSAIL:
		created, items, err = parseSAIL(raw, source.service)
	default:
		err = fmt.Errorf("незнакомый формат %q", source.format)
	}
	if err != nil {
		return bots.Set{}, fmt.Errorf("%s: %w", source.id, err)
	}
	v4, v6, err := normalize(items, true)
	if err != nil {
		return bots.Set{}, fmt.Errorf("%s: %w", source.id, err)
	}
	if len(v4)+len(v6) == 0 {
		return bots.Set{}, fmt.Errorf("%s: ни одного префикса", source.id)
	}
	set := bots.Set{ID: source.id, URL: source.url, V4: v4, V6: v6,
		CreatedFrom: "file"}
	date := built
	if created == "" {
		set.CreatedFrom = "download"
	} else {
		date, err = day(created)
		if err != nil {
			return bots.Set{}, fmt.Errorf("%s: %w", source.id, err)
		}
	}
	set.Created = date.Format("2006-01-02")
	set.Stale = built.Sub(date) > staleAfter
	return set, nil
}

// resolveNames — имена роботов набора по его токенам.
func resolveNames(tokens []string, robots []bots.Robot) ([]string, error) {
	byToken := map[string]string{}
	for _, robot := range robots {
		if robot.RobotsOnly {
			continue
		}
		for _, match := range robot.Match {
			if match.Rule != bots.RuleRegex {
				byToken[strings.ToLower(match.Text)] = robot.Name
			}
		}
	}
	out := []string{}
	for _, token := range tokens {
		name, ok := byToken[strings.ToLower(token)]
		if !ok {
			return nil, fmt.Errorf("токена %q нет в списке роботов", token)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// checkSets — у имени один набор, урезанных наборов нет.
func checkSets(sets []bots.Set, previous *bots.Ranges, allowShrink map[string]bool) error {
	owner := map[string]string{}
	for _, set := range sets {
		for _, name := range set.Names {
			if other, ok := owner[name]; ok {
				return fmt.Errorf("имя %q в двух наборах: %s и %s", name, other, set.ID)
			}
			owner[name] = set.ID
		}
	}
	if previous == nil {
		return nil
	}
	old := map[string]int{}
	for _, set := range previous.Sets {
		old[set.ID] = len(set.V4) + len(set.V6)
	}
	for _, set := range sets {
		now := len(set.V4) + len(set.V6)
		if was, ok := old[set.ID]; ok && shrunk(was, now) && !allowShrink[set.ID] {
			return fmt.Errorf("%s: префиксов %d, в прошлой сборке %d — посмотреть файл и разрешить -allow-shrink %s",
				set.ID, now, was, set.ID)
		}
	}
	return nil
}

func parseLines(raw []byte) []string {
	out := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}
