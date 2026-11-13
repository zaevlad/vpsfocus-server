package seo

// «Не учитывать» (трек AG): обход без находок, которые человек пометил «так
// задумано».
//
// Здесь — только арифметика над обходом: какие находки вычесть, решает
// вызывающий (`internal/ignore`), а что из них следует для сводки, перечня
// и сравнения — знает этот пакет, потому что сводку и перечень считает он
// же. Вторая копия подсчёта в другом пакете однажды разошлась бы с
// `audit` и `BuildChecks`.

// Skip — что не учитывать в обходе одного сайта.
type Skip struct {
	// Находка `code` на странице `url`; пустой адрес — находка о сайте
	// целиком (robots.txt, входы, карта сайта).
	Finding func(code, url string) bool
	// Проверка не учитывается на всём сайте: она уходит из перечня в
	// `IgnoredChecks`.
	Whole func(code string) bool
	// Есть ли записи, которые зависят от страницы. Без страниц обхода их
	// не применить, и сводка такого обхода не пересчитывается.
	PageScoped bool
}

// Empty — пропускать нечего: обход отдаётся как есть.
func (k Skip) Empty() bool { return k.Finding == nil && k.Whole == nil }

func (k Skip) skips(code, url string) bool {
	if k.Whole != nil && k.Whole(code) {
		return true
	}
	return k.Finding != nil && k.Finding(code, url)
}

func (k Skip) whole(code string) bool { return k.Whole != nil && k.Whole(code) }

// Without — сводка обхода без неучтённого.
//
// `pages` — коды находок по адресам страниц этого обхода; nil — страниц не
// сохранилось. Тогда записи «на одной странице» и «по началу адреса»
// применить нечем, и сводка возвращается как есть с ok = false. Записи «на
// всём сайте» применяются и без страниц: проверка уходит целиком.
func (s Stats) Without(pages map[string][]string, skip Skip) (Stats, bool) {
	if skip.Empty() {
		return s, true
	}
	if pages == nil && skip.PageScoped {
		return s, false
	}

	original := s.Checks
	if original == nil {
		original = BuildChecks(s)
	}

	out := s
	if pages != nil {
		out.Issues = summarize(pages, skip)
	} else {
		out.Issues = s.Issues.withoutCodes(skip.whole)
	}
	if s.Site != nil {
		site := *s.Site
		site.Issues = keepIssues(site.Issues, "", skip)
		out.Site = &site
	}
	if s.Canonical != nil {
		canonical := *s.Canonical
		canonical.Issues = keepIssues(canonical.Issues, "", skip)
		out.Canonical = &canonical
	}

	out.Checks = nil
	if original != nil {
		if built := BuildChecks(out); built != nil {
			out.Checks = withIgnored(built, original, skip)
		}
	}
	return out, true
}

// summarize — сводка находок по страницам тем же счётом, что `audit`.
func summarize(pages map[string][]string, skip Skip) IssueSummary {
	var summary IssueSummary
	counts := map[string]int{}
	for url, codes := range pages {
		kept := 0
		for _, code := range codes {
			if skip.skips(code, url) {
				continue
			}
			kept++
			counts[code]++
			switch SeverityOf(code) {
			case SeverityCritical:
				summary.Critical++
			case SeverityWarning:
				summary.Warning++
			default:
				summary.Notice++
			}
		}
		if kept > 0 {
			summary.Pages++
		}
	}
	summary.ByCode = rankCodes(counts)
	return summary
}

// withoutCodes — сводка без проверок, неучтённых на всём сайте, когда
// страниц нет. Число страниц с находками при этом не пересчитать: оно
// остаётся прежним, и в истории его не показывают.
func (s IssueSummary) withoutCodes(whole func(string) bool) IssueSummary {
	out := s
	out.ByCode = nil
	for _, item := range s.ByCode {
		if !whole(item.Code) {
			out.ByCode = append(out.ByCode, item)
			continue
		}
		switch item.Severity {
		case SeverityCritical:
			out.Critical -= item.Pages
		case SeverityWarning:
			out.Warning -= item.Pages
		default:
			out.Notice -= item.Pages
		}
	}
	return out
}

