package checks

import (
	"fmt"
	"strings"
	"time"

	"agent/internal/metrics"
)

// OomWindow — сколько держится тревога об остановке программ (П1, Р62).
//
// Остановка — событие, а не состояние: к утру его уже нет, и тревога,
// заглушённая тихими часами, пропала бы. Поэтому состоянием служит «было за
// последние сутки»: тревога горит, пока в окне есть остановка, и уходит
// утром, если ночью было тихо.
const OomWindow = 24 * time.Hour

// OomWeek — за сколько свод и недельный отчёт считают остановки (П6).
const OomWeek = 7 * 24 * time.Hour

// OomAlert решает, звать ли человека из-за остановок программ.
//
// Не срочная (В7 — Р15): если сайт упал, тревога о сайте придёт сама, а эта
// объясняет почему — и может подождать утра. Одно письмо на окно (ступень
// 1, как у похудевшей копии): новые остановки внутри окна видны в ленте и
// в отчёте, а не вторым письмом. Отбой — без письма (Р62).
func OomAlert(words Words, kills []metrics.OomKill, now time.Time) (firing, resolved []Alert) {
	count := 0
	var latest *metrics.OomKill
	for i := range kills {
		kill := &kills[i]
		if now.Sub(kill.At) > OomWindow || kill.At.After(now.Add(time.Minute)) {
			continue
		}
		count += kill.Count
		if latest == nil || kill.At.After(latest.At) {
			latest = kill
		}
	}

	if count == 0 {
		return nil, []Alert{{Kind: KindOom, Target: "server", QuietResolve: true}}
	}

	names := words.OomNamesUnknown
	if latest.TopKnown && len(latest.Top) > 0 {
		parts := make([]string, 0, len(latest.Top))
		for _, process := range latest.Top {
			parts = append(parts, fmt.Sprintf(words.OomProcess, process.Name, process.MB))
		}
		names = fmt.Sprintf(words.OomNames, strings.Join(parts, ", "))
	}

	return []Alert{{
		Kind:    KindOom,
		Target:  "server",
		Subject: words.OomSubject,
		Body:    fmt.Sprintf(words.OomBody, count, names),
		Step:    1,
	}}, nil
}
