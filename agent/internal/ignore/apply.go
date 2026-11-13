package ignore

import (
	"agent/internal/links"
	"agent/internal/seo"
)

// Links — отчёт о ссылках без неучтённого (Р6, Р17).
//
// Битая ссылка со всех неучтённых страниц-источников уходит в
// `IgnoredFailures`; с части — делится: учтённые источники остаются в
// `Failures`, неучтённые — в `IgnoredFailures`. `Broken` — число учтённых
// ссылок, `Ignored` — неучтённых.
func Links(report links.Report, list List) links.Report {
	report.IgnoredFailures = []links.Failure{}
	entries := list.For(report.Domain, KindLink)
	if len(entries) == 0 {
		report.Ignored = 0
		return report
	}

	kept, ignored := SplitFailures(report.Failures, entries)
	report.Failures, report.IgnoredFailures = kept, ignored
	report.Broken, report.Ignored = len(kept), len(ignored)
	return report
}

// SplitFailures делит битые ссылки по записям одного сайта.
func SplitFailures(failures []links.Failure, entries List) (kept, ignored []links.Failure) {
	kept, ignored = []links.Failure{}, []links.Failure{}
	for _, failure := range failures {
		response := Response(failure.Kind, failure.Code)
		var keptSources, ignoredSources []string
		sources := failure.Sources
		if len(sources) == 0 {
			// Без источника — только запись «на всём сайте».
			sources = []string{""}
		}
		for _, source := range sources {
			if entries.coversLink(failure.URL, response, source) {
				ignoredSources = append(ignoredSources, source)
			} else {
				keptSources = append(keptSources, source)
			}
		}
		if len(keptSources) > 0 {
			part := failure
			part.Sources = withoutBlank(keptSources)
			kept = append(kept, part)
		}
		if len(ignoredSources) > 0 {
			part := failure
			part.Sources = withoutBlank(ignoredSources)
			ignored = append(ignored, part)
		}
	}
	return kept, ignored
}

func withoutBlank(sources []string) []string {
	out := []string{}
	for _, source := range sources {
		if source != "" {
			out = append(out, source)
		}
	}
	return out
}

func (l List) coversLink(target, response, source string) bool {
	for _, entry := range l {
		if entry.CoversLink(target, response, source) {
			return true
		}
	}
	return false
}

// Seo — пропуски для обхода одного сайта.
func Seo(list List, domain string) seo.Skip {
	entries := list.For(domain, KindSeo)
	if len(entries) == 0 {
		return seo.Skip{}
	}

	whole := map[string]bool{}
	var partial List
	for _, entry := range entries {
		if entry.Whole() {
			whole[entry.What] = true
		} else {
			partial = append(partial, entry)
		}
	}
	return seo.Skip{
		Whole: func(code string) bool { return whole[code] },
		Finding: func(code, url string) bool {
			for _, entry := range partial {
				if entry.CoversSeo(code, url) {
					return true
				}
			}
			return false
		},
		PageScoped: len(partial) > 0,
	}
}

// SeoCodes — коды проверок, которых касается хоть одна запись сайта.
func SeoCodes(list List, domain string) func(string) bool {
	touched := map[string]bool{}
	for _, entry := range list.For(domain, KindSeo) {
		touched[entry.What] = true
	}
	if len(touched) == 0 {
		return nil
	}
	return func(code string) bool { return touched[code] }
}

// Advice — не учитывать ли совет Lighthouse `id` на странице `page` сайта
// (Р23): одна запись на обе стратегии. nil — записей у сайта нет.
func Advice(list List, domain string) func(id, page string) bool {
	entries := list.For(domain, KindAdvice)
	if len(entries) == 0 {
		return nil
	}
	return func(id, page string) bool {
		for _, entry := range entries {
			if entry.CoversAdvice(id, page) {
				return true
			}
		}
		return false
	}
}
