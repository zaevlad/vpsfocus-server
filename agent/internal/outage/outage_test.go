package outage

import (
	"testing"
	"time"

	"agent/internal/metrics"
)

func at(minute int) time.Time {
	return time.Date(2026, time.September, 7, 3, minute, 0, 0, time.UTC)
}

func samples(minutes ...int) []metrics.Sample {
	list := make([]metrics.Sample, 0, len(minutes))
	for _, minute := range minutes {
		list = append(list, metrics.Sample{At: at(minute)})
	}
	return list
}

// Главный случай разбора аварии: замеров нет вовсе.
//
// Лежал сервер — лежал и агент. Пустое окно обязано стать провалом, а не
// молчаливым «ничего не происходило»: молчаливый ноль хуже отказа.
func TestEmptyWindowIsOneGap(t *testing.T) {
	gaps := Gaps(nil, at(0), at(30), time.Minute)

	if len(gaps) != 1 {
		t.Fatalf("провалов %d, ждали один", len(gaps))
	}
	if !gaps[0].From.Equal(at(0)) || !gaps[0].To.Equal(at(30)) {
		t.Fatalf("провал %v–%v, ждали всё окно", gaps[0].From, gaps[0].To)
	}
	if gaps[0].Minutes != 30 {
		t.Fatalf("минут %d, ждали 30", gaps[0].Minutes)
	}
}

// Ровный ряд замеров провалов не даёт.
func TestSteadySamplesHaveNoGaps(t *testing.T) {
	list := samples(0, 1, 2, 3, 4, 5)

	if gaps := Gaps(list, at(0), at(5), time.Minute); len(gaps) != 0 {
		t.Fatalf("провалов %d, ждали ноль: %+v", len(gaps), gaps)
	}
}

// Один пропущенный тик — не авария.
//
// Замер снимается тикером, сбор метрик бывает медленным, и объявлять
// провалом каждую подвисшую секунду значит утопить настоящие провалы.
func TestOneMissedTickIsNotAGap(t *testing.T) {
	list := samples(0, 1, 3, 4)

	if gaps := Gaps(list, at(0), at(4), time.Minute); len(gaps) != 0 {
		t.Fatalf("провалов %d, ждали ноль: %+v", len(gaps), gaps)
	}
}

func TestGapInTheMiddle(t *testing.T) {
	list := samples(0, 1, 20, 21)

	gaps := Gaps(list, at(0), at(21), time.Minute)
	if len(gaps) != 1 {
		t.Fatalf("провалов %d, ждали один: %+v", len(gaps), gaps)
	}
	if gaps[0].Minutes != 19 {
		t.Fatalf("минут %d, ждали 19", gaps[0].Minutes)
	}
}

// Авария, начавшаяся до окна: провал считается от его границы.
//
// Не показать его значило бы начать рассказ с середины — а начало как раз и
// есть то, ради чего окно открывают.
func TestGapAtTheLeadingEdge(t *testing.T) {
	list := samples(25, 26)

	gaps := Gaps(list, at(0), at(26), time.Minute)
	if len(gaps) != 1 {
		t.Fatalf("провалов %d, ждали один: %+v", len(gaps), gaps)
	}
	if !gaps[0].From.Equal(at(0)) || !gaps[0].To.Equal(at(25)) {
		t.Fatalf("провал %v–%v, ждали от границы окна", gaps[0].From, gaps[0].To)
	}
}

// И у правой границы: авария, которая ещё идёт.
func TestGapAtTheTrailingEdge(t *testing.T) {
	list := samples(0, 1)

	gaps := Gaps(list, at(0), at(30), time.Minute)
	if len(gaps) != 1 {
		t.Fatalf("провалов %d, ждали один: %+v", len(gaps), gaps)
	}
	if !gaps[0].From.Equal(at(1)) {
		t.Fatalf("провал начался в %v, ждали последний замер", gaps[0].From)
	}
}

// Провал короче минуты не округляется в ноль: «сервер не отвечал 0 минут» —
// это не отчёт, а опечатка.
func TestShortGapStaysOneMinute(t *testing.T) {
	list := []metrics.Sample{
		{At: at(0)},
		{At: at(0).Add(50 * time.Second)},
	}

	gaps := Gaps(list, at(0), at(0).Add(50*time.Second), 10*time.Second)
	if len(gaps) != 1 {
		t.Fatalf("провалов %d, ждали один: %+v", len(gaps), gaps)
	}
	if gaps[0].Minutes != 1 {
		t.Fatalf("минут %d, ждали 1", gaps[0].Minutes)
	}
}

func TestSkew(t *testing.T) {
	agent := time.Date(2026, time.September, 7, 3, 5, 0, 0, time.UTC)
	app := time.Date(2026, time.September, 7, 3, 0, 0, 0, time.UTC)

	if got := Skew(agent, app); got != 5*time.Minute {
		t.Fatalf("расхождение %v, ждали 5 минут", got)
	}
	if got := Skew(agent, app.Add(10*time.Minute)); got != -5*time.Minute {
		t.Fatalf("расхождение %v, ждали минус 5 минут", got)
	}
}

// Приложение не сказало, который час, — сдвигать нечего.
func TestSkewWithoutAppTimeIsZero(t *testing.T) {
	if got := Skew(time.Now(), time.Time{}); got != 0 {
		t.Fatalf("расхождение %v, ждали ноль", got)
	}
}
