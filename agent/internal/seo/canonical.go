package seo

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"agent/internal/robots"
)

// Контроль входа на сайт: одним ли адресом он открывается.
//
// Обход начинается с адреса, который завело агентство, и всё, что дальше,
// разбирается по страницам. Но у сайта есть ещё три входа, по которым в него
// приходят поисковик и посетитель со старой ссылкой: `http://`, `www.` и
// вариант без `www.`. Отдай сайт по двум из них одно и то же содержимое с
// кодом 200 — и в индексе окажутся два сайта вместо одного, а вес поделится
// между ними пополам.
//
// Проверка живёт отдельно от аудита страниц, потому что отвечает на вопрос о
// сайте целиком, а не о странице. Находки у неё те же по форме — код и
// уровень из общей таблицы `severityByCode`, — и в этом весь смысл: линейка
// важности одна на весь SEO.
//
// **Соседний хост — единственное место, где краулер обращается не к тому
// адресу, с которого начал.** Правило «краулер не уходит с сайта клиента»
// про чужие сайты: `<base href>` на чужой домен, перенаправление к партнёру.
// Здесь же речь о том же самом сайте, у которого сняли или добавили `www.`,
// и другого способа узнать, разъехались ли его входы, не существует. Отсюда
// и жёсткие рамки: ровно один соседний хост, ровно корневой адрес, свой
// robots.txt у каждого — и ни шагу дальше.

// Коды находок входа. Как и всё остальное, наружу уходят кодом.
const (
	// Сайт отдаёт содержимое по http:// вместо перенаправления на https://.
	//
	// Это две беды разом: поисковик видит две копии сайта, а посетитель
	// отдаёт свои данные по открытому каналу. Критично по обеим статьям.
	IssueCanonicalNoHTTPS = "canonicalNoHTTPS"
	// И `www.`, и адрес без него отдают содержимое, и ни один не объявляет
	// канонической другую версию.
	//
	// Критично по той же линейке: в поиск попадёт не та страница, которую
	// имели в виду, — а какая именно, решит поисковик.
	IssueCanonicalHostSplit = "canonicalHostSplit"
	// Вход зациклен: перенаправления не приводят к странице.
	//
	// Обычно это `www` → без `www` → `www`. Сайта по такому адресу нет ни
	// для поисковика, ни для посетителя.
	IssueCanonicalLoop = "canonicalLoop"
)

// Коды исходов проверки одного входа. Это не находки: «до http не
// достучались» — обычное дело на сервере, где порт 80 закрыт.
const (
	// Вход отвечает перенаправлением на канонический адрес — так и надо.
	EntryRedirects = "entryRedirects"
	// Вход отдаёт содержимое сам.
	EntryServes = "entryServes"
	// Вход объявил канонической нашу версию: содержимое отдаётся, но
	// поисковику сказано, кто главный.
	EntryDelegates = "entryDelegates"
	// До входа не достучались: адреса нет, порт закрыт, TLS не сошёлся.
	EntryUnreachable = "entryUnreachable"
	// Вход отвечает ошибкой.
	EntryError = "entryError"
	// Вход зациклен.
	EntryLoop = "entryLoop"
	// Вход уводит куда-то ещё — на чужой хост.
	//
	// Не находка: сайт, у которого старый адрес отдан партнёру или ведёт на
	// маркетплейс, настроен так намеренно. Но и не «всё хорошо»: на
	// канонический адрес он не ведёт, и сказать об этом надо своим словом.
	// Дальше этого шага мы не идём — см. `checkEntry`.
	EntryElsewhere = "entryElsewhere"
	// Вход не проверяли: его robots.txt не отдался или закрыл корень.
	//
	// Отдельный исход, а не молчание: «не смотрели» и «посмотрели, всё
	// хорошо» — разные новости, и выдавать первое за второе нельзя.
	EntrySkipped = "entrySkipped"
)

// Entry — один вход на сайт и то, чем он ответил.
type Entry struct {
	// Адрес, по которому стучались.
	URL string `json:"url"`
	// Чем кончилось: один из кодов Entry*.
	Code string `json:"code"`
	// Код ответа. Ноль — ответа не было.
	Status int `json:"status,omitempty"`
	// Куда в итоге привели перенаправления.
	FinalURL string `json:"finalUrl,omitempty"`
}

