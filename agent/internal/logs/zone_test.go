package logs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Время в логах: nginx и Apache пишут местное время сервера без смещения, и
// читать его как UTC значит сдвигать каждое событие.
//
// Ошибка была тихой: события просто попадали в чужое пятиминутное окно, а
// error-ряд и access-ряд одного графика расходились между собой — при том,
// что оба про один сервер.

// withLocation временно ставит часовой пояс разбора.
func withLocation(t *testing.T, name string) {
	t.Helper()

	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("зона %s недоступна: %v", name, err)
	}
	previous := Location()
	SetLocation(zone)
	t.Cleanup(func() { SetLocation(previous) })
}

// Строка error-лога nginx читается в поясе сервера.
func TestNginxErrorTimeIsServerLocal(t *testing.T) {
	withLocation(t, "Europe/Berlin")

	event, ok := ParseError("2026/07/03 12:00:05 [error] 1#1: что-то сломалось")
	if !ok {
		t.Fatal("строка не разобрана")
	}

	// Летом в Берлине UTC+2: полдень по серверу — это 10:00 UTC.
	want := time.Date(2026, 7, 3, 10, 0, 5, 0, time.UTC)
	if !event.At.UTC().Equal(want) {
		t.Errorf("время %s, ожидали %s", event.At.UTC(), want)
	}
}

// И строка Apache тоже.
func TestApacheErrorTimeIsServerLocal(t *testing.T) {
	withLocation(t, "Europe/Berlin")

	event, ok := ParseError("[Fri Jul 03 12:00:05.123456 2026] [php:error] [pid 1] беда")
	if !ok {
		t.Fatal("строка не разобрана")
	}

	want := time.Date(2026, 7, 3, 10, 0, 5, 123456000, time.UTC)
	if !event.At.UTC().Equal(want) {
		t.Errorf("время %s, ожидали %s", event.At.UTC(), want)
	}
}

// Access-лог со смещением читается по смещению, а не по поясу сервера:
// строка сама сказала, какое время имеется в виду.
func TestAccessTimeKeepsItsOffset(t *testing.T) {
	withLocation(t, "Europe/Berlin")

	line := `203.0.113.9 - - [03/Jul/2026:12:00:05 +0000] "GET /x HTTP/1.1" 500 12 "-" "-"`
	event, ok := ParseAccess(line)
	if !ok {
		t.Fatal("строка access-лога не разобрана")
	}

	want := time.Date(2026, 7, 3, 12, 0, 5, 0, time.UTC)
	if !event.At.UTC().Equal(want) {
		t.Errorf("время %s, ожидали %s", event.At.UTC(), want)
	}
}

// Два ряда одного графика обязаны сходиться: событие error-лога и событие
// access-лога, случившиеся в одну секунду, дают одну и ту же метку.
//
// Ровно это и разъезжалось: access нёс смещение и читался верно, error —
// нет, и на сервере не в UTC они расходились на смещение.
func TestErrorAndAccessAgreeOnTime(t *testing.T) {
	withLocation(t, "Europe/Berlin")

	errorEvent, ok := ParseError("2026/07/03 12:00:05 [error] 1#1: беда")
	if !ok {
		t.Fatal("error-строка не разобрана")
	}
	accessEvent, ok := ParseAccess(
		`203.0.113.9 - - [03/Jul/2026:12:00:05 +0200] "GET /x HTTP/1.1" 500 12 "-" "-"`)
	if !ok {
		t.Fatal("access-строка не разобрана")
	}

	if !errorEvent.At.UTC().Equal(accessEvent.At.UTC()) {
		t.Errorf("ряды разъехались: error %s, access %s",
			errorEvent.At.UTC(), accessEvent.At.UTC())
	}
}

// Зона берётся у самого сервера — из его /etc/timezone.
func TestDetectLocationFromTimezoneFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "timezone"),
		[]byte("Europe/Berlin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	zone, ok := DetectLocation(root)
	if !ok {
		t.Fatal("зона не определилась")
	}
	if zone.String() != "Europe/Berlin" {
		t.Errorf("зона %s", zone)
	}
}

// А где файла нет — из ссылки /etc/localtime.
func TestDetectLocationFromSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("символические ссылки требуют прав администратора")
	}

	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/share/zoneinfo/Europe/Berlin",
		filepath.Join(etc, "localtime")); err != nil {
		t.Fatal(err)
	}

	zone, ok := DetectLocation(root)
	if !ok {
		t.Fatal("зона не определилась")
	}
	if zone.String() != "Europe/Berlin" {
		t.Errorf("зона %s", zone)
	}
}

// Не нашли — честный UTC, а не догадка.
func TestDetectLocationFallsBackToUTC(t *testing.T) {
	zone, ok := DetectLocation(t.TempDir())
	if ok {
		t.Error("зона объявлена определённой на пустом корне")
	}
	if zone != time.UTC {
		t.Errorf("запасная зона %s, ожидали UTC", zone)
	}
}

// Файл на чужом сервере пишем не мы, и его содержимое уходит в
// LoadLocation, который открывает файл по имени.
func TestZoneNameIsValidated(t *testing.T) {
	for _, name := range []string{
		"../../etc/shadow",
		"/etc/shadow",
		"Europe/Berlin\x00",
		"зона",
		"",
	} {
		if got := validZoneName(name); got != "" {
			t.Errorf("имя %q принято как %q", name, got)
		}
	}
	if got := validZoneName("Europe/Berlin"); got != "Europe/Berlin" {
		t.Errorf("настоящее имя отвергнуто: %q", got)
	}
}
