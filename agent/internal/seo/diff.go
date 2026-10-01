package seo

import "sort"

// Сравнение двух обходов: что появилось и что исправилось.
//
// Ради этого и хранятся полные снимки двух последних обходов. Один обход
// отвечает на вопрос «что сейчас», а работу агентства видно только на паре:
// «было двести страниц без описания, стало десять» — это отчёт клиенту, а
// «двести страниц без описания» — просто список.

// Сколько адресов показываем примером. Список из пятисот адресов никто не
// читает, а три отвечают на вопрос «где это?» не хуже.
const diffExamples = 3

// ScanCodes — находки одного обхода: по адресу страницы её коды.
//
// Только коды: сравнивать подробности незачем — «title длиной 61» и «title
// длиной 64» это одна и та же находка, и показывать её как исправленную и
// тут же появившуюся было бы обманом.
type ScanCodes struct {
	CheckedAt string
	Pages     map[string][]string
}

// DiffEntry — одна находка в сравнении.
type DiffEntry struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Count    int      `json:"count"`
	Pages    []string `json:"pages,omitempty"`
}

// Diff — что изменилось между обходами.
type Diff struct {
	Domain string `json:"domain"`
	// Когда были обходы. Пустой PreviousAt означает, что сравнивать не с
	// чем: первый обход сайта или единственный сохранившийся.
	PreviousAt string `json:"previousAt,omitempty"`
	CurrentAt  string `json:"currentAt,omitempty"`
	// Находки, которых в прошлый раз на этой странице не было.
	Appeared []DiffEntry `json:"appeared"`
	// Находки, которые были и пропали. Только по страницам, которые есть в
	// обоих обходах: страница, которой не стало, не «исправлена».
	Fixed []DiffEntry `json:"fixed"`
	// Страницы, появившиеся и пропавшие с прошлого обхода.
	NewPages   []string `json:"newPages,omitempty"`
	GonePages  []string `json:"gonePages,omitempty"`
	NewCount   int      `json:"newCount"`
	GoneCount  int      `json:"goneCount"`
	SamePages  int      `json:"samePages"`
	Comparable bool     `json:"comparable"`
}

// Compare сравнивает два обхода одного сайта.
//
// Появившимся считается код, которого на этой странице в прошлый раз не
// было, — включая коды новых страниц: находка на новой странице всё равно
// новая находка сайта. А вот исправленным — только код, пропавший со
// страницы, которая осталась: у снесённой страницы «исправлять» было нечего,
// и записывать её удаление в достижения нельзя.
func Compare(domain string, current, previous ScanCodes) Diff {
	diff := Diff{
		Domain:     domain,
		CurrentAt:  current.CheckedAt,
		PreviousAt: previous.CheckedAt,
		Appeared:   []DiffEntry{},
		Fixed:      []DiffEntry{},
		Comparable: previous.CheckedAt != "" && current.CheckedAt != "",
	}
	if !diff.Comparable {
		return diff
	}

	appeared := map[string][]string{}
	fixed := map[string][]string{}

	for url, codes := range current.Pages {
		before, existed := previous.Pages[url]
		if !existed {
			diff.NewCount++
			// Все адреса, а не первые три: обход карты в Go случаен, и
			// «первые три» менялись от запуска к запуску на одних и тех же
			// данных. Отбор примеров — после сортировки, ниже.
			diff.NewPages = append(diff.NewPages, url)
		} else {
			diff.SamePages++
		}

		had := set(before)
		for _, code := range codes {
			if !had[code] {
				appeared[code] = append(appeared[code], url)
			}
		}
	}

	for url, codes := range previous.Pages {
		now, alive := current.Pages[url]
		if !alive {
			diff.GoneCount++
			diff.GonePages = append(diff.GonePages, url)
			continue
		}

		has := set(now)
		for _, code := range codes {
			if !has[code] {
				fixed[code] = append(fixed[code], url)
			}
		}
	}

	diff.Appeared = entries(appeared)
	diff.Fixed = entries(fixed)
	// Сначала порядок, потом отбор: примеры обязаны быть одними и теми же
	// при одних и тех же данных, иначе экран показывает разное на каждом
	// открытии.
	sort.Strings(diff.NewPages)
	sort.Strings(diff.GonePages)
	diff.NewPages = firstFew(diff.NewPages)
	diff.GonePages = firstFew(diff.GonePages)
	return diff
}

// firstFew оставляет первые diffExamples адресов. Примеры, а не список:
// пропавших страниц бывает пятьсот, и показывать их все на экране незачем.
func firstFew(pages []string) []string {
	if len(pages) <= diffExamples {
		return pages
	}
	return pages[:diffExamples]
}

// entries раскладывает карту «код — страницы» в упорядоченный список.
//
// Порядок — по важности, потом по числу страниц: список, который читают
// сверху вниз, обязан начинаться с того, что чинят первым.
func entries(source map[string][]string) []DiffEntry {
	result := make([]DiffEntry, 0, len(source))
	for code, pages := range source {
		sort.Strings(pages)
		examples := pages
		if len(examples) > diffExamples {
			examples = examples[:diffExamples]
		}
		result = append(result, DiffEntry{
			Code:     code,
			Severity: SeverityOf(code),
			Count:    len(pages),
			Pages:    append([]string{}, examples...),
		})
	}

	sort.Slice(result, func(left, right int) bool {
		first, second := result[left], result[right]
		if first.Severity != second.Severity {
			return severityRank(first.Severity) < severityRank(second.Severity)
		}
		if first.Count != second.Count {
			return first.Count > second.Count
		}
		return first.Code < second.Code
	})
	return result
}

func severityRank(severity string) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

func set(items []string) map[string]bool {
	result := make(map[string]bool, len(items))
	for _, item := range items {
		result[item] = true
	}
	return result
}

// KnownCode пропускает только те коды находок, которые мы сами и выдаём.
//
// Отбор, а не экранирование: код приезжает из нашего же интерфейса, и всё,
// что не из этого списка, — не опечатка пользователя, а чужая строка в
// запросе к базе.
func KnownCode(code string) string {
	if _, ok := severityByCode[code]; ok {
		return code
	}
	return ""
}

// KnownSeverity — то же самое для уровня важности.
//
// Незнакомый уровень AtLeast молча отбрасывает, и отбора не происходит.
// Ответ, вернувший уровень как пришёл, утверждал бы обратное — что страницы
// отобраны по нему.
func KnownSeverity(severity string) string {
	if len(AtLeast(severity)) == 0 {
		return ""
	}
	return severity
}
