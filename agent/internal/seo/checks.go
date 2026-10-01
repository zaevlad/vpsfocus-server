package seo

// Перечень проверок: что аудит смотрел и чем каждая проверка кончилась.
//
// Находки отвечают на вопрос «что не так». Человеку без навыков админа этого
// мало: список из семи бед не говорит, смотрели ли остальное, — а «проверок
// пятьдесят шесть, сорок семь пройдены» говорит (трек U,
// «Наглядные отчёты»).
//
// Считает агент, а не экран, по тому же правилу, что и сами находки: список
// кодов, их важность и условия, при которых проверку нельзя было провести,
// знает только он. Вторая копия на экране однажды разошлась бы, и «пройдено»
// стало бы значить «агент про это не писал».

// Исход проверки.
const (
	// Проверку провели и ошибок не нашли — на тех страницах, что обошли.
	CheckPassed = "passed"
	// Нашли хотя бы одну ошибку.
	CheckFailed = "failed"
	// Проверить не удалось. «Не знаем» — не «в порядке»: пройденной такая
	// проверка не считается.
	CheckUnknown = "unknown"
)

// Почему проверку не удалось провести. Перевод — в приложении.
const (
	// Сверка с картой сайта выключена настройкой (`SEO_SITEMAP=false`).
	CheckReasonSitemapOff = "sitemapOff"
	// Карты сайта нет или она не читается: сверять не с чем.
	CheckReasonNoSitemap = "noSitemap"
	// Карта прочитана не целиком: «страницы нет в карте» значило бы «мы до
	// неё не дочитали».
	CheckReasonSitemapPartial = "sitemapPartial"
	// Входы на сайт не проверялись: обход оборвался по времени.
	CheckReasonEntriesSkipped = "entriesSkipped"
	// Ни одной страницы разобрать не удалось.
	CheckReasonNoPages = "noPages"
)

// Группы проверок — темы, по которым экран раскладывает перечень.
const (
	GroupAccess    = "access"
	GroupSitemap   = "sitemap"
	GroupTitles    = "titles"
	GroupContent   = "content"
	GroupIndexing  = "indexing"
	GroupDelivery  = "delivery"
	GroupAddresses = "addresses"
)

// checkGroup — тема и её проверки в порядке показа.
type checkGroup struct {
	ID    string
	Codes []string
}

// checkGroups — одна копия того, какая проверка к какой теме относится.
//
// Порядок групп и кодов внутри — порядок на экране: сначала то, без чего
// сайта в поиске нет вовсе, потом то, что видно в выдаче, потом гигиена.
// Тест `TestEveryCodeHasExactlyOneGroup` сверяет список с `severityByCode`:
// новая находка без группы не попала бы в перечень и не считалась бы ни
// пройденной, ни проваленной.
var checkGroups = []checkGroup{
	{ID: GroupAccess, Codes: []string{
		IssueSearchEnginesBlocked,
		IssueAISearchBlocked,
		IssueAITrainingBlocked,
		IssueRobotsMissing,
		IssueCanonicalNoHTTPS,
		IssueCanonicalHostSplit,
		IssueCanonicalLoop,
	}},
	{ID: GroupTitles, Codes: []string{
		IssueTitleMissing,
		IssueTitleShort,
		IssueTitleLong,
		IssueTitleDuplicate,
		IssueDescriptionMissing,
		IssueDescriptionShort,
		IssueDescriptionLong,
		IssueDescriptionDuplicate,
		IssueH1Missing,
		IssueH1Multiple,
		IssueH1Duplicate,
		IssueH1Long,
		IssueHeadingOrder,
	}},
	{ID: GroupContent, Codes: []string{
		IssueDuplicateContent,
		IssueThinContent,
		IssueSoft404Title,
		IssueImagesWithoutAlt,
		IssueLangMissing,
		IssueViewportMissing,
		IssueOpenGraphIncomplete,
	}},
	{ID: GroupIndexing, Codes: []string{
		IssueNoindex,
		IssueCanonicalMissing,
		IssueCanonicalOther,
		IssueCanonicalOffsite,
		IssueNoindexWithCanonical,
		IssueUnavailableAfterPassed,
		IssueRobotsNosnippet,
		IssueRobotsNoimageindex,
	}},
	{ID: GroupSitemap, Codes: []string{
		IssueSitemapMissing,
		IssueSitemapUnavailable,
		IssueSitemapBlockedByRobots,
		IssueSitemapBroken,
		IssueSitemapRedirect,
		IssueSitemapNoindex,
		IssueSitemapCanonicalised,
		IssueNotInSitemap,
	}},
	{ID: GroupDelivery, Codes: []string{
		IssuePageUnavailable,
		IssueRedirectChain,
		IssueRedirected,
		IssueRedirectOffsite,
		IssueSlowResponse,
		IssueDeadEnd,
		IssueTooManyLinks,
	}},
	{ID: GroupAddresses, Codes: []string{
		IssueURLCaseDuplicate,
		IssueURLSlashDuplicate,
		IssueURLRepeatingSegment,
		IssueURLShape,
		IssueURLTracking,
		IssueInternalSearchCrawled,
	}},
}

