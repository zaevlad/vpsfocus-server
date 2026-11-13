// Package ignore — «не учитывать» (трек AG): находки, которые человек
// пометил «так задумано».
//
// Одной копией здесь живут три вещи, которые нельзя развести по
// пакетам: приведение адреса к одному виду, проверка записи, пришедшей от
// приложения, и вопрос «покрывает ли запись этот пункт». Ссылки, SEO и
// советы Lighthouse спрашивают одно и то же; разойдись ответы — и экран
// «Битых ссылок» не сошёлся бы с отчётом письмом.
//
// Вычитается неучтённое при чтении, а не при обходе (Р18): сырые данные
// обходов в базе не меняются, поэтому «не учитывать» и «учитывать снова»
// действуют сразу, а не со следующего круга.
package ignore

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agent/internal/seo"
)

// Вид записи.
const (
	KindLink   = "link"
	KindSeo    = "seo"
	KindAdvice = "advice"
)

// Где действует запись.
const (
	// Все страницы сайта.
	ScopeSite = "site"
	// Одна страница: путь целиком.
	ScopePage = "page"
	// Страницы, путь которых начинается с `At` по границе сегмента.
	ScopePrefix = "prefix"
)

// Как сравнивать «что» у ссылки (Р22).
const (
	// Адрес назначения целиком.
	MatchExact = "exact"
	// Адрес назначения начинается с `What`: «все ссылки на claude.ai».
	MatchPrefix = "prefix"
)

// Limit — сколько записей держит один сервер (Р15). Защита базы агента от
// случайной петли в приложении, а не тариф.
const Limit = 1000

// Коды отказов. Перевод — в приложении.
const (
	// Запись не годится: незнакомый вид, охват, адрес или код проверки.
	CodeInvalid = "ignoreInvalid"
	// Сверх предела записей (Р15).
	CodeLimit = "ignoreLimit"
	// Сайта с таким доменом у мониторинга нет.
	CodeUnknownSite = "ignoreUnknownSite"
)

// Предел заметки в знаках: заметка — пара слов «почему так задумано».
const noteMax = 500

// Entry — одна запись «не учитывать».
//
// Ключ — всё, кроме заметки и времени (Р29): повтор той же записи от второй
// копии приложения — «уже есть», а не дубль.
type Entry struct {
	Kind   string `json:"kind"`
	Domain string `json:"domain"`
	// Что: адрес назначения ссылки, код SEO-проверки или id совета
	// Lighthouse.
	What string `json:"what"`
	// Только у ссылок: адрес целиком или его начало. У остальных — `exact`.
	Match string `json:"match"`
	// Только у ссылок: код HTTP или, без кода, вид сбоя (Р3). Изменился
	// ответ — ссылка снова считается.
	Response string `json:"response,omitempty"`
	Scope    string `json:"scope"`
	// Путь страницы (`page`) или начало пути (`prefix`). Пусто у `site`.
	At        string    `json:"at,omitempty"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Key — запись без заметки и времени: то, по чему записи различаются.
type Key struct {
	Kind, Domain, What, Match, Response, Scope, At string
}

func (e Entry) Key() Key {
	return Key{e.Kind, e.Domain, e.What, e.Match, e.Response, e.Scope, e.At}
}

// ErrInvalid — запись не прошла проверку. Подробность — по-английски, для
// журнала; человеку уходит код CodeInvalid.
var ErrInvalid = errors.New(CodeInvalid)

// ErrLimit — записей уже столько, сколько держит сервер (Р15).
var ErrLimit = errors.New(CodeLimit)

// ErrUnknownSite — сайта с таким доменом у мониторинга нет.
var ErrUnknownSite = errors.New(CodeUnknownSite)

func invalid(detail string) error {
	return &invalidError{detail}
}

type invalidError struct{ detail string }

func (e *invalidError) Error() string { return "invalid ignore entry: " + e.detail }
func (e *invalidError) Unwrap() error { return ErrInvalid }

// Lighthouse называет проверки строчными словами через дефис.
var adviceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

// Ответ ссылки без кода — слово вида сбоя (`timeout`, `network`…).
var responseWord = regexp.MustCompile(`^[a-zA-Z]{1,32}$`)

// Normalize проверяет запись от приложения и приводит её к одному виду.
//
// Приведение здесь, а не в приложении: две копии приложения на разных
// машинах обязаны получить одну и ту же запись из одного и того же нажатия,
// иначе уникальный ключ (Р29) не поймал бы повтор.
func Normalize(e Entry) (Entry, error) {
	e.Kind = strings.TrimSpace(e.Kind)
	e.Domain = strings.TrimSpace(strings.ToLower(e.Domain))
	e.What = strings.TrimSpace(e.What)
	e.Match = strings.TrimSpace(e.Match)
	e.Response = strings.TrimSpace(e.Response)
	e.Scope = strings.TrimSpace(e.Scope)
	e.At = strings.TrimSpace(e.At)
	e.Note = strings.TrimSpace(e.Note)

	if e.Domain == "" || strings.ContainsAny(e.Domain, "/ ?#") {
		return Entry{}, invalid("domain")
	}
	if e.Match == "" {
		e.Match = MatchExact
	}

	switch e.Kind {
	case KindLink:
		target, ok := NormalizeURL(e.What)
		if !ok {
			return Entry{}, invalid("link target")
		}
		e.What = target
		if e.Match != MatchExact && e.Match != MatchPrefix {
			return Entry{}, invalid("match")
		}
		if !validResponse(e.Response) {
			return Entry{}, invalid("response")
		}
		e.Response = strings.ToLower(e.Response)
	case KindSeo:
		if seo.KnownCode(e.What) == "" {
			return Entry{}, invalid("seo check")
		}
		if e.Match != MatchExact || e.Response != "" {
			return Entry{}, invalid("seo match")
		}
		// Проверки о сайте целиком страниц не имеют (Р5).
		if seo.SiteWide(e.What) && e.Scope != ScopeSite {
			return Entry{}, invalid("site-wide check scope")
		}
	case KindAdvice:
		if !adviceID.MatchString(e.What) {
			return Entry{}, invalid("advice id")
		}
		if e.Match != MatchExact || e.Response != "" {
			return Entry{}, invalid("advice match")
		}
	default:
		return Entry{}, invalid("kind")
	}

	switch e.Scope {
	case ScopeSite:
		if e.At != "" {
			return Entry{}, invalid("site scope with a page")
		}
	case ScopePage:
		path, ok := pagePath(e.At, true)
		if !ok {
			return Entry{}, invalid("page")
		}
		e.At = path
	case ScopePrefix:
		path, ok := pagePath(e.At, false)
		// Начало «/» — это весь сайт, и для него есть свой охват: две
		// записи с одним смыслом не поймал бы уникальный ключ.
		if !ok || path == "/" {
			return Entry{}, invalid("prefix")
		}
		e.At = path
	default:
		return Entry{}, invalid("scope")
	}

	if utf8.RuneCountInString(e.Note) > noteMax {
		e.Note = string([]rune(e.Note)[:noteMax])
	}
	return e, nil
}

func validResponse(response string) bool {
	if code, err := strconv.Atoi(response); err == nil {
		return code >= 100 && code <= 999
	}
	return responseWord.MatchString(response)
}

// NormalizeURL приводит адрес назначения ссылки к одному виду (Р26).
//
// Схема и хост — строчными, без `#фрагмента` и без порта по умолчанию.
// Строка запроса и путь — как есть: `?page=1` и `?page=2` — разные адреса,
// и чужой сервер вправе по-разному отвечать на `/ru` и `/ru/`. Пустой путь —
// «/»: запрос на `https://claude.ai` уходит как `GET /`, и заодно начало
// `https://claude.ai` не покрывает `https://claude.ai.example`.
func NormalizeURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}

	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		// IPv6 — снова в скобки: Hostname их снимает.
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host += ":" + port
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	out := scheme + "://" + host + path
	if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	return out, true
}

