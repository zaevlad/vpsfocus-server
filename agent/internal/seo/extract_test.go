package seo

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// parsePage — разбор куска разметки так, как это делает обход.
func parsePage(t *testing.T, address, markup, xRobotsTag string) parsed {
	t.Helper()

	base, err := url.Parse(address)
	if err != nil {
		t.Fatalf("адрес: %v", err)
	}
	document, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	return extract(document, base, xRobotsTag)
}

const samplePage = `<!doctype html>
<html lang="ru">
<head>
  <title>  Магазин —
     каталог  </title>
  <meta name="description" content="Описание страницы">
  <meta name="viewport" content="width=device-width">
  <meta property="og:title" content="OG заголовок">
  <meta property="og:image" content="https://example.com/first.png">
  <meta property="og:image" content="https://example.com/second.png">
  <link rel="canonical" href="/catalog/">
</head>
<body>
  <h1>Каталог</h1>
  <h2>Раздел</h2>
  <h3>Подраздел</h3>
  <p>Пять слов в этом тексте</p>
  <img src="/a.png" alt="есть">
  <img src="/b.png" alt="">
  <img src="/c.png">
  <a href="/catalog/page">внутренняя</a>
  <a href="/catalog/page#anchor">она же с якорем</a>
  <a href="https://other.example/x">внешняя</a>
  <a href="/sponsored" rel="nofollow noopener">не ходить</a>
  <a href="mailto:hi@example.com">почта</a>
  <script>var words = "не считать это";</script>
</body>
</html>`

func TestExtractStructure(t *testing.T) {
	result := parsePage(t, "https://example.com/catalog/", samplePage, "")
	structure := result.structure

	if structure.Title != "Магазин — каталог" {
		t.Errorf("title: %q", structure.Title)
	}
	if structure.MetaDescription != "Описание страницы" {
		t.Errorf("description: %q", structure.MetaDescription)
	}
	if structure.Lang != "ru" {
		t.Errorf("lang: %q", structure.Lang)
	}
	if !structure.HasViewport {
		t.Error("viewport не найден")
	}
	if structure.Canonical != "https://example.com/catalog/" {
		t.Errorf("canonical не приведён к абсолютному: %q", structure.Canonical)
	}
	if !structure.CanonicalSelf {
		t.Error("canonical на саму страницу не распознан")
	}
	if len(structure.H1) != 1 || structure.H1[0] != "Каталог" {
		t.Errorf("h1: %v", structure.H1)
	}
	if len(structure.H2) != 1 || len(structure.H3) != 1 {
		t.Errorf("h2/h3: %v %v", structure.H2, structure.H3)
	}
	// Первое значение og:image выигрывает: соцсети берут первую картинку.
	if structure.OpenGraph["image"] != "https://example.com/first.png" {
		t.Errorf("og:image: %q", structure.OpenGraph["image"])
	}
	if structure.OpenGraph["title"] != "OG заголовок" {
		t.Errorf("og:title: %q", structure.OpenGraph["title"])
	}
}

func TestExtractImagesAltPresentButEmpty(t *testing.T) {
	// Пустой alt — законная разметка декоративной картинки. Отсутствующий
	// alt — находка аудита. Считать их одинаково нельзя.
	structure := parsePage(t, "https://example.com/catalog/", samplePage, "").structure

	if structure.Images.Total != 3 {
		t.Fatalf("картинок: %d", structure.Images.Total)
	}
	if structure.Images.WithoutAlt != 1 {
		t.Fatalf("без alt: %d", structure.Images.WithoutAlt)
	}
	if len(structure.Images.Samples) != 1 || structure.Images.Samples[0] != "https://example.com/c.png" {
		t.Fatalf("примеры: %v", structure.Images.Samples)
	}
}

func TestExtractLinksAndFollow(t *testing.T) {
	result := parsePage(t, "https://example.com/catalog/", samplePage, "")

	// Внутренние: две ссылки на одну страницу (с якорем и без) и nofollow.
	// mailto: не считается ни внутренней, ни внешней — это не страница.
	if result.structure.Links.Internal != 3 {
		t.Errorf("внутренних: %d", result.structure.Links.Internal)
	}
	if result.structure.Links.External != 1 {
		t.Errorf("внешних: %d", result.structure.Links.External)
	}
	if result.structure.Links.Nofollow != 1 {
		t.Errorf("nofollow: %d", result.structure.Links.Nofollow)
	}

	// Якорь не создаёт второй страницы, nofollow и mailto в обход не идут.
	if len(result.follow) != 1 || result.follow[0] != "https://example.com/catalog/page" {
		t.Fatalf("к обходу: %v", result.follow)
	}
}

