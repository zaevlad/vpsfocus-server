package seo

import (
	"sort"
	"strconv"
	"strings"
)

// Разбор снятой структуры: что со страницами не так.
//
// Считает агент, а не экран, — по тому же правилу, что и оценки Core Web
// Vitals: пороги обязаны жить в одном месте. Две копии порогов однажды
// разойдутся, и «слишком длинный title» на экране перестанет означать
// «слишком длинный» в отчёте.
//
// Наружу уходят коды и уровень важности, а не текст: интерфейс двуязычный.

// Уровни важности. Три, как и просит план, и делятся они по одному
// вопросу — что случится с поисковым трафиком страницы.
const (
	// Страница не попадёт в поиск или попадёт вместо неё другая.
	SeverityCritical = "critical"
	// Попадёт, но хуже, чем могла бы.
	SeverityWarning = "warning"
	// Гигиена: заметно на длинной дистанции, не горит.
	SeverityNotice = "notice"
)

// Коды находок. Перевод — в приложении, как и везде.
const (
	IssuePageUnavailable      = "pageUnavailable"
	IssueTitleMissing         = "titleMissing"
	IssueTitleShort           = "titleShort"
	IssueTitleLong            = "titleLong"
	IssueTitleDuplicate       = "titleDuplicate"
	IssueDescriptionMissing   = "descriptionMissing"
	IssueDescriptionShort     = "descriptionShort"
	IssueDescriptionLong      = "descriptionLong"
	IssueDescriptionDuplicate = "descriptionDuplicate"
	IssueH1Missing            = "h1Missing"
	IssueH1Multiple           = "h1Multiple"
	IssueHeadingOrder         = "headingOrder"
	IssueImagesWithoutAlt     = "imagesWithoutAlt"
	IssueCanonicalMissing     = "canonicalMissing"
	IssueCanonicalOther       = "canonicalOther"
	IssueCanonicalOffsite     = "canonicalOffsite"
	IssueNoindex              = "noindex"
	IssueDuplicateContent     = "duplicateContent"
	IssueThinContent          = "thinContent"
	IssueLangMissing          = "langMissing"
	IssueViewportMissing      = "viewportMissing"
	IssueOpenGraphIncomplete  = "openGraphIncomplete"
	IssueRedirected           = "redirected"
	IssueRedirectChain        = "redirectChain"
	IssueRedirectOffsite      = "redirectOffsite"

	// Спринт 94: из уже собранных данных (hygiene.go).
	IssueNoindexWithCanonical   = "noindexWithCanonical"
	IssueRobotsNosnippet        = "robotsNosnippet"
	IssueRobotsNoimageindex     = "robotsNoimageindex"
	IssueUnavailableAfterPassed = "unavailableAfterPassed"
	IssueSoft404Title           = "soft404Title"
	IssueSlowResponse           = "slowResponse"
	IssueH1Duplicate            = "h1Duplicate"
	IssueH1Long                 = "h1Long"
	IssueDeadEnd                = "deadEnd"
	IssueTooManyLinks           = "tooManyLinks"
	IssueURLShape               = "urlShape"
	IssueURLTracking            = "urlTracking"
	IssueURLRepeatingSegment    = "urlRepeatingSegment"
	IssueURLCaseDuplicate       = "urlCaseDuplicate"
	IssueURLSlashDuplicate      = "urlSlashDuplicate"
	IssueInternalSearchCrawled  = "internalSearchCrawled"
)

// Уровень находок входа — тот же вопрос и та же линейка: что будет с
// поисковым трафиком. Все три критичны, потому что все три означают одно —
// в поиск попадёт не тот адрес, который имели в виду, или не попадёт
// никакой. Живут они в общей таблице, а не в своей: две линейки важности
// однажды разошлись бы, и «критично» на экране перестало бы значить одно и
// то же.

