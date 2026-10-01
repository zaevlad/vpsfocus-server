package health

import (
	"testing"
	"time"

	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/links"
	"agent/internal/metrics"
	"agent/internal/seo"
	"agent/internal/vitals"
)

// figureOf — показатель проекта по области.
func figureOf(t *testing.T, state ProjectState, area string) Figure {
	t.Helper()
	for _, figure := range state.Figures {
		if figure.Area == area {
			return figure
		}
	}
	t.Fatalf("нет показателя %s: %+v", area, state.Figures)
	return Figure{}
}

func figureInput(now time.Time, domains ...string) Input {
	return Input{
		Now:              now,
		Sample:           &metrics.Sample{At: now, DiskTotalMB: 100, DiskFreeMB: 90},
		DiskAlertPercent: 85,
		CertDays:         14,
		DomainDays:       30,
		Channels:         channels(),
		Projects:         []config.Project{{ID: "one", Name: "Проект", Domains: domains}},
	}
}

// Показателей всегда четыре и в одном порядке, а чего не знаем — `unknown`
// без числа: ноль битых ссылок у необойдённого сайта был бы неправдой.
func TestFiguresAreAlwaysFourAndUnknownByDefault(t *testing.T) {
	state := Compute(figureInput(time.Now(), "example.com")).Projects[0]

	want := []string{AreaSpeed, AreaSeo, AreaLinks, AreaExpiry}
	if len(state.Figures) != len(want) {
		t.Fatalf("показателей %d: %+v", len(state.Figures), state.Figures)
	}
	for index, area := range want {
		figure := state.Figures[index]
		if figure.Area != area {
			t.Errorf("на месте %d стоит %s, ждали %s", index, figure.Area, area)
		}
		if figure.State != StateUnknown || figure.Value != nil {
			t.Errorf("%s без данных: %+v", area, figure)
		}
	}
}

// Скорость — главная страница, мобильный замер; оценка — Google, а уровень
// проекта лабораторный замер по-прежнему не трогает.
func TestSpeedFigureTakesTheHomePageOnMobile(t *testing.T) {
	now := time.Now()
	in := figureInput(now, "example.com")
	lab := func(score int, rating string) *vitals.Lab {
		return &vitals.Lab{Score: score, ScoreRating: rating}
	}
	in.Vitals = []vitals.Report{
		{Domain: "example.com", Path: "/catalog", Strategy: "mobile", Lab: lab(30, vitals.RatingPoor)},
		{Domain: "example.com", Path: "/", Strategy: "desktop", Lab: lab(95, vitals.RatingGood)},
		{Domain: "example.com", Path: "/", Strategy: "mobile", Lab: lab(72, vitals.RatingNeedsImprovement)},
		// Замер не удался — в расчёт не идёт.
		{Domain: "example.com", Path: "/", Strategy: "mobile", Error: "vitalsTimeout"},
	}

	state := Compute(in).Projects[0]
	speed := figureOf(t, state, AreaSpeed)
	if speed.Value == nil || *speed.Value != 72 || speed.State != string(LevelAttention) {
		t.Fatalf("скорость: %+v", speed)
	}
	if state.Level != LevelOK {
		t.Errorf("лабораторный замер поднял уровень проекта: %s", state.Level)
	}
}

// У проекта из нескольких доменов показывается худший, а не средний.
func TestFiguresShowTheWorstDomain(t *testing.T) {
	now := time.Now()
	in := figureInput(now, "good.test", "bad.test")
	lab := func(score int, rating string) *vitals.Lab {
		return &vitals.Lab{Score: score, ScoreRating: rating}
	}
	in.Vitals = []vitals.Report{
		{Domain: "good.test", Path: "/", Strategy: "mobile", Lab: lab(96, vitals.RatingGood)},
		{Domain: "bad.test", Path: "/", Strategy: "mobile", Lab: lab(41, vitals.RatingPoor)},
	}
	in.Links = []links.Report{
		{Domain: "good.test", TotalLinks: 100},
		{Domain: "bad.test", TotalLinks: 40, Broken: 3},
	}

	state := Compute(in).Projects[0]
	if speed := figureOf(t, state, AreaSpeed); *speed.Value != 41 || speed.Domain != "bad.test" || speed.State != string(LevelProblem) {
		t.Errorf("скорость: %+v", speed)
	}
	if broken := figureOf(t, state, AreaLinks); *broken.Value != 3 || broken.Total != 40 || broken.Domain != "bad.test" || broken.State != string(LevelAttention) {
		t.Errorf("ссылки: %+v", broken)
	}
}

