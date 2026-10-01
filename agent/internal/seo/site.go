package seo

import (
	"sort"
	"strconv"
	"strings"

	"agent/internal/bots"
	"agent/internal/robots"
)

// Находки о сайте целиком: robots.txt, карта сайта, кому открыт сайт.
//
// Это вопросы не о странице, и вешать их на главную значило бы соврать,
// где беда, и умножить её в разрезе «сколько страниц». Живут они отдельным
// отчётом рядом с контролем входа (`CanonicalReport`), а уровень берут из
// общей таблицы `severityByCode` — линейка важности одна на весь SEO.
//
// Новых запросов к сайту здесь почти нет: robots.txt и карта сайта уже
// читаются обходом, меняется только то, что из них извлекается.

// Коды находок о сайте.
const (
	// robots.txt нет (4xx). Обход по RFC 9309 разрешён — это только совет.
	IssueRobotsMissing = "robotsMissing"
	// robots.txt закрывает сайт целиком от Google, Bing или Яндекса.
	IssueSearchEnginesBlocked = "searchEnginesBlocked"
	// Закрыт бот ИИ-поиска: сайт не попадёт в ответы ChatGPT, Claude,
	// Perplexity со ссылкой на источник.
	IssueAISearchBlocked = "aiSearchBlocked"
	// Закрыт бот обучения нейросетей. Частый осознанный выбор — поэтому
	// замечание, а не предупреждение.
	IssueAITrainingBlocked = "aiTrainingBlocked"
	// Карты сайта нет: в robots.txt не объявлена, а /sitemap.xml нет.
	IssueSitemapMissing = "sitemapMissing"
	// Карта объявлена или лежит на месте, но не читается.
	IssueSitemapUnavailable = "sitemapUnavailable"
	// Адреса из карты закрыты robots.txt: владелец просит поисковик их
	// индексировать и тут же запрещает туда ходить.
	IssueSitemapBlockedByRobots = "sitemapBlockedByRobots"
)

// Коды находок страницы, которые выводятся из карты сайта. Проверяются
// вместе с остальными находками страницы (`auditPage`), живут здесь — рядом
// с картой, о которой спрашивают.
const (
	// Адрес из карты отвечает ошибкой.
	IssueSitemapBroken = "sitemapBroken"
	// Адрес из карты перенаправляет: в карте должен стоять конечный.
	IssueSitemapRedirect = "sitemapRedirect"
	// Адрес из карты закрыт от индексации.
	IssueSitemapNoindex = "sitemapNoindex"
	// Адрес из карты объявил себя копией другой страницы.
	IssueSitemapCanonicalised = "sitemapCanonicalised"
	// Индексируемой страницы нет в карте.
	IssueNotInSitemap = "notInSitemap"
)

// Состояние robots.txt в отчёте.
const (
	RobotsFound  = "found"
	RobotsAbsent = "absent"
)

// Кто этот бот для сайта. Роли — из общего списка роботов (`bots`): тот же
// список читает и экран «Агенты и ИИ» по логам.
const (
	BotSearchEngine = bots.SearchEngine
	BotAISearch     = bots.AISearch
	BotAITraining   = bots.AITraining
)

// knownBots — роботы, о которых спрашиваем robots.txt. Список один на агента
// (`bots.List`); роботов «по вопросу человека» здесь нет — см. `bots.AIUser`.
var knownBots = bots.ForRobots()

// Находка о закрытом боте — по его роли.
var blockedIssueByKind = map[string]string{
	BotSearchEngine: IssueSearchEnginesBlocked,
	BotAISearch:     IssueAISearchBlocked,
	BotAITraining:   IssueAITrainingBlocked,
}

// SiteReport — сайт целиком.
type SiteReport struct {
	// robots.txt: `found` или `absent`. Неотданный файл сюда не доходит —
	// обход при нём не начинается вовсе (`seoRobotsUnavailable`).
	Robots string `json:"robots"`
	// Кому сайт открыт. Все известные боты, в том числе открытые: «мы
	// посмотрели, и всё открыто» — это ответ, а молчание читалось бы как
	// «не проверяли».
	Bots []BotAccess `json:"bots"`
	// Карта сайта. Пусто — сверка с картой выключена настройкой
	// (`SEO_SITEMAP=false`), и это не то же самое, что «карты нет».
	Sitemap *SitemapReport `json:"sitemap,omitempty"`
	// Находки. Пусто — к сайту целиком вопросов нет.
	Issues []Issue `json:"issues,omitempty"`
}

