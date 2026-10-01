package health

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/links"
	"agent/internal/logs"
	"agent/internal/metrics"
	"agent/internal/seo"
	"agent/internal/store"
	"agent/internal/vitals"
)

func days(n int) *int { return &n }

// channels — каналы уведомлений «настроены».
//
// Нужны почти каждому тесту: агент без единого канала — это повод сам по
// себе, и без него проверка про диск говорила бы заодно про уведомления.
func channels() []string { return []string{"smtp"} }

// Полнота таблицы уровней закрепляется тестом.
//
// Забытый код получил бы уровень «посмотри» из запасной ветки, и повод
// уровня «сломано» тихо потерял бы половину смысла. Тест читает список
// констант прямо из исходника: перечислить их здесь значило бы завести
// вторую копию того же перечня — ровно то, от чего он и защищает.
func TestEveryCodeHasALevel(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "health.go", nil, 0)
	if err != nil {
		t.Fatalf("разбор исходника: %v", err)
	}

	var codes []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		if !strings.HasPrefix(spec.Names[0].Name, "Code") {
			return true
		}
		literal, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		codes = append(codes, strings.Trim(literal.Value, `"`))
		return true
	})

	if len(codes) < 10 {
		t.Fatalf("разбор дал %d кодов — сломался разбор, а не список", len(codes))
	}
	for _, code := range codes {
		if _, ok := levelByCode[code]; !ok {
			t.Errorf("коду %s не назначен уровень: он приедет как «посмотри», чем бы ни был", code)
		}
	}
	if len(levelByCode) != len(codes) {
		t.Errorf("в таблице %d уровней на %d кодов: лишний уровень некому получить",
			len(levelByCode), len(codes))
	}
}

// Беда сервера остаётся бедой сервера.
//
// Кончилось место на VPS с тремя проектами — это одна запись и одно
// действие. Скопируй мы её в каждую карточку, человек увидел бы три аварии
// вместо одной и пошёл бы чинить сайты, с которыми всё в порядке.
func TestServerTroubleDoesNotLeakIntoProjects(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now: now,
		Sample: &metrics.Sample{
			At: now, DiskTotalMB: 100, DiskFreeMB: 2,
			MemTotalMB: 100, MemAvailableMB: 50,
		},
		DiskAlertPercent: 85,
		Channels:         channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	})

	if snapshot.Server.Level != LevelProblem {
		t.Fatalf("диск на 98%% — это проблема, а уровень %s", snapshot.Server.Level)
	}
	if got := snapshot.Projects[0].Level; got != LevelOK {
		t.Errorf("беда сервера утекла в проект: уровень %s, поводы %+v",
			got, snapshot.Projects[0].Reasons)
	}
}

// Заполняющийся диск и почти полный — разные поводы.
func TestDiskFillingIsNotDiskFull(t *testing.T) {
	now := time.Now()
	sample := func(free uint64) *metrics.Sample {
		return &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: free}
	}

	filling := Compute(Input{Now: now, Sample: sample(14), DiskAlertPercent: 85, Channels: channels()})
	if filling.Server.Level != LevelAttention {
		t.Errorf("86%% диска — повод посмотреть, а не авария: %s", filling.Server.Level)
	}
	if filling.Server.Reasons[0].Code != CodeDiskFilling {
		t.Errorf("повод %s", filling.Server.Reasons[0].Code)
	}

	full := Compute(Input{Now: now, Sample: sample(3), DiskAlertPercent: 85, Channels: channels()})
	if full.Server.Reasons[0].Code != CodeDiskFull {
		t.Errorf("97%% диска — это уже авария, а повод %s", full.Server.Reasons[0].Code)
	}
}

// Замеров нет вовсе — это не «всё хорошо».
func TestMissingMetricsAreNotSilence(t *testing.T) {
	snapshot := Compute(Input{Now: time.Now(), Channels: channels()})

	if snapshot.Server.Level == LevelOK {
		t.Fatal("сервер без единого замера объявлен здоровым: молчаливый ноль хуже отказа")
	}
	if snapshot.Server.Reasons[0].Code != CodeMetricsMissing {
		t.Errorf("повод %s", snapshot.Server.Reasons[0].Code)
	}
}