// severityByCode — уровень каждой находки, одной таблицей.
//
// Раньше уровень стоял рядом с каждой проверкой, и один и тот же код мог
// приехать с разным уровнем из двух мест. Здесь же его спрашивает и разбор
// страницы, и сравнение обходов: «что появилось» надо показывать по
// важности, а страниц с этим кодом в сравнении уже нет.
var severityByCode = map[string]string{
	IssuePageUnavailable:      SeverityCritical,
	IssueTitleMissing:         SeverityCritical,
	IssueDuplicateContent:     SeverityCritical,
	IssueCanonicalOffsite:     SeverityCritical,
	IssueCanonicalNoHTTPS:     SeverityCritical,
	IssueCanonicalHostSplit:   SeverityCritical,
	IssueCanonicalLoop:        SeverityCritical,
	IssueTitleShort:           SeverityWarning,
	IssueTitleLong:            SeverityWarning,
	IssueTitleDuplicate:       SeverityWarning,
	IssueDescriptionMissing:   SeverityWarning,
	IssueDescriptionDuplicate: SeverityWarning,
	IssueH1Missing:            SeverityWarning,
	IssueH1Multiple:           SeverityWarning,
	IssueCanonicalMissing:     SeverityWarning,
	IssueNoindex:              SeverityWarning,
	IssueRedirectChain:        SeverityWarning,
	IssueDescriptionShort:     SeverityNotice,
	IssueDescriptionLong:      SeverityNotice,
	IssueHeadingOrder:         SeverityNotice,
	IssueImagesWithoutAlt:     SeverityNotice,
	IssueCanonicalOther:       SeverityNotice,
	IssueThinContent:          SeverityNotice,
	IssueLangMissing:          SeverityNotice,
	IssueViewportMissing:      SeverityNotice,
	IssueOpenGraphIncomplete:  SeverityNotice,
	IssueRedirected:           SeverityNotice,
	IssueRedirectOffsite:      SeverityNotice,

	// Сайт целиком и карта сайта (site.go).
	IssueSearchEnginesBlocked:   SeverityCritical,
	IssueSitemapBroken:          SeverityCritical,
	IssueAISearchBlocked:        SeverityWarning,
	IssueSitemapMissing:         SeverityWarning,
	IssueSitemapUnavailable:     SeverityWarning,
	IssueSitemapBlockedByRobots: SeverityWarning,
	IssueSitemapRedirect:        SeverityWarning,
	IssueSitemapNoindex:         SeverityWarning,
	IssueSitemapCanonicalised:   SeverityWarning,
	IssueRobotsMissing:          SeverityNotice,
	IssueAITrainingBlocked:      SeverityNotice,
	IssueNotInSitemap:           SeverityNotice,

	// Из уже собранных данных (hygiene.go).
	IssueNoindexWithCanonical:   SeverityWarning,
	IssueUnavailableAfterPassed: SeverityWarning,
	IssueSoft404Title:           SeverityWarning,
	IssueURLRepeatingSegment:    SeverityWarning,
	IssueURLCaseDuplicate:       SeverityWarning,
	IssueURLSlashDuplicate:      SeverityWarning,
	IssueRobotsNosnippet:        SeverityNotice,
	IssueRobotsNoimageindex:     SeverityNotice,
	IssueSlowResponse:           SeverityNotice,
	IssueH1Duplicate:            SeverityNotice,
	IssueH1Long:                 SeverityNotice,
	IssueDeadEnd:                SeverityNotice,
	IssueTooManyLinks:           SeverityNotice,
	IssueURLShape:               SeverityNotice,
	IssueURLTracking:            SeverityNotice,
	IssueInternalSearchCrawled:  SeverityNotice,
}

// SeverityOf — уровень находки по её коду. Незнакомый код — замечание:
// выдумать ему важность неоткуда, а промолчать хуже.
func SeverityOf(code string) string {
	if severity, ok := severityByCode[code]; ok {
		return severity
	}
	return SeverityNotice
}