// SEO: сколько проверок пройдено и оценка по линейке экрана аудита.
func TestSeoFigureCountsPassedChecks(t *testing.T) {
	now := time.Now()
	in := figureInput(now, "example.com")
	scan := seo.Scan{
		Domain:    "example.com",
		CheckedAt: now.Format(time.RFC3339),
		Stats: seo.Stats{
			Pages: 5, Fetched: 5,
			Site:      &seo.SiteReport{Robots: seo.RobotsFound, Sitemap: &seo.SitemapReport{Found: true, Complete: true}},
			Canonical: &seo.CanonicalReport{},
			Issues: seo.IssueSummary{Warning: 4, Pages: 4, ByCode: []seo.CodeCount{
				{Code: seo.IssueDescriptionMissing, Severity: seo.SeverityWarning, Pages: 4},
			}},
		},
	}
	in.Seo = []seo.Scan{scan}

	figure := figureOf(t, Compute(in).Projects[0], AreaSeo)
	if figure.Value == nil || figure.Total == 0 || *figure.Value != figure.Total-1 {
		t.Fatalf("SEO: %+v", figure)
	}
	if figure.State != string(LevelAttention) {
		t.Errorf("предупреждение — жёлтое: %+v", figure)
	}

	// Обход без перечня проверок (агент старше 0.16.0) числа не даёт.
	in.Seo[0].Stats.Site = nil
	if old := figureOf(t, Compute(in).Projects[0], AreaSeo); old.State != StateUnknown || old.Value != nil {
		t.Errorf("обходу без перечня выдумано число: %+v", old)
	}
}

// Несостоявшийся круг ссылок — «не знаем», а не «ноль битых».
func TestLinksFigureIgnoresFailedRuns(t *testing.T) {
	now := time.Now()
	in := figureInput(now, "example.com")
	in.Links = []links.Report{{Domain: "example.com", Error: "linksRobotsForbidden"}}

	if figure := figureOf(t, Compute(in).Projects[0], AreaLinks); figure.State != StateUnknown || figure.Value != nil {
		t.Fatalf("ссылки: %+v", figure)
	}

	in.Links = []links.Report{{Domain: "example.com", TotalLinks: 240}}
	figure := figureOf(t, Compute(in).Projects[0], AreaLinks)
	if figure.Value == nil || *figure.Value != 0 || figure.Total != 240 || figure.State != string(LevelOK) {
		t.Fatalf("ссылки без битых: %+v", figure)
	}
}

// Сроки: ближайший из домена и сертификата, теми же порогами, что у свода.
func TestExpiryFigureShowsTheNearestDeadline(t *testing.T) {
	now := time.Now()
	in := figureInput(now, "example.com")
	in.Domains = []checks.DomainStatus{{Domain: "example.com", DaysLeft: days(209), CheckedAt: now}}
	in.Certs = []checks.TLSStatus{{Domain: "example.com", Valid: true, DaysLeft: days(66), CheckedAt: now}}

	figure := figureOf(t, Compute(in).Projects[0], AreaExpiry)
	if *figure.Value != 66 || figure.Kind != ExpiryCert || figure.State != string(LevelOK) {
		t.Fatalf("сроки в порядке: %+v", figure)
	}

	// Сертификат подходит к концу — жёлтый, и число его.
	in.Certs[0].DaysLeft = days(11)
	figure = figureOf(t, Compute(in).Projects[0], AreaExpiry)
	if *figure.Value != 11 || figure.Kind != ExpiryCert || figure.State != string(LevelAttention) {
		t.Fatalf("сертификат истекает: %+v", figure)
	}

	// Домен просрочен — красный, и показатель говорит о нём, а не о
	// сертификате с меньшим числом поводов.
	in.Domains[0].DaysLeft = days(-2)
	figure = figureOf(t, Compute(in).Projects[0], AreaExpiry)
	if *figure.Value != -2 || figure.Kind != ExpiryDomain || figure.State != string(LevelProblem) {
		t.Fatalf("домен просрочен: %+v", figure)
	}
}

// Сертификат не принят: дней у беды нет, и чужое число рядом с красным
// цветом не встаёт.
func TestExpiryFigureOfInvalidCertHasNoDays(t *testing.T) {
	now := time.Now()
	in := figureInput(now, "example.com")
	in.Domains = []checks.DomainStatus{{Domain: "example.com", DaysLeft: days(209), CheckedAt: now}}
	in.Certs = []checks.TLSStatus{{Domain: "example.com", Valid: false, Error: "tlsHostMismatch", CheckedAt: now}}

	figure := figureOf(t, Compute(in).Projects[0], AreaExpiry)
	if figure.State != string(LevelProblem) || figure.Value != nil || figure.Kind != ExpiryCert {
		t.Fatalf("непринятый сертификат: %+v", figure)
	}
}
