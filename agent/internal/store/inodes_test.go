package store

import (
	"testing"
	"time"

	"agent/internal/metrics"
)

// Места под файлы живут своей таблицей (Р5) и возвращаются вместе с
// замером; замер без них — «не знаем», а не ноль занятых. Ротация уносит
// обе таблицы.
func TestInodesTravelWithTheSample(t *testing.T) {
	db := openTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	known := metrics.Sample{At: now.Add(-time.Minute), MemTotalMB: 1000, DiskTotalMB: 2000,
		InodesTotal: 1000, InodesFree: 50}
	unknown := metrics.Sample{At: now, MemTotalMB: 1000, DiskTotalMB: 2000}
	for _, sample := range []metrics.Sample{known, unknown} {
		if err := db.SaveSample(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}

	samples, err := db.Samples(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0].InodesUsedPercent() != 95 || samples[1].InodesKnown() {
		t.Fatalf("замеры: %+v", samples)
	}

	before, ok, err := db.SampleBefore(ctx, now)
	if err != nil || !ok || before.InodesTotal != 1000 {
		t.Fatalf("последний перед: %+v %v %v", before, ok, err)
	}

	if _, err := db.Rotate(ctx, -time.Hour); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.db.QueryRowContext(ctx, `select count(*) from inode_samples`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("после ротации мест под файлы: %d (%v)", left, err)
	}
}