// Codes — коды находок страницы в порядке важности. Хранится строкой рядом
// со страницей: экран просит «покажи страницы с этой находкой», и разбирать
// ради этого JSON у каждой из пятисот страниц значит читать весь обход.
func Codes(issues []Issue) []string {
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

// Пороги длины и объёма.
//
// Числа не догма, а граница, за которой стоит посмотреть глазами. Верхние
// взяты из того, сколько поисковик показывает в выдаче: title обрезается
// около шестидесяти знаков, описание — около ста шестидесяти. Нижние
// отвечают на другой вопрос — есть ли там вообще фраза, по которой страницу
// можно найти.
const (
	titleMinLength       = 20
	titleMaxLength       = 60
	descriptionMinLength = 70
	descriptionMaxLength = 160
	// Сколько слов делает страницу страницей. Меньше — это либо заглушка,
	// либо карточка без описания: и то и другое поисковику нечем ранжировать.
	thinContentWords = 150
	// Со скольких перенаправлений цепочка перестаёт быть нормальной
	// навигацией. Одно — обычное дело; два и больше поисковик проходит
	// неохотно, а вес по дороге теряется.
	redirectChainLength = 2
)

// IssueSummary — сводка находок по обходу.
type IssueSummary struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Notice   int `json:"notice"`
	// Сколько страниц хотя бы с одной находкой. Не то же самое, что сумма
	// выше: на одной странице их бывает пять.
	Pages int `json:"pages"`
	// Находки по кодам, в том порядке, в каком их и чинят: сначала
	// важность, потом число страниц.
	//
	// Списком, а не картой, и с уровнем внутри. Карта чисел вынуждала
	// экран сортировать по количеству — и двести страниц с
	// `imagesWithoutAlt` (замечание) вставали выше пяти с `titleMissing`
	// (критично) под заголовком «что чинить первым». Уровень экрану взять
	// неоткуда, а агент его знает: то же правило, по которому у него живут
	// пороги. Порядок здесь — тот же, что в сравнении обходов
	// (`diff.entries`), и считает его та же функция.
	ByCode []CodeCount `json:"byCode,omitempty"`
}

// CodeCount — одна находка в разрезе по кодам.
type CodeCount struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Сколько страниц с этой находкой. «Нет описания на двухстах
	// страницах» — это одна правка шаблона, а не двести правок.
	Pages int `json:"pages"`
}

// Issue — одна находка на странице.
type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Подробность: длина, число, адрес-двойник. Не текст для человека —
	// переводится код, а это подставляется в перевод.
	Detail string `json:"detail,omitempty"`
}

// Audit разбирает страницы обхода и раскладывает находки по страницам.
//
// Меняет страницы на месте и возвращает сводку. Двумя проходами: дубликаты
// заголовков и описаний видны только тогда, когда обход уже закончен, —
// поэтому проверять их по одной странице нечем.
func Audit(pages []Page) IssueSummary {
	return audit(pages, nil)
}

// audit — то же, что Audit, плюс сверка с картой сайта. Карта известна
// только обходу; тесты разбора страниц зовут Audit без неё.
func audit(pages []Page, sitemap *SitemapReport) IssueSummary {
	for index := range pages {
		pages[index].Issues = auditPage(&pages[index])
	}
	addDuplicateIssues(pages)
	addSiteWideHygiene(pages)
	addNotInSitemap(pages, sitemap)

	var summary IssueSummary
	counts := map[string]int{}
	for index := range pages {
		if len(pages[index].Issues) == 0 {
			continue
		}
		summary.Pages++
		for _, issue := range pages[index].Issues {
			counts[issue.Code]++
			switch issue.Severity {
			case SeverityCritical:
				summary.Critical++
			case SeverityWarning:
				summary.Warning++
			default:
				summary.Notice++
			}
		}
	}

	summary.ByCode = rankCodes(counts)
	return summary
}