// CanonicalReport — что известно про входы на сайт.
type CanonicalReport struct {
	// Канонический адрес: тот, с которого начинался обход.
	Canonical string  `json:"canonical"`
	Entries   []Entry `json:"entries"`
	// Находки. Пусто — входы сходятся в один адрес.
	Issues []Issue `json:"issues,omitempty"`
}

// checkEntries проверяет входы на сайт: http:// и соседний хост по www.
//
// Идёт последним, когда обход уже закончен: это ещё два-четыре запроса к
// чужому серверу, и делать их до основной работы значит задерживать её ради
// проверки, которая меняется раз в год.
func (c *crawler) checkEntries(ctx context.Context) *CanonicalReport {
	report := &CanonicalReport{Canonical: normalize(c.base)}

	add := func(code, detail string) {
		report.Issues = append(report.Issues,
			Issue{Code: code, Severity: SeverityOf(code), Detail: detail})
	}

	// ── http:// ──────────────────────────────────────────────────────────
	// Только если сами пришли по https: сайт без TLS проверять на переход к
	// TLS нечем — обход и так начался по открытому каналу, и об этом
	// говорит сертификат, а не мы.
	if strings.EqualFold(c.base.Scheme, "https") {
		insecure := *c.base
		insecure.Scheme = "http"
		insecure.Path = "/"
		insecure.RawQuery = ""
		insecure.Fragment = ""

		entry := c.checkEntry(ctx, &insecure)
		report.Entries = append(report.Entries, entry)

		switch entry.Code {
		case EntryServes, EntryDelegates:
			// Даже объявленный canonical здесь не спасает: посетитель всё
			// равно идёт по открытому каналу, а поисковик всё равно видит
			// две копии. Обещание canonical — про индекс, а не про TLS.
			add(IssueCanonicalNoHTTPS, entry.URL)
		case EntryLoop:
			add(IssueCanonicalLoop, entry.URL)
		}
	}

	// ── Соседний хост по www ─────────────────────────────────────────────
	if sibling := siblingHost(c.base); sibling != nil {
		entry := c.checkEntry(ctx, sibling)
		report.Entries = append(report.Entries, entry)

		switch entry.Code {
		case EntryServes:
			add(IssueCanonicalHostSplit, entry.URL)
		case EntryLoop:
			add(IssueCanonicalLoop, entry.URL)
		}
	}

	if report.Entries == nil {
		report.Entries = []Entry{}
	}
	return report
}

// siblingHost — тот же сайт с `www.` или без него. nil, если строить нечего.
//
// Ровно один сосед и ровно одна приставка: `www.` — единственное имя, под
// которым сайт живёт вторым адресом по традиции. Всё остальное (`m.`,
// `shop.`) — это отдельные сайты, и объявлять их копиями мы не вправе.
func siblingHost(base *url.URL) *url.URL {
	host := base.Hostname()
	if host == "" {
		return nil
	}

	sibling := *base
	sibling.Path = "/"
	sibling.RawQuery = ""
	sibling.Fragment = ""

	port := base.Port()
	switch {
	case strings.HasPrefix(strings.ToLower(host), "www."):
		host = host[len("www."):]
		// `www.com` без второй точки — это не сосед, а другой домен.
		if !strings.Contains(host, ".") {
			return nil
		}
	default:
		// Соседа заводим только у имени вида `example.com`: у
		// `shop.example.com` вариант `www.shop.example.com` встречается
		// редко, а спрашивать его значит будить чужой сервер впустую.
		if strings.Count(host, ".") != 1 {
			return nil
		}
		host = "www." + host
	}

	if port != "" {
		sibling.Host = host + ":" + port
	} else {
		sibling.Host = host
	}
	return &sibling
}

