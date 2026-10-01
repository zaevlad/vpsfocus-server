package seo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"agent/internal/robots"
)

// Сам обход.
//
// Один запрос за раз и пауза между ними. Параллелить обход чужого прода
// незачем: сайтов у сервера единицы, обход редкий, а разница между «мы
// незаметны» и «сайт лёг под нашим краулером» — это ровно параллельность.

// Сколько перенаправлений согласны пройти. Пять — это уже цепочка, которую
// поисковик не дочитает до конца; дальше начинается петля.
const maxRedirects = 5

// Потолок карт сайта за обход. У большого магазина карта разбита на десятки
// файлов, и вычитывать их все ради потолка в пятьсот страниц бессмысленно.
const maxSitemapDocuments = 10

// Сколько байт читаем у одной карты сайта. Потолок robots.txt живёт в
// пакете robots — вместе с его чтением.
const maxSitemapBytes = 10 << 20

// Сколько адресов карты помним для сверки «есть ли страница в карте». Это
// строки в памяти на время обхода, запросов они не стоят; потолок — чтобы
// карта магазина на миллион товаров не съела память агента. Упёрлись —
// карта считается прочитанной не целиком.
const maxSitemapAddresses = 50000

// Меньше этого числа слов на странице — и в поиске дубликатов она не
// участвует. Считаем слова, а не байты: одинаковый вес у двух страниц
// ничего не значит, а вот «на странице нечего читать» — значит ровно то,
// что сравнивать нечего.
const minComparableWords = 3

// Расширения, за которыми не бывает страницы. Проверяются до запроса: не
// пойти за картинкой дешевле, чем сходить и выбросить ответ, а каждый
// невынужденный запрос — это нагрузка на чужой сервер.
var nonPageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".avif": true, ".svg": true, ".ico": true, ".bmp": true,
	".css": true, ".js": true, ".mjs": true, ".map": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true, ".rtf": true,
	".zip": true, ".gz": true, ".tar": true, ".rar": true, ".7z": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".webm": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".exe": true, ".dmg": true, ".apk": true,
}

// HTTPClient — клиент для обхода.
//
// Перенаправления не следуются автоматически: цепочку надо записать целиком,
// а стандартный клиент показывает только её конец. Для аудита «страница
// отвечает 200» и «страница отвечает 301 на другую страницу, которая
// отвечает 200» — это разные ответы.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// crawler — состояние одного обхода.
type crawler struct {
	client *http.Client
	limits Limits
	base   *url.URL

	robots *robots.Rules
	delay  time.Duration
	// Когда сделали прошлый запрос. Пауза считается от него, а не спится
	// вслепую: разбор страницы тоже занимает время, и спать сверх него
	// значит растягивать обход вдвое без всякой пользы для сайта.
	lastRequest time.Time
	// Сколько всего простояли в паузах. Время ответа страницы считается
	// без них: пауза — наша вежливость, а не медлительность сайта, и с
	// Crawl-delay в десять секунд каждая страница выглядела бы медленной.
	waited time.Duration

	seen   map[string]bool
	hashes map[string]string
	pages  []Page
	stats  Stats

	// Карта сайта: отчёт о ней и все её адреса нашего сайта. Пусто, если
	// сверка с картой выключена.
	sitemap        *SitemapReport
	sitemapMembers map[string]bool
}

type task struct {
	address     string
	depth       int
	fromSitemap bool
}