// Замер, снятый час назад при минутном интервале, значит, что агент встал.
func TestStaleMetricsAreNoticed(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:               now,
		Sample:            &metrics.Sample{At: now.Add(-2 * time.Hour), DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent:  85,
		MetricsStaleAfter: 15 * time.Minute,
		Channels:          channels(),
	})

	if snapshot.Server.Reasons[0].Code != CodeMetricsStale {
		t.Fatalf("двухчасовой замер не замечен: %+v", snapshot.Server.Reasons)
	}
	if snapshot.Server.SampledAt == "" {
		t.Error("дата замера обязана уехать: без неё «несвежий» не проверишь глазами")
	}
}

// Сроки домена и сертификата поднимают уровень по тем же порогам, по каким
// уходят уведомления. Две проверки, спорящие о том, пора ли волноваться, —
// это гарантированный вопрос «кому из них верить».
func TestExpiryUsesTheSameThresholds(t *testing.T) {
	now := time.Now()
	in := Input{
		Now:              now,
		Sample:           &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent: 85,
		CertDays:         14,
		DomainDays:       30,
		Channels:         channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	}

	in.Domains = []checks.DomainStatus{{Domain: "example.com", DaysLeft: days(40), CheckedAt: now}}
	in.Certs = []checks.TLSStatus{{Domain: "example.com", Valid: true, DaysLeft: days(40), CheckedAt: now}}
	if got := Compute(in).Projects[0].Level; got != LevelOK {
		t.Errorf("сорок дней до срока — не повод: %s", got)
	}

	in.Domains = []checks.DomainStatus{{Domain: "example.com", DaysLeft: days(20), CheckedAt: now}}
	if got := Compute(in).Projects[0].Level; got != LevelAttention {
		t.Errorf("двадцать дней до срока домена — повод посмотреть: %s", got)
	}

	in.Domains = []checks.DomainStatus{{Domain: "example.com", DaysLeft: days(-1), CheckedAt: now}}
	if got := Compute(in).Projects[0].Level; got != LevelProblem {
		t.Errorf("просроченный домен — это авария: %s", got)
	}
}

// Проверка, переставшая отвечать, не выглядит как «всё хорошо».
func TestUnknownExpiryIsAReason(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:        now,
		Sample:     &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DomainDays: 30,
		Channels:   channels(),
		Domains: []checks.DomainStatus{
			{Domain: "example.com", Error: checks.ErrLookup, CheckedAt: now},
		},
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	})

	reasons := snapshot.Projects[0].Reasons
	if len(reasons) != 1 || reasons[0].Code != CodeDomainUnknown {
		t.Fatalf("ослепшая проверка промолчала: %+v", reasons)
	}
	if reasons[0].Detail != checks.ErrLookup {
		t.Errorf("код беды проверки потерян: %+v", reasons[0])
	}
}

// Гигиенические находки SEO статус не поднимают, критичные поднимают.
//
// Без этого правила проект с двенадцатью замечаниями нижнего уровня светился
// бы жёлтым вечно, и свод перестал бы отвечать на вопрос, ради которого он
// заведён: «нужно ли что-то делать сегодня».
func TestOnlyCriticalSeoRaisesTheLevel(t *testing.T) {
	now := time.Now()
	in := Input{
		Now:      now,
		Sample:   &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels: channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	}

	in.Seo = []seo.Scan{{
		Domain:    "example.com",
		CheckedAt: now.Format(time.RFC3339),
		Stats:     seo.Stats{Pages: 10, Issues: seo.IssueSummary{Warning: 8, Notice: 12}},
	}}
	if got := Compute(in).Projects[0].Level; got != LevelOK {
		t.Errorf("гигиена подняла статус: %s, поводы %+v", got, Compute(in).Projects[0].Reasons)
	}

	in.Seo[0].Stats.Issues.Critical = 3
	state := Compute(in).Projects[0]
	if state.Level != LevelAttention {
		t.Errorf("критичные находки не подняли статус: %s", state.Level)
	}
	if state.Reasons[0].Count != 3 {
		t.Errorf("число находок потеряно: %+v", state.Reasons[0])
	}
}

