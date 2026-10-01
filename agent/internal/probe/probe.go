// Package probe проверяет не «отвечает ли сайт», а «работает ли то, ради
// чего он есть».
//
// Двухсотка на главной странице ничего не говорит о том, что форма заказа
// принимает заказы, а API отдаёт JSON. Поэтому у проверки есть профиль:
// страница, JSON API, перенаправление, текст на странице. Каждый отвечает на
// свой вопрос и каждый может провалиться по-своему.
//
// **Локальная проверка подписывается честно.** Она идёт с самого VPS и не
// видит облачного файрвола провайдера — правило «финальная проверка
// достижимости делается снаружи» появилось именно из-за этого. Ответ у неё
// «приложение отвечает», а не «посетитель это видит»; вторая подпись была бы
// враньём человеку.
//
// **Наружу уходят коды, а не текст** — как и везде в проекте: интерфейс
// двуязычный, а агент обновляется отдельно от него.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kind — какой вопрос задаёт проверка.
type Kind string

const (
	// KindPage — страница отдаётся и это разметка, а не заглушка балансера.
	KindPage Kind = "page"
	// KindJSON — ответ разбирается как JSON. Проверка для API: страница
	// ошибки с кодом 200 разметкой быть может, JSON — нет.
	KindJSON Kind = "json"
	// KindRedirect — адрес перенаправляет туда, куда должен. Ловит
	// сломанный редирект с www или с http, из-за которого половина ссылок
	// в интернете ведёт в никуда.
	KindRedirect Kind = "redirect"
	// KindText — на странице есть нужный текст. Тем и проверяют, что
	// страница собралась целиком, а не отдала шаблон без данных.
	KindText Kind = "text"
)

// Коды исходов. Пустой код — проверка прошла.
const (
	// До адреса не достучались: DNS, отказ в соединении, оборванная связь.
	CodeUnreachable = "probeUnreachable"
	// Ответ не пришёл за отведённое время.
	CodeTimeout = "probeTimeout"
	// TLS не сошёлся: просроченный, чужой или самоподписанный сертификат.
	CodeTLSFailed = "probeTLSFailed"
	// Код ответа не тот, которого ждали. Сам код едет числом рядом.
	CodeStatus = "probeStatus"
	// Ответ не похож на разметку страницы.
	CodeNotHTML = "probeNotHTML"
	// Ответ не разобрался как JSON.
	CodeNotJSON = "probeNotJSON"
	// Ответ не похож на скрипт: не тот тип содержимого или пустое тело.
	//
	// Свой код, а не CodeNotJSON по соседству: им проверяется скрипт
	// трекинга, и человеку про javascript-файл сообщали бы «ответ не
	// разобрался как JSON». Сказанное невпопад читается как поломка у нас,
	// а не как беда на сервере, — и то же самое уходило в еженедельный
	// отчёт.
	CodeNotScript = "probeNotScript"
	// Перенаправления не было вовсе.
	CodeNoRedirect = "probeNoRedirect"
	// Перенаправление есть, но ведёт не туда.
	CodeWrongTarget = "probeWrongTarget"
	// Нужного текста на странице нет.
	CodeTextMissing = "probeTextMissing"
	// Профиль заполнен так, что проверять нечего.
	CodeMisconfigured = "probeMisconfigured"
)

// Сколько тела читаем. Проверке хватает начала страницы, а качать гигабайт
// с чужого прода каждые несколько минут — плохой способ следить за ним.
const maxBody = 512 << 10

// Profile — что и как проверяем.
//
// Один формат на локальные и внешние проверки, но набор разрешённых полей
// у них разный: `Headers` бывает только у локальной. Она живёт на сервере
// клиента, в `agent.env` с правами 600, и токен в ней — его собственный
// секрет. Внешняя проверка идёт с нашей инфраструктуры, и класть туда чужой
// токен нельзя никогда.
type Profile struct {
	// Имя для человека. Агент его не разбирает и наружу отдаёт как есть.
	Name string `json:"name"`
	// Проект, к которому относится адрес. Свод состояния поднимает уровень
	// именно ему.
	ProjectID string `json:"projectId"`
	Kind      Kind   `json:"kind"`
	URL       string `json:"url"`
	// Ожидаемый код ответа. Ноль — «любой удачный»: 2xx для страницы, JSON
	// и текста, 3xx для перенаправления.
	Status int `json:"status,omitempty"`
	// Что должно быть на странице (KindText) и куда должно вести
	// перенаправление (KindRedirect).
	Expect string `json:"expect,omitempty"`
	// Заголовки запроса. Здесь и живёт токен закрытого раздела.
	Headers map[string]string `json:"headers,omitempty"`
}

