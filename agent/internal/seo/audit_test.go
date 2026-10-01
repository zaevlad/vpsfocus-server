package seo

import (
	"strings"
	"testing"
)

// found — есть ли такая находка и с какой важностью.
func found(page Page, code string) (Issue, bool) {
	for _, issue := range page.Issues {
		if issue.Code == code {
			return issue, true
		}
	}
	return Issue{}, false
}

func codes(page Page) string {
	var list []string
	for _, issue := range page.Issues {
		list = append(list, issue.Code)
	}
	return strings.Join(list, ",")
}

// goodPage — страница, к которой у аудита нет вопросов. От неё отталкиваются
// остальные тесты: меняют одно поле и смотрят, что нашлось.
func goodPage(address string) Page {
	return Page{
		URL:    address,
		Status: 200,
		Structure: &Structure{
			Title:           "Заголовок страницы про товары и услуги",
			MetaDescription: strings.Repeat("описание ", 12),
			Canonical:       address,
			CanonicalSelf:   true,
			Lang:            "ru",
			HasViewport:     true,
			// Свой H1 у каждой страницы и ссылки на соседей: иначе
			// набор эталонов сам давал бы дубли H1 и тупики.
			H1:    []string{"Заголовок страницы " + address},
			Links: LinkCounts{Internal: 5},
			H2:    []string{"Раздел"},
			OpenGraph: map[string]string{
				"title":       "OG",
				"description": "OG",
				"image":       "https://site.test/og.png",
			},
			WordCount: 500,
		},
	}
}

func TestAuditCleanPageHasNoIssues(t *testing.T) {
	pages := []Page{goodPage("https://site.test/")}

	summary := Audit(pages)

	if len(pages[0].Issues) != 0 {
		t.Fatalf("на чистой странице нашлось: %s", codes(pages[0]))
	}
	if summary.Pages != 0 || summary.Critical+summary.Warning+summary.Notice != 0 {
		t.Fatalf("сводка не пуста: %+v", summary)
	}
}

func TestAuditTitleLength(t *testing.T) {
	cases := []struct {
		name  string
		title string
		code  string
	}{
		{"нет вовсе", "", IssueTitleMissing},
		{"слишком короткий", "Товары", IssueTitleShort},
		{"слишком длинный", strings.Repeat("длинный заголовок ", 6), IssueTitleLong},
	}

	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			page := goodPage("https://site.test/")
			page.Structure.Title = item.title
			pages := []Page{page}

			Audit(pages)

			if _, ok := found(pages[0], item.code); !ok {
				t.Fatalf("не найдено %s, найдено: %s", item.code, codes(pages[0]))
			}
		})
	}
}

func TestAuditMissingTitleIsCritical(t *testing.T) {
	// Уровни делятся по одному вопросу — что будет с поисковым трафиком.
	// Без title страница в выдаче не показывается тем, чем является.
	page := goodPage("https://site.test/")
	page.Structure.Title = ""
	pages := []Page{page}

	Audit(pages)

	issue, ok := found(pages[0], IssueTitleMissing)
	if !ok || issue.Severity != SeverityCritical {
		t.Fatalf("важность: %+v", issue)
	}
}

func TestAuditDescriptionAndHeadings(t *testing.T) {
	page := goodPage("https://site.test/")
	page.Structure.MetaDescription = ""
	page.Structure.H1 = []string{"Первый", "Второй"}
	page.Structure.H2 = nil
	page.Structure.H3 = []string{"Третий уровень без второго"}
	pages := []Page{page}

	Audit(pages)

	for _, code := range []string{IssueDescriptionMissing, IssueH1Multiple, IssueHeadingOrder} {
		if _, ok := found(pages[0], code); !ok {
			t.Errorf("не найдено %s, найдено: %s", code, codes(pages[0]))
		}
	}
}

func TestAuditImagesWithoutAltCarryTheCount(t *testing.T) {
	page := goodPage("https://site.test/")
	page.Structure.Images = Images{Total: 10, WithoutAlt: 4}
	pages := []Page{page}

	Audit(pages)

	issue, ok := found(pages[0], IssueImagesWithoutAlt)
	if !ok {
		t.Fatalf("не найдено: %s", codes(pages[0]))
	}
	// Число подставляется в перевод: «картинок без alt: 4» собирает экран,
	// а не агент — интерфейс двуязычный.
	if issue.Detail != "4" {
		t.Fatalf("подробность: %q", issue.Detail)
	}
}

func TestAuditDuplicateTitles(t *testing.T) {
	first := goodPage("https://site.test/a")
	second := goodPage("https://site.test/b")
	second.Structure.Canonical = "https://site.test/b"
	pages := []Page{first, second}

	Audit(pages)

	issue, ok := found(pages[0], IssueTitleDuplicate)
	if !ok {
		t.Fatalf("дубль заголовка не найден: %s", codes(pages[0]))
	}
	// В подробности — адрес двойника: без него человек не найдёт вторую
	// страницу, а пара — это ровно то, что он идёт чинить.
	if issue.Detail != "https://site.test/b" {
		t.Fatalf("двойник: %q", issue.Detail)
	}
	if _, ok := found(pages[1], IssueTitleDuplicate); !ok {
		t.Fatal("вторая страница пары не отмечена")
	}
}

