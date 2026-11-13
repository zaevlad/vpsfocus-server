package store

import (
	"testing"
	"time"

	"agent/internal/metrics"
)

// Подкачка живёт своей таблицей (Р61): ноль — «подкачки нет» и
// возвращается нулём, замер без строки — «не знаем». Ротация уносит и её, и
// остановки.
func TestSwapTravelsWithTheSample(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	zero := uint64(0)

	noSwap := metrics.Sample{At: now.Add(-time.Minute), MemTotalMB: 1000, DiskTotalMB: 2000,
		SwapTotalMB: &zero, SwapFreeMB: &zero}
	unknown := metrics.Sample{At: now, MemTotalMB: 1000, DiskTotalMB: 2000}
	for _, sample := range []metrics.Sample{noSwap, unknown} {
		if err := db.SaveSample(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := db.Samples(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0].SwapTotalMB == nil || *samples[0].SwapTotalMB != 0 || samples[1].SwapTotalMB != nil {
		t.Fatalf("замеры: %+v", samples)
	}
	before, ok, err := db.SampleBefore(ctx, now)
	if err != nil || !ok || before.SwapTotalMB == nil {
		t.Fatalf("последний перед: %+v %v %v", before, ok, err)
	}

	if err := db.SaveOomKill(ctx, metrics.OomKill{At: now, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Rotate(ctx, -time.Hour); err != nil {
		t.Fatal(err)
	}
	kills, err := db.OomKills(ctx, time.Unix(0, 0))
	if err != nil || len(kills) != 0 {
		t.Fatalf("ротация уносит остановки: %+v %v", kills, err)
	}
}

// Счётчик остановок помнится между запусками; остановка с неизвестными
// именами возвращается «не знаем», а не пустым списком.
func TestOomCounterAndKills(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	if _, seen, err := db.OomCounter(ctx); err != nil || seen {
		t.Fatalf("пустая база: %v %v", seen, err)
	}
	if err := db.SaveOomCounter(ctx, 4, time.Now()); err != nil {
		t.Fatal(err)
	}
	if value, seen, err := db.OomCounter(ctx); err != nil || !seen || value != 4 {
		t.Fatalf("счётчик: %d %v %v", value, seen, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.SaveOomKill(ctx, metrics.OomKill{At: now.Add(-time.Hour), Count: 1, TopKnown: true,
		Top: []metrics.Process{{Name: "php-fpm", MB: 400}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveOomKill(ctx, metrics.OomKill{At: now, Count: 2}); err != nil {
		t.Fatal(err)
	}
	kills, err := db.OomKills(ctx, now.Add(-2*time.Hour))
	if err != nil || len(kills) != 2 {
		t.Fatalf("остановки: %+v %v", kills, err)
	}
	if kills[0].Count != 2 || kills[0].TopKnown || kills[1].Top[0].Name != "php-fpm" {
		t.Fatalf("свежие первыми, имена только у известных: %+v", kills)
	}
}