// rankCodes раскладывает счётчики в том порядке, в каком находки чинят:
// важность, потом число страниц, потом код — чтобы порядок не зависел от
// обхода карты, который в Go случаен.
func rankCodes(counts map[string]int) []CodeCount {
	if len(counts) == 0 {
		return nil
	}

	ranked := make([]CodeCount, 0, len(counts))
	for code, pages := range counts {
		ranked = append(ranked, CodeCount{Code: code, Severity: SeverityOf(code), Pages: pages})
	}

	sort.Slice(ranked, func(left, right int) bool {
		first, second := ranked[left], ranked[right]
		if first.Severity != second.Severity {
			return severityRank(first.Severity) < severityRank(second.Severity)
		}
		if first.Pages != second.Pages {
			return first.Pages > second.Pages
		}
		return first.Code < second.Code
	})
	return ranked
}

// PagesWithCode — сколько страниц с этой находкой. Разрез стал списком ради
// порядка, а вопрос «сколько такого-то» задают и тесты, и сравнение.
func (s IssueSummary) PagesWithCode(code string) int {
	for _, item := range s.ByCode {
		if item.Code == code {
			return item.Pages
		}
	}
	return 0
}

// auditPage — всё, что видно по одной странице.
func auditPage(page *Page) []Issue {
	var issues []Issue
	// Уровень не передаётся, а спрашивается по коду: одна таблица на всё,
	// иначе один и тот же код приезжает с разной важностью из двух мест.
	add := func(code, detail string) {
		issues = append(issues, Issue{Code: code, Severity: SeverityOf(code), Detail: detail})
	}

	// Недоступная страница — единственная находка, которая имеет смысл:
	// разбирать нечего, а перечислять «нет title, нет описания» у
	// пятисотки значит утопить настоящую беду в её же следствиях.
	switch page.Error {
	case PageErrNetwork, PageErrTimeout, PageErrStatus, PageErrRedirectLoop:
		detail := page.Error
		if page.Status > 0 {
			detail = strconv.Itoa(page.Status)
		}
		add(IssuePageUnavailable, detail)
		// Вторая находка, а не замена: страницу чинят, а адрес из карты
		// убирают — правки разные.
		if page.InSitemap {
			add(IssueSitemapBroken, detail)
		}
		return issues
	case PageErrOffsite:
		add(IssueRedirectOffsite, page.FinalURL)
		if page.InSitemap {
			add(IssueSitemapRedirect, page.FinalURL)
		}
		return issues
	case PageErrNotHTML:
		// Файл или лента: аудиту здесь смотреть не на что, и придираться
		// к отсутствию title у PDF мы не будем.
		return nil
	}

	if page.Structure == nil {
		return nil
	}
	structure := page.Structure

	switch {
	case len(page.Redirects) >= redirectChainLength:
		add(IssueRedirectChain, strconv.Itoa(len(page.Redirects)))
	case len(page.Redirects) == 1:
		add(IssueRedirected, page.FinalURL)
	}

	// Карта сайта — список того, что владелец просит показать в поиске.
	// Перенаправление, noindex и canonical на другую страницу этой просьбе
	// противоречат.
	if page.InSitemap {
		if len(page.Redirects) > 0 {
			add(IssueSitemapRedirect, page.FinalURL)
		}
		if structure.Noindex {
			add(IssueSitemapNoindex, "")
		}
		if canonicalDelegates(page) {
			add(IssueSitemapCanonicalised, structure.Canonical)
		}
	}

	// Страница, закрытая от индексации, из поиска выпадает целиком. Это
	// бывает и намеренно, поэтому не критично, — но молчать об этом нельзя:
	// случайный noindex на рабочем разделе выглядит точно так же.
	if structure.Noindex {
		add(IssueNoindex, robotsSource(structure))
	}

	// Копия по содержимому. Если у страницы есть canonical на другую —
	// копия объявлена, поисковик разберётся, и находки нет.
	if page.DuplicateOf != "" && !canonicalDelegates(page) {
		add(IssueDuplicateContent, page.DuplicateOf)
	}

	title := strings.TrimSpace(structure.Title)
	switch length := len([]rune(title)); {
	case title == "":
		add(IssueTitleMissing, "")
	case length < titleMinLength:
		add(IssueTitleShort, strconv.Itoa(length))
	case length > titleMaxLength:
		add(IssueTitleLong, strconv.Itoa(length))
	}

	description := strings.TrimSpace(structure.MetaDescription)
	switch length := len([]rune(description)); {
	case description == "":
		add(IssueDescriptionMissing, "")
	case length < descriptionMinLength:
		add(IssueDescriptionShort, strconv.Itoa(length))
	case length > descriptionMaxLength:
		add(IssueDescriptionLong, strconv.Itoa(length))
	}

	switch len(structure.H1) {
	case 0:
		add(IssueH1Missing, "")
	case 1:
	default:
		add(IssueH1Multiple, strconv.Itoa(len(structure.H1)))
	}

	// Порядок заголовков проверяется в том объёме, в каком он у нас есть:
	// хранятся списки h1–h3, а не их последовательность. Подзаголовок
	// третьего уровня без второго — тот случай, который виден и так.
	if len(structure.H3) > 0 && len(structure.H2) == 0 {
		add(IssueHeadingOrder, "")
	}

	if structure.Images.WithoutAlt > 0 {
		add(IssueImagesWithoutAlt, strconv.Itoa(structure.Images.WithoutAlt))
	}

	switch {
	case structure.Canonical == "":
		add(IssueCanonicalMissing, "")
	case canonicalOffsite(page):
		// Страница отдаёт свой вес чужому сайту. Иногда так и задумано, но
		// куда чаще это шаблон, переехавший с другого домена.
		add(IssueCanonicalOffsite, structure.Canonical)
	case !structure.CanonicalSelf:
		add(IssueCanonicalOther, structure.Canonical)
	}

	if structure.WordCount < thinContentWords {
		add(IssueThinContent, strconv.Itoa(structure.WordCount))
	}
	if structure.Lang == "" {
		add(IssueLangMissing, "")
	}
	if !structure.HasViewport {
		add(IssueViewportMissing, "")
	}
	if missing := missingOpenGraph(structure); missing != "" {
		add(IssueOpenGraphIncomplete, missing)
	}

	return append(issues, hygieneIssues(page)...)
}

