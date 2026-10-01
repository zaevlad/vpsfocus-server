package robots

import (
	"testing"
	"time"
)

// Имя бота в тестах. Настоящих имён два — у SEO-обхода и у обхода для
// проверки ссылок, — и разбор про них ничего не знает: имя приходит
// параметром.
const testAgent = "vpsfocus-seo"

func TestParseRobotsNamedGroupWins(t *testing.T) {
	// Группа, адресованная нам по имени, вытесняет группу со звёздочкой
	// целиком: так же поступают поисковики.
	body := []byte(`
User-agent: *
Disallow: /

User-agent: vpsfocus-seo
Disallow: /admin/
Crawl-delay: 2

Sitemap: https://example.com/sitemap.xml
`)

	robots := Parse(body, testAgent)

	if !robots.Allowed("/catalog/") {
		t.Fatal("правило со звёздочкой применилось при наличии именной группы")
	}
	if robots.Allowed("/admin/users") {
		t.Fatal("именной запрет не применился")
	}
	if robots.Delay != 2*time.Second {
		t.Fatalf("crawl-delay: получено %v", robots.Delay)
	}
	if len(robots.Sitemaps) != 1 || robots.Sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Fatalf("карта сайта: получено %v", robots.Sitemaps)
	}
}

func TestParseRobotsEmptyDisallowAllowsEverything(t *testing.T) {
	// Пустой Disallow означает «разрешено всё», а не «запрещено всё».
	// Путают это постоянно, и цена ошибки — обход, которого не было.
	robots := Parse([]byte("User-agent: *\nDisallow:\n"), testAgent)

	if !robots.Allowed("/") || !robots.Allowed("/anything") {
		t.Fatal("пустой Disallow прочитан как запрет")
	}
}

func TestRobotsLongestRuleWins(t *testing.T) {
	robots := Parse([]byte(`
User-agent: *
Disallow: /catalog/
Allow: /catalog/public/
`), testAgent)

	if robots.Allowed("/catalog/secret") {
		t.Fatal("общий запрет не сработал")
	}
	if !robots.Allowed("/catalog/public/item") {
		t.Fatal("более длинное разрешение не победило запрет")
	}
}

func TestRobotsWildcards(t *testing.T) {
	robots := Parse([]byte(`
User-agent: *
Disallow: /*?sort=
Disallow: /*.pdf$
`), testAgent)

	cases := []struct {
		path    string
		allowed bool
	}{
		{"/catalog/", true},
		{"/catalog/?sort=price", false},
		{"/files/report.pdf", false},
		// `$` привязывает шаблон к концу пути: адрес продолжается — правило
		// не про него.
		{"/files/report.pdf.html", true},
	}

	for _, item := range cases {
		if got := robots.Allowed(item.path); got != item.allowed {
			t.Errorf("%s: получено %v, ожидалось %v", item.path, got, item.allowed)
		}
	}
}

func TestRobotsCrawlDelayClamped(t *testing.T) {
	// Обход, который не заканчивается, не отличим от сломанного.
	robots := Parse([]byte("User-agent: *\nCrawl-delay: 3600\n"), testAgent)

	if robots.Delay != maxCrawlDelay {
		t.Fatalf("пауза не урезана: %v", robots.Delay)
	}
}

func TestRobotsSeveralNamedGroupsAdd(t *testing.T) {
	// Две группы с одним и тем же именем складываются, а не вытесняют
	// одна другую.
	robots := Parse([]byte(`
User-agent: vpsfocus-seo
Disallow: /a/

User-agent: vpsfocus-seo
Disallow: /b/
`), testAgent)

	if robots.Allowed("/a/x") || robots.Allowed("/b/x") {
		t.Fatal("одна из именных групп потеряна")
	}
}

func TestRobotsCommentsAndJunkIgnored(t *testing.T) {
	// Кривой файл не должен ни ронять агента, ни закрывать сайт целиком.
	robots := Parse([]byte(`
# комментарий
мусор без двоеточия
User-agent: *   # и тут комментарий
Disallow: /private/
`), testAgent)

	if robots.Allowed("/private/x") {
		t.Fatal("правило после мусора потеряно")
	}
	if !robots.Allowed("/") {
		t.Fatal("мусор прочитан как запрет")
	}
}

