package main

import (
	"path/filepath"
	"testing"
	"time"

	"agent/internal/config"
	"agent/internal/events"
	"agent/internal/metrics"
	"agent/internal/outage"
	"agent/internal/store"
)

func testAgent(t *testing.T) (*Agent, *store.Store) {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("база не открылась: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return &Agent{
		db:      db,
		rootCtx: t.Context(),
		cfg:     config.Config{SampleInterval: time.Minute},
	}, db
}

func around(t *testing.T, agent *Agent, at time.Time, minutes int, now time.Time) outage.Window {
	t.Helper()

	payload, err := agent.Around(at, minutes, now)
	if err != nil {
		t.Fatalf("разбор аварии: %v", err)
	}
	window, ok := payload.(outage.Window)
	if !ok {
		t.Fatalf("вернулось %T, ждали окно", payload)
	}
	return window
}

// Главная беда этой функции: часы не сходятся, и она ломается молча.
//
// Инцидент записан по часам приложения, замеры — по часам сервера клиента.
// Разъедься они на час — и окно «покажи, что было в 3:12» промахнётся мимо
// данных, не сказав об этом ни слова. Окно обязано сдвинуться на
// расхождение, и расхождение обязано уехать наружу.
func TestOutageWindowFollowsTheClockSkew(t *testing.T) {
	agent, db := testAgent(t)

	// Часы сервера на час впереди часов приложения.
	agentNow := time.Now().UTC()
	appNow := agentNow.Add(-time.Hour)

	sampled := agentNow.Add(-2 * time.Minute)
	if err := db.SaveSample(t.Context(), metrics.Sample{
		At: sampled, DiskTotalMB: 40000, DiskFreeMB: 20000, UptimeSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	// «Что было две минуты назад» — по часам приложения.
	incident := appNow.Add(-2 * time.Minute)

	window := around(t, agent, incident, 20, appNow)
	if window.SkewSeconds < 3500 || window.SkewSeconds > 3700 {
		t.Fatalf("расхождение %d секунд, ждали около часа", window.SkewSeconds)
	}
	if len(window.Samples) != 1 {
		t.Fatalf("замеров в окне %d, ждали один: окно промахнулось мимо данных", len(window.Samples))
	}

	// Проверка того, что беда настоящая: без второй половины вопроса окно
	// не сдвигается и данных не находит.
	blind := around(t, agent, incident, 20, time.Time{})
	if blind.SkewSeconds != 0 {
		t.Fatalf("расхождение %d, ждали ноль: время приложения не спрашивали", blind.SkewSeconds)
	}
	if len(blind.Samples) != 0 {
		t.Fatal("несдвинутое окно нашло замеры: тест перестал что-либо доказывать")
	}
}

// Во время падения данных нет, и это надо показать, а не чинить.
//
// Пустое окно — это не «ничего не происходило», а «сервер не отвечал».
// Рядом обязано лежать последнее, что было **перед** ним: другого следа
// аварии, в которой агент лежал вместе с сервером, не существует.
func TestOutageWindowShowsTheGapAndTheLastSampleBefore(t *testing.T) {
	agent, db := testAgent(t)
	// Секунды, а не наносекунды: время замера лежит в базе целыми
	// секундами эпохи, и сравнивать с ним хвост часов бессмысленно.
	agentNow := time.Now().UTC().Truncate(time.Second)

	last := agentNow.Add(-50 * time.Minute)
	if err := db.SaveSample(t.Context(), metrics.Sample{
		At: last, DiskTotalMB: 40000, DiskFreeMB: 12000, UptimeSeconds: 86400,
	}); err != nil {
		t.Fatal(err)
	}

	window := around(t, agent, agentNow.Add(-20*time.Minute), 20, agentNow)

	if len(window.Samples) != 0 {
		t.Fatalf("замеров в окне %d, ждали ноль", len(window.Samples))
	}
	if len(window.Gaps) != 1 {
		t.Fatalf("провалов %d, ждали один: молчание выдано за порядок", len(window.Gaps))
	}
	if window.Before == nil {
		t.Fatal("последнего замера перед окном нет: об аварии не сказано ничего")
	}
	if !window.Before.At.Equal(last) {
		t.Fatalf("замер перед окном в %v, ждали %v", window.Before.At, last)
	}
}

// Отметки журнала попадают в то же окно: «что происходило» и «что при этом
// менялось» — один вопрос, и разносить их по двум запросам незачем.
func TestOutageWindowCarriesTheJournal(t *testing.T) {
	agent, db := testAgent(t)
	agentNow := time.Now().UTC()

	if err := db.SaveEvent(t.Context(), events.Event{
		At: agentNow.Add(-5 * time.Minute), Kind: events.KindServerRebooted,
	}); err != nil {
		t.Fatal(err)
	}
	// Событие вне окна остаётся вне его.
	if err := db.SaveEvent(t.Context(), events.Event{
		At: agentNow.Add(-5 * time.Hour), Kind: events.KindAgentStarted,
	}); err != nil {
		t.Fatal(err)
	}

	window := around(t, agent, agentNow.Add(-5*time.Minute), 20, agentNow)
	if len(window.Events) != 1 {
		t.Fatalf("событий в окне %d, ждали одно: %+v", len(window.Events), window.Events)
	}
	if window.Events[0].Kind != events.KindServerRebooted {
		t.Fatalf("событие %q, ждали перезагрузку сервера", window.Events[0].Kind)
	}
}

// Лента отдаёт расхождение часов вместе с событиями.
//
// Без него два журнала — приложения и агента — на одну ленту не сводятся:
// события установки записаны по нашим часам, события сервера по его.
func TestJournalReportsTheSkew(t *testing.T) {
	agent, db := testAgent(t)
	agentNow := time.Now().UTC()

	if err := db.SaveEvent(t.Context(), events.Event{
		At: agentNow.Add(-time.Minute), Kind: events.KindAgentStarted,
	}); err != nil {
		t.Fatal(err)
	}

	payload, err := agent.Events(24, agentNow.Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("лента: %v", err)
	}
	journal, ok := payload.(Journal)
	if !ok {
		t.Fatalf("вернулось %T, ждали ленту", payload)
	}
	if journal.SkewSeconds < 590 || journal.SkewSeconds > 610 {
		t.Fatalf("расхождение %d секунд, ждали около десяти минут", journal.SkewSeconds)
	}
	if len(journal.Events) != 1 {
		t.Fatalf("событий %d, ждали одно", len(journal.Events))
	}
}
