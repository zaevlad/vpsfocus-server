package health

import (
	"testing"
	"time"
)

// Остановки за неделю — «посмотри» с числом; не знаем — повода нет (Р58).
func TestOomReason(t *testing.T) {
	find := func(snapshot Snapshot) *Reason {
		for i, item := range snapshot.Server.Reasons {
			if item.Code == CodeOomKilled {
				return &snapshot.Server.Reasons[i]
			}
		}
		return nil
	}
	base := Input{Now: time.Now(), Sample: inodeSample(0, 0), DiskAlertPercent: 85, Channels: channels()}

	known := base
	known.OomKnown, known.OomWeek = true, 3
	got := find(Compute(known))
	if got == nil || got.Level != LevelAttention || got.Count != 3 {
		t.Fatalf("три остановки за неделю: %+v", got)
	}

	none := base
	none.OomKnown = true
	if got := find(Compute(none)); got != nil {
		t.Fatalf("остановок не было — повода нет: %+v", got)
	}

	unknown := base
	unknown.OomWeek = 3
	if got := find(Compute(unknown)); got != nil {
		t.Fatalf("не знаем — повода нет: %+v", got)
	}
}
