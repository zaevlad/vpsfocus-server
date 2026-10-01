package health

import (
	"testing"
	"time"

	"agent/internal/config"
	"agent/internal/metrics"
	"agent/internal/probe"
	"agent/internal/store"
)

func sample() *metrics.Sample {
	return &metrics.Sample{
		At:             time.Now(),
		DiskTotalMB:    40000,
		DiskFreeMB:     30000,
		MemTotalMB:     4000,
		MemAvailableMB: 3000,
	}
}

func base() Input {
	return Input{
		Now:              time.Now(),
		Sample:           sample(),
		DiskAlertPercent: 85,
		Channels:         []string{"smtp"},
	}
}

func find(reasons []Reason, code string) (Reason, bool) {
	for _, reason := range reasons {
		if reason.Code == code {
			return reason, true
		}
	}
	return Reason{}, false
}

// Неработающий ключевой адрес — самый прямой повод из всех: он про то, ради
// чего сайт и существует.
func TestFailingProbeRaisesTheProject(t *testing.T) {
	in := base()
	in.Projects = []config.Project{{ID: "p1", Name: "Клиент", Domains: []string{"example.com"}}}
	in.Probes = []probe.Result{{
		ProjectID: "p1",
		Name:      "Оформление заказа",
		URL:       "https://example.com/checkout",
		Kind:      probe.KindText,
		Code:      probe.CodeTextMissing,
		CheckedAt: time.Now(),
	}}

	snapshot := Compute(in)
	if len(snapshot.Projects) != 1 {
		t.Fatalf("проектов %d, ждали один", len(snapshot.Projects))
	}
	if snapshot.Projects[0].Level != LevelProblem {
		t.Fatalf("уровень %q, ждали %q", snapshot.Projects[0].Level, LevelProblem)
	}

	reason, ok := find(snapshot.Projects[0].Reasons, CodeProbeFailing)
	if !ok {
		t.Fatal("повода про ключевой адрес нет")
	}
	// Целью идёт адрес, а не имя: приглушают конкретный адрес, а имя
	// человек в любой момент переименует.
	if reason.Domain != "https://example.com/checkout" {
		t.Fatalf("цель %q, ждали адрес", reason.Domain)
	}
	if reason.Detail != probe.CodeTextMissing {
		t.Fatalf("уточнение %q, ждали код беды проверки", reason.Detail)
	}
}

// Прошедшая проверка повода не рождает — и заодно двигает «когда проверяли».
func TestPassingProbeIsSilent(t *testing.T) {
	in := base()
	in.Projects = []config.Project{{ID: "p1", Domains: []string{"example.com"}}}
	in.Probes = []probe.Result{{
		ProjectID: "p1",
		URL:       "https://example.com/",
		OK:        true,
		CheckedAt: time.Now(),
	}}

	snapshot := Compute(in)
	if snapshot.Projects[0].Level != LevelOK {
		t.Fatalf("уровень %q, ждали %q", snapshot.Projects[0].Level, LevelOK)
	}
	if snapshot.Projects[0].CheckedAt == "" {
		t.Fatal("время проверки потеряно: карточка скажет «ещё ничем»")
	}
}

// Беда стека — про сервер, а не про проекты. Мёртвая Umami на VPS с тремя
// клиентами — одна запись и одно действие, а не три красные карточки.
func TestDeadServiceIsAServerReason(t *testing.T) {
	in := base()
	in.Projects = []config.Project{
		{ID: "p1", Domains: []string{"a.test"}},
		{ID: "p2", Domains: []string{"b.test"}},
	}
	in.Services = []probe.Service{
		{Name: probe.ServicePostgres, Up: true},
		{Name: probe.ServiceUmami, Code: probe.CodeUnreachable},
	}

	snapshot := Compute(in)
	if snapshot.Server.Level != LevelProblem {
		t.Fatalf("уровень сервера %q, ждали %q", snapshot.Server.Level, LevelProblem)
	}
	reason, ok := find(snapshot.Server.Reasons, CodeServiceDown)
	if !ok {
		t.Fatal("повода про сервис нет")
	}
	if reason.Domain != probe.ServiceUmami {
		t.Fatalf("цель %q, ждали имя сервиса", reason.Domain)
	}
	for _, project := range snapshot.Projects {
		if project.Level != LevelOK {
			t.Fatalf("проект %s покрасился из-за беды сервера", project.ID)
		}
	}
}

// Мёртвый сервис виден и до первого замера.
//
// В первую минуту после запуска агента замеров ещё нет, а мёртвый postgres
// уже есть. Промолчать о нём из-за отсутствия цифр про процессор значило бы
// спрятать беду за её же соседкой. Найдено живым прогоном.
func TestDeadServiceShowsBeforeTheFirstSample(t *testing.T) {
	in := base()
	in.Sample = nil
	in.Services = []probe.Service{{Name: probe.ServicePostgres, Code: probe.CodeUnreachable}}

	snapshot := Compute(in)
	if _, ok := find(snapshot.Server.Reasons, CodeServiceDown); !ok {
		t.Fatal("мёртвый сервис спрятан за отсутствием замеров")
	}
	if _, ok := find(snapshot.Server.Reasons, CodeMetricsMissing); !ok {
		t.Fatal("повод про отсутствие замеров пропал")
	}
	if snapshot.Server.Level != LevelProblem {
		t.Fatalf("уровень %q, ждали %q", snapshot.Server.Level, LevelProblem)
	}
}

// Молчание проверки трекинга поводом не считается: `Analytics` пуст ровно до
// первого круга, и объявлять этим счётчик сломанным значит пугать человека
// собственным перезапуском агента.
func TestUncheckedTrackerIsNotAReason(t *testing.T) {
	if _, ok := find(Compute(base()).Server.Reasons, CodeTrackerMissing); ok {
		t.Fatal("непроверенный трекинг объявлен сломанным")
	}
}

// А неотданный трекер — повод, и уровнем ниже мёртвого сервиса: сайты
// работают, теряется статистика.
func TestMissingTrackerIsAttention(t *testing.T) {
	in := base()
	in.Analytics = &probe.Analytics{Code: probe.CodeStatus}

	snapshot := Compute(in)
	if snapshot.Server.Level != LevelAttention {
		t.Fatalf("уровень %q, ждали %q", snapshot.Server.Level, LevelAttention)
	}
	if _, ok := find(snapshot.Server.Reasons, CodeTrackerMissing); !ok {
		t.Fatal("повода про трекинг нет")
	}
}

// Приглушение работает и здесь: адрес, про который сказали «знаю, так и
// задумано», перестаёт поднимать уровень, но из свода не исчезает.
func TestProbeCanBeMuted(t *testing.T) {
	in := base()
	in.Projects = []config.Project{{ID: "p1", Domains: []string{"example.com"}}}
	in.Probes = []probe.Result{{
		ProjectID: "p1",
		URL:       "https://example.com/checkout",
		Code:      probe.CodeStatus,
		CheckedAt: time.Now(),
	}}
	in.Suppressions = []store.Suppression{{
		Code:   CodeProbeFailing,
		Target: "https://example.com/checkout",
	}}

	snapshot := Compute(in)
	if snapshot.Projects[0].Level != LevelOK {
		t.Fatalf("уровень %q, ждали %q: приглушение не сработало",
			snapshot.Projects[0].Level, LevelOK)
	}
	reason, ok := find(snapshot.Projects[0].Reasons, CodeProbeFailing)
	if !ok {
		t.Fatal("приглушённый повод исчез: снять приглушение станет нечем")
	}
	if !reason.Suppressed {
		t.Fatal("повод не помечен приглушённым")
	}
}
