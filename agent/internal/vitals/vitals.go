// Package vitals измеряет производительность сайтов клиента глазами Google.
//
// Два источника, и путать их нельзя. Lab — то, что Lighthouse намерил в
// лаборатории PageSpeed Insights: одна загрузка, стабильные условия, есть
// сразу и у нового сайта. Field — то, что за последние 28 дней видели живые
// посетители в Chrome (отчёт CrUX); именно по нему Google судит о сайте, но
// у страницы без трафика его просто нет.
//
// Раньше field-данные приезжали вместе с lab одним ответом PSI. Google
// собирается это прекратить («We plan to discontinue including real-world
// data from the Chrome User Experience Report in this API») и отправляет за
// ними в CrUX API напрямую — поэтому здесь два клиента, а не один.
//
// Запросы делает агент с сервера клиента, своим ключом агентства. Ключ —
// такой же секрет, как пароль от почты: живёт в agent.env с правами 600 и на
// backend разработчика не уходит никогда.
package vitals

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Тип устройства, для которого меряем. Google считает мобильную и настольную
// версии разными сайтами и ранжирует их отдельно.
const (
	StrategyMobile  = "mobile"
	StrategyDesktop = "desktop"
)

// Оценка метрики по порогам Google. Коды, а не текст: интерфейс двуязычный.
const (
	RatingGood             = "good"
	RatingNeedsImprovement = "needsImprovement"
	RatingPoor             = "poor"
)

// Разрез field-данных: сама страница или сайт целиком.
//
// У малопосещаемой страницы своих данных в CrUX нет, и тогда мы показываем
// данные по сайту — но говорим об этом прямо. Молча подставить одно вместо
// другого значит соврать в единственном, ради чего сюда смотрят.
const (
	ScopeURL    = "url"
	ScopeOrigin = "origin"
)

// Коды ошибок круга. Перевод — в приложении.
const (
	ErrNoKey             = "vitalsNoKey"
	ErrKeyRejected       = "vitalsKeyRejected"
	ErrRateLimited       = "vitalsRateLimited"
	ErrTargetUnreachable = "vitalsTargetUnreachable"
	ErrRequestFailed     = "vitalsRequestFailed"
	ErrTimeout           = "vitalsTimeout"
	ErrQuotaExceeded     = "vitalsQuotaExceeded"
	// Field-данных нет: посетителей в Chrome слишком мало, чтобы Google
	// собрал статистику. Это не сбой, и круг из-за этого не считается
	// неудачным — поэтому код кладётся отдельным полем.
	ErrFieldNoData = "vitalsFieldNoData"
)

// Metric — значение вместе с оценкой по порогам Google.
//
// Оценку считает агент, а не экран: пороги обязаны быть в одном месте. Две
// копии порогов однажды разойдутся, и «хорошо» на графике перестанет
// означать «хорошо» в карточке.
type Metric struct {
	// Миллисекунды у времени, безразмерная величина у CLS.
	Value  float64 `json:"value"`
	Rating string  `json:"rating"`
}

// Пороги Core Web Vitals. Числа Google, а не наши: подписывать своей шкалой
// то, по чему ранжирует он, значит показывать выдуманный результат.
const (
	lcpGood, lcpPoor   = 2500.0, 4000.0
	inpGood, inpPoor   = 200.0, 500.0
	clsGood, clsPoor   = 0.1, 0.25
	fcpGood, fcpPoor   = 1800.0, 3000.0
	ttfbGood, ttfbPoor = 800.0, 1800.0
	// Лабораторные, у Google в отчёте Lighthouse.
	tbtGood, tbtPoor = 200.0, 600.0
	siGood, siPoor   = 3400.0, 5800.0
	// Оценка производительности: 90 и выше зелёная, ниже 50 красная.
	scoreGood, scorePoor = 90, 50
)

// rate оценивает «меньше — лучше»: все метрики здесь именно такие.
func rate(value, good, poor float64) string {
	switch {
	case value <= good:
		return RatingGood
	case value > poor:
		return RatingPoor
	default:
		return RatingNeedsImprovement
	}
}

// RateScore оценивает саму оценку Lighthouse — у неё шкала наоборот.
func RateScore(score int) string {
	switch {
	case score >= scoreGood:
		return RatingGood
	case score < scorePoor:
		return RatingPoor
	default:
		return RatingNeedsImprovement
	}
}

func metric(value, good, poor float64) Metric {
	return Metric{Value: value, Rating: rate(value, good, poor)}
}