// Несостоявшийся обход — повод, а не тишина. То же правило, по которому
// пустой результат обхода считается отказом, а не «всё хорошо».
func TestFailedScansAreReasons(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:      now,
		Sample:   &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels: channels(),
		Links: []links.Report{
			{Domain: "example.com", CheckedAt: now.Format(time.RFC3339), Error: "linksRobotsForbidden"},
		},
		Seo: []seo.Scan{
			{Domain: "example.com", CheckedAt: now.Format(time.RFC3339), Error: "seoRobotsForbidden"},
		},
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	})

	codes := map[string]string{}
	for _, item := range snapshot.Projects[0].Reasons {
		codes[item.Code] = item.Detail
	}
	if codes[CodeLinksFailed] != "linksRobotsForbidden" {
		t.Errorf("неудача проверки ссылок потеряна: %+v", snapshot.Projects[0].Reasons)
	}
	if codes[CodeSeoFailed] != "seoRobotsForbidden" {
		t.Errorf("неудача обхода потеряна: %+v", snapshot.Projects[0].Reasons)
	}
}

// Порядок «за какой браться первым» считает агент, а не экран.
func TestProjectsComeSorted(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:        now,
		Sample:     &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DomainDays: 30,
		Channels:   channels(),
		Domains: []checks.DomainStatus{
			{Domain: "broken.com", DaysLeft: days(-2), CheckedAt: now},
			{Domain: "soon.com", DaysLeft: days(10), CheckedAt: now},
		},
		Projects: []config.Project{
			{ID: "calm", Name: "calm.com", Domains: []string{"calm.com"}},
			{ID: "soon", Name: "soon.com", Domains: []string{"soon.com"}},
			{ID: "broken", Name: "broken.com", Domains: []string{"broken.com"}},
		},
	})

	order := []string{snapshot.Projects[0].ID, snapshot.Projects[1].ID, snapshot.Projects[2].ID}
	want := []string{"broken", "soon", "calm"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("порядок %v, ожидали %v", order, want)
		}
	}
}

// Дата последней проверки проекта — самая свежая из его проверок.
func TestCheckedAtIsTheFreshest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	snapshot := Compute(Input{
		Now:      now,
		Sample:   &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels: channels(),
		Domains: []checks.DomainStatus{
			{Domain: "example.com", DaysLeft: days(100), CheckedAt: now.Add(-48 * time.Hour)},
		},
		Links: []links.Report{
			{Domain: "example.com", CheckedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)},
		},
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	})

	want := now.Add(-2 * time.Hour).Format(time.RFC3339)
	if got := snapshot.Projects[0].CheckedAt; got != want {
		t.Errorf("последняя проверка %s, ожидали %s", got, want)
	}
}

// Пустые списки уезжают списками, а не `null`.
//
// Go сериализует nil-срез в `null`, и разбор на стороне приложения
// спотыкается о него ровно там, где всё хорошо: у сервера без единого
// повода и у сервера без проектов. Грабли уже наступленные — на истории
// замеров в седьмом спринте, — и наступать на них второй раз незачем.
func TestEmptyListsAreListsNotNull(t *testing.T) {
	now := time.Now()
	raw, err := json.Marshal(Compute(Input{
		Now:              now,
		Sample:           &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent: 85,
		Channels:         channels(),
	}))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(raw), "null") {
		t.Errorf("в ответе есть null: %s", raw)
	}

	withProject, err := json.Marshal(Compute(Input{
		Now:              now,
		Sample:           &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent: 85,
		Channels:         channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withProject), "null") {
		t.Errorf("в ответе есть null: %s", withProject)
	}
}

// ── Приглушение ──────────────────────────────────────────────────────────

