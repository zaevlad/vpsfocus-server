package seo

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"agent/internal/text"
)

// Извлечение структуры страницы.
//
// Разбор деревом (`html.Parse`), а не потоком токенов, как в проверке
// ссылок: там нужны были только адреса из href, здесь — текст заголовков и
// видимый текст страницы, а его без дерева не собрать. Битую вёрстку
// `html.Parse` чинит по тем же правилам, что и браузер, — значит, мы видим
// страницу так же, как её увидит поисковик.

// Пределы того, что кладём в базу.
//
// Обрезка щедрая: настоящий title короче двух сотен символов, и «слишком
// длинный» на тысяче знаков остаётся слишком длинным. Она защищает не от
// длинного заголовка, а от страницы, у которой в title лежит роман.
const (
	maxTextField    = 1000
	maxHeadingText  = 300
	maxHeadings     = 10
	maxImageSamples = 5
)

// parsed — то, что снято со страницы: структура для хранения и ссылки для
// обхода. Ссылки в структуру не попадают: их сотни на страницу, а нужны они
// только краулеру и ровно один раз.
type parsed struct {
	structure Structure
	// Внутренние ссылки, по которым можно идти дальше. Без rel=nofollow:
	// поисковик по ним не ходит, и обход, который ходит, показывал бы не
	// тот сайт, который видит поисковик.
	follow []string
	// Что сказано конкретно нашему брату-боту. Копится отдельно от общего
	// `robots` и применяется после обхода дерева: указание боту сильнее
	// общего всегда, а не только когда оно встретилось ниже в разметке.
	metaGooglebot string
}

// extract разбирает страницу. page — адрес самой страницы; он же основа для
// относительных ссылок, пока в разметке не встретился <base href>.
func extract(doc *html.Node, page *url.URL, xRobotsTag string) parsed {
	result := parsed{}
	result.structure.XRobotsTag = strings.TrimSpace(xRobotsTag)

	// <base href> меняет основу для всех относительных адресов страницы, и
	// стоять он может ниже первой ссылки. Поэтому обход двухпроходный:
	// сначала ищем base, потом всё остальное.
	//
	// Адрес самой страницы при этом сохраняется отдельно: `<base href>`
	// пишет владелец страницы, и указывать он может куда угодно, включая
	// чужой домен. Своим считается то, что совпадает с хостом страницы, —
	// иначе `<base href="https://cdn.partner.net/">` объявлял бы внутренними
	// ссылки на чужой сайт, а canonical на саму себя — находкой
	// `canonicalOther`.
	base := page
	if href, ok := findBase(doc); ok {
		if resolved, err := page.Parse(href); err == nil {
			base = resolved
		}
	}

	var (
		seenLinks = map[string]bool{}
		words     int
	)

	var walk func(node *html.Node, skipText bool)
	walk = func(node *html.Node, skipText bool) {
		switch node.Type {
		case html.TextNode:
			if !skipText {
				words += countWords(node.Data)
			}

		case html.ElementNode:
			switch node.DataAtom {
			case atom.Script, atom.Style, atom.Noscript, atom.Template:
				// Текст внутрь не считаем: посетитель его не видит, а
				// «тонкая страница» с килобайтом JSON-LD выглядела бы
				// полной.
				skipText = true

			case atom.Html:
				if lang := attribute(node, "lang"); lang != "" {
					result.structure.Lang = trimField(lang, maxTextField)
				}

			case atom.Title:
				if result.structure.Title == "" {
					result.structure.Title = trimField(textOf(node), maxTextField)
				}

			case atom.Meta:
				readMeta(node, &result)

			case atom.Link:
				readLink(node, base, &result.structure)

			case atom.H1, atom.H2, atom.H3:
				readHeading(node, &result.structure)

			case atom.Img:
				readImage(node, base, &result.structure)

			case atom.A:
				readAnchor(node, base, page, &result.structure, seenLinks, &result.follow)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skipText)
		}
	}
	walk(doc, false)

	result.structure.WordCount = words
	// Указание конкретному боту сильнее общего — независимо от того, какой
	// из мета-тегов встретился в разметке позже. Пока оба писали в одно
	// поле по ходу обхода, `<meta name="googlebot" content="noindex">` перед
	// `<meta name="robots" content="all">` терялся, и страница показывалась
	// открытой для индексации, хотя Google её не проиндексирует.
	if result.metaGooglebot != "" {
		result.structure.MetaRobots = result.metaGooglebot
	}
	applyRobotsDirectives(&result.structure)
	// Страница, закрытая от перехода по ссылкам, — тупик по своей же
	// просьбе. Уважаем её так же, как robots.txt.
	if result.structure.Nofollow {
		result.follow = nil
	}
	result.structure.CanonicalSelf = canonicalPointsHere(result.structure.Canonical, page)

	return result
}

