package seo

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Проверки страницы из уже собранных данных (спринт 94): указания robots,
// «мягкая 404», H1, ссылки, вид адреса, время ответа. Обход ради них не
// делает ни одного нового запроса.
//
// Коды живут в общем списке в audit.go, уровни — в `severityByCode`.

// Пороги.
const (
	// Сервер собирал страницу дольше этого — замечание. Агент стоит на том
	// же сервере, поэтому это время работы сайта, а не сети.
	slowResponseMS = 3000
	// Длиннее этого H1 перестаёт быть заголовком и становится абзацем.
	h1MaxLength = 70
	// Ссылок на странице больше этого — вес страницы дробится в пыль.
	tooManyLinks = 300
	// Адрес длиннее этого читают и копируют с ошибками.
	urlMaxLength = 115
)

// now — часы для `unavailable_after`. Переменная ради тестов.
var now = time.Now

// Метки рекламы и аналитики в адресе. `yclid`, `ysclid` и `_openstat` —
// Яндекс.
var trackingParameters = map[string]bool{
	"gclid": true, "fbclid": true, "yclid": true, "ysclid": true, "_openstat": true,
}

// Отдельное «404» в заголовке. Отдельное — чтобы «Модель X404» не
// считалась страницей ошибки.
var standalone404 = regexp.MustCompile(`(^|[^\p{L}\p{N}])404([^\p{L}\p{N}]|$)`)

// Слова страницы ошибки в заголовке.
var notFoundPhrases = []string{
	"not found", "не найдена", "не найден", "не существует",
}

// hygieneIssues — находки одной страницы. Зовётся из `auditPage` у
// разобранной страницы.
func hygieneIssues(page *Page) []Issue {
	var issues []Issue
	add := func(code, detail string) {
		issues = append(issues, Issue{Code: code, Severity: SeverityOf(code), Detail: detail})
	}
	structure := page.Structure

	// ── указания robots ─────────────────────────────────────────────────
	if structure.Noindex && canonicalDelegates(page) {
		// Google советует выбрать одно: noindex говорит «не показывай
		// эту», canonical — «показывай ту вместо этой».
		add(IssueNoindexWithCanonical, structure.Canonical)
	}
	directives := strings.ToLower(structure.MetaRobots + "," + structure.XRobotsTag)
	if hasToken(directives, "nosnippet") {
		add(IssueRobotsNosnippet, "")
	}
	if hasToken(directives, "noimageindex") {
		add(IssueRobotsNoimageindex, "")
	}
	if date, passed := unavailableAfter(structure.MetaRobots + "," + structure.XRobotsTag); passed {
		add(IssueUnavailableAfterPassed, date)
	}

	// ── страница ошибки с ответом 200 ───────────────────────────────────
	if page.Status == 200 && indexable(page) {
		if heading, ok := notFoundHeading(structure); ok {
			add(IssueSoft404Title, heading)
		}
	}

	// ── время ответа ────────────────────────────────────────────────────
	if page.Error == "" && page.LoadMS >= slowResponseMS {
		add(IssueSlowResponse, strconv.FormatInt(page.LoadMS, 10))
	}

	// ── H1 ──────────────────────────────────────────────────────────────
	for _, heading := range structure.H1 {
		if length := utf8.RuneCountInString(strings.TrimSpace(heading)); length > h1MaxLength {
			add(IssueH1Long, strconv.Itoa(length))
			break
		}
	}

	// ── ссылки ──────────────────────────────────────────────────────────
	if links := structure.Links.Internal + structure.Links.External; links > tooManyLinks {
		add(IssueTooManyLinks, strconv.Itoa(links))
	}

	// ── вид адреса ──────────────────────────────────────────────────────
	if parsed, err := url.Parse(page.URL); err == nil {
		if shape := urlShape(parsed, page.URL); shape != "" {
			add(IssueURLShape, shape)
		}
		if names := trackingNames(parsed); names != "" {
			add(IssueURLTracking, names)
		}
		if segment := repeatedSegment(parsed.Path); segment != "" {
			add(IssueURLRepeatingSegment, segment)
		}
		if isInternalSearch(parsed) && indexable(page) {
			add(IssueInternalSearchCrawled, "")
		}
	}

	return issues
}

// unavailableAfter ищет указание `unavailable_after` и отвечает, прошла ли
// дата. Неразобранная дата — не повод говорить «страница выпала из поиска».
//
// Дату режем не по запятым: в RFC 850 она сама с запятой («Friday,
// 25-Jun-10 …»). Пробуем остаток строки целиком и его куски до каждой
// следующей запятой.
func unavailableAfter(directives string) (string, bool) {
	lower := strings.ToLower(directives)
	index := strings.Index(lower, "unavailable_after")
	if index < 0 {
		return "", false
	}
	rest := directives[index+len("unavailable_after"):]
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	rest = strings.TrimSpace(rest[1:])

	candidates := []string{rest}
	for cut := strings.LastIndexByte(rest, ','); cut > 0; cut = strings.LastIndexByte(rest[:cut], ',') {
		candidates = append(candidates, strings.TrimSpace(rest[:cut]))
	}
	for _, candidate := range candidates {
		if moment, ok := parseRobotsDate(candidate); ok {
			return candidate, moment.Before(now())
		}
	}
	return "", false
}

// Форматы даты, которые принимает Google: RFC 822, 850, 1123 и ISO 8601.
var robotsDateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02",
	time.RFC1123,
	time.RFC1123Z,
	time.RFC850,
	time.RFC822,
	time.RFC822Z,
	"2 Jan 2006 15:04:05 MST",
	"02 Jan 2006 15:04:05 MST",
	"2 Jan 2006",
}

