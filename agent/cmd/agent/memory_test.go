package main

import (
	"slices"
	"testing"
	"time"

	"agent/internal/checks"
	"agent/internal/events"
	"agent/internal/metrics"
)

// fakeOom — подставной счётчик остановок и список процессов.
type fakeOom struct {
	value    uint64
	known    bool
	top      []metrics.Process
	topKnown bool
}

func (f *fakeOom) OomKills() (uint64, bool) { return f.value, f.known }
func (f *fakeOom) TopMemory(int) ([]metrics.Process, bool) {
	return f.top, f.topKnown
}

func oomEvents(t *testing.T, agent *Agent) []string {
	t.Helper()
	list, err := agent.db.Events(t.Context(), time.Unix(0, 0), time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, event := range list {
		if event.Kind == events.KindOomKill {
			out = append(out, event.Detail)
		}
	}
	return out
}

// Первый запуск — только точка отсчёта; рост — событие с числом; меньше
// без перезагрузки — не событие; после перезагрузки всё в счётчике новое;
// перезапуск агента сравнивает с тем, что было до (П2).
func TestWatchOomCountsOnlyGrowth(t *testing.T) {
	now := time.Now()
	agent, _ := quietAgent(t, nil, "", &now)
	reader := &fakeOom{value: 7, known: true, top: []metrics.Process{{Name: "mysqld", MB: 600}}, topKnown: true}

	agent.watchOom(t.Context(), reader, false, now)
	if got := oomEvents(t, agent); len(got) != 0 {
		t.Fatalf("первый взгляд на счётчик — не событие, а записано %v", got)
	}

	reader.value = 9
	agent.watchOom(t.Context(), reader, false, now)
	if got := oomEvents(t, agent); !slices.Equal(got, []string{"2"}) {
		t.Fatalf("рост на 2 — одно событие «2», а записано %v", got)
	}

	// Счётчик стал меньше без признака перезагрузки — новая точка отсчёта.
	reader.value = 3
	agent.watchOom(t.Context(), reader, false, now)
	if got := oomEvents(t, agent); len(got) != 1 {
		t.Fatalf("стало меньше — не событие, а записано %v", got)
	}

	// Перезагрузка: счётчик с нуля, всё в нём — после неё.
	reader.value = 1
	agent.watchOom(t.Context(), reader, true, now)
	if got := oomEvents(t, agent); len(got) != 2 || got[0] != "1" {
		t.Fatalf("после перезагрузки остановка — событие «1», а записано %v", got)
	}

	// Перезапуск агента: новый Agent на той же базе помнит прошлое значение.
	again, _ := quietAgent(t, agent.db, "", &now)
	reader.value = 2
	again.watchOom(t.Context(), reader, false, now)
	if got := oomEvents(t, again); len(got) != 3 || got[0] != "1" {
		t.Fatalf("после перезапуска агента рост на 1 — событие, а записано %v", got)
	}

	kills, err := agent.db.OomKills(t.Context(), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(kills) != 3 || !kills[0].TopKnown || kills[0].Top[0].Name != "mysqld" {
		t.Fatalf("остановки записаны с именами: %+v", kills)
	}
}

// Не знаем счётчика (VPS в контейнере, старое ядро) — ни записи, ни тревоги.
func TestWatchOomUnknownIsSilent(t *testing.T) {
	now := time.Now()
	agent, out := quietAgent(t, nil, "", &now)
	agent.watchOom(t.Context(), &fakeOom{value: 5}, false, now)
	agent.notifyOom(t.Context(), false, now)
	if _, seen, _ := agent.db.OomCounter(t.Context()); seen {
		t.Error("незнакомый счётчик не должен становиться точкой отсчёта")
	}
	if len(out.sent) != 0 {
		t.Errorf("без счётчика писем нет, а ушло %v", out.sent)
	}
}

// Тревога об остановке — одно письмо на сутки, ночью ждёт утра, гаснет
// молча и после этого снова свежая (П1, Р62).
func TestOomAlertWaitsForMorningAndResolvesQuietly(t *testing.T) {
	clock := moscow(t, 10, 3) // ночь
	agent, out := quietAgent(t, nil, "23-08", &clock)
	kill := metrics.OomKill{At: clock.UTC(), Count: 1, TopKnown: false}
	if err := agent.db.SaveOomKill(t.Context(), kill); err != nil {
		t.Fatal(err)
	}

	agent.notifyOom(t.Context(), true, clock)
	if len(out.sent) != 0 {
		t.Fatalf("ночью не срочная тревога ждёт утра, а ушло %v", out.sent)
	}

	clock = moscow(t, 10, 9)
	agent.notifyOom(t.Context(), true, clock)
	clock = moscow(t, 10, 10)
	agent.notifyOom(t.Context(), true, clock)
	if len(out.sent) != 1 {
		t.Fatalf("утром — одно письмо на окно, а ушло %v", out.sent)
	}

	// Окно кончилось — отбой молча, даже не в тихие часы.
	clock = moscow(t, 11, 12)
	agent.notifyOom(t.Context(), true, clock)
	if len(out.sent) != 1 {
		t.Fatalf("отбоя письмом нет, а ушло %v", out.sent)
	}
	state, err := agent.db.AlertState(t.Context(), checks.KindOom, "server")
	if err != nil || state.Firing {
		t.Fatalf("после окна тревога погашена: %+v %v", state, err)
	}

	// Новая остановка — снова свежая тревога.
	if err := agent.db.SaveOomKill(t.Context(), metrics.OomKill{At: clock.UTC(), Count: 2}); err != nil {
		t.Fatal(err)
	}
	agent.notifyOom(t.Context(), true, clock)
	if len(out.sent) != 2 {
		t.Fatalf("новая остановка после отбоя — новое письмо, а ушло %v", out.sent)
	}
}

// Отбой без письма не шлёт и в тихие часы, и днём — и не трогает тревоги,
// которые не горели.
func TestQuietResolveNeverSends(t *testing.T) {
	clock := moscow(t, 10, 12)
	agent, out := quietAgent(t, nil, "", &clock)
	quietOff := checks.Alert{Kind: checks.KindOom, Target: "server", QuietResolve: true}
	agent.dispatch(t.Context(), nil, []checks.Alert{quietOff})
	agent.dispatch(t.Context(), []checks.Alert{{Kind: checks.KindOom, Target: "server", Subject: "oom", Step: 1}}, nil)
	agent.dispatch(t.Context(), nil, []checks.Alert{quietOff})
	if !slices.Equal(out.sent, []string{"oom"}) {
		t.Fatalf("ушло %v, ждали только тревогу", out.sent)
	}
	list, err := agent.db.Events(t.Context(), time.Unix(0, 0), time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range list {
		if event.Kind == events.KindAlertResolved {
			t.Errorf("тихий отбой не пишет «беда кончилась» в ленту: %+v", event)
		}
	}
}