// Crawl обходит сайт и возвращает сводку вместе со страницами.
//
// Стартовый адрес целиком, а не домен: так же устроен обход в проверке
// ссылок, и по той же причине — иначе краулер нечем натравить на что-либо,
// кроме https-сайта на стандартном порту, включая тестовый сервер.
//
// Ошибка обхода кладётся в саму сводку, а не возвращается наверх: экран
// должен увидеть «не получилось и почему», а не молчание о том, что данных
// нет. Страницы при этом возвращаются те, что успели собраться.
func Crawl(ctx context.Context, client *http.Client, startURL string, limits Limits) (Scan, []Page) {
	started := time.Now().UTC()
	scan := Scan{CheckedAt: started.Format(time.RFC3339)}

	start, err := url.Parse(startURL)
	if err != nil || start.Host == "" {
		scan.Error = ErrScanFailed
		scan.DurationSeconds = int64(time.Since(started).Seconds())
		return scan, []Page{}
	}
	scan.Domain = start.Hostname()

	worker := &crawler{
		client: client,
		limits: limits,
		base:   start,
		seen:   map[string]bool{},
		hashes: map[string]string{},
	}

	// robots.txt читается первым и решает всё остальное: файл, который не
	// отдался, по RFC 9309 означает «не ходить».
	//
	// Пауза выдерживается и здесь: обход ещё не начался, но чужой сервер уже
	// отвечает на наши запросы.
	rules, verdict := robots.Fetch(ctx, worker.request, start, UserAgent)
	if verdict == robots.Unavailable {
		scan.Error = ErrRobotsUnavailable
		scan.DurationSeconds = int64(time.Since(started).Seconds())
		return scan, []Page{}
	}
	worker.robots = rules
	worker.delay = rules.DelayAtLeast(limits.Delay)
	worker.stats.DelayMS = worker.delay.Milliseconds()

	// Кому открыт сайт — по уже прочитанному файлу, без запросов.
	site := checkRobots(rules, verdict)

	queue := worker.seed(ctx)
	site.Sitemap = worker.sitemap
	site.checkSitemap()
	site.sortIssues()

	worker.run(ctx, queue)
	worker.markSitemapMembers()

	scan.Stats = worker.stats
	scan.Stats.Site = site
	scan.Stats.Pages = len(worker.pages)
	scan.DurationSeconds = int64(time.Since(started).Seconds())

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		scan.Error = ErrTimeout
	case len(worker.pages) == 0 && worker.stats.BlockedByRobots > 0:
		// robots.txt закрыл всё, куда мы собирались. Пустой результат — это
		// отказ, а не «всё хорошо»: без кода экран показал бы «0 из 0
		// страниц» и число закрытых адресов, ни словом не объяснив причину.
		// Тот же случай и тот же выбор, что у проверки ссылок
		// (`linksRobotsForbidden`).
		scan.Error = ErrRobotsForbidden
	case worker.stats.Fetched == 0 && len(worker.pages) > 0:
		// Ни одной разобранной страницы — значит, сайта на этом адресе для
		// нас нет. Отдельный код: «обошли и ничего не нашли» и «не смогли
		// даже начать» — разные новости.
		scan.Error = ErrStartUnreachable
	case len(worker.pages) == 0:
		// Страниц нет, а сказать про robots.txt или про время нечего.
		// Остаётся наша собственная настройка: шаблон из `SEO_EXCLUDE`,
		// накрывший главную. Раньше этот случай не попадал ни в одну ветку
		// и уходил с пустым `run_error` — то есть обход, не состоявшийся
		// вовсе, выглядел как обход, о котором нечего сказать.
		scan.Error = ErrNoPages
	}

	if worker.pages == nil {
		// Пустой список, а не nil: Go сериализует nil-срез в null, и на
		// стороне приложения это уже ловилось.
		worker.pages = []Page{}
	}

	// Разбор идёт здесь же, сразу после обхода: дубликаты заголовков видны
	// только на всём наборе страниц, а второй раз собрать этот набор
	// неоткуда — HTML мы не храним.
	scan.Stats.Issues = audit(worker.pages, worker.sitemap)

	// Входы на сайт проверяются последними и только у состоявшегося обхода.
	// Спрашивать «а как открывается www?» у сайта, до которого мы не
	// достучались, значит выдать вторую находку про ту же беду; а на обходе,
	// оборванном по времени, это ещё два запроса сверх отведённого.
	if worker.stats.Fetched > 0 && ctx.Err() == nil {
		scan.Stats.Canonical = worker.checkEntries(ctx)
	}

	// Перечень проверок — последним: он собирается из всего, что выше.
	scan.Stats.Checks = BuildChecks(scan.Stats)

	return scan, worker.pages
}

// seed собирает стартовую очередь: главная страница и адреса из карты сайта.
func (c *crawler) seed(ctx context.Context) []task {
	queue := []task{{address: normalize(c.base)}}
	if !c.limits.UseSitemap {
		return queue
	}

	// Карте сайта отдаём не больше половины потолка. Иначе у магазина с
	// картой на пятьдесят тысяч адресов обход по ссылкам не состоится
	// вовсе — а он показывает совсем другое: что видно с главной.
	budget := c.limits.MaxPages / 2
	if budget < 1 {
		budget = 1
	}

	for _, address := range c.sitemapURLs(ctx, budget) {
		queue = append(queue, task{address: address, fromSitemap: true})
		c.stats.FromSitemap++
	}
	return queue
}