// Result — чем кончилась проверка.
type Result struct {
	Name      string `json:"name"`
	ProjectID string `json:"projectId"`
	Kind      Kind   `json:"kind"`
	URL       string `json:"url"`
	// Проверка прошла.
	OK bool `json:"ok"`
	// Код беды. Пусто — прошла.
	Code string `json:"code,omitempty"`
	// Код ответа сервера. Ноль — ответа не было вовсе.
	Status int `json:"status,omitempty"`
	// Сколько миллисекунд ждали ответа.
	DurationMs int64     `json:"durationMs"`
	CheckedAt  time.Time `json:"checkedAt"`
}

// Client — как ходить в сеть.
//
// Отдельным типом, потому что проверок два вида и ходят они по-разному:
// ключевые адреса проекта — обычным клиентом, свой же Caddy — клиентом,
// которому подменён адрес соединения. Перенаправления не проходятся: у
// профиля `redirect` они и есть предмет проверки, а у остальных пройденное
// перенаправление скрыло бы, что страница переехала.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Run выполняет одну проверку.
//
// Ошибку наружу не отдаёт: неудача проверки — это её нормальный исход, а не
// поломка агента. Всё, что случилось, лежит в `Result`.
func Run(ctx context.Context, client *http.Client, profile Profile) Result {
	started := time.Now()
	result := Result{
		Name:      profile.Name,
		ProjectID: profile.ProjectID,
		Kind:      profile.Kind,
		URL:       profile.URL,
		CheckedAt: started.UTC(),
	}

	finish := func(code string) Result {
		result.DurationMs = time.Since(started).Milliseconds()
		result.Code = code
		result.OK = code == ""
		return result
	}

	if profile.URL == "" || !known(profile.Kind) {
		return finish(CodeMisconfigured)
	}
	// Профиль без того, что составляет его смысл, — это не строгая
	// проверка, а тихая: она проходила бы всегда.
	if (profile.Kind == KindText || profile.Kind == KindRedirect) && profile.Expect == "" {
		return finish(CodeMisconfigured)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, profile.URL, nil)
	if err != nil {
		return finish(CodeMisconfigured)
	}
	// Представляемся так же, как оба краулера агента: правило
	// `User-agent: vpsfocus` в robots.txt накрывает и эту проверку. Сама она
	// robots.txt не читает намеренно — адрес назвал владелец сайта, и это
	// не обход, а один запрос по указанному адресу.
	request.Header.Set("User-Agent", "vpsfocus-probe")
	for name, value := range profile.Headers {
		request.Header.Set(name, value)
	}

	response, err := client.Do(request)
	if err != nil {
		return finish(failure(ctx, err))
	}
	defer response.Body.Close()

	result.Status = response.StatusCode

	if !statusFits(profile, response.StatusCode) {
		return finish(CodeStatus)
	}

	if profile.Kind == KindRedirect {
		return finish(checkRedirect(profile, response))
	}

	// Форма ответа проверяется только у удачного. Отказ, которого человек
	// ждал сам (`Status: 401` у закрытого раздела), и есть ожидаемый
	// результат: требовать от него разметки или JSON значит объявить
	// поломкой правильно работающую защиту.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return finish("")
	}

	switch profile.Kind {
	case KindJSON:
		return finish(checkJSON(response))
	case KindText:
		return finish(checkText(profile, response))
	default:
		return finish(checkHTML(response))
	}
}

func known(kind Kind) bool {
	switch kind {
	case KindPage, KindJSON, KindRedirect, KindText:
		return true
	}
	return false
}

// statusFits — тот ли код ответа.
//
// Заданный код проверяется точно, незаданный — по смыслу профиля: у
// перенаправления удачей считается 3xx, у остальных 2xx. Считать удачей
// «не пятисотку» нельзя: 404 на ключевом адресе — это ровно та беда, ради
// которой проверку и заводят.
func statusFits(profile Profile, status int) bool {
	if profile.Status != 0 {
		return status == profile.Status
	}
	if profile.Kind == KindRedirect {
		return status >= 300 && status < 400
	}
	return status >= 200 && status < 300
}

// checkRedirect сверяет, куда ведёт перенаправление.
//
// Сравниваются **хост целиком и путь**, а не начало строки. Начало строки
// пропускало `Location: https://example.com.evil.io/` под ожиданием
// `https://example.com` — ровно та ловушка, от которой SEO-краулер
// защищается сравнением хоста (`strings.EqualFold(next.Host, c.base.Host)`),
// а сайт защищается сверкой `Origin` по хосту, а не подстрокой.
//
// Причина, по которой нельзя требовать равенства строк, при этом остаётся
// верной: человек пишет адрес без слэша, а сервер добавляет параметры.
// Ответ на неё — сравнение разобранного адреса: строка запроса и хвостовой
// слэш не мешают, чужой хост не проходит.
func checkRedirect(profile Profile, response *http.Response) string {
	raw := strings.TrimSpace(response.Header.Get("Location"))
	if raw == "" {
		return CodeNoRedirect
	}

	target, err := url.Parse(raw)
	if err != nil {
		return CodeWrongTarget
	}
	// Относительный `Location` («/login») законен по RFC 9110 и встречается
	// чаще полного. Разрешается он относительно того адреса, который мы
	// спрашивали, — иначе сравнивать было бы нечего с чем.
	if response.Request != nil && response.Request.URL != nil {
		target = response.Request.URL.ResolveReference(target)
	}

	expect := expectedTarget(profile)
	if expect == nil {
		return CodeWrongTarget
	}

	// Схема сверяется, только если человек её назвал: перенаправление с
	// https на http — это понижение, а не мелочь, и молчать о нём нельзя.
	if expect.Scheme != "" && !strings.EqualFold(target.Scheme, expect.Scheme) {
		return CodeWrongTarget
	}
	if !strings.EqualFold(target.Host, expect.Host) {
		return CodeWrongTarget
	}
	if trimSlash(target.Path) != trimSlash(expect.Path) {
		return CodeWrongTarget
	}
	return ""
}

