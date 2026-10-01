package logs

import (
	"strconv"
	"strings"
	"time"

	"agent/internal/bots"
)

// Разбор идёт вручную, а не одной большой регуляркой.
//
// Причина простая: форматов много и все они «почти combined». У Apache
// впереди бывает виртуальный хост, у nginx его дописывают по-разному, за
// кодом ответа хвост у каждого свой. Регулярка на все случаи получается
// нечитаемой и всё равно не покрывает следующий формат, а разбор по
// опорным точкам — скобки, кавычки — переживает лишние поля спокойно.

// ParseAccess разбирает строку access-лога.
//
// Возвращает false, если строка не похожа на общий или комбинированный
// формат: чужой формат — не повод считать это ошибкой, просто пропускаем.
func ParseAccess(line string) (Event, bool) {
	open := strings.IndexByte(line, '[')
	closing := strings.IndexByte(line, ']')
	if open < 0 || closing < open {
		return Event{}, false
	}

	at, ok := parseAccessTime(line[open+1 : closing])
	if !ok {
		return Event{}, false
	}

	// Запрос в кавычках сразу за временем.
	rest := line[closing+1:]
	first := strings.IndexByte(rest, '"')
	if first < 0 {
		return Event{}, false
	}
	second := strings.IndexByte(rest[first+1:], '"')
	if second < 0 {
		return Event{}, false
	}
	request := rest[first+1 : first+1+second]

	// Дальше — код ответа: первое поле после закрывающей кавычки.
	after := rest[first+1+second+1:]
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return Event{}, false
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil || status < 100 || status > 599 {
		return Event{}, false
	}

	event := Event{Kind: KindAccess, At: at, Status: status}

	// User-agent — второе поле в кавычках после запроса (первое — источник
	// перехода) в формате combined. Нет его — формат лога его не пишет, и
	// это «не знаем», а не «людей нет». Сама строка не сохраняется: из неё
	// берётся только имя робота из закрытого списка.
	if quoted := quotedFields(after); len(quoted) >= 2 {
		event.HasAgent = true
		event.Robot = bots.Classify(quoted[1])
	}

	// «GET /path HTTP/1.1». Бывает и мусор от ботов — тогда путь берём как
	// есть: он всё равно попадёт в группу «что-то странное».
	parts := strings.Fields(request)
	switch len(parts) {
	case 0:
		event.Path = "/"
	case 1:
		event.Path = NormalizePath(parts[0])
	default:
		event.Method = parts[0]
		event.Path = NormalizePath(parts[1])
	}

	// Виртуальный хост, если формат его пишет: у Apache vhost_combined он
	// стоит первым полем, до адреса посетителя.
	event.Host = accessHost(line[:open])

	return event, true
}

// quotedFields — поля в двойных кавычках по порядку. Кавычку внутри поля
// nginx пишет как \x22, Apache — как \", и вторую здесь пропускаем.
func quotedFields(text string) []string {
	var fields []string
	for {
		start := strings.IndexByte(text, '"')
		if start < 0 {
			return fields
		}
		text = text[start+1:]
		end := -1
		for i := 0; i < len(text); i++ {
			if text[i] == '\\' {
				i++
				continue
			}
			if text[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return fields
		}
		fields = append(fields, text[:end])
		text = text[end+1:]
	}
}

// accessHost достаёт домен из части строки до времени.
//
// Полей там три (адрес, identd, пользователь) или четыре — тогда первое и
// есть виртуальный хост. Адрес посетителя не сохраняем ни в каком случае.
func accessHost(prefix string) string {
	fields := strings.Fields(prefix)
	if len(fields) < 4 {
		return ""
	}

	host := fields[0]
	// «example.com:443» — отрезаем порт.
	if colon := strings.LastIndexByte(host, ':'); colon > 0 && !strings.Contains(host[:colon], ":") {
		host = host[:colon]
	}
	if host == "" || host == "-" || !strings.Contains(host, ".") {
		return ""
	}
	return truncate(strings.ToLower(host), 200)
}

func parseAccessTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)

	// Со смещением — читаем как есть: строка сама сказала, какое время
	// имеется в виду.
	if at, err := time.Parse("02/Jan/2006:15:04:05 -0700", raw); err == nil {
		return at.UTC(), true
	}
	// Без смещения — местное время сервера, как и в error-логе.
	if at, err := time.ParseInLocation("02/Jan/2006:15:04:05", raw, Location()); err == nil {
		return at.UTC(), true
	}
	return time.Time{}, false
}

