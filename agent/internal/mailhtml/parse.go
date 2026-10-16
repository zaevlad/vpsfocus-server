// Package mailhtml оформляет письмо: из текста, который уходит и в
// Telegram, делает HTML с логотипом, полосой состояния и подвалом.
//
// HTML выводится из текста, а не пишется второй копией (трек AC, Р3): писем
// около сорока на двух языках, и вторая копия разошлась бы с первой на
// первой же правке. Сверх текста письму нужны только тон, слово на полосе,
// кнопка и техническая подробность — их передаёт тот, кто письмо пишет, на
// своём языке (Р26). Языка этот пакет не знает.
//
// Пакет живёт в двух местах: оригинал — здесь, в агенте (его код открыт, и
// его тесты не могут читать закрытый backend), копия —
// services/backend/internal/mailhtml. Тест backend сверяет файлы байт в байт
// (Р10), поэтому правка делается здесь и копируется туда целиком.
package mailhtml

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Правила разбора (Р23) узкие намеренно: всё, что не подошло, становится
// обычным абзацем, а текст не теряется никогда — это сторожит [Missing].
//
//   - пустая строка — новый абзац; одиночный перевод строки — <br>;
//   - «1. » и «• » — пункты списка, в том числе с отступом;
//   - строка без отступа, а под ней строки с отступом — заголовок и список
//     (так устроен еженедельный отчёт агента, Р20);
//   - строка, кончающаяся двоеточием, а под ней пункты — заголовок списка;
//   - «Метка: значение», где в метке не больше трёх слов, — метка жирным;
//     абзац из таких строк целиком — таблица «метка — значение»;
//   - строка «—» — начало подвала: всё ниже неё мелко и серым.

type blockKind string

const (
	kindParagraph blockKind = "paragraph"
	kindPairs     blockKind = "pairs"
	kindList      blockKind = "list"
	kindDetails   blockKind = "details"
	kindAction    blockKind = "action"
)

type block struct {
	Kind blockKind
	// Абзац: метка жирным перед первой строкой (без двоеточия) и строки.
	Lead  string
	Lines []string
	// Список: заголовок над ним и пункты.
	Heading string
	Items   []item
	// Таблица «метка — значение».
	Pairs []pair
	// Техническая подробность: подпись над плашкой и сама подробность.
	Label string
	Code  string
}

type item struct {
	// Номер «1.», «•» или пусто (пункт отчёта без маркера).
	Marker string
	// Метка отчёта «[!]» / «[·]» — остаётся текстом, только цветом.
	Mark     string
	MarkTone Tone
	Text     string
}

type pair struct {
	Key   string
	Value string
}

// parsed — письмо, разобранное на части.
type parsed struct {
	Body   []block
	Footer []string
}

var (
	numbered = regexp.MustCompile(`^(\d{1,2}\.)\s+(.*)$`)
	bulleted = regexp.MustCompile(`^(•)\s+(.*)$`)
	// Метка — до «: » с пробелом после: «https://» меткой не станет.
	labelled = regexp.MustCompile(`^([^:]{1,40}?):\s+(\S.*)$`)
)

// Метки отчёта агента (digest/words.go). Свои у каждой ступени, одинаковые
// на обоих языках.
var reportMarks = map[string]Tone{"[!]": Problem, "[·]": Warning}

func parse(text, details string, action *Action) parsed {
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// Подробность ищется в тексте целиком, до деления на абзацы: вывод
	// задачи клиента бывает многострочным и с пустыми строками внутри (Р22).
	var before, after string
	var detailsBlock *block
	// «—» подробностью не бывает: так выглядит и черта подвала.
	if details != "" && details != "—" {
		if at := lastWhole(text, details); at >= 0 {
			before, after = text[:at], text[at+1+len(details):]
			label := ""
			if cut := strings.LastIndex(before, "\n\n"); cut >= 0 {
				if candidate := strings.TrimSpace(before[cut+2:]); strings.HasSuffix(candidate, ":") {
					label, before = candidate, before[:cut]
				}
			} else if candidate := strings.TrimSpace(before); strings.HasSuffix(candidate, ":") {
				label, before = candidate, ""
			}
			detailsBlock = &block{Kind: kindDetails, Label: label, Code: details}
		}
	}
	if detailsBlock == nil {
		before, after = text, ""
	}

	var out parsed
	inFooter := false
	add := func(chunk string) {
		for _, paragraph := range paragraphs(chunk) {
			if inFooter {
				out.Footer = append(out.Footer, paragraph...)
				continue
			}
			if paragraph[0] == "—" {
				inFooter = true
				out.Footer = append(out.Footer, paragraph[1:]...)
				continue
			}
			out.Body = append(out.Body, classify(paragraph, action))
		}
	}
	add(before)
	if detailsBlock != nil {
		if inFooter {
			out.Footer = append(out.Footer, detailsBlock.Label, detailsBlock.Code)
		} else {
			out.Body = append(out.Body, *detailsBlock)
		}
	}
	add(after)
	return out
}

