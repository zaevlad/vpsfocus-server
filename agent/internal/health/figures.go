package health

import (
	"strings"

	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/links"
	"agent/internal/seo"
	"agent/internal/vitals"
)

// Показатели проекта: четыре числа, по которым карточка на главном экране
// отвечает «как дела» одним взглядом (трек U «Наглядные отчёты»).
//
// Поводы отвечают на вопрос «нужно ли что-то делать сегодня» и потому молчат
// о гигиене: скорость в лаборатории, предупреждения SEO уровень не
// поднимают. Человеку же интересно и «насколько хорошо» — оценка 72 из 100,
// 47 проверок из 56. Показатель несёт это число и собственную оценку.
//
// Оценка показателя и уровень проекта — разные вещи, и расходиться им
// можно: уровень «в порядке» значит «сегодня ничего не горит», жёлтый
// показатель скорости — «можно лучше». Уровень показатели не трогают.
//
// Считает агент: пороги сроков, оценка скорости и перечень проверок живут
// у него, и вторая копия на экране однажды разошлась бы.

// Области показателей.
const (
	AreaSpeed  = "speed"
	AreaSeo    = "seo"
	AreaLinks  = "links"
	AreaExpiry = "expiry"
)

// StateUnknown — показателя нет: не мерили, не обходили, проверка не
// состоялась. Не «в порядке» и не ноль.
const StateUnknown = "unknown"

// Что истекает раньше — для подписи показателя сроков.
const (
	ExpiryDomain = "domain"
	ExpiryCert   = "cert"
)

// Figure — один показатель проекта.
type Figure struct {
	// `speed` | `seo` | `links` | `expiry`.
	Area string `json:"area"`
	// `ok` | `attention` | `problem` | `unknown`.
	State string `json:"state"`
	// Само число: оценка скорости, пройденные проверки, битые ссылки, дни
	// до ближайшего срока. Пусто — числа нет.
	Value *int `json:"value,omitempty"`
	// Из скольких: проверок SEO или ссылок.
	Total int `json:"total,omitempty"`
	// К какому домену относится число. У проекта их бывает несколько, и
	// показывается худший: усреднять «хорошо» и «плохо» значит спрятать беду.
	Domain string `json:"domain,omitempty"`
	// `domain` | `cert` — что истекает раньше. Только у сроков.
	Kind string `json:"kind,omitempty"`
}

// figureSources — собранное по доменам, из чего показатели считаются.
type figureSources struct {
	domains  map[string]checks.DomainStatus
	certs    map[string]checks.TLSStatus
	links    map[string]links.Report
	scans    map[string]seo.Scan
	measured map[string][]vitals.Report
}

// figures — показатели проекта, всегда четыре и всегда в одном порядке:
// экран не гадает, которого нет.
func figures(project config.Project, in Input, sources figureSources) []Figure {
	return []Figure{
		speedFigure(project.Domains, sources.measured),
		seoFigure(project.Domains, sources.scans),
		linksFigure(project.Domains, sources.links),
		expiryFigure(project.Domains, sources, in),
	}
}

func unknownFigure(area string) Figure {
	return Figure{Area: area, State: StateUnknown}
}

// levelOfRating — оценка Google как состояние показателя.
func levelOfRating(rating string) string {
	switch rating {
	case vitals.RatingGood:
		return string(LevelOK)
	case vitals.RatingPoor:
		return string(LevelProblem)
	default:
		return string(LevelAttention)
	}
}

// speedFigure — оценка скорости главной страницы.
//
// Лабораторная: она есть у любой страницы, а у живых посетителей малого
// сайта данных нет вовсе. Из замеров домена берётся главная и мобильный
// замер, когда они есть: по мобильному судит Google. Среди доменов проекта
// — худшая.
func speedFigure(domains []string, measured map[string][]vitals.Report) Figure {
	figure := unknownFigure(AreaSpeed)
	for _, domain := range domains {
		report, ok := homeReport(measured[strings.ToLower(domain)])
		if !ok {
			continue
		}
		if figure.Value != nil && *figure.Value <= report.Lab.Score {
			continue
		}
		score := report.Lab.Score
		figure.Value = &score
		figure.State = levelOfRating(report.Lab.ScoreRating)
		figure.Domain = report.Domain
	}
	return figure
}

