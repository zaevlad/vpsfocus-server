package vitals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Пороги — числа Google, а не наши. Ошибка в них означает, что «хорошо» в
// нашем интерфейсе не совпадает с «хорошо» в его отчёте, а именно за этим
// совпадением сюда и приходят.
func TestRatingsFollowGoogleThresholds(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"LCP ровно на границе хорошего", metric(2500, lcpGood, lcpPoor).Rating, RatingGood},
		{"LCP чуть хуже", metric(2501, lcpGood, lcpPoor).Rating, RatingNeedsImprovement},
		{"LCP на границе плохого", metric(4000, lcpGood, lcpPoor).Rating, RatingNeedsImprovement},
		{"LCP за границей", metric(4001, lcpGood, lcpPoor).Rating, RatingPoor},
		{"CLS хороший", metric(0.1, clsGood, clsPoor).Rating, RatingGood},
		{"CLS плохой", metric(0.26, clsGood, clsPoor).Rating, RatingPoor},
		{"INP на границе", metric(200, inpGood, inpPoor).Rating, RatingGood},
		{"INP плохой", metric(501, inpGood, inpPoor).Rating, RatingPoor},
		{"оценка 90 — зелёная", RateScore(90), RatingGood},
		{"оценка 89 — средняя", RateScore(89), RatingNeedsImprovement},
		{"оценка 49 — красная", RateScore(49), RatingPoor},
	}

	for _, item := range cases {
		if item.got != item.want {
			t.Errorf("%s: получили %s, ждали %s", item.name, item.got, item.want)
		}
	}
}

// Урезанный ответ PSI: только то, что мы разбираем.
const psiFixture = `{
  "lighthouseResult": {
    "lighthouseVersion": "13.0.0",
    "categories": {"performance": {"score": 0.87}},
    "audits": {
      "largest-contentful-paint": {"numericValue": 2712.5},
      "cumulative-layout-shift": {"numericValue": 0.031},
      "total-blocking-time": {"numericValue": 150},
      "first-contentful-paint": {"numericValue": 1200},
      "speed-index": {"numericValue": 3100}
    }
  }
}`

const cruxFixture = `{
  "record": {
    "metrics": {
      "largest_contentful_paint": {"percentiles": {"p75": 1900}},
      "interaction_to_next_paint": {"percentiles": {"p75": 240}},
      "cumulative_layout_shift": {"percentiles": {"p75": "0.12"}},
      "first_contentful_paint": {"percentiles": {"p75": 1100}},
      "experimental_time_to_first_byte": {"percentiles": {"p75": 700}}
    },
    "collectionPeriod": {
      "firstDate": {"year": 2026, "month": 8, "day": 7},
      "lastDate": {"year": 2026, "month": 9, "day": 3}
    }
  }
}`

const notFoundFixture = `{"error": {"code": 404, "message": "chrome ux report data not found", "status": "NOT_FOUND"}}`

// stub поднимает подставных Google: PSI и CrUX на одном сервере, ответы
// задаёт вызывающий. Ходить в настоящий Google из тестов нельзя ни по
// скорости, ни по квоте.
func stub(t *testing.T, psi http.HandlerFunc, crux http.HandlerFunc) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "runPagespeed") {
			psi(w, r)
			return
		}
		crux(w, r)
	}))
	t.Cleanup(server.Close)

	psiWas, cruxWas := psiEndpoint, cruxEndpoint
	psiEndpoint = server.URL + "/pagespeedonline/v5/runPagespeed"
	cruxEndpoint = server.URL + "/v1/records:queryRecord"
	t.Cleanup(func() { psiEndpoint, cruxEndpoint = psiWas, cruxWas })
}

func options() Options {
	return Options{Key: "test-key", Strategy: StrategyMobile, Timeout: 10 * time.Second}
}

