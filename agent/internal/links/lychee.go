package links

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report — итог одного круга по одному сайту.
type Report struct {
	Domain string `json:"domain"`
	// Когда начался круг.
	CheckedAt string `json:"checkedAt"`
	// Сколько длился весь круг: обход плюс проверка.
	DurationSeconds int64 `json:"durationSeconds"`
	// Сколько страниц сайта обошёл краулер.
	Pages int `json:"pages"`
	// Сколько ссылок увидел lychee на этих страницах.
	TotalLinks int `json:"totalLinks"`
	// Сколько уникальных ссылок оказалось битыми.
	Broken   int       `json:"broken"`
	Failures []Failure `json:"failures"`
	// Почему круг не состоялся. Пусто — всё получилось.
	Error string `json:"error,omitempty"`

	// История прошлых кругов по этому сайту, от старых к новым. Без
	// деталей: детали хранит только последний отчёт.
	History []HistoryPoint `json:"history,omitempty"`
}

// Failure — битая ссылка со всеми страницами, где она встретилась.
type Failure struct {
	URL string `json:"url"`
	// Стабильный код причины: httpStatus | timeout | network | unsupported.
	// Переводится интерфейсом; Detail при этом техническая подробность,
	// а не текст для человека.
	Kind   string `json:"kind"`
	Code   int    `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Страницы-источники, на которых найдена ссылка.
	Sources []string `json:"sources"`
}

// HistoryPoint — сводка прошлого круга без деталей.
type HistoryPoint struct {
	CheckedAt  string `json:"checkedAt"`
	Broken     int    `json:"broken"`
	TotalLinks int    `json:"totalLinks"`
}

// Options — как звать lychee.
type Options struct {
	// Путь к бинарнику. Задаётся окружением, чтобы тот же код работал
	// и в образе, и на машине разработчика.
	Binary string
	// Одновременные запросы. По умолчанию консервативно: мы на проде
	// клиента, а не на стенде производительности.
	Concurrency int
	// Таймаут одного запроса в секундах.
	TimeoutSeconds int
	// Регулярки исключений для lychee. То же множество, что режет обход:
	// страницу, которую не краулим, незачем и проверять в ней ссылки.
	Exclude []string
}

// Check прогоняет список страниц через lychee и собираёт отчёт.
//
// Возвращённый отчёт всегда годный: даже когда битых ссылок нет или часть
// ссылок исключена. Ошибка возвращается только когда проверки не было
// вовсе — нет бинарника, нет списка страниц.
// Scan — полный круг по одному сайту: обход плюс проверка найденного.
//
// Круг собран здесь, а не в главном цикле агента, потому что здесь же
// собирается отчёт. Pages и DurationSeconds знает только тот, кто держит в
// руках оба шага: lychee не знает, сколько страниц обошёл краулер, а краулер
// не знает, сколько заняла проверка. Когда сборка жила в вызывающем, оба
// поля терялись — «Страниц: 0» показывалось всегда, а длительностью круга
// называлась длительность одного lychee.
//
// Отчёт возвращается и вместе с ошибкой: в нём уже есть домен и время
// начала, и вызывающему остаётся проставить код ошибки. Молчание вместо
// отчёта означало бы пустой экран там, где человеку нужно «не получилось».
func Scan(ctx context.Context, client *http.Client, siteURL string, limits Limits, opts Options) (Report, error) {
	started := time.Now().UTC()
	report := Report{
		CheckedAt: started.Format(time.RFC3339),
		Failures:  []Failure{},
	}
	if parsed, err := url.Parse(siteURL); err == nil {
		report.Domain = parsed.Host
	}
	finish := func(r Report) Report {
		r.DurationSeconds = int64(time.Since(started).Seconds())
		return r
	}

	pages, err := Crawl(ctx, client, siteURL, limits)
	if err != nil {
		return finish(report), err
	}
	report.Pages = len(pages)

	checked, err := Check(ctx, opts, report.Domain, pages)
	if err != nil {
		return finish(report), err
	}

	checked.Domain = report.Domain
	checked.CheckedAt = report.CheckedAt
	checked.Pages = report.Pages
	return finish(checked), nil
}

func Check(ctx context.Context, opts Options, domain string, pages []string) (Report, error) {
	report := Report{
		Domain:   domain,
		Failures: []Failure{},
	}

	if len(pages) == 0 {
		return report, nil
	}

	output, err := runLycheeFunc(ctx, opts, pages)
	if err != nil {
		return report, err
	}

	report.DurationSeconds = int64(output.Duration.Seconds)
	report.TotalLinks = output.Total

	byURL := map[string]*Failure{}

	// Битые ссылки приходят сгруппированными по странице-источнику.
	// Экрану нужен обратный разрез: одна ссылка — все её источники.
	for _, group := range []map[string][]lycheeEntry{
		output.ErrorMap,
		output.TimeoutMap,
	} {
		for source, entries := range group {
			for _, entry := range entries {
				existing, ok := byURL[entry.URL]
				if !ok {
					kind, code := classify(entry.Status)
					existing = &Failure{
						URL:     entry.URL,
						Kind:    kind,
						Code:    code,
						Detail:  detail(entry.Status),
						Sources: []string{},
					}
					byURL[entry.URL] = existing
				}
				if !contains(existing.Sources, source) {
					existing.Sources = append(existing.Sources, source)
				}
			}
		}
	}

	for _, failure := range byURL {
		report.Failures = append(report.Failures, *failure)
	}
	sortFailures(report.Failures)

	report.Broken = len(report.Failures)
	return report, nil
}

// runLycheeFunc — точка подмены для тестов: настоящий запуск требует
// бинарника и сети, а логика сборки отчёта проверяется и без них.
var runLycheeFunc = runLychee

// runLychee запускает бинарник и разбирает его JSON-вывод.
//
// Код возврата lychee ничего не значит для нас: он отличает «битые ссылки
// есть» от «битых нет», а обе ситуации — нормальный результат проверки.
// Годится любой запуск, чей stdout разобрался в JSON.
func runLychee(ctx context.Context, opts Options, pages []string) (*lycheeOutput, error) {
	list, err := os.CreateTemp("", "vpsfocus-pages-*.txt")
	if err != nil {
		return nil, fmt.Errorf("список страниц: %w", err)
	}
	defer os.Remove(list.Name())

	for _, page := range pages {
		// Адрес с переносом строки — не наш адрес: он сломал бы файл
		// входов молча. Отбрасываем такие заранее.
		if strings.ContainsAny(page, "\r\n") {
			continue
		}
		fmt.Fprintln(list, page)
	}
	list.Close()

	args := []string{
		"--format", "json",
		"--no-progress",
		"--files-from", list.Name(),
		fmt.Sprintf("--max-concurrency=%d", opts.Concurrency),
		fmt.Sprintf("--timeout=%d", opts.TimeoutSeconds),
		// Повторы только удлинили бы круг: недоступная сейчас ссылка
		// перепроверится следующим кругом по расписанию.
		"--max-retries=1",
	}
	for _, pattern := range opts.Exclude {
		args = append(args, "--exclude", pattern)
	}

	cmd := exec.CommandContext(ctx, opts.Binary, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil && stdout.Len() == 0 {
		// Ни байта вывода — значит, проверки не было: нет бинарника или
		// он упал до начала работы.
		//
		// Ошибка заворачивается через %w, а не %v: по ней вызывающий
		// отличает круг, убитый нашим же таймаутом, от круга, который
		// сломался. С %v причина внутрь не пролезала, errors.Is не
		// срабатывал никогда, и «обход не уложился в отведённое время»
		// показывалось как «обход не удался».
		return nil, fmt.Errorf("lychee: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}

	output, err := parseLychee(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("вывод lychee не разобрался: %w", err)
	}
	return output, nil
}

// parseLychee разбирает JSON-вывод lychee.
func parseLychee(raw string) (*lycheeOutput, error) {
	var output lycheeOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return nil, err
	}
	return &output, nil
}

// classify превращает статус lychee в стабильный код и HTTP-код.
//
// Текст статуса — человеческое сообщение из чужой библиотеки: опираться на
// него можно только по префиксу, поэтому каждый вариант помечен своим кодом.
func classify(status lycheeStatus) (kind string, code int) {
	switch {
	case status.Code != nil:
		return "httpStatus", *status.Code
	case strings.HasPrefix(status.Text, "Timeout"):
		return "timeout", 0
	case strings.HasPrefix(status.Text, "Unsupported"):
		return "unsupported", 0
	default:
		return "network", 0
	}
}

func detail(status lycheeStatus) string {
	if status.Details != "" {
		return status.Details
	}
	return status.Text
}

func contains(list []string, item string) bool {
	for _, existing := range list {
		if existing == item {
			return true
		}
	}
	return false
}

// sortFailures ставит худшее выше: сначала HTTP-ошибки, потом таймауты,
// потом сетевые отказы; внутри группы — по адресу.
func sortFailures(failures []Failure) {
	rank := map[string]int{"httpStatus": 0, "timeout": 1, "network": 2, "unsupported": 3}
	sort.Slice(failures, func(i, j int) bool {
		a, b := failures[i], failures[j]
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		return a.URL < b.URL
	})
}

// ── Формы ответа lychee ──────────────────────────────────────────────────
//
// Сняты с живого бинарника v0.24.2, а не взяты из документации. Числа
// приходят числами, но поля вида code бывают не во всех вариантах статуса —
// поэтому указатель, а не значение.

type lycheeOutput struct {
	Total      int                      `json:"total"`
	Unique     int                      `json:"unique"`
	Successful int                      `json:"successful"`
	Errors     int                      `json:"errors"`
	ErrorMap   map[string][]lycheeEntry `json:"error_map"`
	TimeoutMap map[string][]lycheeEntry `json:"timeout_map"`
	Duration   struct {
		Seconds int64 `json:"secs"`
	} `json:"duration"`
}

type lycheeEntry struct {
	URL    string       `json:"url"`
	Status lycheeStatus `json:"status"`
}

type lycheeStatus struct {
	Text    string `json:"text"`
	Code    *int   `json:"code"`
	Details string `json:"details"`
}

// BinaryName — как называется бинарник на разных платформах. Нужен тестам
// на машинах без образа: Windows добавляет .exe.
func BinaryName(base string) string {
	if filepath.Separator == '\\' {
		return base + ".exe"
	}
	return base
}