// addDuplicateIssues ищет одинаковые заголовки и описания.
//
// Только среди страниц, которые сами претендуют на место в поиске:
// закрытые от индексации и объявленные копиями через canonical повторяют
// чужой заголовок законно, и придираться к ним значит выдать сотню находок
// там, где всё сделано правильно.
func addDuplicateIssues(pages []Page) {
	titles := map[string][]string{}
	descriptions := map[string][]string{}

	for index := range pages {
		page := &pages[index]
		if !indexable(page) {
			continue
		}
		if title := strings.TrimSpace(page.Structure.Title); title != "" {
			titles[title] = append(titles[title], page.URL)
		}
		if description := strings.TrimSpace(page.Structure.MetaDescription); description != "" {
			descriptions[description] = append(descriptions[description], page.URL)
		}
	}

	for index := range pages {
		page := &pages[index]
		if !indexable(page) {
			continue
		}
		if others := twins(titles[strings.TrimSpace(page.Structure.Title)], page.URL); others != "" {
			page.Issues = append(page.Issues, Issue{
				Code:     IssueTitleDuplicate,
				Severity: SeverityOf(IssueTitleDuplicate),
				Detail:   others,
			})
		}
		if others := twins(
			descriptions[strings.TrimSpace(page.Structure.MetaDescription)],
			page.URL,
		); others != "" {
			page.Issues = append(page.Issues, Issue{
				Code:     IssueDescriptionDuplicate,
				Severity: SeverityOf(IssueDescriptionDuplicate),
				Detail:   others,
			})
		}
	}
}