// expectedTarget разбирает то, что человек написал в поле «ведёт на».
//
// Три формы, и все три встречаются: полный адрес («https://example.com/»),
// адрес без схемы («example.com/») и путь на том же сайте («/login»). Нет
// хоста ни после разбора, ни после разрешения — сравнивать не с чем.
func expectedTarget(profile Profile) *url.URL {
	raw := strings.TrimSpace(profile.Expect)
	if raw == "" {
		return nil
	}

	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		base, err := url.Parse(profile.URL)
		if err != nil {
			return nil
		}
		relative, err := url.Parse(raw)
		if err != nil {
			return nil
		}
		return base.ResolveReference(relative)
	}

	expect, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if expect.Host == "" {
		// «example.com/новая» — не путь, а адрес без схемы: две косые черты
		// делают из него то, чем он и был задуман.
		expect, err = url.Parse("//" + raw)
		if err != nil || expect.Host == "" {
			return nil
		}
	}
	return expect
}

// trimSlash приводит «/» и «» к одному виду.
//
// Хвостовой слэш адрес не меняет, а человек пишет то так, то этак: разница
// между `https://example.com` и `https://example.com/` — это ложная тревога
// каждые пять минут, а не находка.
func trimSlash(path string) string {
	return strings.TrimSuffix(path, "/")
}

func checkJSON(response *http.Response) string {
	body, err := read(response)
	if err != nil {
		return CodeUnreachable
	}
	var parsed any
	if json.Unmarshal(body, &parsed) != nil {
		return CodeNotJSON
	}
	return ""
}

// checkText ищет текст в теле ответа.
//
// Без учёта регистра: человек пишет искомое по памяти, а не копирует из
// разметки, и «Оформить заказ» против «ОФОРМИТЬ ЗАКАЗ» — это ложная тревога,
// а не находка.
func checkText(profile Profile, response *http.Response) string {
	body, err := read(response)
	if err != nil {
		return CodeUnreachable
	}
	if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(profile.Expect)) {
		return CodeTextMissing
	}
	return ""
}

// checkHTML отличает страницу от всего остального.
//
// По заголовку, а не по содержимому: заглушка балансера и страница ошибки
// прокси — это тоже разметка, и разбирать её было бы гаданием. А вот JSON с
// ошибкой или пустой ответ по типу видно сразу.
func checkHTML(response *http.Response) string {
	kind := response.Header.Get("Content-Type")
	if kind != "" && !strings.Contains(strings.ToLower(kind), "html") {
		return CodeNotHTML
	}
	// Пустая страница с кодом 200 — обычный признак приложения, которое
	// упало после отправки заголовков.
	body, err := read(response)
	if err != nil {
		return CodeUnreachable
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return CodeNotHTML
	}
	return ""
}

func read(response *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(response.Body, maxBody))
}

// failure переводит ошибку похода в сеть в код.
//
// Таймаут отделяется от отказа в соединении: «сервер думает дольше десяти
// секунд» и «сервер не отвечает» — разные беды с разными причинами, и
// сваливать их в одну строку значит лишить человека половины диагноза.
func failure(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return CodeTimeout
	}
	if isTimeout(err) {
		return CodeTimeout
	}
	// Ошибку TLS отделяем по тексту: `crypto/tls` и `x509` не дают одного
	// типа, за который можно зацепиться, а совет человеку тут особый —
	// смотреть сертификат, а не сеть.
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "x509") || strings.Contains(text, "tls") ||
		strings.Contains(text, "certificate") {
		return CodeTLSFailed
	}
	return CodeUnreachable
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// Describe — короткая подпись адреса для лога агента. Наружу не уезжает:
// человеку строку собирает интерфейс на его языке.
func Describe(result Result) string {
	if result.OK {
		return fmt.Sprintf("%s: ок (%d, %d мс)", result.URL, result.Status, result.DurationMs)
	}
	return fmt.Sprintf("%s: %s (%d)", result.URL, result.Code, result.Status)
}