// Проверки о сайте целиком: их находки лежат не у страниц, а в отчётах о
// сайте и о входах, и числа страниц у них нет.
var siteWideChecks = map[string]bool{
	IssueSearchEnginesBlocked:   true,
	IssueAISearchBlocked:        true,
	IssueAITrainingBlocked:      true,
	IssueRobotsMissing:          true,
	IssueCanonicalNoHTTPS:       true,
	IssueCanonicalHostSplit:     true,
	IssueCanonicalLoop:          true,
	IssueSitemapMissing:         true,
	IssueSitemapUnavailable:     true,
	IssueSitemapBlockedByRobots: true,
}

// Проверки входов на сайт (`canonical.go`).
var entryChecks = []string{IssueCanonicalNoHTTPS, IssueCanonicalHostSplit, IssueCanonicalLoop}

// Проверки, которым нужна прочитанная карта сайта.
var sitemapDependentChecks = []string{
	IssueSitemapBlockedByRobots,
	IssueSitemapBroken,
	IssueSitemapRedirect,
	IssueSitemapNoindex,
	IssueSitemapCanonicalised,
	IssueNotInSitemap,
}

// Check — одна проверка и её исход.
type Check struct {
	Code     string `json:"code"`
	Group    string `json:"group"`
	Severity string `json:"severity"`
	// `passed` | `failed` | `unknown`.
	State string `json:"state"`
	// На скольких страницах нашли. Ноль у проверок о сайте целиком: там
	// вопрос не про страницы.
	Pages int `json:"pages,omitempty"`
	// Почему не удалось проверить. Только у `unknown`.
	Reason string `json:"reason,omitempty"`
}

// CheckTally — счёт перечня. Сумма всех полей, кроме `Total`, равна `Total`.
type CheckTally struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	// Непройденные — по важности, той же линейкой, что у находок.
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Notice   int `json:"notice"`
	Unknown  int `json:"unknown"`
}

// ChecksReport — перечень проверок одного обхода.
type ChecksReport struct {
	Tally CheckTally `json:"tally"`
	// Все проверки, в порядке групп.
	Items []Check `json:"items"`
}

// Progress — что изменилось в перечне с прошлого обхода.
//
// По проверкам, а не по страницам: «исправлено шесть» на экране итога должно
// сходиться с «пройдено было 42, стало 47» рядом. Подробности по страницам —
// отдельным запросом (`diff.go`), как и раньше.
type Progress struct {
	// Когда был обход, с которым сравнили.
	PreviousAt string `json:"previousAt"`
	// Была не пройдена, стала пройдена.
	Fixed []string `json:"fixed"`
	// Была пройдена, стала не пройдена.
	Appeared []string `json:"appeared"`
}