// twins возвращает соседей страницы с тем же текстом — не больше трёх и без
// неё самой. Не больше трёх потому, что у каталога с одинаковым описанием
// их бывает пятьсот, и список из пятисот адресов в каждой строке — это
// мегабайты на чужом диске ради того, что и так понятно с трёх.
func twins(all []string, self string) string {
	var others []string
	for _, address := range all {
		if address != self {
			others = append(others, address)
		}
	}
	if len(others) == 0 {
		return ""
	}

	sort.Strings(others)
	if len(others) > 3 {
		return strings.Join(others[:3], " ") + " +" + strconv.Itoa(len(others)-3)
	}
	return strings.Join(others, " ")
}

// indexable — претендует ли страница на место в поиске сама.
//
// Смотрим на наличие структуры, а не на пустой Error: у страницы с
// `seoPageTooLarge` структура снята и аудит проведён полностью — разметку
// обрезали на потолке, но title и описание лежат в самом её начале. Требуя
// пустой Error, мы выбрасывали такие страницы из поиска дублей заголовков и
// описаний, то есть молчали ровно там, где дубль вероятнее всего.
func indexable(page *Page) bool {
	return page.Structure != nil &&
		!page.Structure.Noindex &&
		!canonicalDelegates(page)
}

// canonicalDelegates — объявила ли страница себя копией другой.
func canonicalDelegates(page *Page) bool {
	return page.Structure != nil &&
		page.Structure.Canonical != "" &&
		!page.Structure.CanonicalSelf
}

// canonicalOffsite — указывает ли canonical на другой сайт.
func canonicalOffsite(page *Page) bool {
	if page.Structure == nil || page.Structure.Canonical == "" {
		return false
	}
	return !sameHost(page.Structure.Canonical, address(page))
}

// address — по какому адресу страница в итоге оказалась.
func address(page *Page) string {
	if page.FinalURL != "" {
		return page.FinalURL
	}
	return page.URL
}

// missingOpenGraph — чего не хватает карточке для соцсетей и мессенджеров.
// Пусто означает, что хватает всего.
func missingOpenGraph(structure *Structure) string {
	var missing []string
	for _, key := range []string{"title", "description", "image"} {
		if strings.TrimSpace(structure.OpenGraph[key]) == "" {
			missing = append(missing, key)
		}
	}
	// Открытого графа нет вовсе — это одна находка, а не три: ставят его
	// целиком или не ставят никак.
	if len(missing) == 3 {
		return "og"
	}
	return strings.Join(missing, ",")
}

// robotsSource — откуда пришёл запрет индексации. Заголовка в разметке не
// видно вовсе, и не сказать об этом значит отправить человека искать в HTML
// то, чего там нет.
func robotsSource(structure *Structure) string {
	if hasToken(strings.ToLower(structure.XRobotsTag), "noindex") ||
		hasToken(strings.ToLower(structure.XRobotsTag), "none") {
		return "header"
	}
	return "meta"
}

// Worst — самый тяжёлый уровень среди находок страницы. Пусто, если находок
// нет.
//
// Живёт здесь, а не в хранилище: порядок уровней — это знание о смысле
// кодов, и держать его в SQL значит завести вторую копию, которая однажды
// разойдётся с первой.
func Worst(issues []Issue) string {
	worst := ""
	for _, issue := range issues {
		switch issue.Severity {
		case SeverityCritical:
			return SeverityCritical
		case SeverityWarning:
			worst = SeverityWarning
		default:
			if worst == "" {
				worst = SeverityNotice
			}
		}
	}
	return worst
}

// AtLeast — уровни не легче заданного. Пустой или незнакомый уровень
// означает «все страницы», и фильтра не будет вовсе.
func AtLeast(severity string) []string {
	switch severity {
	case SeverityCritical:
		return []string{SeverityCritical}
	case SeverityWarning:
		return []string{SeverityCritical, SeverityWarning}
	case SeverityNotice:
		return []string{SeverityCritical, SeverityWarning, SeverityNotice}
	default:
		return nil
	}
}
