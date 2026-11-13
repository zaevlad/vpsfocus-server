package main

// Память (трек AH, спринт 265): остановки программ ядром и то, что о них
// узнаёт приложение.

import (
	"context"
	"log"
	"strconv"
	"time"

	"agent/internal/checks"
	"agent/internal/events"
	"agent/internal/metrics"
)

// Сколько программ запоминать «кто занимал память» (В8 — Р16).
const oomTopLimit = 5

// oomReader — откуда круг берёт счётчик и список процессов. Настоящий —
// `metrics.Collector`; тесты подсовывают свой.
type oomReader interface {
	OomKills() (uint64, bool)
	TopMemory(limit int) ([]metrics.Process, bool)
}

// watchOom сравнивает счётчик `oom_kill` с прошлым и записывает выросшее.
//
// Прошлое значение — из базы (П2): сам агент тоже бывает остановленной
// программой, и после его перезапуска сравнивать надо с тем, что было до.
// Первый запуск на сервере — только точка отсчёта: накопленное с загрузки
// не наше дело. Перезагрузка (`rebooted`) — счётчик начался с нуля, всё в
// нём — новое. Стало меньше без перезагрузки — точка отсчёта заново, не
// событие.
func (a *Agent) watchOom(ctx context.Context, reader oomReader, rebooted bool, now time.Time) {
	value, known := reader.OomKills()
	if !known {
		return
	}
	stored, seen, err := a.db.OomCounter(ctx)
	if err != nil {
		log.Printf("счётчик остановок: %v", err)
		return
	}

	var grown uint64
	switch {
	case !seen:
	case rebooted:
		grown = value
	case value >= stored:
		grown = value - stored
	}

	if err := a.db.SaveOomCounter(ctx, value, now); err != nil {
		log.Printf("запись счётчика остановок: %v", err)
		return
	}
	if grown == 0 {
		return
	}

	top, topKnown := reader.TopMemory(oomTopLimit)
	kill := metrics.OomKill{At: now.UTC(), Count: int(grown), Top: top, TopKnown: topKnown}
	if err := a.db.SaveOomKill(ctx, kill); err != nil {
		log.Printf("запись остановки: %v", err)
	}
	a.note(events.KindOomKill, "", strconv.FormatUint(grown, 10))
}

// notifyOom зовёт человека, пока в последних сутках есть остановка.
//
// На каждом круге замеров, а не только в минуту остановки (Р62): тревога,
// заглушённая тихими часами, уходит утром на ближайшем круге.
func (a *Agent) notifyOom(ctx context.Context, known bool, now time.Time) {
	if !known {
		return
	}
	kills, err := a.db.OomKills(ctx, now.Add(-checks.OomWindow))
	if err != nil {
		log.Printf("остановки за сутки: %v", err)
		return
	}
	firing, resolved := checks.OomAlert(a.words(), kills, now)
	a.dispatch(ctx, firing, resolved)
}

// MemoryInfo — что приложение знает об остановках программ.
//
// Отдельным полем ответа замеров: приложение со старым агентом не видит его
// вовсе и говорит «обновите мониторинг», а не «остановок не было».
type MemoryInfo struct {
	// Счётчик читается. Нет — «не знаем»: VPS в контейнере (`Container`),
	// старое ядро или закрытый /proc.
	OomKnown  bool `json:"oomKnown"`
	Container bool `json:"container,omitempty"`
	// Остановки за последнюю неделю (или за период экрана, если он длиннее),
	// свежие первыми.
	OomKills []metrics.OomKill `json:"oomKills"`
}

func (a *Agent) memoryInfo(ctx context.Context, since time.Time) *MemoryInfo {
	known := a.oomKnown()
	info := &MemoryInfo{
		OomKnown:  known,
		Container: a.collector != nil && a.collector.Containerized(),
		OomKills:  []metrics.OomKill{},
	}
	if !known {
		return info
	}
	if week := time.Now().Add(-checks.OomWeek); week.Before(since) {
		since = week
	}
	kills, err := a.db.OomKills(ctx, since)
	if err != nil {
		log.Printf("остановки программ: %v", err)
		return info
	}
	info.OomKills = kills
	return info
}

// oomWeek — сколько остановок за неделю, для свода. false — не знаем.
func (a *Agent) oomWeek(ctx context.Context, now time.Time) (int, bool) {
	if !a.oomKnown() {
		return 0, false
	}
	kills, err := a.db.OomKills(ctx, now.Add(-checks.OomWeek))
	if err != nil {
		log.Printf("свод: остановки программ: %v", err)
		return 0, false
	}
	total := 0
	for _, kill := range kills {
		total += kill.Count
	}
	return total, true
}

// oomKnown — читается ли счётчик остановок на этом сервере.
func (a *Agent) oomKnown() bool {
	if a.collector == nil {
		return false
	}
	_, known := a.collector.OomKills()
	return known
}
