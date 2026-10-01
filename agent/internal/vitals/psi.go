package vitals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"agent/internal/text"
)

// Адрес PageSpeed Insights. Переменной, а не константой: тесты подставляют
// сюда свой сервер — ходить в настоящий Google из тестов нельзя ни по
// скорости, ни по квоте.
var psiEndpoint = "https://www.googleapis.com/pagespeedonline/v5/runPagespeed"

// psiResponse — то, что нам нужно из ответа PSI.
//
// Полный ответ — сотни килобайт разобранного HTML и скриншотов страницы
// клиента. Разбираем числа, оценки категорий и советы (трек P); скриншоты и
// трассировки не наше дело, и хранить их на сервере клиента незачем.
type psiResponse struct {
	LighthouseResult struct {
		LighthouseVersion string `json:"lighthouseVersion"`
		RuntimeError      *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"runtimeError"`
		Categories map[string]psiCategory `json:"categories"`
		Audits     map[string]psiAudit    `json:"audits"`
	} `json:"lighthouseResult"`
}

// googleError — общий для обоих API формат отказа.
type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// runPageSpeed просит Lighthouse измерить страницу.
//
// Запрос идёт с сервера клиента, но грузит страницу не он: PSI открывает её
// своим браузером из инфраструктуры Google. Поэтому канал сервера на
// результат не влияет, а сама страница обязана быть доступна из интернета.
func runPageSpeed(ctx context.Context, client *http.Client, opts Options, target string) (*Lab, error) {
	query := url.Values{}
	query.Set("url", target)
	query.Set("strategy", opts.Strategy)
	// Все четыре категории одним запросом (трек P): Lighthouse считает их за
	// один проход, и квота тратится одна. По одной на категорию было бы
	// вчетверо дороже.
	for _, category := range categoryOrder {
		query.Add("category", category)
	}
	// Названия проверок и советы Lighthouse переводит сам — на язык агента,
	// тот же, что у еженедельного отчёта.
	if opts.Locale != "" {
		query.Set("locale", opts.Locale)
	}
	query.Set("key", opts.Key)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, psiEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	// Потолок на чтение: ответ PSI и так велик, а полагаться на вежливость
	// чужого сервера, читая его в память сервера клиента, не стоит.
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	if response.StatusCode != http.StatusOK {
		return nil, googleFailure(response.StatusCode, body)
	}

	var parsed psiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("разбор ответа PSI: %w", err)
	}

	result := parsed.LighthouseResult
	if result.RuntimeError != nil && result.RuntimeError.Code != "" {
		// Lighthouse дошёл до страницы и не смог её измерить: чаще всего
		// она не открывается, отдаёт ошибку или требует авторизации.
		return nil, apiError{
			code:    ErrTargetUnreachable,
			status:  response.StatusCode,
			message: result.RuntimeError.Code + ": " + result.RuntimeError.Message,
		}
	}
	performance, ok := result.Categories["performance"]
	if !ok || performance.Score == nil {
		return nil, apiError{
			code:    ErrRequestFailed,
			status:  response.StatusCode,
			message: "в ответе нет оценки производительности",
		}
	}

	audit := func(id string) float64 {
		value, ok := result.Audits[id]
		if !ok || value.NumericValue == nil {
			return 0
		}
		return *value.NumericValue
	}

	// Оценка приезжает долей от единицы, а показывается процентами.
	score := int(*performance.Score*100 + 0.5)

	var summary []psiItem
	if audit, ok := result.Audits["resource-summary"]; ok {
		summary = audit.items()
	}
	list, totalBytes, requests := resources(summary)

	optional := func(id string, good, poor float64) *Metric {
		audit, ok := result.Audits[id]
		if !ok || audit.NumericValue == nil {
			return nil
		}
		value := metric(*audit.NumericValue, good, poor)
		return &value
	}

	return &Lab{
		Score:       score,
		ScoreRating: RateScore(score),
		LCP:         metric(audit("largest-contentful-paint"), lcpGood, lcpPoor),
		CLS:         metric(audit("cumulative-layout-shift"), clsGood, clsPoor),
		TBT:         metric(audit("total-blocking-time"), tbtGood, tbtPoor),
		FCP:         metric(audit("first-contentful-paint"), fcpGood, fcpPoor),
		SpeedIndex:  metric(audit("speed-index"), siGood, siPoor),
		Lighthouse:  result.LighthouseVersion,

		TTFB:        optional("server-response-time", ttfbGood, ttfbPoor),
		Interactive: optional("interactive", ttiGood, ttiPoor),
		TotalBytes:  totalBytes,
		Requests:    requests,
		Resources:   list,
		Categories:  categories(result.Categories),
		Advice:      advice(result.Categories, result.Audits),
	}, nil
}

// googleFailure разбирает отказ обоих API до кода интерфейса.
//
// Различать их обязательно: «ключ не подошёл» чинится в настройках, «слишком
// часто» пройдёт само, а «страница не открылась» — беда сайта клиента, и
// показывать её как нашу поломку нельзя.
func googleFailure(status int, body []byte) error {
	var parsed googleError
	_ = json.Unmarshal(body, &parsed)
	message := strings.TrimSpace(parsed.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	// По символам, а не по байтам: ответ Google бывает и по-русски, и
	// оборванная посередине руна уехала бы в лог агента мусором. Правило
	// записано без исключений — «здесь можно» однажды переезжает туда, где
	// нельзя.
	message = text.Truncate(message, 500)

	lower := strings.ToLower(message)
	code := ErrRequestFailed

	switch {
	case status == http.StatusTooManyRequests,
		parsed.Error.Status == "RESOURCE_EXHAUSTED":
		code = ErrRateLimited
	case status == http.StatusUnauthorized,
		status == http.StatusForbidden,
		strings.Contains(lower, "api key"),
		strings.Contains(lower, "api_key"),
		strings.Contains(lower, "has not been used"),
		parsed.Error.Status == "PERMISSION_DENIED":
		// Сюда же попадает не включённый в проекте API: Google отвечает
		// «has not been used in project … before or it is disabled».
		code = ErrKeyRejected
	case strings.Contains(lower, "errored_document_request"),
		strings.Contains(lower, "failed_document_request"),
		strings.Contains(lower, "dns_failure"),
		strings.Contains(lower, "unable to reach"),
		strings.Contains(lower, "lighthouse returned error"):
		code = ErrTargetUnreachable
	}

	return apiError{code: code, status: status, message: message}
}