func brokenDomain(now time.Time) Input {
	return Input{
		Now:        now,
		Sample:     &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DomainDays: 30,
		Channels:   channels(),
		Domains: []checks.DomainStatus{
			{Domain: "example.com", DaysLeft: days(-1), CheckedAt: now},
		},
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	}
}

// Приглушённая находка уровень не поднимает, но из свода не исчезает.
//
// Исчезни она совсем — снять приглушение стало бы нечем, и через полгода
// никто бы не вспомнил, что его вообще ставили.
func TestSuppressedReasonStaysVisibleButQuiet(t *testing.T) {
	now := time.Now()
	in := brokenDomain(now)

	if got := Compute(in).Projects[0].Level; got != LevelProblem {
		t.Fatalf("без приглушения это авария, а уровень %s", got)
	}

	in.Suppressions = []store.Suppression{
		{Code: CodeDomainExpired, Target: "example.com", Note: "домен отдан клиенту"},
	}
	state := Compute(in).Projects[0]

	if state.Level != LevelOK {
		t.Errorf("приглушение не сняло уровень: %s", state.Level)
	}
	if len(state.Reasons) != 1 {
		t.Fatalf("находка исчезла — снять приглушение будет нечем: %+v", state.Reasons)
	}
	if !state.Reasons[0].Suppressed {
		t.Error("находка не помечена приглушённой")
	}
}

// Приглушить можно повод целиком, без привязки к домену.
func TestSuppressionWithoutTargetCoversTheWholeCode(t *testing.T) {
	now := time.Now()
	in := brokenDomain(now)
	in.Suppressions = []store.Suppression{{Code: CodeDomainExpired}}

	if got := Compute(in).Projects[0].Level; got != LevelOK {
		t.Errorf("приглушение повода целиком не сработало: %s", got)
	}
}

// Приглушение чужой находки на эту не действует.
func TestSuppressionDoesNotLeakToOtherTargets(t *testing.T) {
	now := time.Now()
	in := brokenDomain(now)
	in.Suppressions = []store.Suppression{
		{Code: CodeDomainExpired, Target: "another.org"},
	}

	if got := Compute(in).Projects[0].Level; got != LevelProblem {
		t.Errorf("приглушение уехало на чужой домен: %s", got)
	}
}

// Истёкшее приглушение не действует.
//
// Хранилище убирает такие строки само, но свод обязан быть верным и тогда,
// когда уборка не случилась: между ней и запросом проходит время.
func TestExpiredSuppressionIsIgnored(t *testing.T) {
	now := time.Now()
	in := brokenDomain(now)
	in.Suppressions = []store.Suppression{
		{Code: CodeDomainExpired, Target: "example.com", Until: now.Add(-time.Hour)},
	}

	if got := Compute(in).Projects[0].Level; got != LevelProblem {
		t.Errorf("истёкшее приглушение всё ещё гасит находку: %s", got)
	}
}

// Приглушение меняет порядок проектов: тот, у кого беду приглушили, уезжает
// вниз.
func TestSuppressionReordersProjects(t *testing.T) {
	now := time.Now()
	in := Input{
		Now:        now,
		Sample:     &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DomainDays: 30,
		Channels:   channels(),
		Domains: []checks.DomainStatus{
			{Domain: "muted.com", DaysLeft: days(-2), CheckedAt: now},
			{Domain: "loud.com", DaysLeft: days(10), CheckedAt: now},
		},
		Projects: []config.Project{
			{ID: "muted", Name: "muted.com", Domains: []string{"muted.com"}},
			{ID: "loud", Name: "loud.com", Domains: []string{"loud.com"}},
		},
		Suppressions: []store.Suppression{
			{Code: CodeDomainExpired, Target: "muted.com"},
		},
	}

	if got := Compute(in).Projects[0].ID; got != "loud" {
		t.Errorf("первым идёт %s, а приглушённый должен уехать вниз", got)
	}
}

// ── Окно обслуживания ────────────────────────────────────────────────────

