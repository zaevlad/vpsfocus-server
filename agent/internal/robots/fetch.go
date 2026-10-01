package robots

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Загрузка robots.txt. Отдельно от разбора, потому что разбор — чистая
// функция, а здесь чужой сервер, перенаправления и потолки.

// Сколько байт читаем у robots.txt. RFC 9309 требует разобрать хотя бы
// 500 КиБ; больше не читаем — файл длиннее полумегабайта пишут не для нас.
const maxBytes = 512 << 10

// Сколько перенаправлений проходим, разыскивая robots.txt.
//
// RFC 9309 велит следовать «хотя бы пяти». Пять и берём: цепочка длиннее —
// это уже не «файл переехал», а петля.
const maxRedirects = 5

// Verdict — чем кончилась попытка прочитать robots.txt.
type Verdict int

const (
	// Found — файл прочитан, правила из него.
	Found Verdict = iota
	// Absent — файла нет (4xx). По RFC это разрешение ходить куда угодно.
	Absent
	// Unavailable — файл не отдался: 5xx, обрыв связи, петля из
	// перенаправлений. По RFC 9309 это означает «не ходить»: сервер не
	// сказал, что можно, и решать за него мы не станем.
	Unavailable
)

// Doer делает один запрос к чужому серверу.
//
// Запрос выполняет вызывающий, а не этот пакет: у краулера есть пауза между
// обращениями, свой User-Agent и учёт времени последнего запроса. Отдай мы
// сюда голый *http.Client — robots.txt читался бы мимо паузы, и первый же
// запрос обхода уходил бы вплотную к нему.
type Doer func(ctx context.Context, target *url.URL) (*http.Response, error)

// Fetch читает robots.txt сайта.
//
// Перенаправления проходятся здесь, а не клиентом: краулеры ходят по чужому
// проду с выключенным автоследованием — цепочку редиректов страницы надо
// записать целиком. Для robots.txt цепочка не нужна, нужен сам файл, и
// «robots.txt переехал на www» не должно означать «сайт запретил обход».
func Fetch(ctx context.Context, do Doer, site *url.URL, agent string) (*Rules, Verdict) {
	target := *site
	target.Path = "/robots.txt"
	target.RawQuery = ""
	target.Fragment = ""

	next := &target
	for redirects := 0; redirects <= maxRedirects; redirects++ {
		response, err := do(ctx, next)
		if err != nil {
			return nil, Unavailable
		}

		switch {
		case response.StatusCode == http.StatusOK:
			body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes))
			response.Body.Close()
			if err != nil {
				return nil, Unavailable
			}
			return Parse(body, agent), Found

		case isRedirect(response.StatusCode):
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return nil, Unavailable
			}
			resolved, err := next.Parse(location)
			if err != nil {
				return nil, Unavailable
			}
			// Перенаправление на чужой хост — это уже не наш robots.txt.
			// Читать правила соседнего домена и применять их к сайту
			// клиента значит ходить не туда, куда ходит поисковик; а
			// «сервер не сказал, что можно» — это Unavailable.
			if !strings.EqualFold(resolved.Hostname(), site.Hostname()) {
				return nil, Unavailable
			}
			next = resolved

		case response.StatusCode >= 400 && response.StatusCode < 500:
			response.Body.Close()
			return AllowAll(), Absent

		default:
			response.Body.Close()
			return nil, Unavailable
		}
	}

	// Цепочка не кончилась за отведённые переходы: считаем файл неотданным.
	return nil, Unavailable
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// DelayAtLeast — пауза между запросами: своя настройка или запрошенная
// сайтом, смотря что больше.
//
// Crawl-delay сайта сильнее нашей настройки: это его сервер. Потолок при
// разборе уже применён — обход, который не заканчивается, не отличим от
// сломанного.
func (r *Rules) DelayAtLeast(own time.Duration) time.Duration {
	if r == nil || r.Delay < own {
		return own
	}
	return r.Delay
}
