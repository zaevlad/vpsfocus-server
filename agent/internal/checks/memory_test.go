package checks

import (
	"strings"
	"testing"
	"time"

	"agent/internal/metrics"
)

// Тревога горит, пока в последних сутках есть остановка; считает все
// остановки окна и называет программы последней, если их видели.
func TestOomAlertWindow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	words := WordsFor("en")
	kills := []metrics.OomKill{
		{At: now.Add(-time.Hour), Count: 2, TopKnown: true, Top: []metrics.Process{{Name: "mysqld", MB: 700}, {Name: "node", MB: 300}}},
		{At: now.Add(-5 * time.Hour), Count: 1},
		{At: now.Add(-30 * time.Hour), Count: 9},
	}
	firing, resolved := OomAlert(words, kills, now)
	if len(firing) != 1 || len(resolved) != 0 {
		t.Fatalf("ждали одну тревогу: %v %v", firing, resolved)
	}
	alert := firing[0]
	if alert.Kind != KindOom || alert.Urgent || alert.Step != 1 {
		t.Errorf("не срочная, одно письмо на окно: %+v", alert)
	}
	if !strings.Contains(alert.Body, "3 stop") || !strings.Contains(alert.Body, "mysqld (700 MB), node (300 MB)") {
		t.Errorf("в письме три остановки и имена: %q", alert.Body)
	}

	// Имена не видели — так и сказано.
	firing, _ = OomAlert(words, kills[1:2], now)
	if !strings.Contains(firing[0].Body, words.OomNamesUnknown) {
		t.Errorf("без имён — «не видно»: %q", firing[0].Body)
	}

	// В окне пусто — тихий отбой.
	firing, resolved = OomAlert(words, kills[2:], now)
	if len(firing) != 0 || len(resolved) != 1 || !resolved[0].QuietResolve {
		t.Fatalf("вне окна — отбой без письма: %v %v", firing, resolved)
	}
}
