// Package links проверяет сайт на битые ссылки.
//
// Обход и проверка разделены: краулер собирает список страниц сайта,
// а lychee по нему извлекает и проверяет ссылки. Lychee — линк-чекер, а не
// краулер: сам по сайту он не ходит, зато умеет то, что нам пришлось бы
// писать самому — асинхронную проверку, перенаправления, разбор HTML.
package links

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"agent/internal/robots"
)

// UserAgent — чем краулер представляется чужому серверу.
//
// Имя обязано быть: по нему сайт пишет для нас правила в robots.txt и по
// нему же видит в своих логах, кто к нему ходил. Общий префикс с
// SEO-краулером не случаен — правило User-agent: vpsfocus накрывает обоих.
const UserAgent = "vpsfocus-links"

// ErrRobotsUnavailable — robots.txt сайта не отдался.
//
// По RFC 9309 это означает «не ходить»: сервер не сказал, что можно, и
// решать за него мы не станем. Отдельная ошибка, а не общая «обход не
// удался»: обход не сломался, он не состоялся — а для человека это разное.
var ErrRobotsUnavailable = errors.New("robots.txt не отдался")

// ErrRobotsForbidden — robots.txt запрещает нам даже главную страницу.
//
// Тоже отдельная ошибка, и по причине важнее прочих: без неё обход возвращал
// бы пустой список страниц без единого признака беды, а экран показывал бы
// зелёное «битых ссылок нет» по сайту, на который мы не заходили ни разу.
// Молчаливый ноль хуже отказа.
var ErrRobotsForbidden = errors.New("robots.txt запрещает обход")

// Limits — как далеко можно уходить от главной страницы.
//
// Всё консервативно по умолчанию: это чужой прод, и краулинг не имеет права
// мешать работе сайта клиента.
type Limits struct {
	// Сколько переходов от стартовой страницы разрешено. Ноль означает
	// «только сама страница»: ссылки на ней проверятся, но дальше агент
	// не пойдёт.
	MaxDepth int
	// Потолок числа страниц на один круг. Сайт без ограничений обхода
	// способен завесить и агента, и сервер клиента.
	MaxPages int
	// Куда не заходить. Применяется к адресу страницы целиком.
	Exclude []*regexp.Regexp
	// Пауза между запросами к сайту. Crawl-delay из robots.txt, если он
	// больше, заменяет её собой: это сервер клиента, и его просьба сильнее
	// нашей настройки.
	Delay time.Duration
	// Сколько байт читать со страницы. Ноль — умолчание [defaultMaxBytes].
	//
	// Потолок обязателен: агент живёт на сервере клиента, и память там не
	// наша — то же рассуждение, по которому круг копии смотрит на свободное
	// место до дампа. Настройка та же, что у SEO-обхода (`SEO_MAX_PAGE_KB`),
	// а не своё число рядом: два потолка чтения одной и той же страницы
	// однажды разошлись бы.
	MaxBytes int64
}

// Сколько читать, когда потолок не задан. Столько же, сколько по умолчанию
// читает SEO-обход.
const defaultMaxBytes = 2 << 20