// findBase ищет <base href> до основного обхода.
func findBase(node *html.Node) (string, bool) {
	if node.Type == html.ElementNode && node.DataAtom == atom.Base {
		if href := attribute(node, "href"); href != "" {
			return href, true
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if href, ok := findBase(child); ok {
			return href, true
		}
	}
	return "", false
}

func readMeta(node *html.Node, result *parsed) {
	structure := &result.structure

	content := strings.TrimSpace(attribute(node, "content"))
	if content == "" {
		return
	}

	// Open Graph живёт в property, обычные мета-теги — в name. Сайты
	// путают одно с другим постоянно, поэтому смотрим оба.
	name := strings.ToLower(strings.TrimSpace(attribute(node, "name")))
	property := strings.ToLower(strings.TrimSpace(attribute(node, "property")))
	if name == "" {
		name = property
	}

	switch name {
	case "description":
		if structure.MetaDescription == "" {
			structure.MetaDescription = trimField(content, maxTextField)
		}
	case "robots":
		structure.MetaRobots = trimField(content, maxTextField)
	case "viewport":
		structure.HasViewport = true
	}

	// Указание конкретному боту сильнее общего: Google читает `googlebot`
	// вместо `robots`, и SEO-аудит обязан показывать то же самое, а не «в
	// общем-то открыто». Кто из двух тегов победил, решается после обхода
	// дерева, а не порядком в разметке.
	if name == "googlebot" && result.metaGooglebot == "" {
		result.metaGooglebot = trimField(content, maxTextField)
	}

	if strings.HasPrefix(property, "og:") {
		key := strings.TrimPrefix(property, "og:")
		if key == "" {
			return
		}
		if structure.OpenGraph == nil {
			structure.OpenGraph = map[string]string{}
		}
		// Первое значение выигрывает: og:image часто перечисляют списком, а
		// поисковики и соцсети берут первую картинку.
		if _, exists := structure.OpenGraph[key]; !exists {
			structure.OpenGraph[key] = trimField(content, maxTextField)
		}
	}
}

func readLink(node *html.Node, base *url.URL, structure *Structure) {
	rel := strings.ToLower(strings.TrimSpace(attribute(node, "rel")))
	if !hasToken(rel, "canonical") || structure.Canonical != "" {
		return
	}
	href := strings.TrimSpace(attribute(node, "href"))
	if href == "" {
		return
	}
	// Приводим к абсолютному виду: относительный canonical встречается
	// часто, а сравнивать его с адресом страницы иначе нечем.
	if resolved, err := base.Parse(href); err == nil {
		structure.Canonical = trimField(resolved.String(), maxTextField)
		return
	}
	structure.Canonical = trimField(href, maxTextField)
}

func readHeading(node *html.Node, structure *Structure) {
	// Не `text`: пакет с таким именем теперь импортирован файлом, и
	// одноимённая переменная спрятала бы его от следующей правки — с
	// сообщением компилятора, по которому причину не угадать.
	heading := trimField(textOf(node), maxHeadingText)
	if heading == "" {
		return
	}

	switch node.DataAtom {
	case atom.H1:
		if len(structure.H1) < maxHeadings {
			structure.H1 = append(structure.H1, heading)
		}
	case atom.H2:
		if len(structure.H2) < maxHeadings {
			structure.H2 = append(structure.H2, heading)
		}
	case atom.H3:
		if len(structure.H3) < maxHeadings {
			structure.H3 = append(structure.H3, heading)
		}
	}
}

func readImage(node *html.Node, base *url.URL, structure *Structure) {
	structure.Images.Total++

	// Атрибут есть, но пустой — это законная разметка для декоративной
	// картинки. Отсутствующий alt — совсем другое дело, и различать их
	// обязан аудит, а не мы за него.
	if _, present := lookupAttribute(node, "alt"); present {
		return
	}

	structure.Images.WithoutAlt++
	if len(structure.Images.Samples) >= maxImageSamples {
		return
	}
	src := strings.TrimSpace(attribute(node, "src"))
	if src == "" {
		return
	}
	if resolved, err := base.Parse(src); err == nil {
		src = resolved.String()
	}
	structure.Images.Samples = append(structure.Images.Samples, trimField(src, maxTextField))
}

func readAnchor(
	node *html.Node,
	base *url.URL,
	page *url.URL,
	structure *Structure,
	seen map[string]bool,
	follow *[]string,
) {
	href := strings.TrimSpace(attribute(node, "href"))
	if href == "" {
		return
	}

	resolved, err := base.Parse(href)
	if err != nil {
		return
	}
	switch resolved.Scheme {
	case "http", "https":
	default:
		// mailto:, tel:, javascript: — не страницы сайта и не внешние
		// ссылки. Считать их было бы враньём в обе стороны.
		return
	}

	nofollow := hasToken(strings.ToLower(attribute(node, "rel")), "nofollow")
	if nofollow {
		structure.Links.Nofollow++
	}

	// Поддомен и другой порт — другой сайт: внутренним он не считается ни
	// для счётчика, ни для обхода. Сверяемся с адресом страницы, а не с
	// `<base href>`: последний пишет владелец страницы, и объявить своим
	// чужой домен он не вправе.
	if !strings.EqualFold(resolved.Host, page.Host) {
		structure.Links.External++
		return
	}

	structure.Links.Internal++
	if nofollow {
		return
	}
	address := normalize(resolved)
	if seen[address] {
		return
	}
	seen[address] = true
	*follow = append(*follow, address)
}

// applyRobotsDirectives сводит указания из разметки и из заголовка в два
// признака. Заголовок и мета-тег равноправны, а запрет сильнее разрешения:
// «noindex» в любом из них означает, что страницы в поиске не будет.
func applyRobotsDirectives(structure *Structure) {
	directives := strings.ToLower(structure.MetaRobots + "," + structure.XRobotsTag)
	structure.Noindex = hasToken(directives, "noindex") || hasToken(directives, "none")
	structure.Nofollow = hasToken(directives, "nofollow") || hasToken(directives, "none")
}

// canonicalPointsHere сравнивает canonical с адресом самой страницы.
func canonicalPointsHere(canonical string, base *url.URL) bool {
	if canonical == "" {
		return false
	}
	parsed, err := url.Parse(canonical)
	if err != nil {
		return false
	}
	return normalize(parsed) == normalize(base)
}

// hasToken ищет слово в списке через запятую или пробелы: и `rel="nofollow
// noopener"`, и `content="noindex, nofollow"` — законные формы записи.
func hasToken(list, token string) bool {
	for _, part := range strings.FieldsFunc(list, func(symbol rune) bool {
		return symbol == ',' || symbol == ' ' || symbol == '\t' || symbol == '\n'
	}) {
		if strings.TrimSpace(part) == token {
			return true
		}
	}
	return false
}

func attribute(node *html.Node, name string) string {
	value, _ := lookupAttribute(node, name)
	return value
}

func lookupAttribute(node *html.Node, name string) (string, bool) {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val, true
		}
	}
	return "", false
}

// textOf собирает текст поддерева одной строкой.
func textOf(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			return
		}
		if current.Type == html.ElementNode {
			switch current.DataAtom {
			case atom.Script, atom.Style, atom.Template:
				return
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

// countWords считает слова в куске текста.
func countWords(value string) int {
	return len(strings.Fields(value))
}

// trimField убирает лишние пробелы и переводы строк и обрезает по длине.
//
// Пробелы схлопываются потому, что заголовок из вёрстки приезжает с
// отступами и переносами: без этого «слишком длинный title» считался бы по
// пробелам, а в базе лежала бы разметка вместо текста.
func trimField(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	// По символам, а не по байтам, и общей функцией, а не своей копией:
	// см. `internal/text`.
	return text.Truncate(value, limit)
}
