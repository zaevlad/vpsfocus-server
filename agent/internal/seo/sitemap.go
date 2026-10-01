package seo

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html/charset"
)

// Карта сайта как источник адресов.
//
// Без неё аудит не увидит страницы, на которые не ведёт ни одна ссылка, — а
// это классическая находка: раздел выложили, из меню убрали, из карты сайта
// забыли, и он висит в поиске годами. Владелец сайта перечислил эти адреса
// сам, поэтому в очередь они попадают наравне с главной, а не глубже неё.

// sitemapDocument покрывает оба вида карты: список карт и список адресов.
// Разбирать их одной структурой можно потому, что элементы не пересекаются:
// у настоящей карты заполнено ровно одно из полей.
type sitemapDocument struct {
	Sitemaps []sitemapEntry `xml:"sitemap"`
	URLs     []sitemapEntry `xml:"url"`
}

type sitemapEntry struct {
	Location string `xml:"loc"`
}

// sitemapLoad — чем кончилось чтение одного файла карты.
type sitemapLoad int

const (
	sitemapLoaded sitemapLoad = iota
	// 4xx или отдан не файл карты. Для /sitemap.xml, который мы ищем сами,
	// это «карты нет»; для объявленной в robots.txt — поломка.
	sitemapNotFound
	// 5xx, перенаправление, обрыв, битый gzip, неразбираемая разметка.
	sitemapBroken
)

// sitemapURLs читает карту сайта: заполняет отчёт о ней и множество её
// адресов, а в очередь отдаёт не больше budget штук.
//
// Карта дочитывается до конца в пределах потолков файлов, байт и адресов, а
// не до бюджета очереди: иначе «страницы нет в карте» стало бы ложной
// находкой для всего, что в бюджет не влезло. Цена — до
// `maxSitemapDocuments` запросов раз в сутки, по одному на файл карты.
func (c *crawler) sitemapURLs(ctx context.Context, budget int) []string {
	report := &SitemapReport{Declared: len(c.robots.Sitemaps) > 0}
	c.sitemap = report
	c.sitemapMembers = map[string]bool{}

	queue := append([]string{}, c.robots.Sitemaps...)
	if len(queue) == 0 {
		// Карты нет в robots.txt — смотрим там, где она лежит у всех.
		fallback := *c.base
		fallback.Path = "/sitemap.xml"
		fallback.RawQuery = ""
		queue = append(queue, fallback.String())
	}
	report.Address = queue[0]

	var (
		found     []string
		seenDocs  = map[string]bool{}
		seenPages = map[string]bool{}
		documents int
		complete  = true
	)

	for len(queue) > 0 {
		if ctx.Err() != nil || documents >= maxSitemapDocuments {
			complete = false
			break
		}

		address := queue[0]
		queue = queue[1:]
		if seenDocs[address] {
			continue
		}
		seenDocs[address] = true

		target, err := url.Parse(address)
		if err != nil || !strings.EqualFold(target.Host, c.base.Host) {
			// Карта на чужом хосте — законная конструкция, но ходить за
			// ней мы не станем: мы обходим сайт клиента, а не тот, на
			// который он сослался. Её адресов мы не знаем — значит, и
			// карту целиком не прочли.
			complete = false
			continue
		}

		documents++
		document, load, truncated := c.loadSitemap(ctx, target)
		switch load {
		case sitemapNotFound:
			// Ненайденный /sitemap.xml, который мы искали сами, — это «карты
			// нет». Объявленная карта или файл из списка карт — поломка.
			if report.Declared || documents > 1 {
				report.fail(address)
			}
			complete = false
			continue
		case sitemapBroken:
			report.fail(address)
			complete = false
			continue
		}
		report.Found = true
		if truncated {
			// Дочитали до потолка: конец файла мы не видели.
			complete = false
		}

		for _, entry := range document.Sitemaps {
			if location := strings.TrimSpace(entry.Location); location != "" {
				queue = append(queue, location)
			}
		}
		for _, entry := range document.URLs {
			key, parsed, ok := c.sitemapMember(entry.Location)
			if !ok || c.sitemapMembers[key] {
				continue
			}
			if len(c.sitemapMembers) >= maxSitemapAddresses {
				complete = false
				break
			}
			c.sitemapMembers[key] = true
			if !c.robots.Allowed(requestPath(parsed)) {
				report.BlockedByRobots++
			}
			if len(found) >= budget {
				continue
			}
			if page, ok := c.acceptSitemapURL(entry.Location, seenPages); ok {
				found = append(found, page)
			}
		}
	}

	report.Addresses = len(c.sitemapMembers)
	// Не нашли ни одного файла — сверять не с чем, и «целиком» здесь
	// значило бы «целиком пустая».
	report.Complete = complete && report.Found
	return found
}