// markSitemapMembers отмечает страницы, адреса которых есть в карте сайта.
func (c *crawler) markSitemapMembers() {
	if len(c.sitemapMembers) == 0 {
		return
	}
	for index := range c.pages {
		c.pages[index].InSitemap = c.sitemapMembers[c.pages[index].URL]
	}
}

// run — сам обход в ширину.
func (c *crawler) run(ctx context.Context, queue []task) {
	for len(queue) > 0 {
		if ctx.Err() != nil {
			break
		}
		if len(c.pages) >= c.limits.MaxPages {
			// Оставшееся в очереди — это честный ответ на вопрос «а это
			// весь сайт?». Врать «весь» мы не станем — но и завышать тоже:
			// в очереди лежат уже виденные и исключённые адреса, они
			// отсеиваются только при извлечении.
			c.stats.Queued = c.countPending(queue)
			break
		}

		current := queue[0]
		queue = queue[1:]

		target, ok := c.accept(current.address)
		if !ok {
			continue
		}
		c.seen[current.address] = true

		page, follow := c.fetch(ctx, target, current)
		c.record(page)

		if current.depth >= c.limits.MaxDepth {
			continue
		}
		for _, address := range follow {
			if c.seen[address] || skipByExtension(address) {
				continue
			}
			queue = append(queue, task{address: address, depth: current.depth + 1})
		}
	}
}

// accept решает, идём ли мы по этому адресу, и заодно разбирает его.
//
// Отбор один на все источники адресов — главную, карту сайта и ссылки со
// страниц, — и живёт он в цикле обхода, а не в разборе разметки. Забытую
// проверку в середине разбора не видно; здесь же её обязан пройти каждый
// адрес, откуда бы он ни пришёл.
//
// Проверка хоста тут не формальность: `readAnchor` считал ссылку внутренней
// по совпадению с `<base href>`, а его пишет владелец страницы. Страница с
// `<base href="https://cdn.partner.net/">` уводила краулер на чужой сайт —
// с robots.txt сайта клиента, — а правило проекта требует обратного: мы на
// чужом проде и обходим сайт клиента, а не тот, на который он сослался.
func (c *crawler) accept(address string) (*url.URL, bool) {
	if c.seen[address] {
		return nil, false
	}

	target, err := url.Parse(address)
	if err != nil {
		return nil, false
	}
	if !strings.EqualFold(target.Host, c.base.Host) {
		return nil, false
	}
	if excluded(c.limits.Exclude, address) {
		return nil, false
	}
	if !c.robots.Allowed(requestPath(target)) {
		c.stats.BlockedByRobots++
		return nil, false
	}
	return target, true
}

// countPending — сколько адресов из очереди мы бы действительно взяли.
//
// Сырая длина завышает остаток: счётчик заведён ради честного ответа на
// вопрос «а это весь сайт?», и число больше настоящего отвечает на него
// неправильно.
func (c *crawler) countPending(queue []task) int {
	pending := 0
	counted := make(map[string]bool, len(queue))

	for _, item := range queue {
		if counted[item.address] || c.seen[item.address] {
			continue
		}
		counted[item.address] = true

		target, err := url.Parse(item.address)
		if err != nil || !strings.EqualFold(target.Host, c.base.Host) {
			continue
		}
		if excluded(c.limits.Exclude, item.address) || !c.robots.Allowed(requestPath(target)) {
			continue
		}
		pending++
	}
	return pending
}

// record кладёт страницу и обновляет счётчики.
func (c *crawler) record(page Page) {
	if page.Structure != nil {
		c.stats.Fetched++
		if page.Structure.Noindex {
			c.stats.Noindex++
		}
	} else if page.Error == PageErrNetwork ||
		page.Error == PageErrTimeout ||
		page.Error == PageErrStatus ||
		page.Error == PageErrRedirectLoop {
		c.stats.Failed++
	}
	if len(page.Redirects) > 0 {
		c.stats.Redirected++
	}
	if page.DuplicateOf != "" {
		c.stats.Duplicates++
	}
	c.pages = append(c.pages, page)
}