func TestMeasureReadsBothSources(t *testing.T) {
	stub(t,
		func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("strategy"); got != StrategyMobile {
				t.Errorf("тип устройства не доехал: %q", got)
			}
			if got := r.URL.Query().Get("url"); got != "https://site.test/catalog" {
				t.Errorf("адрес не доехал: %q", got)
			}
			_, _ = w.Write([]byte(psiFixture))
		},
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(cruxFixture))
		})

	report, spent := Measure(t.Context(), HTTPClient(time.Second*10), options(), "site.test", "/catalog")

	if report.Error != "" {
		t.Fatalf("замер не удался: %s", report.Error)
	}
	if spent != 2 {
		t.Errorf("обращений потрачено %d, ждали 2", spent)
	}
	if report.Lab == nil || report.Lab.Score != 87 {
		t.Fatalf("оценка разобрана неверно: %+v", report.Lab)
	}
	if report.Lab.LCP.Value != 2712.5 || report.Lab.LCP.Rating != RatingNeedsImprovement {
		t.Errorf("LCP лаборатории: %+v", report.Lab.LCP)
	}
	if report.Lab.Lighthouse != "13.0.0" {
		t.Errorf("версия Lighthouse потеряна: %q", report.Lab.Lighthouse)
	}
	if report.Field == nil || report.Field.Scope != ScopeURL {
		t.Fatalf("field-данные не разобраны: %+v", report.Field)
	}
	// CLS у CrUX приезжает строкой, а не числом: разбор обязан это пережить.
	if report.Field.CLS == nil || report.Field.CLS.Value != 0.12 {
		t.Errorf("CLS посетителей: %+v", report.Field.CLS)
	}
	if report.Field.INP == nil || report.Field.INP.Rating != RatingNeedsImprovement {
		t.Errorf("INP посетителей: %+v", report.Field.INP)
	}
	if report.Field.LastDate != "2026-09-03" {
		t.Errorf("окно наблюдения: %q", report.Field.LastDate)
	}
}

// У страницы небольшого сайта своих данных в CrUX почти никогда нет.
// Показать вместо них данные сайта целиком — честно, но только если сказано,
// чьи они.
func TestFieldFallsBackToOrigin(t *testing.T) {
	var asked []string

	stub(t,
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(psiFixture)) },
		func(w http.ResponseWriter, r *http.Request) {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			asked = append(asked, string(body))

			if strings.Contains(string(body), `"url"`) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(notFoundFixture))
				return
			}
			_, _ = w.Write([]byte(cruxFixture))
		})

	report, spent := Measure(t.Context(), HTTPClient(time.Second*10), options(), "site.test", "/")

	if report.Field == nil {
		t.Fatalf("после отказа по странице ждали данные сайта: %+v", report)
	}
	if report.Field.Scope != ScopeOrigin {
		t.Errorf("разрез должен быть по сайту, получили %q", report.Field.Scope)
	}
	if spent != 3 {
		t.Errorf("обращений потрачено %d, ждали 3", spent)
	}
	if len(asked) != 2 || !strings.Contains(asked[1], `"origin":"https://site.test"`) {
		t.Errorf("второй запрос ушёл не по сайту: %v", asked)
	}
}

// «Посетителей мало» — это не сбой круга: lab-данные при этом есть и
// полезны, и портить ими отчёт нельзя.
func TestNoFieldDataIsNotAFailure(t *testing.T) {
	stub(t,
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(psiFixture)) },
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFoundFixture))
		})

	report, _ := Measure(t.Context(), HTTPClient(time.Second*10), options(), "site.test", "/")

	if report.Error != "" {
		t.Errorf("круг не должен считаться неудачным: %s", report.Error)
	}
	if report.FieldError != ErrFieldNoData {
		t.Errorf("ждали код «данных нет», получили %q", report.FieldError)
	}
	if report.Lab == nil {
		t.Error("lab-данные обязаны остаться")
	}
}

// Ключ, который Google не принял, чинится в настройках приложения — и
// сказать об этом надо именно так, а не «что-то пошло не так».
func TestRejectedKeyIsRecognised(t *testing.T) {
	deny := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED",` +
			`"message":"PageSpeed Insights API has not been used in project 42 before or it is disabled"}}`))
	}

	stub(t, deny, deny)

	report, _ := Measure(t.Context(), HTTPClient(time.Second*10), options(), "site.test", "/")

	if report.Error != ErrKeyRejected {
		t.Errorf("ждали %s, получили %q", ErrKeyRejected, report.Error)
	}
}