// lastWhole — где в тексте последний раз стоит подробность целыми строками:
// с начала строки и до её конца (позиция перевода строки перед ней). Иначе
// короткая подробность («exit status 1») совпала бы с началом длинной строки.
func lastWhole(text, details string) int {
	end := len(text)
	for {
		at := strings.LastIndex(text[:end], "\n"+details)
		if at < 0 {
			return -1
		}
		rest := text[at+1+len(details):]
		if rest == "" || rest[0] == '\n' {
			return at
		}
		end = at
	}
}

// paragraphs делит текст по пустым строкам; концевые пробелы срезаются,
// ведущие (отступ пункта) остаются.
func paragraphs(text string) [][]string {
	var out [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			out = append(out, current)
			current = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return out
}

func classify(lines []string, action *Action) block {
	if action != nil && len(lines) == 1 && strings.TrimSpace(lines[0]) == action.URL {
		return block{Kind: kindAction}
	}

	if items, ok := listOf(lines); ok {
		return block{Kind: kindList, Items: items}
	}
	if len(lines) > 1 && !indented(lines[0]) {
		if items, ok := listOf(lines[1:]); ok && (allIndented(lines[1:]) || strings.HasSuffix(lines[0], ":")) {
			return block{Kind: kindList, Heading: lines[0], Items: items}
		}
		if allIndented(lines[1:]) {
			return block{Kind: kindList, Heading: lines[0], Items: plainItems(lines[1:])}
		}
	}

	if len(lines) > 1 {
		pairs := make([]pair, 0, len(lines))
		for _, line := range lines {
			key, value, ok := label(line)
			if !ok {
				pairs = nil
				break
			}
			pairs = append(pairs, pair{Key: key, Value: value})
		}
		if pairs != nil {
			return block{Kind: kindPairs, Pairs: pairs}
		}
	}

	out := block{Kind: kindParagraph, Lines: lines}
	if key, value, ok := label(lines[0]); ok {
		out.Lead = key
		out.Lines = append([]string{value}, lines[1:]...)
	}
	return out
}

// listOf — все строки пункты нумерованного или маркированного списка.
func listOf(lines []string) ([]item, bool) {
	items := make([]item, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		match := numbered.FindStringSubmatch(trimmed)
		if match == nil {
			match = bulleted.FindStringSubmatch(trimmed)
		}
		if match == nil {
			return nil, false
		}
		items = append(items, item{Marker: match[1], Text: match[2]})
	}
	return items, true
}

// plainItems — пункты отчёта: с меткой «[!]» / «[·]» или без неё.
func plainItems(lines []string) []item {
	items := make([]item, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		next := item{Text: trimmed}
		for mark, tone := range reportMarks {
			if rest, ok := strings.CutPrefix(trimmed, mark+" "); ok {
				next = item{Mark: mark, MarkTone: tone, Text: rest}
			}
		}
		items = append(items, next)
	}
	return items
}

func indented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

func allIndented(lines []string) bool {
	for _, line := range lines {
		if !indented(line) {
			return false
		}
	}
	return len(lines) > 0
}

// label — «Метка: значение», если в метке не больше трёх слов.
func label(line string) (key, value string, ok bool) {
	if indented(line) {
		return "", "", false
	}
	match := labelled.FindStringSubmatch(line)
	if match == nil || len(strings.Fields(match[1])) > 3 {
		return "", "", false
	}
	return match[1], strings.TrimSpace(match[2]), true
}

// preheader — строка, которую почта показывает в списке писем рядом с
// темой (Р9): первый содержательный абзац. Приветствие — короткая строка на
// «!» или «,» — пропускается.
func preheader(body []block) string {
	for _, part := range body {
		if part.Kind != kindParagraph {
			continue
		}
		text := strings.Join(part.Lines, " ")
		if part.Lead != "" {
			text = part.Lead + ": " + text
		}
		if len(part.Lines) == 1 && part.Lead == "" && utf8.RuneCountInString(text) <= 30 &&
			(strings.HasSuffix(text, "!") || strings.HasSuffix(text, ",")) {
			continue
		}
		return clip(text, 150)
	}
	return ""
}

func clip(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