func parseRobotsDate(value string) (time.Time, bool) {
	for _, layout := range robotsDateLayouts {
		if moment, err := time.Parse(layout, value); err == nil {
			return moment, true
		}
	}
	return time.Time{}, false
}

// notFoundHeading — title или H1, похожий на заголовок страницы ошибки.
func notFoundHeading(structure *Structure) (string, bool) {
	headings := append([]string{structure.Title}, structure.H1...)
	for _, heading := range headings {
		heading = strings.TrimSpace(heading)
		if heading == "" {
			continue
		}
		lower := strings.ToLower(heading)
		if standalone404.MatchString(lower) {
			return heading, true
		}
		for _, phrase := range notFoundPhrases {
			if strings.Contains(lower, phrase) {
				return heading, true
			}
		}
	}
	return "", false
}

// urlShape — чем адрес неудобен. Значки, а не слова: подробность уходит в
// перевод как есть.
func urlShape(parsed *url.URL, raw string) string {
	var marks []string
	path := parsed.Path // уже раскодирован: `%D0%9F` — это буква, а не заглавные

	if strings.IndexFunc(path, unicode.IsUpper) >= 0 {
		marks = append(marks, "A-Z")
	}
	if strings.Contains(path, "_") {
		marks = append(marks, "_")
	}
	if strings.ContainsAny(path, " \t") {
		marks = append(marks, "%20")
	}
	if strings.Contains(path, "//") {
		marks = append(marks, "//")
	}
	if utf8.RuneCountInString(raw) > urlMaxLength {
		marks = append(marks, ">"+strconv.Itoa(urlMaxLength))
	}
	return strings.Join(marks, " ")
}

// trackingNames — имена меток рекламы и аналитики в адресе.
func trackingNames(parsed *url.URL) string {
	var names []string
	for name := range parsed.Query() {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "utm_") || trackingParameters[lower] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// repeatedSegment — первый сегмент пути, встретившийся второй раз.
func repeatedSegment(path string) string {
	seen := map[string]bool{}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		key := strings.ToLower(segment)
		if seen[key] {
			return segment
		}
		seen[key] = true
	}
	return ""
}

// isInternalSearch — страница поиска по сайту: сегмент `search` или
// `poisk` в пути либо параметр `s` (так ищет WordPress).
func isInternalSearch(parsed *url.URL) bool {
	if parsed.Query().Has("s") {
		return true
	}
	for _, segment := range strings.Split(strings.ToLower(parsed.Path), "/") {
		if segment == "search" || segment == "poisk" || segment == "поиск" {
			return true
		}
	}
	return false
}

// addSiteWideHygiene — то, что видно только на всём обходе: одинаковые H1,
// тупики, один адрес в двух написаниях.
func addSiteWideHygiene(pages []Page) {
	var candidates []*Page
	for index := range pages {
		page := &pages[index]
		if page.Structure == nil {
			continue
		}
		candidates = append(candidates, page)
	}

	// Тупик: ссылаться некуда только у одностраничного сайта.
	if len(candidates) > 1 {
		for _, page := range candidates {
			if page.Structure.Links.Internal == 0 && indexable(page) {
				page.Issues = append(page.Issues, newIssue(IssueDeadEnd, ""))
			}
		}
	}

	// Одинаковые H1 — только среди тех, кто сам претендует на место в
	// поиске, как и у заголовков.
	headings := map[string][]string{}
	for _, page := range candidates {
		if !indexable(page) || len(page.Structure.H1) == 0 {
			continue
		}
		if heading := strings.TrimSpace(page.Structure.H1[0]); heading != "" {
			headings[heading] = append(headings[heading], page.URL)
		}
	}
	for _, page := range candidates {
		if !indexable(page) || len(page.Structure.H1) == 0 {
			continue
		}
		if others := twins(headings[strings.TrimSpace(page.Structure.H1[0])], page.URL); others != "" {
			page.Issues = append(page.Issues, newIssue(IssueH1Duplicate, others))
		}
	}

	// Один адрес в двух написаниях. Только страницы, отдавшие 200 сами, без
	// перенаправления: перенаправление на один вариант или canonical — это
	// правильная настройка, а не находка.
	byCase := map[string][]string{}
	bySlash := map[string][]string{}
	for _, page := range candidates {
		if !servesItself(page) {
			continue
		}
		byCase[strings.ToLower(page.URL)] = append(byCase[strings.ToLower(page.URL)], page.URL)
		bySlash[withoutTrailingSlash(page.URL)] = append(bySlash[withoutTrailingSlash(page.URL)], page.URL)
	}
	for _, page := range candidates {
		if !servesItself(page) {
			continue
		}
		if others := twins(byCase[strings.ToLower(page.URL)], page.URL); others != "" {
			page.Issues = append(page.Issues, newIssue(IssueURLCaseDuplicate, others))
		}
		if others := twins(bySlash[withoutTrailingSlash(page.URL)], page.URL); others != "" {
			page.Issues = append(page.Issues, newIssue(IssueURLSlashDuplicate, others))
		}
	}
}

// servesItself — страница отдала содержимое сама, под своим адресом, и
// претендует на место в поиске.
func servesItself(page *Page) bool {
	return page.Status == 200 && len(page.Redirects) == 0 && indexable(page)
}

// withoutTrailingSlash — адрес без `/` в конце пути. Корень не трогаем:
// `https://site/` и `https://site` — один адрес.
func withoutTrailingSlash(address string) string {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Path == "/" || !strings.HasSuffix(parsed.Path, "/") {
		return address
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String()
}

func newIssue(code, detail string) Issue {
	return Issue{Code: code, Severity: SeverityOf(code), Detail: detail}
}
