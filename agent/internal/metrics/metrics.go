// Package metrics снимает показатели сервера, а не контейнера.
//
// Агент живёт в контейнере, но рассказывать обязан про машину: агентство
// смотрит, не кончается ли диск у клиента, а не сколько занял наш образ.
// Поэтому `/proc` и корень хоста монтируются внутрь только на чтение, и всё
// читается оттуда.
//
// Только Linux: стек ставится на VPS, а не на ноутбук разработчика.
package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Sample — один замер.
type Sample struct {
	At time.Time `json:"at"`

	// Загрузка процессора в процентах за интервал между замерами.
	// Отрицательное значение означает «первый замер, сравнивать не с чем».
	CPUPercent float64 `json:"cpuPercent"`

	MemTotalMB     uint64 `json:"memTotalMb"`
	MemAvailableMB uint64 `json:"memAvailableMb"`

	DiskTotalMB uint64 `json:"diskTotalMb"`
	DiskFreeMB  uint64 `json:"diskFreeMb"`

	Load1 float64 `json:"load1"`

	// Аптайм сервера в секундах.
	UptimeSeconds uint64 `json:"uptimeSeconds"`
}

// MemUsedPercent — сколько памяти занято.
func (s Sample) MemUsedPercent() int {
	if s.MemTotalMB == 0 {
		return 0
	}
	used := s.MemTotalMB - s.MemAvailableMB
	return int(used * 100 / s.MemTotalMB)
}

// DiskUsedPercent — сколько диска занято.
func (s Sample) DiskUsedPercent() int {
	if s.DiskTotalMB == 0 {
		return 0
	}
	used := s.DiskTotalMB - s.DiskFreeMB
	return int(used * 100 / s.DiskTotalMB)
}

// Collector помнит предыдущий счётчик процессора: мгновенной загрузки в
// Linux не существует, она считается разницей между двумя замерами.
type Collector struct {
	procDir string
	rootDir string

	lastBusy  uint64
	lastTotal uint64
	seeded    bool
}

func NewCollector(procDir, rootDir string) *Collector {
	return &Collector{procDir: procDir, rootDir: rootDir}
}

// Collect снимает замер. Ошибка отдельного показателя не отменяет остальные:
// половина картины полезнее пустоты.
func (c *Collector) Collect() (Sample, error) {
	sample := Sample{At: time.Now().UTC(), CPUPercent: -1}

	busy, total, err := c.cpuCounters()
	if err == nil {
		if c.seeded && total > c.lastTotal {
			deltaBusy := busy - c.lastBusy
			deltaTotal := total - c.lastTotal
			sample.CPUPercent = float64(deltaBusy) * 100 / float64(deltaTotal)
		}
		c.lastBusy, c.lastTotal, c.seeded = busy, total, true
	}

	if total, available, err := c.memory(); err == nil {
		sample.MemTotalMB = total
		sample.MemAvailableMB = available
	}

	if total, free, err := c.disk(); err == nil {
		sample.DiskTotalMB = total
		sample.DiskFreeMB = free
	}

	sample.Load1 = c.loadAverage()
	sample.UptimeSeconds = c.uptime()

	// Совсем пустой замер писать в базу незачем: это не «сервер молчит», а
	// «мы не смогли посмотреть».
	if sample.MemTotalMB == 0 && sample.DiskTotalMB == 0 {
		return sample, fmt.Errorf("ни один показатель не прочитался: проверьте монтирование %s", c.procDir)
	}

	return sample, nil
}

// cpuCounters читает первую строку /proc/stat: суммарные такты по режимам.
func (c *Collector) cpuCounters() (busy, total uint64, err error) {
	file, err := os.Open(c.procDir + "/stat")
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return 0, 0, fmt.Errorf("/proc/stat пуст")
	}

	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("/proc/stat: неожиданная первая строка")
	}

	for index, raw := range fields[1:] {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			continue
		}
		total += value
		// Поля 3 и 4 (idle и iowait) — это простой. Всё остальное работа.
		if index != 3 && index != 4 {
			busy += value
		}
	}

	return busy, total, nil
}

func (c *Collector) memory() (totalMB, availableMB uint64, err error) {
	file, err := os.Open(c.procDir + "/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalMB = value / 1024
		case "MemAvailable:":
			availableMB = value / 1024
		}
	}

	if totalMB == 0 {
		return 0, 0, fmt.Errorf("/proc/meminfo: MemTotal не найден")
	}
	return totalMB, availableMB, nil
}

func (c *Collector) loadAverage() float64 {
	raw, err := os.ReadFile(c.procDir + "/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return value
}

func (c *Collector) uptime() uint64 {
	raw, err := os.ReadFile(c.procDir + "/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return uint64(seconds)
}