// Crawl обходит сайт от startURL по внутренним ссылкам и возвращает список
// страниц. Сама стартовая страница входит в ответ, если её не запрещает
// robots.txt, — даже когда обход дальше не пошёл.
//
// Внутренними считаются ссылки на тот же хост: поддомен — уже другой сайт,
// и проверять его без спроса мы не станем.
//
// robots.txt читается первым и решает всё остальное — так же, как в
// SEO-обходе, и по той же причине: агент остаётся ботом на живом сервере,
// пусть сервер и принадлежит агентству. Запрещённые разделы в отчёт о битых
// ссылках не попадут; это цена вежливости, и она принята сознательно
// (решение 2026-09-04).
func Crawl(ctx context.Context, client *http.Client, startURL string, limits Limits) ([]string, error) {
	start, err := url.Parse(startURL)
	if err != nil {
		return nil, fmt.Errorf("стартовый адрес: %w", err)
	}
	if start.Host == "" {
		return nil, fmt.Errorf("стартовый адрес без хоста: %s", startURL)
	}

	walker := &crawler{client: client, delay: limits.Delay, maxBytes: limits.MaxBytes}

	rules, verdict := robots.Fetch(ctx, walker.request, start, UserAgent)
	if verdict == robots.Unavailable {
		return nil, ErrRobotsUnavailable
	}
	walker.delay = rules.DelayAtLeast(limits.Delay)

	// Запрещена главная — запрещено всё: дальше идти неоткуда, а пустой
	// ответ без ошибки экран показал бы как «проверено, всё хорошо».
	if !rules.Allowed(requestPath(start)) {
		return nil, ErrRobotsForbidden
	}

	type job struct {
		raw   string
		depth int
	}

	seen := map[string]bool{}
	pages := []string{}
	queue := []job{{raw: normalized(start)}}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			break
		}
		if len(pages) >= limits.MaxPages {
			break
		}

		current := queue[0]
		queue = queue[1:]
		if seen[current.raw] {
			continue
		}
		seen[current.raw] = true

		parsed, err := url.Parse(current.raw)
		if err != nil || parsed.Host != start.Host {
			continue
		}
		// Запрещённую страницу не забираем и в список для lychee не кладём:
		// он пошёл бы за ней сам.
		if !rules.Allowed(requestPath(parsed)) {
			continue
		}
		pages = append(pages, current.raw)

		// Дальше от стартовой страницы не идём: лимиты существуют, чтобы
		// краулинг заканчивался, а не «почти заканчивался».
		if current.depth >= limits.MaxDepth {
			continue
		}

		found, err := walker.fetchLinks(ctx, parsed)
		if err != nil {
			// Недоступная страница — не повод бросать обход: остальные
			// страницы сайта всё равно надо проверить. Её собственные
			// ссылки lychee пометит ошибкой сам.
			continue
		}

		for _, link := range found {
			resolved, ok := internal(parsed, link, start.Host)
			if !ok || seen[resolved] {
				continue
			}
			if excluded(limits.Exclude, resolved) {
				continue
			}
			queue = append(queue, job{raw: resolved, depth: current.depth + 1})
		}
	}

	// Пустой ответ без ошибки означал бы для экрана «проверено, всё
	// хорошо». Запрет robots отсечён выше, значит остаться без страниц
	// можно только по оборванному контексту — так и скажем.
	if len(pages) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	return pages, nil
}

// crawler — состояние обхода: клиент и выдержанная пауза.
//
// Пауза — это состояние, а не параметр: она отсчитывается от времени
// последнего запроса, и без общего места «последний запрос» у каждого вызова
// был бы свой.
type crawler struct {
	client      *http.Client
	delay       time.Duration
	lastRequest time.Time
	// Сколько байт читать со страницы. Ноль — [defaultMaxBytes].
	maxBytes int64
}