// fetch забирает одну страницу вместе с цепочкой перенаправлений.
func (c *crawler) fetch(ctx context.Context, target *url.URL, current task) (Page, []string) {
	page := Page{
		URL:         current.address,
		Depth:       current.depth,
		FromSitemap: current.fromSitemap,
	}

	started := time.Now()
	waitedBefore := c.waited
	address := target
	var redirects []string

	for hop := 0; ; hop++ {
		if hop > maxRedirects {
			page.Error = PageErrRedirectLoop
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}

		response, err := c.request(ctx, address)
		if err != nil {
			page.Error = requestErrorCode(err)
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}

		page.Status = response.StatusCode
		location := response.Header.Get("Location")
		if !isRedirect(response.StatusCode) || location == "" {
			body, tooLarge, err := readBody(response, c.limits.MaxBytes)
			_ = response.Body.Close()
			if err != nil {
				page.Error = requestErrorCode(err)
				page.Redirects = redirects
				page.LoadMS = c.loadMS(started, waitedBefore)
				return page, nil
			}

			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			if len(redirects) > 0 {
				page.FinalURL = normalize(address)
			}
			return c.finishPage(page, response, address, body, tooLarge)
		}

		// Перенаправление: закрываем тело, не читая, — в нём ничего нет.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()

		next, err := address.Parse(location)
		if err != nil {
			page.Error = PageErrNetwork
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}
		if next.Scheme != "http" && next.Scheme != "https" {
			page.Error = PageErrOffsite
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}

		redirects = append(redirects, normalize(next))
		page.FinalURL = normalize(next)

		if !strings.EqualFold(next.Host, c.base.Host) {
			// Ушли с сайта: это законный ответ (домен с www на без www —
			// частая настройка), но дальше идти нам некуда.
			page.Error = PageErrOffsite
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}
		if c.seen[normalize(next)] {
			// Цель уже обойдена: скачивать её второй раз незачем, а
			// цепочку мы записали.
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}
		if !c.robots.Allowed(requestPath(next)) {
			c.stats.BlockedByRobots++
			page.Redirects = redirects
			page.LoadMS = c.loadMS(started, waitedBefore)
			return page, nil
		}

		// Цель перенаправления — та же страница, и второй раз мы к ней не
		// придём: отмечаем её виденной прямо здесь.
		c.seen[normalize(next)] = true
		address = next
	}
}

// finishPage разбирает тело ответа и считает отпечаток.
func (c *crawler) finishPage(
	page Page,
	response *http.Response,
	address *url.URL,
	body []byte,
	tooLarge bool,
) (Page, []string) {
	page.Bytes = len(body)
	page.ContentType = response.Header.Get("Content-Type")

	if page.Status >= 400 {
		page.Error = PageErrStatus
		return page, nil
	}
	if !isHTML(page.ContentType) {
		// Не страница: PDF, картинка, лента. Это не сбой — просто аудиту
		// здесь смотреть не на что.
		page.Error = PageErrNotHTML
		return page, nil
	}

	// Кодировку берём из заголовка и из самой разметки: сайты на
	// windows-1251 живее всех живых, а без перекодировки в базу легли бы
	// заголовки, которые никто не прочитает.
	reader, err := charset.NewReader(bytes.NewReader(body), page.ContentType)
	if err != nil {
		reader = bytes.NewReader(body)
	}
	document, err := html.Parse(reader)
	if err != nil {
		page.Error = PageErrNetwork
		return page, nil
	}

	result := extract(document, address, response.Header.Get("X-Robots-Tag"))
	page.Structure = &result.structure

	// Отпечаток снимается только со страницы, в которой есть что
	// сравнивать, — поэтому и после разбора, а не до него.
	//
	// Две пустые страницы совпадают побайтово просто потому, что в них
	// ничего нет: первая объявляла вторую копией и вешала на неё критичную
	// находку `duplicateContent` на пустом месте. Заглушка, страница
	// «загружаем…» и каркас, который дорисовывает JavaScript, — это не
	// дубликаты друг друга, а отсутствие содержимого, и говорить о них надо
	// другими находками (`thinContent`). Порог тут не про экономию, а про
	// смысл: одинаковое ничто не значит дубликат.
	if result.structure.WordCount >= minComparableWords {
		sum := sha256.Sum256(body)
		page.Hash = hex.EncodeToString(sum[:])
		if original, exists := c.hashes[page.Hash]; exists && original != page.URL {
			page.DuplicateOf = original
		} else if !exists {
			c.hashes[page.Hash] = page.URL
		}
	}
	if tooLarge {
		// Разметку обрезали на потолке: структура собрана по началу
		// страницы, и молчать об этом нельзя — конца страницы мы не
		// видели.
		page.Error = PageErrTooLarge
	}
	return page, result.follow
}

