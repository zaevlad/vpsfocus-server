package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

// Подсовываем агенту поддельный /proc: так проверяется разбор, а не то,
// что происходит на машине, где гоняются тесты.
func fakeProc(t *testing.T, stat, meminfo, loadavg, uptime string) string {
	t.Helper()

	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("подготовка %s: %v", name, err)
		}
	}
	write("stat", stat)
	write("meminfo", meminfo)
	write("loadavg", loadavg)
	write("uptime", uptime)
	return dir
}

const meminfoSample = `MemTotal:       16316200 kB
MemFree:         1234567 kB
MemAvailable:   12345678 kB
Buffers:          123456 kB
`

// Мгновенной загрузки процессора в Linux нет: она считается разницей между
// двумя замерами. Первый замер обязан честно сказать «сравнивать не с чем».
func TestCPUNeedsTwoSamples(t *testing.T) {
	first := "cpu  100 0 100 700 100 0 0 0 0 0\n"
	dir := fakeProc(t, first, meminfoSample, "0.42 0.30 0.20 1/200 1234", "100.5 90.0")

	collector := NewCollector(dir, t.TempDir())

	sample, err := collector.Collect()
	if err != nil {
		t.Fatalf("первый замер: %v", err)
	}
	if sample.CPUPercent >= 0 {
		t.Errorf("первый замер не с чем сравнивать, а он дал %v", sample.CPUPercent)
	}

	// Второй замер: занятость выросла на 100, простой на 100 — ровно половина.
	second := "cpu  150 0 150 750 150 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(second), 0o644); err != nil {
		t.Fatalf("подмена stat: %v", err)
	}

	sample, err = collector.Collect()
	if err != nil {
		t.Fatalf("второй замер: %v", err)
	}
	if sample.CPUPercent < 49 || sample.CPUPercent > 51 {
		t.Errorf("ожидали около 50%%, получили %v", sample.CPUPercent)
	}
}

func TestMemoryAndLoadParsed(t *testing.T) {
	dir := fakeProc(t, "cpu  1 0 1 1 1 0 0 0 0 0\n", meminfoSample, "0.42 0.30 0.20 1/200 1234", "3600.5 90.0")
	collector := NewCollector(dir, t.TempDir())

	sample, err := collector.Collect()
	if err != nil {
		t.Fatalf("замер: %v", err)
	}

	if sample.MemTotalMB != 16316200/1024 {
		t.Errorf("MemTotal разобран неверно: %d", sample.MemTotalMB)
	}
	if sample.MemAvailableMB != 12345678/1024 {
		t.Errorf("MemAvailable разобран неверно: %d", sample.MemAvailableMB)
	}
	if sample.Load1 != 0.42 {
		t.Errorf("load1 разобран неверно: %v", sample.Load1)
	}
	if sample.UptimeSeconds != 3600 {
		t.Errorf("аптайм разобран неверно: %d", sample.UptimeSeconds)
	}
}

// Проценты считаются от общего объёма, и деление на ноль здесь встречается
// чаще, чем кажется: замер без данных о диске — обычное дело при неверном
// монтировании.
func TestPercentagesSurviveZeroTotals(t *testing.T) {
	empty := Sample{}
	if empty.MemUsedPercent() != 0 || empty.DiskUsedPercent() != 0 {
		t.Error("пустой замер должен давать нули, а не панику")
	}

	sample := Sample{MemTotalMB: 1000, MemAvailableMB: 250, DiskTotalMB: 2000, DiskFreeMB: 500}
	if got := sample.MemUsedPercent(); got != 75 {
		t.Errorf("память: получили %d%%, ожидали 75%%", got)
	}
	if got := sample.DiskUsedPercent(); got != 75 {
		t.Errorf("диск: получили %d%%, ожидали 75%%", got)
	}
}

// Совсем пустой /proc — это не «сервер идеально чист», а неверно
// смонтированный контейнер. Такой замер писать в базу нельзя.
func TestMissingProcIsAnError(t *testing.T) {
	collector := NewCollector(filepath.Join(t.TempDir(), "нет-такого"), t.TempDir())

	if _, err := collector.Collect(); err == nil {
		t.Error("отсутствие /proc должно быть ошибкой")
	}
}