// Окно обслуживания уезжает в свод, но уровни считаются честно.
//
// Человек, открывший экран во время собственного обновления, должен видеть,
// что происходит, а не гладкое «всё хорошо». Приглушается исходящее —
// тревоги и еженедельный отчёт: будить оно должно не того, кто и так
// смотрит.
func TestMaintenanceIsReportedButDoesNotHideState(t *testing.T) {
	now := time.Now()
	in := brokenDomain(now)
	in.Maintenance = &store.Maintenance{
		Until:   now.Add(time.Hour),
		Note:    "обновляю стек",
		Started: now,
	}

	snapshot := Compute(in)

	if snapshot.Maintenance == nil {
		t.Fatal("окно обслуживания не уехало в свод")
	}
	if snapshot.Maintenance.Note != "обновляю стек" {
		t.Errorf("заметка потеряна: %+v", snapshot.Maintenance)
	}
	if snapshot.Projects[0].Level != LevelProblem {
		t.Errorf("обслуживание спрятало настоящую беду: %s", snapshot.Projects[0].Level)
	}
}

// ── Каналы уведомлений ───────────────────────────────────────────────────

// Агент без единого канала — повод сам по себе.
//
// Сторож, которому некуда позвать, не сторож, и молчать об этом нельзя:
// человек уверен, что за сервером следят.
func TestAgentWithoutChannelsSaysSo(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:              now,
		Sample:           &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent: 85,
	})

	found := false
	for _, item := range snapshot.Server.Reasons {
		if item.Code == CodeNoChannels {
			found = true
		}
	}
	if !found {
		t.Errorf("агент молчит о том, что ему некуда написать: %+v", snapshot.Server.Reasons)
	}
}

// Уведомления с сервера выключил сам человек — это решение, а не беда, и
// повод «некуда написать» висел бы в списке дел вечно.
func TestMutedServerIsNotNaggedAboutChannels(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:              now,
		Sample:           &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent: 85,
		NotifyMuted:      true,
	})

	for _, item := range snapshot.Server.Reasons {
		if item.Code == CodeNoChannels {
			t.Errorf("выключенному серверу напоминают про каналы: %+v", snapshot.Server.Reasons)
		}
	}
}

// ── Core Web Vitals ──────────────────────────────────────────────────────

func poor(value float64) *vitals.Metric {
	return &vitals.Metric{Value: value, Rating: vitals.RatingPoor}
}

// Статус поднимают только field-данные и только оценка «плохо».
//
// Lab — одна загрузка в лаборатории Google; будить по ней человека значит
// беспокоить его из-за того, чего его посетители могли не заметить.
func TestOnlyPoorFieldVitalsRaiseTheLevel(t *testing.T) {
	now := time.Now()
	in := Input{
		Now:      now,
		Sample:   &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels: channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	}

	// Лаборатория говорит «плохо», живых посетителей нет — молчим.
	in.Vitals = []vitals.Report{{
		Domain:    "example.com",
		CheckedAt: now.Format(time.RFC3339),
		Lab:       &vitals.Lab{},
	}}
	if got := Compute(in).Projects[0].Level; got != LevelOK {
		t.Errorf("lab-данные подняли статус: %s", got)
	}

	// Замер не удался — тоже молчим: причина не про сайт клиента.
	in.Vitals = []vitals.Report{{
		Domain:     "example.com",
		CheckedAt:  now.Format(time.RFC3339),
		FieldError: "vitalsNoFieldData",
	}}
	if got := Compute(in).Projects[0].Level; got != LevelOK {
		t.Errorf("неудача замера подняла статус: %s", got)
	}

	// Живые посетители видят «плохо» — говорим.
	in.Vitals = []vitals.Report{{
		Domain:    "example.com",
		CheckedAt: now.Format(time.RFC3339),
		Field:     &vitals.Field{LCP: poor(6000), CLS: poor(0.4)},
	}}
	state := Compute(in).Projects[0]
	if state.Level != LevelAttention {
		t.Fatalf("плохие field-данные не подняли статус: %s", state.Level)
	}
	if state.Reasons[0].Count != 2 {
		t.Errorf("число плохих метрик потеряно: %+v", state.Reasons[0])
	}
}