func TestExtractWordCountSkipsScripts(t *testing.T) {
	structure := parsePage(t, "https://example.com/catalog/", samplePage, "").structure

	// Считается видимый текст: заголовки, абзац и тексты ссылок. Скрипт —
	// нет: страница с килобайтом JSON-LD не становится от него полной.
	if structure.WordCount < 10 || structure.WordCount > 25 {
		t.Fatalf("слов насчитано %d — похоже, посчитан скрипт или потерян текст", structure.WordCount)
	}
	if strings.Contains(strings.Join(structure.H1, " "), "не считать") {
		t.Fatal("текст скрипта попал в заголовки")
	}
}

func TestExtractNoindexFromHeader(t *testing.T) {
	// X-Robots-Tag в разметке не виден вовсе, и не заметить его проще
	// всего — поэтому он читается наравне с мета-тегом.
	result := parsePage(t, "https://example.com/x", "<html><body>текст</body></html>", "noindex, nofollow")

	if !result.structure.Noindex || !result.structure.Nofollow {
		t.Fatalf("заголовок не прочитан: %+v", result.structure)
	}
}

func TestExtractNofollowPageStopsCrawl(t *testing.T) {
	markup := `<html><head><meta name="robots" content="nofollow"></head>
	<body><a href="/next">дальше</a></body></html>`

	result := parsePage(t, "https://example.com/", markup, "")

	if !result.structure.Nofollow {
		t.Fatal("nofollow страницы не распознан")
	}
	if len(result.follow) != 0 {
		t.Fatalf("со страницы с nofollow пошли дальше: %v", result.follow)
	}
	// Ссылку при этом посчитали: она на странице есть.
	if result.structure.Links.Internal != 1 {
		t.Fatalf("внутренних: %d", result.structure.Links.Internal)
	}
}

func TestExtractBaseHref(t *testing.T) {
	// <base href> меняет основу для всех относительных адресов и может
	// стоять ниже первой ссылки — поэтому ищется отдельным проходом.
	markup := `<html><head><base href="https://example.com/shop/"></head>
	<body><a href="item">товар</a></body></html>`

	result := parsePage(t, "https://example.com/old/page", markup, "")

	if len(result.follow) != 1 || result.follow[0] != "https://example.com/shop/item" {
		t.Fatalf("base href не учтён: %v", result.follow)
	}
}

func TestExtractGooglebotOverridesRobots(t *testing.T) {
	// Оба порядка тегов, а не только удачный. Пока указания писали в одно
	// поле по ходу обхода дерева, побеждал тот, что встретился позже, — и
	// страница с googlebot-noindex перед robots-all показывалась открытой
	// для индексации, хотя Google её не проиндексирует.
	orders := map[string]string{
		"robots первым": `<meta name="robots" content="index">` +
			`<meta name="googlebot" content="noindex">`,
		"googlebot первым": `<meta name="googlebot" content="noindex">` +
			`<meta name="robots" content="all">`,
	}

	for name, head := range orders {
		markup := `<html><head>` + head + `</head><body></body></html>`
		structure := parsePage(t, "https://example.com/", markup, "").structure

		if !structure.Noindex {
			t.Errorf("%s: указание googlebot проигнорировано", name)
		}
	}
}

// <base href> на чужой хост не делает чужие ссылки внутренними и не
// превращает canonical на саму себя в находку canonicalOther.
func TestExtractBaseHrefDoesNotChangeWhatIsOurs(t *testing.T) {
	markup := `<html><head>` +
		`<base href="https://cdn.partner.net/">` +
		`<link rel="canonical" href="https://example.com/page">` +
		`</head><body><a href="/other">туда</a></body></html>`

	result := parsePage(t, "https://example.com/page", markup, "")

	if !result.structure.CanonicalSelf {
		t.Error("canonical на саму себя принят за чужой: сверялись с base href")
	}
	if result.structure.Links.Internal != 0 {
		t.Errorf("ссылка на чужой домен посчитана внутренней: %+v", result.structure.Links)
	}
	if len(result.follow) != 0 {
		t.Errorf("краулеру предложены чужие адреса: %v", result.follow)
	}
}

func TestExtractCanonicalElsewhere(t *testing.T) {
	markup := `<html><head><link rel="canonical" href="https://example.com/other"></head><body></body></html>`

	structure := parsePage(t, "https://example.com/page", markup, "").structure

	if structure.Canonical != "https://example.com/other" {
		t.Fatalf("canonical: %q", structure.Canonical)
	}
	if structure.CanonicalSelf {
		t.Fatal("canonical на другую страницу принят за собственный")
	}
}
