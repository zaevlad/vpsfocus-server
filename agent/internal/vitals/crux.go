package vitals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Адрес CrUX API. Переменной по той же причине, что и адрес PSI: тесты
// подставляют сюда свой сервер.
var cruxEndpoint = "https://chromeuxreport.googleapis.com/v1/records:queryRecord"

// Имена метрик в ответе CrUX. Список Google, менять его нельзя.
const (
	cruxLCP  = "largest_contentful_paint"
	cruxINP  = "interaction_to_next_paint"
	cruxCLS  = "cumulative_layout_shift"
	cruxFCP  = "first_contentful_paint"
	cruxTTFB = "experimental_time_to_first_byte"
)

type cruxRequest struct {
	// Либо URL, либо Origin — вместе Google их не принимает.
	URL        string   `json:"url,omitempty"`
	Origin     string   `json:"origin,omitempty"`
	FormFactor string   `json:"formFactor,omitempty"`
	Metrics    []string `json:"metrics,omitempty"`
}

type cruxDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

func (d cruxDate) String() string {
	if d.Year == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

type cruxResponse struct {
	Record struct {
		Metrics map[string]struct {
			Percentiles struct {
				// У времени это число, у CLS — строка вроде "0.12".
				// Один тип на оба случая: json.RawMessage разбирается
				// вручную ниже.
				P75 json.RawMessage `json:"p75"`
			} `json:"percentiles"`
		} `json:"metrics"`
		CollectionPeriod struct {
			FirstDate cruxDate `json:"firstDate"`
			LastDate  cruxDate `json:"lastDate"`
		} `json:"collectionPeriod"`
	} `json:"record"`
}

// runCrux забирает данные живых посетителей.
//
// Сначала спрашиваем про саму страницу, а если Google о ней ничего не знает
// (404 «chrome ux report data not found») — про сайт целиком. У страницы
// небольшого сайта своих данных почти никогда нет, а у сайта они обычно
// есть, и показать их честнее, чем показать пустоту.
//
// Возвращает ещё и число потраченных обращений: их считает вызывающий, а не
// мы, — квота общая на ключ.
func runCrux(
	ctx context.Context,
	client *http.Client,
	opts Options,
	domain, target string,
) (*Field, int, error) {
	requests := 0

	field, err := cruxQuery(ctx, client, opts, cruxRequest{
		URL:        target,
		FormFactor: formFactor(opts.Strategy),
	}, ScopeURL)
	requests++
	switch {
	case err != nil && !isNotFound(err):
		return nil, requests, err
	case err == nil && field != nil:
		return field, requests, nil
	}

	field, err = cruxQuery(ctx, client, opts, cruxRequest{
		Origin:     Origin(domain),
		FormFactor: formFactor(opts.Strategy),
	}, ScopeOrigin)
	requests++
	switch {
	case err != nil && !isNotFound(err):
		return nil, requests, err
	case err == nil && field != nil:
		return field, requests, nil
	}

	// Ни о странице, ни о сайте Google данных не собрал: посетителей в
	// Chrome слишком мало. Это состояние, а не ошибка.
	return nil, requests, nil
}

// isNotFound отличает «данных нет» от настоящего отказа: 404 у CrUX означает
// именно «столько посетителей я не видел», и обрывать из-за него круг нельзя.
func isNotFound(err error) bool {
	var api apiError
	if errors.As(err, &api) {
		return api.status == http.StatusNotFound
	}
	return false
}

func formFactor(strategy string) string {
	if strategy == StrategyDesktop {
		return "DESKTOP"
	}
	return "PHONE"
}

func cruxQuery(
	ctx context.Context,
	client *http.Client,
	opts Options,
	query cruxRequest,
	scope string,
) (*Field, error) {
	query.Metrics = []string{cruxLCP, cruxINP, cruxCLS, cruxFCP, cruxTTFB}

	payload, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cruxEndpoint+"?key="+opts.Key, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if response.StatusCode != http.StatusOK {
		return nil, googleFailure(response.StatusCode, body)
	}

	var parsed cruxResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("разбор ответа CrUX: %w", err)
	}

	field := &Field{
		Scope:     scope,
		FirstDate: parsed.Record.CollectionPeriod.FirstDate.String(),
		LastDate:  parsed.Record.CollectionPeriod.LastDate.String(),
	}

	value := func(name string) (float64, bool) {
		entry, ok := parsed.Record.Metrics[name]
		if !ok {
			return 0, false
		}
		return parseP75(entry.Percentiles.P75)
	}

	if raw, ok := value(cruxLCP); ok {
		rated := metric(raw, lcpGood, lcpPoor)
		field.LCP = &rated
	}
	if raw, ok := value(cruxINP); ok {
		rated := metric(raw, inpGood, inpPoor)
		field.INP = &rated
	}
	if raw, ok := value(cruxCLS); ok {
		rated := metric(raw, clsGood, clsPoor)
		field.CLS = &rated
	}
	if raw, ok := value(cruxFCP); ok {
		rated := metric(raw, fcpGood, fcpPoor)
		field.FCP = &rated
	}
	if raw, ok := value(cruxTTFB); ok {
		rated := metric(raw, ttfbGood, ttfbPoor)
		field.TTFB = &rated
	}

	// Ответ есть, а метрик в нём нет — считаем, что данных нет вовсе:
	// пустая карточка «данные посетителей» хуже честного «их пока мало».
	if field.LCP == nil && field.INP == nil && field.CLS == nil &&
		field.FCP == nil && field.TTFB == nil {
		return nil, nil
	}

	return field, nil
}

// parseP75 разбирает 75-й процентиль: у времени это число, у CLS — строка.
func parseP75(raw json.RawMessage) (float64, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false
	}

	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
