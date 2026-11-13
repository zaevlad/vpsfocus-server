package metrics

// Память сверх «всего / доступно» (трек AH, предложение 1, спринт 265):
// подкачка, остановки программ ядром из-за нехватки памяти и то, кто
// занимал память сразу после остановки.
//
// Всё читается из того же `/host/proc` под uid 10001 без новых прав —
// сверено в контейнере (спринт 264, Р56–Р60).

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OomKill — одна замеченная остановка (или несколько, если счётчик вырос
// сразу на несколько между двумя замерами).
type OomKill struct {
	At time.Time `json:"at"`
	// На сколько вырос счётчик ядра `oom_kill` с прошлого замера.
	Count int `json:"count"`
	// Кто больше всего занимал память **сразу после** остановки (П4):
	// остановленного в списке уже нет. Только имя процесса, без строки
	// запуска — в ней бывают пароли (Р16).
	Top []Process `json:"top,omitempty"`
	// Удалось ли посмотреть процессы. Нет — «не знаем» (`hidepid`, Р60), а
	// не пустой список.
	TopKnown bool `json:"topKnown"`
}

// Process — имя программы и сколько памяти занимают все её процессы.
type Process struct {
	Name string `json:"name"`
	// Сумма VmRSS всех процессов с этим именем, в мегабайтах.
	MB uint64 `json:"mb"`
}

// Containerized — VPS сам живёт в контейнере (LXC, OpenVZ, Docker, Podman).
//
// Там `vmstat` и подкачка — хоста (ядро их по пространствам имён не делит,
// lxcfs `vmstat` не подменяет — Р58), и остановки у соседей по машине стали
// бы нашими тревогами. Признаки — Р59: `/proc/1/environ` под uid 10001
// закрыт, поэтому смотрим то, что читается.
func Containerized(procDir, rootDir string) bool {
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	// systemd пишет этот файл сам, когда менеджер контейнера задал
	// `container=`.
	if raw, err := os.ReadFile(filepath.Join(rootDir, "run/systemd/container")); err == nil &&
		strings.TrimSpace(string(raw)) != "" {
		return true
	}
	if exists(filepath.Join(rootDir, ".dockerenv")) || exists(filepath.Join(rootDir, "run/.containerenv")) {
		return true
	}
	// Гость OpenVZ: `/proc/vz` есть, `/proc/bc` — только у хоста. Так же
	// судит проверка «Безопасности» (`server-check.sh`).
	if exists(filepath.Join(procDir, "vz")) && !exists(filepath.Join(procDir, "bc")) {
		return true
	}
	// LXC без systemd: lxcfs подмонтирован поверх /proc.
	if raw, err := os.ReadFile(filepath.Join(procDir, "1/mountinfo")); err == nil && lxcfsOverProc(string(raw)) {
		return true
	}
	return false
}

// lxcfsOverProc — смонтирован ли lxcfs поверх файла /proc (так видит себя
// гость LXC). Просто слово «lxcfs» не годится: на обычном сервере с LXD или
// с пакетом lxcfs (Ubuntu 18.04 ставил его сам) он смонтирован в
// /var/lib/lxcfs, и хост посчитал бы себя контейнером (аудит 265–268).
//
// Строка mountinfo: «id parent major:minor root точка-монтирования … - тип
// источник …».
func lxcfsOverProc(mountinfo string) bool {
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[4], "/proc/") {
			continue
		}
		_, tail, found := strings.Cut(line, " - ")
		if found && strings.Contains(tail, "lxcfs") {
			return true
		}
	}
	return false
}

// OomKills — счётчик остановок программ ядром с загрузки сервера.
//
// Нет строки (ядро старше 4.13), файл не читается или VPS в контейнере —
// «не знаем» (Р56, Р58): тревог тогда нет.
func (c *Collector) OomKills() (uint64, bool) {
	if c.containerized {
		return 0, false
	}
	file, err := os.Open(filepath.Join(c.procDir, "vmstat"))
	if err != nil {
		return 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), " ")
		if !found || name != "oom_kill" {
			continue
		}
		count, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		return count, true
	}
	return 0, false
}

// Containerized — то, что Collector решил о сервере при создании.
func (c *Collector) Containerized() bool {
	return c.containerized
}

// TopMemory — какие программы занимают больше всего памяти, по имени.
//
// Процессы с одним именем складываются: двадцать `php-fpm` по 40 МБ — это
// одна программа на 800 МБ, а не двадцать строк. Не видно первого процесса
// (`hidepid`) — «не знаем»: список из одного своего процесса за ответ не
// выдаётся (Р60).
func (c *Collector) TopMemory(limit int) ([]Process, bool) {
	if _, err := os.Stat(filepath.Join(c.procDir, "1/status")); err != nil {
		return nil, false
	}
	entries, err := os.ReadDir(c.procDir)
	if err != nil {
		return nil, false
	}

	byName := map[string]uint64{}
	for _, entry := range entries {
		if !entry.IsDir() || !isPid(entry.Name()) {
			continue
		}
		name, rssKB, ok := processStatus(filepath.Join(c.procDir, entry.Name(), "status"))
		// Процесс успел завершиться или это поток ядра (у него нет VmRSS).
		if !ok || rssKB == 0 {
			continue
		}
		byName[name] += rssKB
	}

	out := make([]Process, 0, len(byName))
	for name, kb := range byName {
		out = append(out, Process{Name: name, MB: kb / 1024})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MB != out[j].MB {
			return out[i].MB > out[j].MB
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, true
}

func isPid(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// processStatus читает из `/proc/<pid>/status` только имя и VmRSS.
//
// `cmdline` не читается никогда: в строке запуска бывают пароли (Р16).
func processStatus(path string) (name string, rssKB uint64, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Name":
			name = value
		case "VmRSS":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				rssKB, _ = strconv.ParseUint(fields[0], 10, 64)
			}
		}
	}
	return name, rssKB, name != ""
}