// Страница, которую Lighthouse не смог открыть, — беда сайта клиента, а не
// нашей связи с Google: коды разные, и совет пользователю тоже.
func TestUnreachablePageHasItsOwnCode(t *testing.T) {
	stub(t,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"lighthouseResult":{"runtimeError":` +
				`{"code":"ERRORED_DOCUMENT_REQUEST","message":"Lighthouse не смог загрузить страницу"}}}`))
		},
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(cruxFixture)) })

	report, _ := Measure(t.Context(), HTTPClient(time.Second*10), options(), "site.test", "/")

	if report.Error != ErrTargetUnreachable {
		t.Errorf("ждали %s, получили %q", ErrTargetUnreachable, report.Error)
	}
	// Lab не получился, но данные посетителей за прошлые недели у Google
	// есть: одно не отменяет другого.
	if report.Field == nil {
		t.Error("field-данные обязаны остаться")
	}
}

// Без ключа не ходим никуда вовсе: у PSI без него квота в пару запросов, и
// упереться в неё молча хуже, чем честно сказать, чего не хватает.
func TestNoKeyCostsNothing(t *testing.T) {
	stub(t,
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("без ключа в PSI ходить нельзя")
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("без ключа в CrUX ходить нельзя")
		})

	opts := options()
	opts.Key = "  "
	report, spent := Measure(t.Context(), HTTPClient(time.Second*10), opts, "site.test", "/")

	if report.Error != ErrNoKey || spent != 0 {
		t.Errorf("получили %q, потратили %d обращений", report.Error, spent)
	}
}

func TestPageURLAlwaysHTTPS(t *testing.T) {
	cases := map[string]string{
		"":         "https://site.test/",
		"/":        "https://site.test/",
		"catalog":  "https://site.test/catalog",
		"/catalog": "https://site.test/catalog",
	}

	for path, want := range cases {
		if got := PageURL("site.test", path); got != want {
			t.Errorf("путь %q: получили %s, ждали %s", path, got, want)
		}
	}
}

// Точка графика собирается из замера в одном месте — иначе свежий замер и
// поднятый из базы рисовались бы по-разному.
func TestPointCarriesBothSources(t *testing.T) {
	lcp := metric(1900, lcpGood, lcpPoor)
	report := Report{
		CheckedAt: "2026-09-03T10:00:00Z",
		Lab: &Lab{
			Score: 87,
			LCP:   metric(2712, lcpGood, lcpPoor),
			CLS:   metric(0.03, clsGood, clsPoor),
			TBT:   metric(150, tbtGood, tbtPoor),
		},
		Field: &Field{LCP: &lcp},
	}

	point := report.Point()

	if point.Score == nil || *point.Score != 87 {
		t.Errorf("оценка: %+v", point.Score)
	}
	if point.LabLCP == nil || *point.LabLCP != 2712 {
		t.Errorf("LCP лаборатории: %+v", point.LabLCP)
	}
	if point.FieldLCP == nil || *point.FieldLCP != 1900 {
		t.Errorf("LCP посетителей: %+v", point.FieldLCP)
	}
	// INP посетителей не было — и выдумывать ноль вместо него нельзя: ноль
	// на графике означал бы идеальную отзывчивость.
	if point.FieldINP != nil {
		t.Errorf("INP взялся из ниоткуда: %+v", point.FieldINP)
	}
}

// ── Трек P: подробности из живого ответа Lighthouse 13.5.0 ──────────────

// liveLab разбирает урезанный настоящий ответ PSI для getgliph.com
// (мобильный, locale=ru, все четыре категории).
func liveLab(t *testing.T) *Lab {
	t.Helper()
	raw, err := os.ReadFile("testdata/psi-getgliph-mobile.json")
	if err != nil {
		t.Fatal(err)
	}
	var captured url.Values
	stub(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		_, _ = w.Write(raw)
	}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(notFoundFixture))
	})

	report, _ := Measure(context.Background(), HTTPClient(5*time.Second), Options{
		Key: "k", Strategy: "mobile", Timeout: 5 * time.Second, Locale: "ru",
	}, "getgliph.com", "/")
	if report.Lab == nil {
		t.Fatalf("замера нет: %+v", report)
	}

	// Все четыре категории — одним запросом, и на языке агента.
	if got := captured["category"]; len(got) != 4 {
		t.Errorf("категорий в запросе %v, ждали четыре", got)
	}
	if captured.Get("locale") != "ru" {
		t.Errorf("язык советов не ушёл в запрос: %q", captured.Get("locale"))
	}
	return report.Lab
}

func TestLiveResponseGivesAllCategories(t *testing.T) {
	lab := liveLab(t)

	want := map[string]int{"performance": 62, "accessibility": 93, "best-practices": 100, "seo": 100}
	if len(lab.Categories) != 4 {
		t.Fatalf("категорий %d: %+v", len(lab.Categories), lab.Categories)
	}
	for _, category := range lab.Categories {
		if category.Score != want[category.ID] {
			t.Errorf("%s: оценка %d, ждали %d", category.ID, category.Score, want[category.ID])
		}
		if category.Title == "" {
			t.Errorf("%s без названия", category.ID)
		}
	}
	// Оценка производительности та же, что в плитке «Оценка».
	if lab.Score != 62 {
		t.Errorf("оценка %d", lab.Score)
	}
}

func TestLiveResponseGivesWeightAndServerTime(t *testing.T) {
	lab := liveLab(t)

	if lab.TotalBytes != 1461073 || lab.Requests != 25 {
		t.Errorf("вес %d, запросов %d", lab.TotalBytes, lab.Requests)
	}
	if lab.TTFB == nil || lab.TTFB.Value != 7 || lab.TTFB.Rating != RatingGood {
		t.Errorf("время ответа сервера: %+v", lab.TTFB)
	}
	if lab.Interactive == nil || lab.Interactive.Rating != RatingPoor {
		t.Errorf("время до интерактивности: %+v", lab.Interactive)
	}
	// Разбивка без итога и без «сторонних», тяжёлые первыми.
	if len(lab.Resources) == 0 || lab.Resources[0].Type != "script" {
		t.Fatalf("ресурсы: %+v", lab.Resources)
	}
	var sum int64
	for _, resource := range lab.Resources {
		if resource.Type == "total" || resource.Type == "third-party" || resource.Requests == 0 {
			t.Errorf("лишняя строка: %+v", resource)
		}
		sum += resource.Bytes
	}
	if sum > lab.TotalBytes {
		t.Errorf("типы весят %d — больше страницы %d", sum, lab.TotalBytes)
	}
}

func TestLiveResponseGivesAdvice(t *testing.T) {
	lab := liveLab(t)

	byID := map[string]Advice{}
	for _, item := range lab.Advice {
		byID[item.ID] = item
	}

	// Самый дорогой совет производительности — первым.
	if lab.Advice[0].ID != "unused-javascript" || lab.Advice[0].SavingsMs != 1800 {
		t.Errorf("первый совет: %+v", lab.Advice[0])
	}
	unused := byID["unused-javascript"]
	if unused.Title != "Удалите неиспользуемый код JavaScript" {
		t.Errorf("совет не по-русски: %q", unused.Title)
	}
	if len(unused.Items) == 0 || !strings.Contains(unused.Items[0].Label, "/_next/static/") || unused.Items[0].Bytes == 0 {
		t.Errorf("не сказано, какой файл: %+v", unused.Items)
	}
	// Объяснение без разметки ссылок.
	if strings.Contains(unused.Description, "](") {
		t.Errorf("разметка ссылки осталась: %q", unused.Description)
	}

	// Доступность: элементы страницы, а не адреса.
	contrast, ok := byID["color-contrast"]
	if !ok || contrast.Category != "accessibility" || len(contrast.Items) == 0 {
		t.Fatalf("контраст: %+v", contrast)
	}

	// Проверка вида checklist с объектом вместо массива не роняет разбор,
	// а пройденные проверки и сами метрики советами не становятся.
	for _, id := range []string{"document-latency-insight", "largest-contentful-paint", "server-response-time"} {
		if _, found := byID[id]; found {
			t.Errorf("%s не должен быть советом", id)
		}
	}
	// У SEO на 100 советов нет.
	for _, item := range lab.Advice {
		if item.Category == "seo" {
			t.Errorf("совет SEO при оценке 100: %+v", item)
		}
	}
}

func TestDescriptionLosesLinksAndLearnMore(t *testing.T) {
	got := cleanDescription("Удалите ненужное. [Подробнее об этом](https://developer.chrome.com/x).")
	if got != "Удалите ненужное." {
		t.Errorf("получили %q", got)
	}
	got = cleanDescription("Use [WebP](https://x) images. [Learn more](https://y).")
	if got != "Use WebP images." {
		t.Errorf("получили %q", got)
	}
}