// Lab — замер Lighthouse из лаборатории PSI.
type Lab struct {
	// Оценка производительности, 0–100.
	Score       int    `json:"score"`
	ScoreRating string `json:"scoreRating"`

	LCP Metric `json:"lcp"`
	CLS Metric `json:"cls"`
	// INP в лаборатории не измеряется вовсе: там нет посетителя, который бы
	// нажимал на кнопки. TBT — общепринятая замена, и подписан он в
	// интерфейсе именно так, а не как INP.
	TBT        Metric `json:"tbt"`
	FCP        Metric `json:"fcp"`
	SpeedIndex Metric `json:"speedIndex"`

	// Версия Lighthouse. Между её выпусками числа заметно разъезжаются, и
	// без этой подписи падение оценки списали бы на правку сайта.
	Lighthouse string `json:"lighthouse,omitempty"`

	// Подробности (трек P) — из того же ответа PSI. Указатели и omitempty:
	// замеры, сохранённые агентом до 0.14.0, их не несут, и экран обязан
	// отличать «не мерили» от нуля.
	//
	// Время ответа сервера в лаборатории Google — не посетителя: сервер
	// клиента отвечал машине Google.
	TTFB *Metric `json:"ttfb,omitempty"`
	// Время до интерактивности.
	Interactive *Metric `json:"interactive,omitempty"`
	// Вес страницы в байтах при передаче и число запросов.
	TotalBytes int64      `json:"totalBytes,omitempty"`
	Requests   int        `json:"requests,omitempty"`
	Resources  []Resource `json:"resources,omitempty"`
	// Оценки всех категорий: производительность, доступность,
	// рекомендации, SEO.
	Categories []Category `json:"categories,omitempty"`
	// Что исправить.
	Advice []Advice `json:"advice,omitempty"`
}

// Field — то, что видели настоящие посетители за окно наблюдения CrUX.
type Field struct {
	Scope string `json:"scope"`
	// Окно наблюдения, обычно 28 дней. Даты в виде ГГГГ-ММ-ДД.
	FirstDate string `json:"firstDate,omitempty"`
	LastDate  string `json:"lastDate,omitempty"`

	// Указатели, потому что у сайта может не быть части метрик: INP
	// появился позже остальных, и у редко посещаемых страниц он пуст, когда
	// LCP уже есть.
	LCP  *Metric `json:"lcp,omitempty"`
	INP  *Metric `json:"inp,omitempty"`
	CLS  *Metric `json:"cls,omitempty"`
	FCP  *Metric `json:"fcp,omitempty"`
	TTFB *Metric `json:"ttfb,omitempty"`
}

// Report — итог одного замера: одна страница, один тип устройства.
type Report struct {
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	URL      string `json:"url"`
	Strategy string `json:"strategy"`
	// Когда начали мерить, RFC3339.
	CheckedAt string `json:"checkedAt"`

	// Код беды, из-за которой замера нет вовсе.
	Error string `json:"error,omitempty"`
	// Код беды только с field-частью. Отдельно, потому что «посетителей
	// мало» — это не сбой: lab при этом есть и полезен.
	FieldError string `json:"fieldError,omitempty"`

	Lab   *Lab   `json:"lab,omitempty"`
	Field *Field `json:"field,omitempty"`

	// Прошлые замеры этой же страницы, от старых к новым.
	History []HistoryPoint `json:"history,omitempty"`
}

// HistoryPoint — сводка прошлого замера для графика динамики.
//
// Не весь отчёт: экрану нужны кривые, а не тридцать копий карточки. Поля
// указателями, потому что замер мог не удаться или обойтись без field-данных.
type HistoryPoint struct {
	CheckedAt string   `json:"checkedAt"`
	Score     *int     `json:"score,omitempty"`
	LabLCP    *float64 `json:"labLcp,omitempty"`
	LabCLS    *float64 `json:"labCls,omitempty"`
	LabTBT    *float64 `json:"labTbt,omitempty"`
	FieldLCP  *float64 `json:"fieldLcp,omitempty"`
	FieldINP  *float64 `json:"fieldInp,omitempty"`
	FieldCLS  *float64 `json:"fieldCls,omitempty"`

	// Остальное, у чего экран показывает «было → стало» (трек U). Точка
	// строится при чтении из сохранённого замера, поэтому поля появляются и
	// у замеров, снятых до 0.18.0, — если сам замер их нёс (агент 0.14.0+).
	LabFCP         *float64 `json:"labFcp,omitempty"`
	LabTTFB        *float64 `json:"labTtfb,omitempty"`
	LabInteractive *float64 `json:"labInteractive,omitempty"`
	// Вес страницы, байты.
	TotalBytes *int64 `json:"totalBytes,omitempty"`
	// Оценки категорий Lighthouse по их id: accessibility, best-practices,
	// seo. Производительность — в `Score`.
	Categories map[string]int `json:"categories,omitempty"`
}