// BuildChecks собирает перечень из сводки обхода.
//
// Из сводки, а не из страниц: так он строится и сразу после обхода, и при
// чтении обхода, сохранённого агентом до 0.18.0 (`EnsureChecks`).
//
// Пусто, если сказать нечего: robots.txt не прочитан (отчёта о сайте нет)
// или обход не принёс ни одной страницы.
func BuildChecks(stats Stats) *ChecksReport {
	if stats.Site == nil || stats.Pages == 0 {
		return nil
	}

	failedPages := map[string]int{}
	failed := map[string]bool{}
	for _, item := range stats.Issues.ByCode {
		failed[item.Code] = true
		failedPages[item.Code] = item.Pages
	}
	for _, issue := range stats.Site.Issues {
		failed[issue.Code] = true
	}
	if stats.Canonical != nil {
		for _, issue := range stats.Canonical.Issues {
			failed[issue.Code] = true
		}
	}

	unknown := unknownChecks(stats)

	report := &ChecksReport{Items: []Check{}}
	for _, group := range checkGroups {
		for _, code := range group.Codes {
			check := Check{Code: code, Group: group.ID, Severity: SeverityOf(code)}
			switch {
			// Находка сильнее «не удалось проверить»: раз нашли — значит,
			// проверили.
			case failed[code]:
				check.State = CheckFailed
				check.Pages = failedPages[code]
				switch check.Severity {
				case SeverityCritical:
					report.Tally.Critical++
				case SeverityWarning:
					report.Tally.Warning++
				default:
					report.Tally.Notice++
				}
			case unknown[code] != "":
				check.State = CheckUnknown
				check.Reason = unknown[code]
				report.Tally.Unknown++
			default:
				check.State = CheckPassed
				report.Tally.Passed++
			}
			report.Tally.Total++
			report.Items = append(report.Items, check)
		}
	}
	return report
}

// unknownChecks — какие проверки провести было нельзя и почему.
func unknownChecks(stats Stats) map[string]string {
	unknown := map[string]string{}
	set := func(reason string, codes ...string) {
		for _, code := range codes {
			if unknown[code] == "" {
				unknown[code] = reason
			}
		}
	}

	switch sitemap := stats.Site.Sitemap; {
	case sitemap == nil:
		set(CheckReasonSitemapOff, IssueSitemapMissing, IssueSitemapUnavailable)
		set(CheckReasonSitemapOff, sitemapDependentChecks...)
	case !sitemap.Found:
		set(CheckReasonNoSitemap, sitemapDependentChecks...)
		// Карты нет вовсе — спрашивать, читается ли она, не о чем. Когда
		// она объявлена и не отдалась, эта проверка провалена, а не
		// неизвестна: находка перекроет причину.
		set(CheckReasonNoSitemap, IssueSitemapUnavailable)
	case !sitemap.Complete:
		set(CheckReasonSitemapPartial, IssueNotInSitemap)
	}

	if stats.Canonical == nil {
		set(CheckReasonEntriesSkipped, entryChecks...)
	}

	if stats.Fetched == 0 {
		for _, group := range checkGroups {
			for _, code := range group.Codes {
				if !siteWideChecks[code] {
					set(CheckReasonNoPages, code)
				}
			}
		}
	}
	return unknown
}

// EnsureChecks достраивает перечень обходу, сохранённому без него.
//
// Это обходы агента 0.16.0–0.17.0: набор проверок у них тот же, и всё, из
// чего перечень собирается, в их сводке лежит. Обходы старше 0.16.0 отчёта о
// сайте не несут и перечня не получают: проверок тогда было вдвое меньше, и
// назвать остальные «пройденными» было бы неправдой.
func (s *Stats) EnsureChecks() {
	if s.Checks == nil {
		s.Checks = BuildChecks(*s)
	}
}

// CompareChecks — что изменилось между двумя перечнями.
//
// «Не удалось проверить» в сравнении не участвует: проверка, которая вчера
// не проводилась, а сегодня пройдена, не «исправлена», а проверка, которую
// сегодня провести не удалось, не «появилась».
func CompareChecks(current, previous *ChecksReport, previousAt string) *Progress {
	if current == nil || previous == nil {
		return nil
	}

	before := map[string]string{}
	for _, check := range previous.Items {
		before[check.Code] = check.State
	}

	progress := &Progress{PreviousAt: previousAt, Fixed: []string{}, Appeared: []string{}}
	for _, check := range current.Items {
		switch {
		case before[check.Code] == CheckFailed && check.State == CheckPassed:
			progress.Fixed = append(progress.Fixed, check.Code)
		case before[check.Code] == CheckPassed && check.State == CheckFailed:
			progress.Appeared = append(progress.Appeared, check.Code)
		}
	}
	return progress
}
