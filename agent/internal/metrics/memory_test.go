package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const meminfoWithSwap = `MemTotal:       2000000 kB
MemAvailable:    500000 kB
SwapTotal:      1048576 kB
SwapFree:        524288 kB
`

// Счётчик остановок — строка `oom_kill` из vmstat; нет строки — не знаем.
func TestOomKillsReadsVmstat(t *testing.T) {
	dir := fakeProc(t, "cpu  1 0 1 1 0 0 0 0 0 0\n", meminfoSample, "0 0 0 1/1 1", "1 1")
	writeFile(t, filepath.Join(dir, "vmstat"), "nr_free_pages 100\noom_kill 3\npgfault 9\n")
	collector := NewCollector(dir, t.TempDir())
	if value, known := collector.OomKills(); !known || value != 3 {
		t.Fatalf("ждали 3, получили %d %v", value, known)
	}

	writeFile(t, filepath.Join(dir, "vmstat"), "nr_free_pages 100\n")
	if _, known := collector.OomKills(); known {
		t.Error("ядро без oom_kill — «не знаем»")
	}
}

// Подкачка: ноль — ответ «подкачки нет», а не «не знаем» (Р61).
func TestSwapZeroIsAnAnswer(t *testing.T) {
	dir := fakeProc(t, "cpu  1 0 1 1 0 0 0 0 0 0\n", meminfoWithSwap, "0 0 0 1/1 1", "1 1")
	sample, err := NewCollector(dir, t.TempDir()).Collect()
	if err != nil {
		t.Fatal(err)
	}
	if sample.SwapTotalMB == nil || *sample.SwapTotalMB != 1024 || *sample.SwapFreeMB != 512 {
		t.Fatalf("подкачка 1024/512, а получили %v %v", sample.SwapTotalMB, sample.SwapFreeMB)
	}

	writeFile(t, filepath.Join(dir, "meminfo"), "MemTotal: 2000000 kB\nMemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	sample, _ = NewCollector(dir, t.TempDir()).Collect()
	if sample.SwapTotalMB == nil || *sample.SwapTotalMB != 0 {
		t.Fatal("SwapTotal 0 — «подкачки нет», это известно")
	}

	writeFile(t, filepath.Join(dir, "meminfo"), meminfoSample)
	sample, _ = NewCollector(dir, t.TempDir()).Collect()
	if sample.SwapTotalMB != nil {
		t.Fatal("нет строки SwapTotal — «не знаем»")
	}
}

// VPS в контейнере — счётчик и подкачка «не знаем» (Р58, Р59): каждый
// признак сам по себе.
func TestContainerizedHidesHostCounters(t *testing.T) {
	cases := map[string]func(proc, root string){
		"systemd": func(_, root string) { writeFile(t, filepath.Join(root, "run/systemd/container"), "lxc\n") },
		"docker":  func(_, root string) { writeFile(t, filepath.Join(root, ".dockerenv"), "") },
		"podman":  func(_, root string) { writeFile(t, filepath.Join(root, "run/.containerenv"), "") },
		"openvz":  func(proc, _ string) { writeFile(t, filepath.Join(proc, "vz/veinfo"), "") },
		"lxcfs": func(proc, _ string) {
			writeFile(t, filepath.Join(proc, "1/mountinfo"), "1 0 0:1 / /proc/meminfo rw - fuse.lxcfs lxcfs rw\n")
		},
		"vz хоста": nil,
		// Хост с LXD или пакетом lxcfs: lxcfs смонтирован в /var/lib, а не
		// поверх /proc, — это не гость.
		"lxcfs хоста": func(proc, _ string) {
			writeFile(t, filepath.Join(proc, "1/mountinfo"),
				"25 1 0:22 / / rw - ext4 /dev/sda1 rw\n"+
					"40 25 0:35 / /var/lib/lxcfs rw,nosuid - fuse.lxcfs lxcfs rw\n")
		},
	}
	for name, mark := range cases {
		t.Run(name, func(t *testing.T) {
			proc := fakeProc(t, "cpu  1 0 1 1 0 0 0 0 0 0\n", meminfoWithSwap, "0 0 0 1/1 1", "1 1")
			writeFile(t, filepath.Join(proc, "vmstat"), "oom_kill 1\n")
			root := t.TempDir()
			want := name != "lxcfs хоста"
			if mark == nil {
				// Хост OpenVZ: есть и /proc/vz, и /proc/bc — это не гость.
				writeFile(t, filepath.Join(proc, "vz/veinfo"), "")
				writeFile(t, filepath.Join(proc, "bc/0"), "")
				want = false
			} else {
				mark(proc, root)
			}
			collector := NewCollector(proc, root)
			if collector.Containerized() != want {
				t.Fatalf("контейнер: ждали %v", want)
			}
			_, known := collector.OomKills()
			sample, _ := collector.Collect()
			if want && (known || sample.SwapTotalMB != nil) {
				t.Error("в контейнере счётчик и подкачка — «не знаем»")
			}
			if !want && (!known || sample.SwapTotalMB == nil) {
				t.Error("не контейнер — счётчик и подкачка читаются")
			}
		})
	}
}

// Кто занимал память: по имени, процессы складываются; потоки ядра без
// VmRSS не считаются; не видно первого процесса (hidepid) — «не знаем».
func TestTopMemoryGroupsByName(t *testing.T) {
	proc := t.TempDir()
	status := func(pid, name, rss string) {
		body := "Name:\t" + name + "\nState:\tS\n"
		if rss != "" {
			body += "VmRSS:\t" + rss + " kB\n"
		}
		writeFile(t, filepath.Join(proc, pid, "status"), body)
	}
	status("1", "systemd", "10240")
	status("20", "php-fpm", "204800")
	status("21", "php-fpm", "204800")
	status("30", "mysqld", "307200")
	status("40", "kworker/0:1", "")
	writeFile(t, filepath.Join(proc, "cmdline-like", "status"), "Name:\tnot-a-pid\nVmRSS:\t999999 kB\n")

	collector := &Collector{procDir: proc}
	top, known := collector.TopMemory(2)
	if !known {
		t.Fatal("процессы видны")
	}
	if len(top) != 2 || top[0] != (Process{Name: "php-fpm", MB: 400}) || top[1] != (Process{Name: "mysqld", MB: 300}) {
		t.Fatalf("ждали php-fpm 400 и mysqld 300, получили %+v", top)
	}

	if err := os.RemoveAll(filepath.Join(proc, "1")); err != nil {
		t.Fatal(err)
	}
	if _, known := collector.TopMemory(5); known {
		t.Error("не видно /proc/1 (hidepid) — «не знаем»")
	}
}