// Point сводит замер к точке графика.
//
// Считает агент, а не экран: точка обязана строиться одинаково и для свежего
// замера, и для тридцати прошлых, поднятых из базы.
func (r Report) Point() HistoryPoint {
	point := HistoryPoint{CheckedAt: r.CheckedAt}

	if r.Lab != nil {
		score := r.Lab.Score
		lcp := r.Lab.LCP.Value
		cls := r.Lab.CLS.Value
		tbt := r.Lab.TBT.Value
		point.Score = &score
		point.LabLCP = &lcp
		point.LabCLS = &cls
		point.LabTBT = &tbt

		fcp := r.Lab.FCP.Value
		point.LabFCP = &fcp
		if r.Lab.TTFB != nil {
			value := r.Lab.TTFB.Value
			point.LabTTFB = &value
		}
		if r.Lab.Interactive != nil {
			value := r.Lab.Interactive.Value
			point.LabInteractive = &value
		}
		// Ноль здесь — «замер веса не нёс», а не пустая страница.
		if r.Lab.TotalBytes > 0 {
			value := r.Lab.TotalBytes
			point.TotalBytes = &value
		}
		if len(r.Lab.Categories) > 0 {
			point.Categories = map[string]int{}
			for _, category := range r.Lab.Categories {
				point.Categories[category.ID] = category.Score
			}
		}
	}

	if r.Field != nil {
		if r.Field.LCP != nil {
			value := r.Field.LCP.Value
			point.FieldLCP = &value
		}
		if r.Field.INP != nil {
			value := r.Field.INP.Value
			point.FieldINP = &value
		}
		if r.Field.CLS != nil {
			value := r.Field.CLS.Value
			point.FieldCLS = &value
		}
	}

	return point
}

// Options — что нужно знать одному замеру.
type Options struct {
	// Ключ Google Cloud агентства. Пустой означает, что мерить нечем.
	Key string
	// mobile или desktop.
	Strategy string
	// Язык советов Lighthouse: ru или en, как у еженедельного отчёта.
	// Пустой — английский по умолчанию Google.
	Locale string
	// Таймаут одного обращения к API. PSI грузит страницу по-настоящему и
	// думает десятками секунд — короткий таймаут здесь означает «всегда
	// не успели».
	Timeout time.Duration
}

// HTTPClient — клиент для обращений к Google.
//
// Отдельный от клиента краулера: у того таймауты в секундах, а PSI отвечает
// минуту и это норма.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// Measure меряет одну страницу и возвращает отчёт вместе с числом
// потраченных обращений к API.
//
// Число возвращается наружу, а не считается внутри: квота общая на ключ, а
// ключ общий на все серверы агентства, и учёт ведёт тот, кто видит базу.
func Measure(ctx context.Context, client *http.Client, opts Options, domain, path string) (Report, int) {
	started := time.Now().UTC()
	target := PageURL(domain, path)

	report := Report{
		Domain:    domain,
		Path:      normalizePath(path),
		URL:       target,
		Strategy:  opts.Strategy,
		CheckedAt: started.Format(time.RFC3339),
	}

	if strings.TrimSpace(opts.Key) == "" {
		report.Error = ErrNoKey
		return report, 0
	}

	spent := 0

	lab, err := runPageSpeed(ctx, client, opts, target)
	spent++
	if err != nil {
		report.Error = classify(err)
		// Lab не получился — за field всё равно сходим: причина у них
		// разная, и «страница не грузится у Lighthouse» не значит, что
		// Google ничего не знает о сайте.
	} else {
		report.Lab = lab
	}

	field, requests, err := runCrux(ctx, client, opts, domain, target)
	spent += requests
	switch {
	case err != nil:
		report.FieldError = classify(err)
	case field == nil:
		report.FieldError = ErrFieldNoData
	default:
		report.Field = field
	}

	// Ключ не приняли — это беда всего круга, а не одной страницы: дальше
	// будет то же самое. Пусть код видно и в главной ошибке отчёта.
	if report.Error == "" && report.FieldError == ErrKeyRejected {
		report.Error = ErrKeyRejected
	}

	return report, spent
}

// PageURL собирает адрес страницы. Только https: стек ставит сертификат, а
// мерить сайт по http значит мерить не то, что видит посетитель.
func PageURL(domain, path string) string {
	return "https://" + domain + normalizePath(path)
}

// Origin — адрес сайта целиком, каким его знает CrUX.
func Origin(domain string) string {
	return "https://" + domain
}

// normalizePath приводит путь к виду, который примет Google.
func normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}

// apiError — отказ Google, разобранный до кода.
type apiError struct {
	code    string
	status  int
	message string
}

func (e apiError) Error() string {
	return fmt.Sprintf("%s (HTTP %d): %s", e.code, e.status, e.message)
}

// classify сводит ошибку к коду для интерфейса. Текст уезжает в лог сервера,
// но не в перевод: интерфейс двуязычный, а сообщения Google — нет.
func classify(err error) string {
	var api apiError
	if errors.As(err, &api) {
		return api.code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrTimeout
	}
	// http.Client отдаёт свой таймаут обёрткой url.Error, из которой
	// DeadlineExceeded достаётся не всегда: смотрим ещё и на текст.
	if strings.Contains(err.Error(), "Client.Timeout") {
		return ErrTimeout
	}
	return ErrRequestFailed
}