// request делает один запрос, соблюдая паузу между обращениями к сайту.
func (c *crawler) request(ctx context.Context, target *url.URL) (*http.Response, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", UserAgent)
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	// Accept-Encoding не ставим: транспорт Go просит gzip сам и сам же
	// распаковывает ответ — но только если попросил он. Заголовок,
	// поставленный здесь, выключал распаковку, и со спринта 19 до 94 на
	// любом сайте со сжатием аудит разбирал сжатые байты как HTML: пустые
	// страницы без title и нечитаемая карта сайта.

	response, err := c.client.Do(request)
	c.lastRequest = time.Now()
	return response, err
}

// wait выдерживает паузу между запросами.
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
	slept := time.Now()
	defer func() { c.waited += time.Since(slept) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// loadMS — сколько страница отвечала: от начала её разбора, за вычетом
// наших пауз перед запросами цепочки.
func (c *crawler) loadMS(started time.Time, waitedBefore time.Duration) int64 {
	elapsed := time.Since(started) - (c.waited - waitedBefore)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Milliseconds()
}

// readBody читает тело с потолком. Второе значение — упёрлись ли в него.
func readBody(response *http.Response, limit int64) ([]byte, bool, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return body[:limit], true, nil
	}
	return body, false, nil
}

func requestErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || os_IsTimeout(err) {
		return PageErrTimeout
	}
	return PageErrNetwork
}

// os_IsTimeout отделяет таймаут от прочих сетевых бед: у клиента с
// установленным Timeout он приезжает своей ошибкой, а не контекстом.
func os_IsTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	}
	return false
}

func isHTML(contentType string) bool {
	mime := contentType
	if semicolon := strings.IndexByte(mime, ';'); semicolon >= 0 {
		mime = mime[:semicolon]
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	// Пустой Content-Type — обычная беда старых сайтов. Считаем страницей:
	// разбор пустого места ничего не стоит, а выбросить настоящую страницу
	// из-за незаполненного заголовка обидно.
	return mime == "" || mime == "text/html" || mime == "application/xhtml+xml"
}

// normalize приводит адрес к виду, в котором его можно сравнивать.
//
// Убирается всё, что не меняет страницу: якорь, учётные данные, порт по
// умолчанию. Порядок параметров запроса не трогаем — для сайта это разные
// адреса, и решать за него мы не станем.
func normalize(u *url.URL) string {
	clean := *u
	clean.Fragment = ""
	clean.RawFragment = ""
	clean.User = nil
	clean.Scheme = strings.ToLower(clean.Scheme)
	clean.Host = strings.ToLower(clean.Host)

	if (clean.Scheme == "http" && strings.HasSuffix(clean.Host, ":80")) ||
		(clean.Scheme == "https" && strings.HasSuffix(clean.Host, ":443")) {
		clean.Host = clean.Host[:strings.LastIndexByte(clean.Host, ':')]
	}
	if clean.Path == "" {
		clean.Path = "/"
	}
	return clean.String()
}

// sameHost — один ли это сайт. Кривой адрес считается чужим: разобрать его
// мы не смогли, и выдавать «свой» за неразобранное нельзя.
func sameHost(left, right string) bool {
	first, err := url.Parse(left)
	if err != nil {
		return false
	}
	second, err := url.Parse(right)
	if err != nil {
		return false
	}
	return strings.EqualFold(first.Host, second.Host)
}

// requestPath — то, с чем сверяются правила robots.txt: путь вместе со
// строкой запроса.
func requestPath(u *url.URL) string {
	result := u.EscapedPath()
	if result == "" {
		result = "/"
	}
	if u.RawQuery != "" {
		result += "?" + u.RawQuery
	}
	return result
}

// skipByExtension отсекает адреса, за которыми заведомо не страница.
func skipByExtension(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	return nonPageExtensions[strings.ToLower(path.Ext(parsed.Path))]
}

func excluded(patterns []*regexp.Regexp, address string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(address) {
			return true
		}
	}
	return false
}

// CompileExcludes собирает регулярки исключений из настроек.
//
// Кривая регулярка не роняет агента и не игнорируется молча: она не
// исключит ничего, а вызывающий получит список без неё. Проверять
// корректность настроек — работа экрана, который их вводит.
func CompileExcludes(patterns []string) []*regexp.Regexp {
	var compiled []*regexp.Regexp
	for _, pattern := range patterns {
		if expression, err := regexp.Compile(pattern); err == nil {
			compiled = append(compiled, expression)
		}
	}
	return compiled
}