// ParseError разбирает строку error-лога nginx или Apache.
//
// Возвращает false и для чужого формата, и для строки уровня ниже error:
// warn и notice в nginx — это будни работающего сайта.
func ParseError(line string) (Event, bool) {
	if strings.HasPrefix(line, "[") {
		return parseApacheError(line)
	}
	return parseNginxError(line)
}

// parseNginxError: «2026/09/03 12:00:00 [error] 123#0: *45 сообщение».
func parseNginxError(line string) (Event, bool) {
	if len(line) < 20 {
		return Event{}, false
	}

	// Местное время сервера: смещения nginx в error-лог не пишет. Разбор
	// как UTC сдвигал каждое событие на смещение сервера — и разводил
	// error-ряд с access-рядом на одном графике.
	at, err := time.ParseInLocation("2006/01/02 15:04:05", line[:19], Location())
	if err != nil {
		return Event{}, false
	}

	open := strings.IndexByte(line, '[')
	closing := strings.IndexByte(line, ']')
	if open < 0 || closing < open {
		return Event{}, false
	}
	level := strings.ToLower(strings.TrimSpace(line[open+1 : closing]))
	if !interestingLevels[level] {
		return Event{}, false
	}

	message := strings.TrimSpace(line[closing+1:])
	// «123#0: *45 » — номера процесса и соединения. Для человека это шум,
	// для группировки — вред: из-за них одна беда рассыпается на сотни.
	if colon := strings.Index(message, ": "); colon >= 0 && colon < 24 {
		message = strings.TrimSpace(message[colon+2:])
	}
	message = strings.TrimPrefix(message, "*")
	if space := strings.IndexByte(message, ' '); space > 0 && isDigits(message[:space]) {
		message = message[space+1:]
	}

	event := Event{Kind: KindError, At: at.UTC(), Level: level}
	event.Host = quotedField(line, "host:")
	if request := quotedField(line, "request:"); request != "" {
		parts := strings.Fields(request)
		if len(parts) >= 2 {
			event.Method = parts[0]
			event.Path = NormalizePath(parts[1])
		}
	}

	event.Sample = truncate(StripClient(message), 400)
	event.Signature = Signature(message)
	return event, true
}

// parseApacheError: «[Wed Sep 03 12:00:00.123456 2026] [php:error] [pid 1] [client 1.2.3.4:5] сообщение».
func parseApacheError(line string) (Event, bool) {
	blocks, rest := leadingBlocks(line)
	if len(blocks) < 2 {
		return Event{}, false
	}

	at, ok := parseApacheTime(blocks[0])
	if !ok {
		return Event{}, false
	}

	// «php:error» или «error» — берём то, что после двоеточия.
	level := strings.ToLower(strings.TrimSpace(blocks[1]))
	if colon := strings.LastIndexByte(level, ':'); colon >= 0 {
		level = level[colon+1:]
	}
	if !interestingLevels[level] {
		return Event{}, false
	}

	message := strings.TrimSpace(rest)
	event := Event{Kind: KindError, At: at.UTC(), Level: level}
	event.Sample = truncate(StripClient(message), 400)
	event.Signature = Signature(message)
	return event, true
}

// parseApacheTime — то же местное время сервера, что и у nginx: Apache
// смещения тоже не пишет.
func parseApacheTime(raw string) (time.Time, bool) {
	for _, layout := range []string{
		"Mon Jan 02 15:04:05.000000 2006",
		"Mon Jan 02 15:04:05 2006",
	} {
		if at, err := time.ParseInLocation(layout, strings.TrimSpace(raw), Location()); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// leadingBlocks разбирает череду «[…]» в начале строки и отдаёт остаток.
func leadingBlocks(line string) ([]string, string) {
	var blocks []string
	rest := line

	for strings.HasPrefix(rest, "[") {
		closing := strings.IndexByte(rest, ']')
		if closing < 0 {
			break
		}
		blocks = append(blocks, rest[1:closing])
		rest = strings.TrimSpace(rest[closing+1:])
	}
	return blocks, rest
}

// quotedField достаёт значение поля вида «host: "example.com"».
func quotedField(line, name string) string {
	at := strings.Index(line, name)
	if at < 0 {
		return ""
	}
	rest := line[at+len(name):]
	first := strings.IndexByte(rest, '"')
	if first < 0 {
		return ""
	}
	second := strings.IndexByte(rest[first+1:], '"')
	if second < 0 {
		return ""
	}
	return truncate(strings.TrimSpace(rest[first+1:first+1+second]), 300)
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, symbol := range value {
		if symbol < '0' || symbol > '9' {
			return false
		}
	}
	return true
}
