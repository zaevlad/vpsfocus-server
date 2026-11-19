package bots

import (
	"regexp"
	"strings"
)

// Узнавание робота по строке User-agent по вшитому списку (трек AI, спринт
// 272, Р4).
//
// Строка разбивается на слова один раз: слово — непрерывный кусок из букв,
// цифр и `-_.`. Имена ищутся по карте слов, а не подстрокой по сотням имён:
// так и быстро, и без ложных совпадений внутри чужого слова («oast» в
// «Toast», «bot» в «Cubot»). Имя из нескольких слов («Screaming Frog SEO
// Spider») ищется по первому слову и сверяется с продолжением строки.
//
// Кто побеждает, если совпало несколько: конкретный робот — у общих имён
// (`curl`, `python-requests`, роль Script); при равных — более длинное
// совпадение («Claude-SearchBot», а не «ClaudeBot»). Регистр не важен:
// Bing пишет «bingbot/2.0», а в списке «Bingbot».

type entry struct {
	robot int
	rule  string
	// Текст правила в нижнем регистре: у имени из нескольких слов — целиком
	// (ключ карты — его первое слово).
	text string
}

type index struct {
	robots   []Robot
	byName   map[string]int
	words    map[string][]entry
	exact    map[string]int
	prefixes []entry
	regexes  []struct {
		re    *regexp.Regexp
		robot int
	}
}

var catalog = mustIndex()

func mustIndex() *index {
	loaded, err := LoadCatalog()
	if err != nil {
		// Данные вшиты и проверены тестами пакета: сюда сборка с битым
		// списком не доходит.
		panic(err)
	}
	return buildIndex(loaded.Robots)
}

func buildIndex(robots []Robot) *index {
	idx := &index{robots: robots, byName: map[string]int{}, words: map[string][]entry{}, exact: map[string]int{}}
	for i, robot := range robots {
		idx.byName[robot.Name] = i
		// Правило только для robots.txt в логах не ищется: под этим именем
		// никто не ходит.
		if robot.RobotsOnly {
			continue
		}
		for _, match := range robot.Match {
			text := strings.ToLower(match.Text)
			switch match.Rule {
			case RuleExact:
				idx.exact[strings.TrimSpace(text)] = i
			case RulePrefix:
				idx.prefixes = append(idx.prefixes, entry{robot: i, rule: match.Rule, text: text})
			case RuleRegex:
				re, err := regexp.Compile(match.Text)
				if err != nil {
					panic(err)
				}
				idx.regexes = append(idx.regexes, struct {
					re    *regexp.Regexp
					robot int
				}{re, i})
			default:
				key := text
				if end := wordEnd(text, 0); end < len(text) {
					key = text[:end]
				}
				if key == "" {
					continue
				}
				idx.words[key] = append(idx.words[key], entry{robot: i, rule: match.Rule, text: text})
			}
		}
	}
	return idx
}

func wordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' ||
		c >= 'A' && c <= 'Z' || c >= 0x80
}

func wordEnd(s string, from int) int {
	for from < len(s) && wordByte(s[from]) {
		from++
	}
	return from
}

type candidate struct {
	robot  int
	length int
	ok     bool
}

func (idx *index) better(best candidate, robot, length int) candidate {
	next := candidate{robot: robot, length: length, ok: true}
	if !best.ok {
		return next
	}
	nextScript, bestScript := idx.robots[robot].Role == Script, idx.robots[best.robot].Role == Script
	if nextScript != bestScript {
		if bestScript {
			return next
		}
		return best
	}
	if length > best.length || length == best.length && robot < best.robot {
		return next
	}
	return best
}

// match — лучший робот списка для строки; ok=false — в списке нет.
func (idx *index) match(agent string) (int, bool) {
	lower := strings.ToLower(agent)
	best := candidate{}
	if i, ok := idx.exact[strings.TrimSpace(lower)]; ok {
		best = idx.better(best, i, len(lower))
	}
	for start := 0; start < len(lower); {
		if !wordByte(lower[start]) {
			start++
			continue
		}
		end := wordEnd(lower, start)
		word := lower[start:end]
		best = idx.matchWord(best, lower, start, end, word)
		// «SemrushBot-SA», «msnbot-media»: имя с хвостом через дефис — тот же
		// робот, если хвоста в списке нет (более длинное имя победит само).
		for cut := strings.LastIndexByte(word, '-'); cut > 0; cut = strings.LastIndexByte(word[:cut], '-') {
			best = idx.matchWord(best, lower, start, start+cut, word[:cut])
		}
		for _, prefix := range idx.prefixes {
			if strings.HasPrefix(word, prefix.text) {
				best = idx.better(best, prefix.robot, len(prefix.text))
			}
		}
		start = end
	}
	// Регулярки дороже слов: только если конкретного робота ещё нет.
	if !best.ok || idx.robots[best.robot].Role == Script {
		for _, item := range idx.regexes {
			if idx.robots[item.robot].Role == Script && best.ok {
				continue
			}
			if item.re.MatchString(agent) {
				best = idx.better(best, item.robot, 0)
			}
		}
	}
	return best.robot, best.ok
}

func (idx *index) matchWord(best candidate, lower string, start, end int, word string) candidate {
	for _, item := range idx.words[word] {
		stop := end
		if len(item.text) != len(word) {
			// Имя из нескольких слов: строка должна продолжаться им же, и
			// кончаться оно должно на границе слова.
			if !strings.HasPrefix(lower[start:], item.text) {
				continue
			}
			stop = start + len(item.text)
			if stop < len(lower) && wordByte(lower[stop]) && wordByte(item.text[len(item.text)-1]) {
				continue
			}
		}
		if item.rule == RuleVersioned && (stop >= len(lower) || lower[stop] != '/') {
			continue
		}
		best = idx.better(best, item.robot, len(item.text))
	}
	return best
}

// Classify — какой робот назвался этой строкой User-agent: имя из списка
// (у семейства — имя семейства, Р20), [Other] или пусто — «похоже на
// человека».
//
// Сама строка нигде не сохраняется: из функции выходит только имя из
// закрытого списка. Подлинность здесь не проверяется — это делает [Verify]
// по адресу.
func Classify(agent string) string {
	if agent == "" || agent == "-" {
		return ""
	}
	if i, ok := catalog.match(agent); ok {
		robot := catalog.robots[i]
		if robot.Family != "" {
			return robot.Family
		}
		return robot.Name
	}
	lower := strings.ToLower(agent)
	for _, word := range robotWords {
		if strings.Contains(lower, word) {
			return Other
		}
	}
	if strings.HasSuffix(lower, "bot") {
		return Other
	}
	return ""
}

// KindOf — роль робота из списка по имени.
func KindOf(name string) (string, bool) {
	i, ok := catalog.byName[name]
	if !ok {
		return "", false
	}
	return catalog.robots[i].Role, true
}

// Legacy — один из прежних 14 (Р17–Р19): только у них «пока не приходил»,
// отказы, «пропал» и прежние поля ответа.
func Legacy(name string) bool {
	i, ok := catalog.byName[name]
	return ok && catalog.robots[i].Legacy
}