// checkEntry стучится в один вход и проходит его перенаправления.
//
// robots.txt читается для каждого входа свой: у http:// и у соседнего хоста
// это отдельные адреса, и файл сайта клиента про них не говорит ничего.
// Файл, который не отдался, по RFC 9309 означает «не ходить» — и это же
// закрывает главный ложный след: на сервере с закрытым портом 80 сюда
// приходит отказ соединения, а не находка.
//
// **Цепочка обрывается, как только уводит с сайта клиента.** Иначе
// перенаправление на партнёрский домен утащило бы нас на чужой хост — за
// его robots.txt, которого мы не читали, и за его страницами, которые нас
// не касаются. Проверенных хостов ровно два: канонический и сам вход.
func (c *crawler) checkEntry(ctx context.Context, target *url.URL) Entry {
	entry := Entry{URL: normalize(target)}

	rules, verdict := robots.Fetch(ctx, c.request, target, UserAgent)
	if verdict == robots.Unavailable {
		entry.Code = EntrySkipped
		return entry
	}
	if !rules.Allowed("/") {
		entry.Code = EntrySkipped
		return entry
	}

	address := target
	for hop := 0; ; hop++ {
		if hop > maxRedirects {
			entry.Code = EntryLoop
			return entry
		}

		response, err := c.request(ctx, address)
		if err != nil {
			entry.Code = EntryUnreachable
			return entry
		}
		entry.Status = response.StatusCode

		location := response.Header.Get("Location")
		if isRedirect(response.StatusCode) && location != "" {
			response.Body.Close()

			next, err := address.Parse(location)
			if err != nil {
				entry.Code = EntryUnreachable
				return entry
			}
			entry.FinalURL = normalize(next)

			// Ушли с сайта клиента — дальше не идём и запроса не делаем.
			// Ответ честный: «ведёт куда-то ещё», а не выдуманная находка
			// про чужой хост, который мы не смотрели.
			if !ours(next, c.base, target) {
				entry.Code = EntryElsewhere
				return entry
			}

			address = next
			continue
		}

		body, _, err := readBody(response, c.limits.MaxBytes)
		response.Body.Close()
		if err != nil {
			entry.Code = EntryUnreachable
			return entry
		}

		if response.StatusCode >= 400 {
			entry.Code = EntryError
			return entry
		}
		// Перенаправление привело на канонический адрес — так и надо. Хост
		// сверяется целиком, вместе с портом: `example.com` →
		// `example.com.evil.io` это не «сайт настроен правильно».
		if entry.FinalURL != "" && strings.EqualFold(address.Host, c.base.Host) &&
			strings.EqualFold(address.Scheme, c.base.Scheme) {
			entry.Code = EntryRedirects
			return entry
		}
		if entry.FinalURL != "" {
			// Перенаправление осталось среди своих, но до канонического
			// адреса не довело: `http://example.com` → `http://www.example.com`
			// при каноническом `https://example.com`. Содержимое отдаётся не
			// по тому адресу, который мы считаем главным, — и для поиска это
			// то же самое, что вторая копия сайта.
			entry.Code = EntryServes
			return entry
		}

		if canonicalPointsAt(body, response, address, c.base) {
			entry.Code = EntryDelegates
			return entry
		}
		entry.Code = EntryServes
		return entry
	}
}

// ours — свой ли это хост: канонический или сам проверяемый вход.
//
// Сравнивается `Host` вместе с портом — тем же полем и по той же причине,
// что и в `accept` у самого обхода: `example.com:8443` и `example.com:9999`
// это разные места, и считать их одним значило бы завести дыру ровно там,
// где мы обещали её не заводить. Порт по умолчанию в `Host` не пишется, так
// что `http://example.com` и `https://example.com` сходятся сами.
func ours(candidate, canonical, entry *url.URL) bool {
	return strings.EqualFold(candidate.Host, canonical.Host) ||
		strings.EqualFold(candidate.Host, entry.Host)
}

// canonicalPointsAt — объявляет ли страница канонической нашу версию сайта.
//
// Разбор ради одного тега, но полноценный: `<link rel=canonical>` бывает и в
// конце `<head>`, и с относительным адресом, и с лишними пробелами внутри
// атрибута. Регулярка на такое ломается ровно тогда, когда ответ важен.
func canonicalPointsAt(body []byte, response *http.Response, address, canonical *url.URL) bool {
	contentType := response.Header.Get("Content-Type")
	if !isHTML(contentType) {
		return false
	}

	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		reader = bytes.NewReader(body)
	}
	document, err := html.Parse(reader)
	if err != nil {
		return false
	}

	parsed := extract(document, address, response.Header.Get("X-Robots-Tag"))
	if parsed.structure.Canonical == "" {
		return false
	}

	declared, err := url.Parse(parsed.structure.Canonical)
	if err != nil {
		return false
	}
	return strings.EqualFold(declared.Hostname(), canonical.Hostname()) &&
		strings.EqualFold(declared.Scheme, canonical.Scheme)
}