// PagePath — путь страницы сайта для «где» (Р26): из полного адреса или из
// пути, без хоста и `#фрагмента`, без завершающей косой черты (`/ru` и
// `/ru/` — одна страница сайта), со строкой запроса.
//
// Пустая строка — «страницы нет»: находка о сайте целиком.
func PagePath(raw string) string {
	path, _ := pagePath(raw, true)
	return path
}

// pagePath — то же с ответом «годится ли»; `withQuery` — оставить строку
// запроса (у начала адреса её нет: начало — это путь).
func pagePath(raw string, withQuery bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Host == "" && !strings.HasPrefix(parsed.Path, "/") {
		return "", false
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	if withQuery && parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return path, true
}

// coversPage — действует ли запись на странице `page` (путь из PagePath,
// пусто — находка о сайте целиком).
func (e Entry) coversPage(page string) bool {
	switch e.Scope {
	case ScopeSite:
		return true
	case ScopePage:
		return page != "" && page == e.At
	case ScopePrefix:
		if page == "" {
			return false
		}
		// Начало сравнивается по пути, без строки запроса, и по границе
		// сегмента: `/ru` покрывает `/ru/…`, но не `/rus`.
		path, _, _ := strings.Cut(page, "?")
		return path == e.At || strings.HasPrefix(path, e.At+"/")
	}
	return false
}

// List — записи одного сервера.
type List []Entry

// For — записи сайта одного вида.
func (l List) For(domain, kind string) List {
	domain = strings.ToLower(domain)
	var out List
	for _, entry := range l {
		if entry.Kind == kind && entry.Domain == domain {
			out = append(out, entry)
		}
	}
	return out
}

// Response — ответ битой ссылки так, как его запоминает запись (Р3): код
// HTTP, а без кода — вид сбоя.
func Response(kind string, code int) string {
	if code != 0 {
		return strconv.Itoa(code)
	}
	return strings.ToLower(kind)
}

// CoversLink — покрывает ли запись битую ссылку `target` с ответом
// `response` на странице-источнике `source`.
func (e Entry) CoversLink(target, response, source string) bool {
	if e.Kind != KindLink || e.Response != response {
		return false
	}
	normalized, ok := NormalizeURL(target)
	if !ok {
		// Не адрес http — сравнивать можно только как есть.
		normalized = target
	}
	switch e.Match {
	case MatchExact:
		if normalized != e.What {
			return false
		}
	case MatchPrefix:
		if !strings.HasPrefix(normalized, e.What) {
			return false
		}
	default:
		return false
	}
	return e.coversPage(PagePath(source))
}

// CoversSeo — покрывает ли запись находку `code` на странице `page` (адрес
// или путь; пусто — находка о сайте целиком).
func (e Entry) CoversSeo(code, page string) bool {
	return e.Kind == KindSeo && e.What == code && e.coversPage(PagePath(page))
}

// CoversAdvice — покрывает ли запись совет Lighthouse `id` на странице
// `page`. Мобильная и компьютерная версия — одной записью (Р23).
func (e Entry) CoversAdvice(id, page string) bool {
	return e.Kind == KindAdvice && e.What == id && e.coversPage(PagePath(page))
}

// Whole — запись про весь сайт: «эту проверку не учитывать нигде».
func (e Entry) Whole() bool { return e.Scope == ScopeSite }