// Свод говорит, каким ключом находка погашена.
//
// Он расходится с доменом находки, когда приглушён повод целиком: находка
// про example.com, а приглушение — с пустой целью. Сними приложение
// приглушение по домену находки, оно не сняло бы ничего, и кнопка «вернуть»
// молча не работала бы.
func TestSuppressionReportsTheKeyItWasMutedBy(t *testing.T) {
	now := time.Now()
	in := brokenDomain(now)
	in.Suppressions = []store.Suppression{{Code: CodeDomainExpired}}

	reason := Compute(in).Projects[0].Reasons[0]

	if !reason.Suppressed {
		t.Fatal("находка не помечена приглушённой")
	}
	if reason.SuppressedTarget != "" {
		t.Errorf("ключ приглушения %q, а гасили повод целиком", reason.SuppressedTarget)
	}
	if reason.Domain != "example.com" {
		t.Errorf("домен находки подменён ключом приглушения: %+v", reason)
	}
}

// ── Всплески ошибок в логах ──────────────────────────────────────────────

// spiking — вход со всплеском и порогами, при которых он считается
// всплеском.
func spiking(now time.Time, spikes []logs.Spike) Input {
	return Input{
		Now:                  now,
		Sample:               &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels:             channels(),
		LogErrorThreshold:    25,
		LogNotFoundThreshold: 100,
		LogWindowMinutes:     15,
		LogSpikes:            spikes,
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
	}
}

// Домен в строке лога есть — повод у проекта: агентству надо знать, чей
// именно сайт сыплет пятисотками.
func TestLogSpikeWithAHostGoesToItsProject(t *testing.T) {
	now := time.Now()
	snapshot := Compute(spiking(now, []logs.Spike{
		{Host: "example.com", ServerErrors: 40},
	}))

	if len(snapshot.Server.Reasons) != 0 {
		t.Errorf("всплеск с известным доменом остался и у сервера: %+v", snapshot.Server.Reasons)
	}

	project := snapshot.Projects[0]
	if project.Level != LevelProblem {
		t.Fatalf("уровень проекта %s, ждали %s", project.Level, LevelProblem)
	}
	reason := project.Reasons[0]
	if reason.Code != CodeErrorSpike || reason.Count != 40 || reason.WindowMinutes != 15 {
		t.Errorf("повод: %+v", reason)
	}
}

// Домена в строке нет — повод у сервера. Формат `combined` у nginx домена
// не пишет, и молчать о таком всплеске нельзя: это тот же молчаливый ноль.
func TestLogSpikeWithoutAHostStaysWithTheServer(t *testing.T) {
	now := time.Now()
	snapshot := Compute(spiking(now, []logs.Spike{
		{Host: "", ServerErrors: 30},
		// Чужой сайт на том же сервере — законный случай: мы на чужом
		// проде. Он тоже про сервер, а не про проект.
		{Host: "чужой.example.net", ServerErrors: 10},
	}))

	if snapshot.Projects[0].Level != LevelOK {
		t.Errorf("ничей всплеск покрасил проект: %+v", snapshot.Projects[0].Reasons)
	}
	if len(snapshot.Server.Reasons) != 1 {
		t.Fatalf("поводов у сервера %d: %+v", len(snapshot.Server.Reasons), snapshot.Server.Reasons)
	}
	// Один повод, а не по одному на домен: это одна беда веб-сервера и одно
	// действие.
	if snapshot.Server.Reasons[0].Count != 40 {
		t.Errorf("всплески не сложились: %+v", snapshot.Server.Reasons[0])
	}
}

// Порог — про всплеск, а не про сумму: ниже порога повода нет вовсе.
func TestSpikeBelowThresholdIsSilent(t *testing.T) {
	now := time.Now()
	snapshot := Compute(spiking(now, []logs.Spike{
		{Host: "example.com", ServerErrors: 24, NotFound: 99},
	}))

	if snapshot.Projects[0].Level != LevelOK {
		t.Errorf("фон объявлен аварией: %+v", snapshot.Projects[0].Reasons)
	}
}

