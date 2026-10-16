package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"agent/internal/checks"
	"agent/internal/notify"
	"agent/internal/quiet"
	"agent/internal/store"
)

// recorder — подставной канал: помнит, что ушло.
type recorder struct{ sent []string }

func (r *recorder) Silent() bool { return false }
func (r *recorder) Send(_ context.Context, _ notify.Look, subject, _ string) error {
	r.sent = append(r.sent, subject)
	return nil
}

// quietAgent — агент с подставным каналом и часами. Пустые hours — тихих
// часов нет.
func quietAgent(t *testing.T, db *store.Store, hours string, clock *time.Time) (*Agent, *recorder) {
	t.Helper()
	if db == nil {
		_, db = testAgent(t)
	}
	out := &recorder{}
	agent := &Agent{
		db:      db,
		rootCtx: t.Context(),
		sendVia: out,
		clock:   func() time.Time { return *clock },
	}
	agent.cfg.Alerts.RepeatAfter = 24 * time.Hour
	agent.cfg.Quiet = quiet.Parse(hours, "Europe/Moscow")
	return agent, out
}

func moscow(t *testing.T, day, hour int) time.Time {
	t.Helper()
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, 10, day, hour, 0, 0, 0, location)
}

var (
	diskFull    = checks.Alert{Kind: checks.KindDisk, Target: "root", Subject: "disk", Urgent: true}
	diskOK      = checks.Alert{Kind: checks.KindDisk, Target: "root", Subject: "disk ok", Urgent: true}
	certSoon    = checks.Alert{Kind: checks.KindCert, Target: "a.com", Subject: "cert soon"}
	certOK      = checks.Alert{Kind: checks.KindCert, Target: "a.com", Subject: "cert ok"}
	domainStep  = checks.Alert{Kind: checks.KindDomain, Target: "a.com", Subject: "domain 14", Step: 14}
	shrunk      = checks.Alert{Kind: checks.KindBackupShrunk, Target: "database", Subject: "shrunk", Step: 1}
	shrunkOK    = checks.Alert{Kind: checks.KindBackupShrunk, Target: "database", Subject: "shrunk ok"}
	brokenProbe = checks.Alert{Kind: checks.KindProbe, Target: "p", Subject: "probe", Urgent: true}
)

// script — набор кругов тех же видов, что ходят через dispatch: свежая
// тревога, повтор внутри суток и после, ступени, отбой.
type round struct {
	at               time.Duration // от начала сценария
	firing, resolved []checks.Alert
}

var script = []round{
	{0, []checks.Alert{diskFull, certSoon, domainStep, shrunk}, nil},
	{time.Hour, []checks.Alert{diskFull, certSoon, domainStep, shrunk}, nil},
	{25 * time.Hour, []checks.Alert{diskFull, certSoon, domainStep, shrunk}, nil},
	{26 * time.Hour, nil, []checks.Alert{diskOK, certOK, shrunkOK}},
	{27 * time.Hour, nil, []checks.Alert{diskOK, certOK, shrunkOK}},
	{28 * time.Hour, []checks.Alert{brokenProbe}, nil},
}

// Что dispatch слал до тихих часов: свежее сразу, повтор раз в сутки,
// ступень — один раз, отбой — один раз. Записано руками по правилам
// dispatch, а не снято с новой версии.
var before = []string{
	"disk", "cert soon", "domain 14", "shrunk",
	"disk", "cert soon",
	"disk ok", "cert ok", "shrunk ok",
	"probe",
}

func play(t *testing.T, hours string, start time.Time) []string {
	t.Helper()
	clock := start
	agent, out := quietAgent(t, nil, hours, &clock)
	for _, r := range script {
		clock = start.Add(r.at)
		agent.dispatch(t.Context(), r.firing, r.resolved)
	}
	return out.sent
}

// Сторож «без тихих часов отправлено ровно то же, что раньше» — в том числе
// днём при заданных тихих часах.
func TestDispatchWithoutQuietHoursSendsAsBefore(t *testing.T) {
	noon := moscow(t, 15, 12)
	if got := play(t, "", noon); !slices.Equal(got, before) {
		t.Fatalf("без тихих часов:\n%v\nждали\n%v", got, before)
	}
	// Днём сценарий занимает 28 часов и задел бы ночь — тихие часы ставим
	// туда, где ни один круг не окажется: с 9 до 10, а круги — в 12-13.
	if got := play(t, "9-10", noon); !slices.Equal(got, before) {
		t.Fatalf("тихие часы вне кругов:\n%v\nждали\n%v", got, before)
	}
}