// BotAccess — открыт ли сайт одному боту.
type BotAccess struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Разрешена ли боту главная. Частичный запрет (админка, поиск) закрытием
	// не считается: это норма, и находка сыпалась бы на каждый сайт.
	Allowed bool `json:"allowed"`
}

// SitemapReport — что узнали о карте сайта.
type SitemapReport struct {
	// Объявлена ли карта в robots.txt.
	Declared bool `json:"declared"`
	// Прочитан ли хоть один файл карты.
	Found bool `json:"found"`
	// Первый файл карты, который мы читали или пытались прочитать.
	Address string `json:"address,omitempty"`
	// Сколько адресов нашего сайта в карте.
	Addresses int `json:"addresses"`
	// Прочитана ли карта целиком. Ложь — упёрлись в потолок файлов, байт
	// или адресов, либо часть файлов не отдалась. Тогда «страницы нет в
	// карте» не утверждается вовсе: проверка, обещающая больше, чем делает,
	// хуже отсутствующей.
	Complete bool `json:"complete"`
	// Сколько адресов из карты закрыто robots.txt.
	BlockedByRobots int `json:"blockedByRobots"`
	// Первый файл карты, который не отдался или оказался не картой.
	// Пусто — все прочитанные файлы прочитались.
	Failed string `json:"failed,omitempty"`
}

// checkRobots — robots.txt и боты. Запросов не делает: файл уже прочитан.
func checkRobots(rules *robots.Rules, verdict robots.Verdict) *SiteReport {
	report := &SiteReport{Robots: RobotsFound}
	if verdict == robots.Absent {
		report.Robots = RobotsAbsent
		report.add(IssueRobotsMissing, "")
	}

	blocked := map[string][]string{}
	for _, bot := range knownBots {
		allowed := rules.AllowedFor(bot.Name, "/")
		report.Bots = append(report.Bots, BotAccess{Name: bot.Name, Kind: bot.Kind, Allowed: allowed})
		if !allowed {
			blocked[bot.Kind] = append(blocked[bot.Kind], bot.Name)
		}
	}
	for _, kind := range []string{BotSearchEngine, BotAISearch, BotAITraining} {
		if names := blocked[kind]; len(names) > 0 {
			report.add(blockedIssueByKind[kind], strings.Join(names, ", "))
		}
	}
	return report
}

// checkSitemap — находки о карте сайта. Зовётся после чтения карты.
func (r *SiteReport) checkSitemap() {
	sitemap := r.Sitemap
	if sitemap == nil {
		return
	}
	switch {
	case sitemap.Failed != "":
		r.add(IssueSitemapUnavailable, sitemap.Failed)
	case !sitemap.Found:
		// Не объявлена и на обычном месте её нет: /sitemap.xml ответил 4xx
		// или отдал не карту (сайт на одной странице отвечает своей главной
		// на любой адрес).
		r.add(IssueSitemapMissing, "")
	}
	if sitemap.BlockedByRobots > 0 {
		r.add(IssueSitemapBlockedByRobots, strconv.Itoa(sitemap.BlockedByRobots))
	}
}

func (r *SiteReport) add(code, detail string) {
	r.Issues = append(r.Issues, Issue{Code: code, Severity: SeverityOf(code), Detail: detail})
}

// sortIssues раскладывает находки по важности: экран читает сверху.
func (r *SiteReport) sortIssues() {
	sort.SliceStable(r.Issues, func(left, right int) bool {
		return severityRank(r.Issues[left].Severity) < severityRank(r.Issues[right].Severity)
	})
}

// addNotInSitemap отмечает индексируемые страницы, которых нет в карте.
//
// Только когда карта есть и прочитана целиком: у недочитанной карты
// «страницы нет» значило бы «мы до неё не дочитали». И только у страниц,
// которые сами претендуют на место в поиске: закрытую или склеенную
// владелец в карту и не должен класть.
func addNotInSitemap(pages []Page, sitemap *SitemapReport) {
	if sitemap == nil || !sitemap.Found || !sitemap.Complete {
		return
	}
	for index := range pages {
		page := &pages[index]
		if page.InSitemap || page.Status != 200 || len(page.Redirects) > 0 || !indexable(page) {
			continue
		}
		page.Issues = append(page.Issues, Issue{
			Code:     IssueNotInSitemap,
			Severity: SeverityOf(IssueNotInSitemap),
		})
	}
}