func TestAuditDuplicateSkipsCanonicalizedPages(t *testing.T) {
	// Страница, объявившая себя копией через canonical, повторяет чужой
	// заголовок законно. Придираться к ней значит выдать сотню находок там,
	// где всё сделано правильно.
	original := goodPage("https://site.test/product")
	copy := goodPage("https://site.test/product?color=red")
	copy.Structure.Canonical = "https://site.test/product"
	copy.Structure.CanonicalSelf = false
	pages := []Page{original, copy}

	Audit(pages)

	if _, ok := found(pages[0], IssueTitleDuplicate); ok {
		t.Fatalf("оригинал отмечен дублем: %s", codes(pages[0]))
	}
	if _, ok := found(pages[1], IssueTitleDuplicate); ok {
		t.Fatalf("объявленная копия отмечена дублем: %s", codes(pages[1]))
	}
	// Но сам факт «canonical смотрит на другую страницу» показать надо.
	if _, ok := found(pages[1], IssueCanonicalOther); !ok {
		t.Fatalf("canonical на другую страницу не отмечен: %s", codes(pages[1]))
	}
}

func TestAuditNoindexPagesDoNotMakeDuplicates(t *testing.T) {
	first := goodPage("https://site.test/a")
	second := goodPage("https://site.test/b")
	second.Structure.Noindex = true
	pages := []Page{first, second}

	Audit(pages)

	if _, ok := found(pages[0], IssueTitleDuplicate); ok {
		t.Fatal("страница, закрытая от индексации, посчиталась двойником")
	}
	if _, ok := found(pages[1], IssueNoindex); !ok {
		t.Fatalf("noindex не отмечен: %s", codes(pages[1]))
	}
}

func TestAuditNoindexTellsWhereItCameFrom(t *testing.T) {
	// X-Robots-Tag в разметке не виден вовсе. Не сказать, что запрет пришёл
	// заголовком, значит отправить человека искать в HTML то, чего там нет.
	page := goodPage("https://site.test/")
	page.Structure.Noindex = true
	page.Structure.XRobotsTag = "noindex"
	pages := []Page{page}

	Audit(pages)

	issue, _ := found(pages[0], IssueNoindex)
	if issue.Detail != "header" {
		t.Fatalf("источник запрета: %q", issue.Detail)
	}
}

func TestAuditUnavailablePageGetsOneIssue(t *testing.T) {
	// У пятисотки нет ни title, ни описания, ни h1 — и перечислять это
	// значит утопить настоящую беду в её же следствиях.
	pages := []Page{{URL: "https://site.test/gone", Status: 500, Error: PageErrStatus}}

	Audit(pages)

	if len(pages[0].Issues) != 1 {
		t.Fatalf("находок: %s", codes(pages[0]))
	}
	issue := pages[0].Issues[0]
	if issue.Code != IssuePageUnavailable || issue.Severity != SeverityCritical {
		t.Fatalf("находка: %+v", issue)
	}
	if issue.Detail != "500" {
		t.Fatalf("подробность: %q", issue.Detail)
	}
}

func TestAuditNonHTMLIsNotAudited(t *testing.T) {
	// Придираться к отсутствию title у PDF незачем: это не страница.
	pages := []Page{{URL: "https://site.test/feed", Status: 200, Error: PageErrNotHTML}}

	Audit(pages)

	if len(pages[0].Issues) != 0 {
		t.Fatalf("у не-страницы нашлось: %s", codes(pages[0]))
	}
}

func TestAuditCanonicalOffsiteIsCritical(t *testing.T) {
	// Страница отдаёт свой вес чужому сайту — обычно это шаблон, переехавший
	// с другого домена.
	page := goodPage("https://site.test/")
	page.Structure.Canonical = "https://other.test/"
	page.Structure.CanonicalSelf = false
	pages := []Page{page}

	Audit(pages)

	issue, ok := found(pages[0], IssueCanonicalOffsite)
	if !ok || issue.Severity != SeverityCritical {
		t.Fatalf("находка: %+v, все: %s", issue, codes(pages[0]))
	}
	if _, ok := found(pages[0], IssueCanonicalOther); ok {
		t.Fatal("чужой canonical посчитан и своим, и чужим — находка задвоилась")
	}
}