func TestNightSendsOnlyUrgentAndMorningCatchesUp(t *testing.T) {
	clock := moscow(t, 15, 2)
	agent, out := quietAgent(t, nil, "23-08", &clock)

	agent.dispatch(t.Context(), []checks.Alert{diskFull, certSoon, domainStep, shrunk}, nil)
	if !slices.Equal(out.sent, []string{"disk"}) {
		t.Fatalf("ночью — только беда, ушло %v", out.sent)
	}

	clock = moscow(t, 15, 8)
	agent.dispatch(t.Context(), []checks.Alert{diskFull, certSoon, domainStep, shrunk}, nil)
	if want := []string{"disk", "cert soon", "domain 14", "shrunk"}; !slices.Equal(out.sent, want) {
		t.Fatalf("утром — остальное как свежее: %v, ждали %v", out.sent, want)
	}
}

// Тревога, прошедшая за ночь, утром не приходит ни тревогой, ни отбоем.
func TestAlertGoneOvernightLeavesNoTrace(t *testing.T) {
	clock := moscow(t, 15, 1)
	agent, out := quietAgent(t, nil, "23-08", &clock)

	agent.dispatch(t.Context(), []checks.Alert{certSoon, shrunk}, nil)
	clock = moscow(t, 15, 5)
	agent.dispatch(t.Context(), nil, []checks.Alert{certOK, shrunkOK})
	clock = moscow(t, 15, 9)
	agent.dispatch(t.Context(), nil, []checks.Alert{certOK, shrunkOK})

	if len(out.sent) != 0 {
		t.Fatalf("за ночь прошло — сообщать не о чем, ушло %v", out.sent)
	}
}

// Беда, начавшаяся днём, кончилась ночью — отбой сразу.
func TestUrgentResolveAtNightGoesAtOnce(t *testing.T) {
	clock := moscow(t, 15, 20)
	agent, out := quietAgent(t, nil, "23-08", &clock)
	agent.dispatch(t.Context(), []checks.Alert{diskFull}, nil)
	clock = moscow(t, 16, 3)
	agent.dispatch(t.Context(), nil, []checks.Alert{diskOK})
	if !slices.Equal(out.sent, []string{"disk", "disk ok"}) {
		t.Fatalf("ушло %v", out.sent)
	}
}

// Отбой того, что ждёт утра, ночью не уходит — и утром тоже, если тревогу
// так и не объявляли; объявленную днём — отбивает утром.
func TestNonUrgentResolveWaitsForMorning(t *testing.T) {
	clock := moscow(t, 15, 20)
	agent, out := quietAgent(t, nil, "23-08", &clock)
	agent.dispatch(t.Context(), []checks.Alert{shrunk}, nil)
	clock = moscow(t, 16, 3)
	agent.dispatch(t.Context(), nil, []checks.Alert{shrunkOK})
	if !slices.Equal(out.sent, []string{"shrunk"}) {
		t.Fatalf("ночью отбой ждёт: %v", out.sent)
	}
	clock = moscow(t, 16, 8)
	agent.dispatch(t.Context(), nil, []checks.Alert{shrunkOK})
	if !slices.Equal(out.sent, []string{"shrunk", "shrunk ok"}) {
		t.Fatalf("утром отбой уходит: %v", out.sent)
	}
}

// Перезапуск агента ночью ничего не теряет: накопленного нет, а состояние
// заглушённой тревоги не записывалось.
func TestRestartAtNightLosesNothing(t *testing.T) {
	_, db := testAgent(t)
	clock := moscow(t, 15, 2)
	first, _ := quietAgent(t, db, "23-08", &clock)
	first.dispatch(t.Context(), []checks.Alert{certSoon}, nil)

	clock = moscow(t, 15, 9)
	second, out := quietAgent(t, db, "23-08", &clock)
	second.dispatch(t.Context(), []checks.Alert{certSoon}, nil)
	if !slices.Equal(out.sent, []string{"cert soon"}) {
		t.Fatalf("после перезапуска утром — тревога: %v", out.sent)
	}
}

// Неразобранные тихие часы не глушат ничего.
func TestBrokenQuietHoursStayLoud(t *testing.T) {
	clock := moscow(t, 15, 2)
	agent, out := quietAgent(t, nil, "23-23", &clock)
	agent.dispatch(t.Context(), []checks.Alert{certSoon}, nil)
	if !slices.Equal(out.sent, []string{"cert soon"}) {
		t.Fatalf("ушло %v", out.sent)
	}
}

// Ступень у похудевшей копии одна (Р39): тревога зовётся на каждом круге,
// а письмо одно, пока не придёт отбой; после отбоя — снова одно.
func TestShrunkBackupSpeaksOnce(t *testing.T) {
	clock := moscow(t, 15, 12)
	agent, out := quietAgent(t, nil, "", &clock)
	for hours := range 50 {
		clock = moscow(t, 15, 12).Add(time.Duration(hours) * time.Hour)
		agent.dispatch(t.Context(), []checks.Alert{shrunk}, nil)
	}
	agent.dispatch(t.Context(), nil, []checks.Alert{shrunkOK})
	agent.dispatch(t.Context(), []checks.Alert{shrunk}, nil)
	if want := []string{"shrunk", "shrunk ok", "shrunk"}; !slices.Equal(out.sent, want) {
		t.Fatalf("ушло %v, ждали %v", out.sent, want)
	}
}
