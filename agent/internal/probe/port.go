package probe

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Проверка порта изнутри сервера (трек W, спринт 149, misc/idea2.md, идея 1б).
//
// **Агент не подключается к порту.** Он сидит в своей сети Docker и до
// `127.0.0.1` хоста не достанет, а база клиента обычно слушает именно там.
// Вместо этого агент читает таблицу сокетов сервера — `/proc/1/net/tcp` и
// `tcp6` смонтированного `/host/proc`: первый процесс живёт в сетевом
// пространстве хоста, и его таблица — таблица сервера, а не контейнера
// (сверено на живом сервере 2026-10-12: те же 22 и 443, что у `ss -ltn`).
// Сокет Docker для этого не нужен, и правило «агенту его не дают» стоит.
//
// **«Слушает» — не «работает».** Зависшая база порт держит. Экран так и
// говорит: «порт занят службой», а не «база работает» (Р6). Разговор по
// протоколу (`SELECT 1`, `PING`) не делается: агенту понадобились бы
// пароли клиента.

// PortScheme — адрес проверки порта: `port:5432`. Адрес у неё всё равно
// нужен: он часть личности проверки и то, что видит человек в письме.
const PortScheme = "port:"

// Где слушает порт (Р5): только этот сервер или все адреса.
const (
	// Только петля: `127.0.0.1`, `::1`. Снаружи сервера порт не виден.
	ListenLocal = "local"
	// Любой другой адрес, в том числе «все» (`0.0.0.0`, `::`): порт
	// принимает соединения из сети. Открыт ли он в интернет, решает ещё
	// файрвол — поэтому слово «из сети», а не «из интернета».
	ListenNetwork = "network"
)

// hostProc — `/proc` сервера внутри контейнера агента. Задаётся при запуске
// (`SetHostProc`): путь — из настроек агента, а не константа пакета.
var hostProc = "/host/proc"

// SetHostProc задаёт, где смонтирован `/proc` сервера.
func SetHostProc(path string) {
	if path != "" {
		hostProc = path
	}
}

// PortOf разбирает адрес `port:N`: номер 1–65535 без ведущих нулей.
func PortOf(address string) (int, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(address), PortScheme)
	if !ok || raw == "" || len(raw) > 5 || raw[0] == '0' {
		return 0, false
	}
	for _, symbol := range raw {
		if symbol < '0' || symbol > '9' {
			return 0, false
		}
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// runPort — исход проверки порта по таблицам сокетов сервера.
func runPort(profile Profile, result Result, started time.Time) Result {
	finish := func(code, listen string) Result {
		result.DurationMs = time.Since(started).Milliseconds()
		result.Code = code
		result.OK = code == ""
		result.Listen = listen
		return result
	}

	port, ok := PortOf(profile.URL)
	if !ok {
		return finish(CodeMisconfigured, "")
	}

	listen, known := listening(filepath.Join(hostProc, "1", "net"), port)
	if !known {
		return finish(CodePortUnknown, "")
	}
	if listen == "" {
		return finish(CodePortClosed, "")
	}
	return finish("", listen)
}

// listening ищет порт среди слушающих сокетов `tcp` и `tcp6`.
//
// Отвечает, где он слушает (`ListenLocal`/`ListenNetwork`, пусто — нигде), и
// удалось ли хоть что-то прочитать. Нет файла `tcp6` — не беда: на сервере
// без IPv6 его нет, и это не «не знаем».
func listening(dir string, port int) (string, bool) {
	known := false
	listen := ""
	for _, name := range []string{"tcp", "tcp6"} {
		file, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) && name == "tcp6" {
			continue
		}
		if err != nil {
			continue
		}
		found, ok := scanListening(file, port)
		_ = file.Close()
		if !ok {
			continue
		}
		known = true
		switch {
		case found == ListenNetwork:
			listen = ListenNetwork
		case found == ListenLocal && listen == "":
			listen = ListenLocal
		}
	}
	return listen, known
}

// scanListening разбирает одну таблицу: строки в состоянии `0A` (LISTEN) на
// нужном порту. Сеть побеждает петлю: порт, открытый хоть на одном не-петлевом
// адресе, принимает соединения из сети.
func scanListening(file *os.File, port int) (string, bool) {
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		// Даже заголовка нет — таблица не прочиталась.
		return "", false
	}
	listen := ""
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		address, hexPort, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		value, err := strconv.ParseUint(hexPort, 16, 32)
		if err != nil || int(value) != port {
			continue
		}
		if loopback(address) {
			if listen == "" {
				listen = ListenLocal
			}
			continue
		}
		listen = ListenNetwork
	}
	if scanner.Err() != nil {
		return "", false
	}
	return listen, true
}

// loopback — адрес из таблицы ядра петлевой.
//
// Адрес записан шестнадцатеричными словами в порядке байтов машины: для
// IPv4 `0100007F` — это 127.0.0.1 на little-endian, единственном порядке
// серверов, для которых собирается агент (amd64, arm64). IPv6 — четыре
// таких слова; `::1` и `::ffff:127.x.x.x` тоже петля.
func loopback(address string) bool {
	switch len(address) {
	case 8:
		// Младший байт IPv4 — последняя пара: 127.x.x.x.
		return strings.EqualFold(address[6:8], "7F")
	case 32:
		if strings.EqualFold(address, "00000000000000000000000001000000") {
			return true
		}
		// ::ffff:127.x.x.x — третье слово `FFFF0000`, четвёртое — IPv4.
		if strings.EqualFold(address[:16], "0000000000000000") &&
			strings.EqualFold(address[16:24], "FFFF0000") {
			return strings.EqualFold(address[30:32], "7F")
		}
	}
	return false
}