// homeReport выбирает замер, по которому судят о домене: главная, мобильный.
func homeReport(reports []vitals.Report) (vitals.Report, bool) {
	var (
		best  vitals.Report
		found bool
		rank  int
	)
	for _, report := range reports {
		if report.Lab == nil {
			continue
		}
		// Главная важнее устройства: замер каталога с телефона о сайте
		// говорит меньше, чем главная с компьютера.
		current := 1
		if report.Path == "/" {
			current += 2
		}
		if report.Strategy == "mobile" {
			current++
		}
		if !found || current > rank {
			best, rank, found = report, current, true
		}
	}
	return best, found
}

// seoFigure — сколько проверок аудита пройдено.
//
// Оценка — по той же линейке, что на экране аудита: критичная находка
// красная, предупреждение жёлтое. Среди доменов проекта — худший.
func seoFigure(domains []string, scans map[string]seo.Scan) Figure {
	figure := unknownFigure(AreaSeo)
	worstRank := -1
	for _, domain := range domains {
		scan, ok := scans[strings.ToLower(domain)]
		if !ok {
			continue
		}
		stats := scan.Stats
		stats.EnsureChecks()
		if stats.Checks == nil {
			continue
		}
		tally := stats.Checks.Tally

		state := LevelOK
		switch {
		case tally.Critical > 0:
			state = LevelProblem
		case tally.Warning > 0:
			state = LevelAttention
		}

		better := rank(state) < worstRank ||
			(rank(state) == worstRank && figure.Value != nil && tally.Passed >= *figure.Value)
		if figure.Value != nil && better {
			continue
		}
		passed := tally.Passed
		figure.Value = &passed
		figure.Total = tally.Total
		figure.State = string(state)
		figure.Domain = scan.Domain
		worstRank = rank(state)
	}
	return figure
}

// linksFigure — сколько ссылок битых. Круг, который не состоялся, числа не
// даёт: ноль битых у необойдённого сайта был бы неправдой.
func linksFigure(domains []string, reports map[string]links.Report) Figure {
	figure := unknownFigure(AreaLinks)
	for _, domain := range domains {
		report, ok := reports[strings.ToLower(domain)]
		if !ok || report.Error != "" {
			continue
		}
		if figure.Value != nil && *figure.Value >= report.Broken {
			continue
		}
		broken := report.Broken
		figure.Value = &broken
		figure.Total = report.TotalLinks
		figure.Domain = report.Domain
		figure.State = string(LevelOK)
		if broken > 0 {
			figure.State = string(levelByCode[CodeLinksBroken])
		}
	}
	return figure
}

// expiryFigure — сколько дней до ближайшего срока: домена или сертификата.
//
// Оценка — худший из поводов про сроки, теми же порогами, что у свода и у
// писем. Сертификат, который не принят вовсе, дней не имеет, но показатель
// красит: молчать о нём нельзя.
func expiryFigure(domains []string, sources figureSources, in Input) Figure {
	figure := unknownFigure(AreaExpiry)
	var reasons []Reason

	consider := func(days *int, domain, kind string) {
		if days == nil {
			return
		}
		if figure.Value != nil && *figure.Value <= *days {
			return
		}
		value := *days
		figure.Value = &value
		figure.Domain = domain
		figure.Kind = kind
	}

	checked := false
	for _, domain := range domains {
		key := strings.ToLower(domain)
		if status, ok := sources.domains[key]; ok {
			checked = true
			reasons = append(reasons, domainReasons(status, in.DomainDays)...)
			consider(status.DaysLeft, status.Domain, ExpiryDomain)
		}
		if status, ok := sources.certs[key]; ok {
			checked = true
			reasons = append(reasons, certReasons(status, in.CertDays)...)
			consider(status.DaysLeft, status.Domain, ExpiryCert)
		}
	}

	switch {
	case !checked:
		return figure
	case len(reasons) > 0:
		// Показатель рассказывает о худшем поводе целиком — и числом, и
		// доменом. Иначе красный цвет от непринятого сертификата встал бы
		// рядом с «200 дней» чужого домена.
		level := worst(reasons)
		var chosen *Reason
		for index := range reasons {
			item := &reasons[index]
			if item.Level != level {
				continue
			}
			// Среди равных — ближайший срок; беда без срока (сертификат
			// не принят) важнее любой с числом.
			if chosen == nil ||
				(chosen.Days != nil && (item.Days == nil || *item.Days < *chosen.Days)) {
				chosen = item
			}
		}
		figure.State = string(level)
		figure.Value = chosen.Days
		figure.Domain = chosen.Domain
		figure.Kind = ExpiryCert
		if strings.HasPrefix(chosen.Code, "healthDomain") {
			figure.Kind = ExpiryDomain
		}
	case figure.Value != nil:
		figure.State = string(LevelOK)
	}
	return figure
}