func TestAuditDeclaredDuplicateIsNotFlagged(t *testing.T) {
	// Копия по содержимому, у которой есть canonical на оригинал, —
	// нормально настроенная страница, а не находка.
	declared := goodPage("https://site.test/product?utm=1")
	declared.DuplicateOf = "https://site.test/product"
	declared.Structure.Canonical = "https://site.test/product"
	declared.Structure.CanonicalSelf = false

	silent := goodPage("https://site.test/copy")
	silent.DuplicateOf = "https://site.test/original"

	pages := []Page{declared, silent}
	Audit(pages)

	if _, ok := found(pages[0], IssueDuplicateContent); ok {
		t.Fatal("объявленная копия отмечена как беда")
	}
	issue, ok := found(pages[1], IssueDuplicateContent)
	if !ok || issue.Severity != SeverityCritical {
		t.Fatalf("необъявленная копия: %+v", issue)
	}
}

func TestAuditRedirectChain(t *testing.T) {
	single := goodPage("https://site.test/one")
	single.Redirects = []string{"https://site.test/final"}
	single.FinalURL = "https://site.test/final"

	chain := goodPage("https://site.test/two")
	chain.Redirects = []string{"https://site.test/mid", "https://site.test/final"}
	chain.FinalURL = "https://site.test/final"

	pages := []Page{single, chain}
	Audit(pages)

	if _, ok := found(pages[0], IssueRedirected); !ok {
		t.Fatalf("одиночное перенаправление: %s", codes(pages[0]))
	}
	issue, ok := found(pages[1], IssueRedirectChain)
	if !ok || issue.Severity != SeverityWarning {
		t.Fatalf("цепочка: %+v, все: %s", issue, codes(pages[1]))
	}
}

func TestAuditOpenGraphMissingEntirely(t *testing.T) {
	// Открытый граф ставят целиком или не ставят никак — это одна находка,
	// а не три.
	page := goodPage("https://site.test/")
	page.Structure.OpenGraph = nil
	pages := []Page{page}

	Audit(pages)

	issue, ok := found(pages[0], IssueOpenGraphIncomplete)
	if !ok || issue.Detail != "og" {
		t.Fatalf("находка: %+v", issue)
	}

	partial := goodPage("https://site.test/x")
	delete(partial.Structure.OpenGraph, "image")
	half := []Page{partial}

	Audit(half)

	issue, _ = found(half[0], IssueOpenGraphIncomplete)
	if issue.Detail != "image" {
		t.Fatalf("чего не хватает: %q", issue.Detail)
	}
}

func TestAuditSummaryCountsPagesAndCodes(t *testing.T) {
	// «Нет описания на двухстах страницах» — это одна правка шаблона, а не
	// двести правок. Разрез по кодам нужен ровно для этого.
	first := goodPage("https://site.test/a")
	first.Structure.Title = "Заголовок первой страницы про товары"
	first.Structure.MetaDescription = ""
	second := goodPage("https://site.test/b")
	second.Structure.MetaDescription = ""
	second.Structure.Title = ""
	// У чистой страницы свой заголовок: иначе она стала бы двойником
	// первой, и находка была бы у всех трёх.
	clean := goodPage("https://site.test/c")
	clean.Structure.Title = "Заголовок третьей страницы про услуги"

	pages := []Page{first, second, clean}
	summary := Audit(pages)

	if summary.Pages != 2 {
		t.Fatalf("страниц с находками: %d", summary.Pages)
	}
	if summary.PagesWithCode(IssueDescriptionMissing) != 2 {
		t.Fatalf("описаний не хватает на %d страницах", summary.PagesWithCode(IssueDescriptionMissing))
	}
	if summary.Critical != 1 {
		t.Fatalf("критичных: %d", summary.Critical)
	}
	// Заголовки у всех разные — дублей быть не должно.
	if summary.PagesWithCode(IssueTitleDuplicate) != 0 {
		t.Fatalf("дубли заголовков: %d", summary.PagesWithCode(IssueTitleDuplicate))
	}
}

func TestAuditThinContent(t *testing.T) {
	page := goodPage("https://site.test/")
	page.Structure.WordCount = 40
	pages := []Page{page}

	Audit(pages)

	issue, ok := found(pages[0], IssueThinContent)
	if !ok || issue.Detail != "40" {
		t.Fatalf("находка: %+v, все: %s", issue, codes(pages[0]))
	}
}

func TestAuditTwinsListIsCapped(t *testing.T) {
	// У каталога с одинаковым описанием двойников бывает пятьсот, и список
	// из пятисот адресов в каждой строке — это мегабайты на чужом диске.
	var pages []Page
	for _, suffix := range []string{"a", "b", "c", "d", "e", "f"} {
		pages = append(pages, goodPage("https://site.test/"+suffix))
	}

	Audit(pages)

	issue, ok := found(pages[0], IssueTitleDuplicate)
	if !ok {
		t.Fatal("дубль не найден")
	}
	if !strings.Contains(issue.Detail, "+2") {
		t.Fatalf("список двойников не урезан: %q", issue.Detail)
	}
	if len(strings.Fields(issue.Detail)) != 4 {
		t.Fatalf("в списке лишнее: %q", issue.Detail)
	}
}