// fail запоминает первый файл карты, который не прочитался.
func (r *SitemapReport) fail(address string) {
	if r.Failed == "" {
		r.Failed = address
	}
}

// sitemapMember — адрес из карты в том виде, в каком его знает обход, если
// он с нашего сайта.
func (c *crawler) sitemapMember(raw string) (string, *url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", nil, false
	}
	if !strings.EqualFold(parsed.Host, c.base.Host) {
		return "", nil, false
	}
	return normalize(parsed), parsed, true
}

// acceptSitemapURL отбирает адрес из карты: наш хост, разрешён robots.txt,
// не исключён настройками и ещё не встречался.
func (c *crawler) acceptSitemapURL(raw string, seen map[string]bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if !strings.EqualFold(parsed.Host, c.base.Host) {
		return "", false
	}

	address := normalize(parsed)
	if seen[address] {
		return "", false
	}
	if !c.robots.Allowed(requestPath(parsed)) {
		// Карта сайта не отменяет robots.txt: она говорит, что страницу
		// хотят видеть в поиске, а robots.txt — что туда нельзя ходить.
		// Спор двух файлов владельца сайта решается в пользу запрета.
		c.stats.BlockedByRobots++
		return "", false
	}
	if excluded(c.limits.Exclude, address) {
		return "", false
	}
	if skipByExtension(address) {
		return "", false
	}

	seen[address] = true
	return address, true
}

// loadSitemap забирает и разбирает одну карту.
func (c *crawler) loadSitemap(ctx context.Context, target *url.URL) (sitemapDocument, sitemapLoad, bool) {
	response, err := c.request(ctx, target)
	if err != nil {
		return sitemapDocument{}, sitemapBroken, false
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode >= 400 && response.StatusCode < 500:
		return sitemapDocument{}, sitemapNotFound, false
	case response.StatusCode != http.StatusOK:
		// 5xx и перенаправления: за перенаправлением карты мы не ходим —
		// поисковик тоже ждёт её по объявленному адресу.
		return sitemapDocument{}, sitemapBroken, false
	}

	body, truncated, err := readBody(response, maxSitemapBytes)
	if err != nil {
		return sitemapDocument{}, sitemapBroken, false
	}

	// Карты часто лежат сжатыми: транспорт распаковывает Content-Encoding,
	// но `.xml.gz` — это сам файл, а не кодировка передачи. Смотрим на
	// сигнатуру gzip, а не только на имя: сервер, отдавший `.xml.gz` ещё и
	// с Content-Encoding, получит от транспорта уже распакованный файл.
	if strings.HasSuffix(strings.ToLower(target.Path), ".gz") && bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return sitemapDocument{}, sitemapBroken, false
		}
		defer reader.Close()
		body, err = io.ReadAll(io.LimitReader(reader, maxSitemapBytes))
		if err != nil {
			return sitemapDocument{}, sitemapBroken, false
		}
	}

	var document sitemapDocument
	decoder := xml.NewDecoder(bytes.NewReader(body))
	// Карты встречаются в windows-1251 не реже страниц, а стандартный
	// разборщик знает только UTF-8 и отказывается читать остальное.
	decoder.CharsetReader = charset.NewReaderLabel
	// Строгость здесь ни к чему: карта с одной кривой записью всё равно
	// содержит остальные адреса.
	decoder.Strict = false
	if err := decoder.Decode(&document); err != nil {
		return sitemapDocument{}, sitemapBroken, false
	}
	// Разбор без строгости и без имени корня примет что угодно — в том
	// числе HTML-страницу, которой сайт на одной странице отвечает на любой
	// адрес. Файл без единой записи картой не считаем.
	if len(document.URLs) == 0 && len(document.Sitemaps) == 0 {
		return sitemapDocument{}, sitemapNotFound, false
	}
	return document, sitemapLoaded, truncated
}