func TestRobotsNoRulesAllowsAll(t *testing.T) {
	if !AllowAll().Allowed("/anything") {
		t.Fatal("отсутствие robots.txt должно разрешать всё")
	}
}

// Crawl-delay — директива группы, а не файла: чужая просьба к чужому боту
// нас не касается. Пауза в тридцать секунд из группы SemrushBot растягивала
// наш обход на пятьсот страниц далеко за пределы отведённых ему трёх часов.
func TestRobotsCrawlDelayComesFromOurGroup(t *testing.T) {
	robots := Parse([]byte(`
User-agent: SemrushBot
Crawl-delay: 30

User-agent: *
Crawl-delay: 1
`), testAgent)

	if robots.Delay != time.Second {
		t.Fatalf("пауза %v, ожидали секунду из адресованной нам группы", robots.Delay)
	}
}

// А когда группа для нас есть, она вытесняет звёздочку целиком — вместе с
// паузой, как и вместе с правилами.
func TestRobotsNamedGroupDelayWins(t *testing.T) {
	robots := Parse([]byte(`
User-agent: *
Crawl-delay: 10

User-agent: vpsfocus-seo
Disallow: /admin/
`), testAgent)

	if robots.Delay != 0 {
		t.Fatalf("пауза %v: взята из вытесненной группы", robots.Delay)
	}
}

// Хвост с якорем ищется с конца пути. Жадный поиск слева направо находил
// первое вхождение, до конца оно не дотягивало, и запрет молча переставал
// действовать — то есть мы шли туда, куда запрещено.
func TestRobotsAnchoredTailSearchesFromTheEnd(t *testing.T) {
	robots := Parse([]byte(`
User-agent: *
Disallow: /*.pdf$
Disallow: /*x$
`), testAgent)

	cases := map[string]bool{
		"/files/report.pdf":      false,
		"/files/a.pdf.pdf":       false,
		"/files/report.pdf.html": true,
		"/xax":                   false,
		"/xa":                    true,
	}

	for path, allowed := range cases {
		if got := robots.Allowed(path); got != allowed {
			t.Errorf("%s: получено %v, ожидалось %v", path, got, allowed)
		}
	}
}

func TestAllowedForAsksTheSameFileAboutAnotherBot(t *testing.T) {
	// SEO-аудит спрашивает прочитанный файл о Google и ИИ-ботах. Разбор тот
	// же, что и для нас: именная группа вытесняет звёздочку, частичный
	// запрет не закрывает главную.
	robots := Parse([]byte(`
User-agent: *
Disallow: /admin/

User-agent: GPTBot
Disallow: /

User-agent: Googlebot
Disallow: /search
`), testAgent)

	if robots.AllowedFor("GPTBot", "/") {
		t.Fatal("GPTBot закрыт целиком, а главная ему разрешена")
	}
	if !robots.AllowedFor("Googlebot", "/") {
		t.Fatal("у Googlebot закрыт только поиск, а главная запрещена")
	}
	if robots.AllowedFor("Googlebot", "/search?q=1") {
		t.Fatal("именная группа Googlebot не применилась")
	}
	if !robots.AllowedFor("Googlebot", "/admin/") {
		t.Fatal("звёздочка применилась к боту со своей группой")
	}
	if !robots.AllowedFor("ClaudeBot", "/") || robots.AllowedFor("ClaudeBot", "/admin/x") {
		t.Fatal("бот без своей группы живёт по звёздочке")
	}
	// Наш собственный разбор от вопросов о чужих ботах не меняется.
	if robots.Allowed("/admin/x") || !robots.Allowed("/") {
		t.Fatal("правила для нас испорчены")
	}
}

func TestAllowedForWithoutFileAllowsEveryone(t *testing.T) {
	// Файла нет — разрешено всем, а не только нам.
	if !AllowAll().AllowedFor("Googlebot", "/") {
		t.Fatal("отсутствие robots.txt прочитано как запрет для Google")
	}
	var missing *Rules
	if !missing.AllowedFor("Googlebot", "/") {
		t.Fatal("пустые правила прочитаны как запрет")
	}
}