// wait выдерживает паузу между запросами к сайту.
//
// Один запрос за раз и пауза между ними — то же, что в SEO-обходе.
// Параллелить обход чужого прода незачем: сайтов у сервера единицы, обход
// редкий, а разница между «нас не заметили» и «сайт лёг под нашим
// краулером» — это ровно параллельность.
func (c *crawler) wait(ctx context.Context) error {
	if c.delay <= 0 || c.lastRequest.IsZero() {
		return nil
	}
	remaining := c.delay - time.Since(c.lastRequest)
	if remaining <= 0 {
		return nil
	}

	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// request делает один запрос, соблюдая паузу между обращениями к сайту.
//
// Через него ходят все: и страницы обхода, и сам robots.txt. Иначе
// robots.txt читался бы мимо паузы, и первый запрос обхода уходил бы к
// чужому серверу вплотную за ним.
func (c *crawler) request(ctx context.Context, target *url.URL) (*http.Response, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/html")
	request.Header.Set("User-Agent", UserAgent)

	response, err := c.client.Do(request)
	c.lastRequest = time.Now()
	return response, err
}

// requestPath — то, с чем сверяются правила robots.txt: путь вместе со
// строкой запроса. Правило вида Disallow: /search? без неё не сработает.
func requestPath(target *url.URL) string {
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	if target.RawQuery != "" {
		path += "?" + target.RawQuery
	}
	return path
}

// normalized убирает из адреса то, что не меняет страницу: фрагмент якоря
// и пустой запрос. Без этого одна страница с якорями стала бы тремя.
func normalized(u *url.URL) string {
	clean := *u
	clean.Fragment = ""
	clean.User = nil
	return clean.String()
}

// internal решает, является ли ссылка внутренней, и приводит её к каноническому
// виду. Поддомен и другой порт — другой сайт: туда не ходим.
func internal(base *url.URL, href string, host string) (string, bool) {
	ref, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	switch ref.Scheme {
	case "", "http", "https":
	default:
		return "", false
	}

	resolved := base.ResolveReference(ref)
	if resolved.Host != host {
		return "", false
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	return normalized(resolved), true
}

func excluded(patterns []*regexp.Regexp, raw string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(raw) {
			return true
		}
	}
	return false
}

// CompileExcludes собирает регулярки исключений из настроек.
//
// Кривая регулярка не роняет агента и не игнорируется молча: она не
// исключит ничего, а вызывающий получит список без неё. Проверять корректность
// настроек — работа экрана, который их вводит.
func CompileExcludes(patterns []string) []*regexp.Regexp {
	var compiled []*regexp.Regexp
	for _, pattern := range patterns {
		if expression, err := regexp.Compile(pattern); err == nil {
			compiled = append(compiled, expression)
		}
	}
	return compiled
}

// fetchLinks забирает страницу и вытаскивает значения атрибутов href.
//
// Разбор через x/net/html, а не регуляркой: реальный HTML живёт вёрсткой,
// которую регэксп переживает хуже, чем кажется до первой встречи.
func (c *crawler) fetchLinks(ctx context.Context, target *url.URL) ([]string, error) {
	resp, err := c.request(ctx, target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("статус %d", resp.StatusCode)
	}
	// Страница может отдаваться без расширения: ориентируемся на то,
	// что сказал сервер, а не на адрес.
	if ct := resp.Header.Get("Content-Type"); ct != "" && !isHTML(ct) {
		return nil, nil
	}

	// Потолок на читаемое: размер страницы на чужом проде нам не
	// принадлежит, а без ограничения его не держало ничто, кроме таймаута.
	limit := c.maxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}
	tokenizer := html.NewTokenizer(io.LimitReader(resp.Body, limit))
	var found []string
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return found, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.Data != "a" {
				continue
			}
			for _, attr := range token.Attr {
				if attr.Key == "href" && attr.Val != "" {
					found = append(found, attr.Val)
				}
			}
		}
	}
}

func isHTML(contentType string) bool {
	// До точки с запятой — сам тип, дальше параметры вроде charset.
	mime := contentType
	for i, char := range contentType {
		if char == ';' {
			mime = contentType[:i]
			break
		}
	}
	mime = trimSpace(mime)
	return mime == "text/html" || mime == "application/xhtml+xml"
}

func trimSpace(raw string) string {
	start, end := 0, len(raw)
	for start < end && isSpaceByte(raw[start]) {
		start++
	}
	for end > start && isSpaceByte(raw[end-1]) {
		end--
	}
	return raw[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// HTTPClient — клиент для краулинга. Таймаут щедрее, чем у lychee: сюда
// попадает и медленная генерация страницы на проде клиента.
//
// Перенаправление на чужой хост не проходится. Хост фильтруется при
// постановке в очередь, но перенаправление случается **после** отбора, и без
// этой проверки краулер скачивал и разбирал страницу того, куда его увели:
// правило «краулер не уходит с сайта клиента, и проверка хоста стоит в цикле
// обхода» писалось про SEO-краулер, а выполняться одним краулером из двух
// оно не может — вежливость у них общая.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Редиректы — норма жизни сайтов, но бесконечная цепочка
			// означает петлю, а не навигацию.
			if len(via) >= 5 {
				return fmt.Errorf("слишком много перенаправлений")
			}
			if len(via) > 0 && !sameSite(req.URL.Host, via[0].URL.Host) {
				return fmt.Errorf("перенаправление уводит с сайта: %s", req.URL.Host)
			}
			return nil
		},
	}
}

// sameSite — тот же сайт или его сосед по `www.`.
//
// Единственное законное исключение — ровно `www.` и только он: `m.` и
// `shop.` это отдельные сайты. Сосед разрешён потому, что перенаправление
// всего сайта с голого домена на www (и обратно) — обычная настройка, а без
// исключения обход такого сайта не принёс бы ни одной страницы и показал бы
// зелёное «битых ссылок нет» по сайту, на который мы не заходили.
func sameSite(host, base string) bool {
	host = strings.ToLower(host)
	base = strings.ToLower(base)
	if host == base {
		return true
	}
	return strings.TrimPrefix(host, "www.") == strings.TrimPrefix(base, "www.")
}
