package health

import (
	"testing"
	"time"

	"agent/internal/metrics"
)

func inodeSample(total, free uint64) *metrics.Sample {
	return &metrics.Sample{
		At: time.Now(), MemTotalMB: 1000, MemAvailableMB: 900,
		DiskTotalMB: 1000, DiskFreeMB: 900,
		InodesTotal: total, InodesFree: free,
	}
}

func inodeReason(snapshot Snapshot) *Reason {
	for i, item := range snapshot.Server.Reasons {
		if item.Code == CodeInodesFilling || item.Code == CodeInodesFull {
			return &snapshot.Server.Reasons[i]
		}
	}
	return nil
}

// Места под файлы: с 90% — «посмотри», с 97% — «сломано» (Р7), ниже — ничего.
// Места на диске при этом сколько угодно: в этом и беда.
func TestInodesReasonByThreshold(t *testing.T) {
	cases := []struct {
		free  uint64
		code  string
		level Level
	}{
		{free: 110, code: ""},
		{free: 100, code: CodeInodesFilling, level: LevelAttention},
		{free: 31, code: CodeInodesFilling, level: LevelAttention},
		{free: 30, code: CodeInodesFull, level: LevelProblem},
	}
	for _, tc := range cases {
		snapshot := Compute(Input{Now: time.Now(), Sample: inodeSample(1000, tc.free),
			DiskAlertPercent: 85, Channels: channels()})
		got := inodeReason(snapshot)
		if tc.code == "" {
			if got != nil {
				t.Errorf("свободно %d из 1000: лишний повод %s", tc.free, got.Code)
			}
			continue
		}
		if got == nil || got.Code != tc.code || got.Level != tc.level {
			t.Errorf("свободно %d из 1000: ждали %s, пришло %+v", tc.free, tc.code, got)
			continue
		}
		if got.Count != int((1000-tc.free)*100/1000) {
			t.Errorf("процент: %d", got.Count)
		}
	}
}

// Файловая система мест под файлы не считает (btrfs) — повода нет вовсе:
// «не знаем» экран рисует серым сам, вечное «посмотри» было бы шумом (Р9).
func TestInodesUnknownRaisesNothing(t *testing.T) {
	snapshot := Compute(Input{Now: time.Now(), Sample: inodeSample(0, 0),
		DiskAlertPercent: 85, Channels: channels()})
	if got := inodeReason(snapshot); got != nil {
		t.Fatalf("неизвестные места под файлы подняли повод %+v", got)
	}
	if snapshot.Server.Level != LevelOK {
		t.Fatalf("уровень %s", snapshot.Server.Level)
	}
}

// Похудевшая копия — свой повод «посмотри» с «было → стало», и он не
// прячется за неудачей последнего круга.
func TestBackupShrunkIsItsOwnReason(t *testing.T) {
	now := time.Now()
	state := BackupState{
		Configured: true, Interval: 24 * time.Hour, Attempted: true, LastOK: true,
		Freshest: now.Add(-time.Hour), ShrunkFrom: 120 << 20, ShrunkTo: 40 << 20,
	}
	snapshot := Compute(backupInput(state, now))

	var shrunk *Reason
	for i, item := range snapshot.Server.Reasons {
		if item.Code == CodeBackupShrunk {
			shrunk = &snapshot.Server.Reasons[i]
		}
	}
	if shrunk == nil || shrunk.Level != LevelAttention || shrunk.BytesBefore != 120<<20 || shrunk.Bytes != 40<<20 {
		t.Fatalf("повод: %+v", shrunk)
	}

	state.LastOK, state.LastCode = false, "backupUploadFailed"
	codes := map[string]bool{}
	for _, item := range Compute(backupInput(state, now)).Server.Reasons {
		codes[item.Code] = true
	}
	if !codes[CodeBackupFailed] || !codes[CodeBackupShrunk] {
		t.Fatalf("неудача спрятала похудевшую копию: %v", codes)
	}

	state = BackupState{Configured: true, Interval: 24 * time.Hour, Attempted: true, LastOK: true,
		Freshest: now.Add(-time.Hour)}
	for _, item := range Compute(backupInput(state, now)).Server.Reasons {
		if item.Code == CodeBackupShrunk {
			t.Fatal("повод без похудевшей копии")
		}
	}
}