// «Страницы нет» — свой повод и свой уровень: сайт работает, пропали
// страницы. Красить этим карточку в красный значит уравнять выкатку,
// снёсшую раздел, с лежащим сайтом.
func TestNotFoundSpikeIsQuieterThanServerErrors(t *testing.T) {
	now := time.Now()
	snapshot := Compute(spiking(now, []logs.Spike{
		{Host: "example.com", NotFound: 300},
	}))

	project := snapshot.Projects[0]
	if project.Level != LevelAttention {
		t.Fatalf("уровень %s, ждали %s", project.Level, LevelAttention)
	}
	if project.Reasons[0].Code != CodeNotFoundSpike || project.Reasons[0].Count != 300 {
		t.Errorf("повод: %+v", project.Reasons[0])
	}
}

// Нулевой порог выключает повод совсем — законный выбор, как и у тревоги.
func TestZeroThresholdTurnsTheSpikeOff(t *testing.T) {
	now := time.Now()
	in := spiking(now, []logs.Spike{{Host: "example.com", ServerErrors: 4000}})
	in.LogErrorThreshold = 0

	if snapshot := Compute(in); snapshot.Projects[0].Level != LevelOK {
		t.Errorf("выключенный порог всё равно поднял повод: %+v", snapshot.Projects[0].Reasons)
	}
}

// Находки входа приезжают по одной, каждая со своим кодом внутри: их не
// бывает больше трёх, и чинится каждая своей настройкой.
func TestCanonicalFindingsBecomeReasons(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:      now,
		Sample:   &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels: channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
		Seo: []seo.Scan{{
			Domain:    "example.com",
			CheckedAt: now.Format(time.RFC3339),
			Stats: seo.Stats{
				Pages: 10,
				Canonical: &seo.CanonicalReport{
					Canonical: "https://example.com/",
					Issues: []seo.Issue{
						{Code: seo.IssueCanonicalNoHTTPS, Severity: seo.SeverityCritical},
						{Code: seo.IssueCanonicalHostSplit, Severity: seo.SeverityCritical},
					},
				},
			},
		}},
	})

	project := snapshot.Projects[0]
	if len(project.Reasons) != 2 {
		t.Fatalf("поводов %d: %+v", len(project.Reasons), project.Reasons)
	}
	for _, reason := range project.Reasons {
		if reason.Code != CodeSeoCanonical {
			t.Errorf("повод не тот: %+v", reason)
		}
		if reason.Detail == "" {
			t.Errorf("повод без уточнения — человек не узнает, что чинить: %+v", reason)
		}
	}
}

// Сайт, закрытый от поисковиков, — повод в своде; закрытые ИИ-боты — нет:
// это частый осознанный выбор, и свод от него жёлтым не становится.
func TestBlockedSearchEnginesBecomeReason(t *testing.T) {
	now := time.Now()
	snapshot := Compute(Input{
		Now:      now,
		Sample:   &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		Channels: channels(),
		Projects: []config.Project{
			{ID: "one", Name: "example.com", Domains: []string{"example.com"}},
		},
		Seo: []seo.Scan{{
			Domain:    "example.com",
			CheckedAt: now.Format(time.RFC3339),
			Stats: seo.Stats{
				Pages: 10,
				Site: &seo.SiteReport{
					Robots: seo.RobotsFound,
					Issues: []seo.Issue{
						{Code: seo.IssueSearchEnginesBlocked, Severity: seo.SeverityCritical, Detail: "Googlebot"},
						{Code: seo.IssueAITrainingBlocked, Severity: seo.SeverityNotice, Detail: "GPTBot"},
						{Code: seo.IssueSitemapMissing, Severity: seo.SeverityWarning},
					},
				},
			},
		}},
	})

	project := snapshot.Projects[0]
	if len(project.Reasons) != 1 {
		t.Fatalf("поводов %d: %+v", len(project.Reasons), project.Reasons)
	}
	if reason := project.Reasons[0]; reason.Code != CodeSeoBlocked || reason.Detail != "Googlebot" {
		t.Fatalf("повод: %+v", reason)
	}
}
