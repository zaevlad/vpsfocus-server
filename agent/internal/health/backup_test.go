package health

import (
	"strings"
	"testing"
	"time"

	"agent/internal/config"
)

// Не делать копий — законный выбор, а не находка.
//
// Свод отвечает на вопрос «нужно ли что-то делать сегодня». Агентство,
// которое возит копии своими средствами, не должно видеть нашу жёлтую
// строчку вечно.
func TestBackupSilentWhenNotConfigured(t *testing.T) {
	snapshot := Compute(backupInput(BackupState{Configured: false}, time.Now()))

	for _, reason := range snapshot.Server.Reasons {
		if reason.Code == CodeBackupStale || reason.Code == CodeBackupFailed {
			t.Fatalf("выключенные копии подняли повод %s", reason.Code)
		}
	}
}

// Первые минуты после запуска агента копий ещё нет, и это не беда.
//
// Круг копии стартует через двенадцать минут после запуска: объявить в это
// время «копий нет вовсе» значило бы пугать человека собственным
// перезапуском стека — ровно та беда, из-за которой еженедельный отчёт не
// уходит сразу при старте.
func TestBackupSilentBeforeTheFirstRound(t *testing.T) {
	snapshot := Compute(backupInput(BackupState{
		Configured: true,
		Interval:   24 * time.Hour,
		Attempted:  false,
	}, time.Now()))

	for _, reason := range snapshot.Server.Reasons {
		if reason.Code == CodeBackupStale || reason.Code == CodeBackupFailed {
			t.Fatalf("круг ещё не был, а повод %s уже есть", reason.Code)
		}
	}
}

// Одна неудачная ночь — это «посмотри», а не «копий нет».
func TestBackupFailureIsItsOwnReason(t *testing.T) {
	now := time.Now()
	snapshot := Compute(backupInput(BackupState{
		Configured: true,
		Interval:   24 * time.Hour,
		Attempted:  true,
		LastOK:     false,
		LastCode:   "backupAccessDenied",
		// Вчерашняя копия ещё свежая: страховка есть, сломалась только
		// последняя попытка.
		Freshest: now.Add(-20 * time.Hour),
	}, now))

	reason, ok := find(snapshot.Server.Reasons, CodeBackupFailed)
	if !ok {
		t.Fatal("неудачная копия не подняла повода")
	}
	if reason.Detail != "backupAccessDenied" {
		t.Errorf("код беды не доехал: %q", reason.Detail)
	}
	if _, ok := find(snapshot.Server.Reasons, CodeBackupStale); ok {
		t.Error("«копий нет» встало поверх «попытка не удалась»")
	}
}

// Копий нет дольше двух суток — это уже про отсутствие страховки.
//
// И второй повод поверх первого не встаёт: «попытка провалилась» означает
// «смотри ошибку», «копий нет» — «страховки у тебя сейчас нет вовсе». Разные
// первые шаги, та же линия, что у сторожа задач.
func TestBackupStaleReplacesFailure(t *testing.T) {
	now := time.Now()
	snapshot := Compute(backupInput(BackupState{
		Configured: true,
		Interval:   24 * time.Hour,
		Attempted:  true,
		LastOK:     false,
		LastCode:   "backupDumpFailed",
		Freshest:   now.Add(-5 * 24 * time.Hour),
	}, now))

	reason, ok := find(snapshot.Server.Reasons, CodeBackupStale)
	if !ok {
		t.Fatal("пятидневное молчание копий не подняло повода")
	}
	if reason.Count != 5 {
		t.Errorf("возраст копии посчитан как %d дн.", reason.Count)
	}
	if reason.Detail != "backupDumpFailed" {
		t.Errorf("код беды не доехал: %q", reason.Detail)
	}
	if _, ok := find(snapshot.Server.Reasons, CodeBackupFailed); ok {
		t.Error("два повода об одной беде сразу")
	}
}

// Ни одной удачной копии — это отдельная новость, а не «ноль дней назад».
func TestBackupNeverSucceededIsItsOwnReason(t *testing.T) {
	now := time.Now()
	snapshot := Compute(backupInput(BackupState{
		Configured: true,
		Interval:   24 * time.Hour,
		Attempted:  true,
		LastOK:     false,
		LastCode:   "backupBucketMissing",
	}, now))

	reason, ok := find(snapshot.Server.Reasons, CodeBackupMissing)
	if !ok {
		t.Fatal("копий не было ни разу, а свод молчит")
	}
	if reason.Detail != "backupBucketMissing" {
		t.Errorf("код беды не доехал: %q", reason.Detail)
	}
	// Возраст здесь не считается вовсе: «ноль дней назад» — неправда в
	// самую тревожную сторону.
	if _, ok := find(snapshot.Server.Reasons, CodeBackupStale); ok {
		t.Error("копия, которой не было, объявлена протухшей")
	}
}

// Свежая копия поводов не рождает.
func TestFreshBackupSaysNothing(t *testing.T) {
	now := time.Now()
	snapshot := Compute(backupInput(BackupState{
		Configured: true,
		Interval:   24 * time.Hour,
		Attempted:  true,
		LastOK:     true,
		Freshest:   now.Add(-time.Hour),
	}, now))

	for _, reason := range snapshot.Server.Reasons {
		if reason.Code == CodeBackupStale || reason.Code == CodeBackupFailed {
			t.Fatalf("свежая копия подняла повод %s", reason.Code)
		}
	}
}

// Копия — беда сервера, а не проекта.
//
// Копия снимается со стека, один на все проекты этого VPS: три красные
// карточки вместо одной строки отправили бы человека чинить исправные сайты.
func TestBackupTroubleStaysWithTheServer(t *testing.T) {
	now := time.Now()
	in := backupInput(BackupState{
		Configured: true,
		Interval:   24 * time.Hour,
		Attempted:  true,
		LastOK:     false,
		LastCode:   "backupAccessDenied",
		Freshest:   now.Add(-time.Hour),
	}, now)
	in.Projects = []config.Project{
		{ID: "p1", Name: "Первый", Domains: []string{"first.example"}},
		{ID: "p2", Name: "Второй", Domains: []string{"second.example"}},
	}

	snapshot := Compute(in)
	for _, project := range snapshot.Projects {
		for _, reason := range project.Reasons {
			if strings.HasPrefix(reason.Code, "healthBackup") {
				t.Fatalf("повод о копии протёк в проект %s", project.ID)
			}
		}
	}
}

func backupInput(state BackupState, now time.Time) Input {
	in := base()
	in.Now = now
	in.Sample.At = now.Add(-time.Minute)
	in.MetricsStaleAfter = 15 * time.Minute
	in.CertDays = 14
	in.DomainDays = 30
	in.Backup = state
	return in
}
