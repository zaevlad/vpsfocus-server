package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agent/internal/links"
	"agent/internal/store"
)

// Круг, упёршийся в свой потолок, обязан сохранить то, что успел.
//
// Пункт 1.1 спринта 25. Итог писался тем же контекстом, чей потолок только
// что и сработал: следующий же запрос к базе падал с DeadlineExceeded, и
// отчёт терялся целиком — вместе с кодом `linkScanTimeout`, который спринт
// 22 специально сделал достижимым. Тот же рисунок был у замеров, и там он
// стоил дороже: расход квоты Google не доезжал до счёта, и завтрашняя
// квота начиналась заниженной.
func TestExpiredLoopContextStillSavesReport(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("база не открылась: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	agent := &Agent{db: db, rootCtx: t.Context()}

	// Контекст круга, который уже истёк, — ровно то состояние, в котором
	// круг доходит до записи итога.
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	report := links.Report{
		Domain:    "site.test",
		CheckedAt: "2026-09-05T10:00:00Z",
		Pages:     300,
		Error:     "linkScanTimeout",
	}

	// Проверка того, что беда настоящая: прежним контекстом запись не идёт.
	if err := db.SaveLinkReport(expired, report); err == nil {
		t.Fatal("запись истёкшим контекстом прошла: тест перестал что-либо доказывать")
	}

	if err := agent.saving(func(save context.Context) error {
		return db.SaveLinkReport(save, report)
	}); err != nil {
		t.Fatalf("запись итога круга: %v", err)
	}

	stored, err := db.LinkReports(t.Context(), map[string]bool{"site.test": true})
	if err != nil {
		t.Fatalf("чтение отчётов: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("отчётов в базе %d, ожидался один", len(stored))
	}
	if stored[0].Error != "linkScanTimeout" {
		t.Fatalf("код отчёта %q, ожидался linkScanTimeout", stored[0].Error)
	}
	if stored[0].Pages != 300 {
		t.Fatalf("страниц в отчёте %d: частичный результат потерян", stored[0].Pages)
	}
}

// Таймаут круга опознаётся, даже когда lychee не сказал о нём ни слова.
//
// Вторая половина пункта 1.1. Lychee, убитый нашим же контекстом, возвращает
// `*exec.ExitError` («signal: killed»): `Cmd.Wait` подставляет ошибку
// контекста, только если процесс завершился успешно. Ошибка контекста
// приходила лишь тогда, когда время вышло до запуска lychee, — то есть
// почти никогда, и круг, не уложившийся во время, показывался общим
// `linkScanFailed`.
func TestLinkErrorCodeSeesLoopTimeout(t *testing.T) {
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	killed := errors.New("lychee: signal: killed")
	if code := linkErrorCode(expired, killed); code != "linkScanTimeout" {
		t.Fatalf("код %q, ожидался linkScanTimeout", code)
	}

	// А без истёкшего контекста та же ошибка остаётся общим отказом: «мы не
	// уложились» и «оно сломалось» — разные новости.
	if code := linkErrorCode(t.Context(), killed); code != "linkScanFailed" {
		t.Fatalf("код %q, ожидался linkScanFailed", code)
	}

	// Истёкший контекст не имеет права затирать причины, которые мы
	// называем точнее.
	if code := linkErrorCode(expired, links.ErrRobotsForbidden); code != "linksRobotsForbidden" {
		t.Fatalf("код %q, ожидался linksRobotsForbidden", code)
	}

	// Отсутствие бинарника опознаётся по тексту чужой ошибки — проверяем,
	// что ветка жива и стоит выше таймаута.
	notFound := &exec.Error{Name: "lychee", Err: exec.ErrNotFound}
	if code := linkErrorCode(expired, notFound); code != "lycheeUnavailable" {
		t.Fatalf("код %q, ожидался lycheeUnavailable", code)
	}
}
