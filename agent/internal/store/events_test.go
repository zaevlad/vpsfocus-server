package store

import (
	"testing"
	"time"

	"agent/internal/events"
	"agent/internal/metrics"
)

func sampleAt(at time.Time) metrics.Sample {
	return metrics.Sample{At: at, DiskTotalMB: 40000, DiskFreeMB: 20000, UptimeSeconds: 3600}
}

// Лента читается сверху: последнее случившееся — первая строка.
func TestEventsComeNewestFirst(t *testing.T) {
	db := openTestStore(t)
	base := time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)

	for index, kind := range []string{
		events.KindAgentStarted,
		events.KindServerRebooted,
		events.KindLinksScan,
	} {
		saved := events.Event{At: base.Add(time.Duration(index) * time.Minute), Kind: kind}
		if err := db.SaveEvent(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}

	list, err := db.Events(t.Context(), base.Add(-time.Hour), base.Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("событий %d, ждали три", len(list))
	}
	if list[0].Kind != events.KindLinksScan {
		t.Fatalf("первым пришло %q, ждали последнее по времени", list[0].Kind)
	}
	if list[2].Kind != events.KindAgentStarted {
		t.Fatalf("последним пришло %q, ждали самое старое", list[2].Kind)
	}
}

// Окно отсекает то, что было вне его: разбор аварии спрашивает про час, а
// не про всё, что помнит агент.
func TestEventsOutsideTheWindowStayOut(t *testing.T) {
	db := openTestStore(t)
	base := time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{base.Add(-2 * time.Hour), base, base.Add(2 * time.Hour)} {
		if err := db.SaveEvent(t.Context(),
			events.Event{At: at, Kind: events.KindAgentStarted}); err != nil {
			t.Fatal(err)
		}
	}

	list, err := db.Events(t.Context(), base.Add(-time.Hour), base.Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("событий %d, ждали одно", len(list))
	}
	if !list[0].At.Equal(base) {
		t.Fatalf("время %v, ждали %v", list[0].At, base)
	}
}

// Уточнение и цель доезжают как есть: по ним экран собирает строку, а
// приглушённое расхождение здесь стоило бы неверного разбора аварии.
func TestEventKeepsTargetAndDetail(t *testing.T) {
	db := openTestStore(t)
	at := time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)

	saved := events.Event{
		At:     at,
		Kind:   events.KindAlertFiring,
		Target: "example.com",
		Detail: "cert",
	}
	if err := db.SaveEvent(t.Context(), saved); err != nil {
		t.Fatal(err)
	}

	list, err := db.Events(t.Context(), at.Add(-time.Minute), at.Add(time.Minute), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("событий %d, ждали одно", len(list))
	}
	if list[0].Target != "example.com" || list[0].Detail != "cert" {
		t.Fatalf("вернулось %+v", list[0])
	}
}

// Журнал живёт столько же, сколько замеры: событие, к которому нечего
// приложить, ничего не объясняет, а место на диске клиента не наше.
func TestRotateEventsDropsOldOnes(t *testing.T) {
	db := openTestStore(t)

	old := events.Event{At: time.Now().UTC().Add(-40 * 24 * time.Hour), Kind: events.KindAgentStarted}
	fresh := events.Event{At: time.Now().UTC().Add(-time.Hour), Kind: events.KindAgentStarted}
	for _, event := range []events.Event{old, fresh} {
		if err := db.SaveEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := db.RotateEvents(t.Context(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("удалено %d, ждали одно", removed)
	}

	list, err := db.Events(t.Context(), time.Now().UTC().Add(-365*24*time.Hour), time.Now().UTC(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("осталось %d, ждали одно", len(list))
	}
}

// Последний замер перед окном — то единственное, что известно об аварии, в
// которой агент лежал вместе с сервером.
func TestSampleBeforeTheWindow(t *testing.T) {
	db := openTestStore(t)
	base := time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)

	for _, minute := range []int{-10, -5, 20} {
		if err := db.SaveSample(t.Context(), sampleAt(base.Add(time.Duration(minute)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	before, ok, err := db.SampleBefore(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("замера перед окном не нашлось")
	}
	if !before.At.Equal(base.Add(-5 * time.Minute)) {
		t.Fatalf("замер в %v, ждали ближайший перед окном", before.At)
	}

	// Замеров до начала времён нет — и это не ошибка, а честный ответ.
	if _, ok, err := db.SampleBefore(t.Context(), base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("нашёлся замер там, где их нет")
	}
}

// Окно замеров не тащит хвост: экран здоровья спрашивает «с тех пор», разбор
// аварии — «в этот час», и путать их нельзя.
func TestSamplesBetweenBoundsBothEnds(t *testing.T) {
	db := openTestStore(t)
	base := time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)

	for _, minute := range []int{-10, 5, 30} {
		if err := db.SaveSample(t.Context(), sampleAt(base.Add(time.Duration(minute)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.SamplesBetween(t.Context(), base, base.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("замеров %d, ждали один: %+v", len(got), got)
	}
}