func keepIssues(issues []Issue, url string, skip Skip) []Issue {
	var out []Issue
	for _, issue := range issues {
		if !skip.skips(issue.Code, url) {
			out = append(out, issue)
		}
	}
	return out
}

// withIgnored раскладывает перечень: проверки, неучтённые на всём сайте, —
// в `IgnoredChecks` с исходом, какой был бы без «не учитывать»; у
// неучтённых на части страниц — число этих страниц. Счёт — заново по
// оставшимся.
func withIgnored(built, original *ChecksReport, skip Skip) *ChecksReport {
	before := map[string]Check{}
	for _, check := range original.Items {
		before[check.Code] = check
	}

	out := &ChecksReport{Items: []Check{}}
	for _, check := range built.Items {
		if skip.whole(check.Code) {
			if was, ok := before[check.Code]; ok {
				check = was
			}
			out.IgnoredChecks = append(out.IgnoredChecks, check)
			continue
		}
		if was, ok := before[check.Code]; ok && was.Pages > check.Pages {
			check.IgnoredPages = was.Pages - check.Pages
		}
		out.Items = append(out.Items, check)
	}
	out.Tally = tallyOf(out.Items)
	out.Tally.Ignored = len(out.IgnoredChecks)
	return out
}

func tallyOf(items []Check) CheckTally {
	var tally CheckTally
	for _, check := range items {
		tally.Total++
		switch check.State {
		case CheckPassed:
			tally.Passed++
		case CheckUnknown:
			tally.Unknown++
		default:
			switch check.Severity {
			case SeverityCritical:
				tally.Critical++
			case SeverityWarning:
				tally.Warning++
			default:
				tally.Notice++
			}
		}
	}
	return tally
}

// Without — коды находок по страницам без неучтённого: для сравнения двух
// обходов (`Compare`, Р7). Обе стороны вычитаются одним списком —
// сегодняшним, — и «перестали учитывать» не выглядит «исправлено».
func (c ScanCodes) Without(skip Skip) ScanCodes {
	if skip.Empty() || c.Pages == nil {
		return c
	}
	out := ScanCodes{CheckedAt: c.CheckedAt, Pages: make(map[string][]string, len(c.Pages))}
	for url, codes := range c.Pages {
		kept := []string{}
		for _, code := range codes {
			if !skip.skips(code, url) {
				kept = append(kept, code)
			}
		}
		out.Pages[url] = kept
	}
	return out
}

// Split — страница с неучтёнными находками в `IgnoredIssues`.
func (p Page) Split(skip Skip) Page {
	if skip.Empty() || len(p.Issues) == 0 {
		return p
	}
	kept := []Issue{}
	var ignored []Issue
	for _, issue := range p.Issues {
		if skip.skips(issue.Code, p.URL) {
			ignored = append(ignored, issue)
		} else {
			kept = append(kept, issue)
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	p.Issues, p.IgnoredIssues = kept, ignored
	return p
}

// Without — «что изменилось» без проверок, которые сравнивать нельзя:
// прошлый обход не пересчитан по сегодняшнему списку, и проверка,
// неучтённая на части страниц, выглядела бы «исправленной» (Р7).
func (p *Progress) Without(drop func(code string) bool) *Progress {
	if p == nil || drop == nil {
		return p
	}
	out := &Progress{PreviousAt: p.PreviousAt, Fixed: []string{}, Appeared: []string{}}
	for _, code := range p.Fixed {
		if !drop(code) {
			out.Fixed = append(out.Fixed, code)
		}
	}
	for _, code := range p.Appeared {
		if !drop(code) {
			out.Appeared = append(out.Appeared, code)
		}
	}
	return out
}
