package logs

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Часовой пояс сервера клиента.
//
// nginx и Apache пишут в error-лог местное время сервера и смещения к нему
// не добавляют. Разобрать такую строку как UTC значит сдвинуть каждое
// событие на смещение сервера: на машине в UTC+3 всё уезжает на три часа
// назад, попадает в чужое пятиминутное окно и может вылететь за границу
// ротации.
//
// Строки access-лога смещение несут и разбираются верно, поэтому ошибка
// заодно разводила два ряда одного графика между собой — а они про один и
// тот же сервер.
//
// Зона берётся у самого сервера: агенту смонтирован его корень, и /etc его
// файловой системы виден целиком. Не нашли — остаёмся в UTC: это честное
// «мы не знаем», и на сервере в UTC (а такова настройка по умолчанию у
// большинства провайдеров) оно верно.

// location — часовой пояс, в котором читаются времена без смещения.
//
// atomic.Pointer, а не обычная переменная: устанавливается один раз при
// старте, читается из горутины, которая дочитывает логи.
var location atomic.Pointer[time.Location]

// Location — текущий часовой пояс разбора. UTC, пока не задан другой.
func Location() *time.Location {
	if loc := location.Load(); loc != nil {
		return loc
	}
	return time.UTC
}

// SetLocation задаёт часовой пояс разбора.
func SetLocation(loc *time.Location) {
	if loc == nil {
		loc = time.UTC
	}
	location.Store(loc)
}

// DetectLocation определяет часовой пояс сервера по его смонтированному
// корню.
//
// Два источника, оба стандартные для Linux:
//
//   - /etc/timezone — имя зоны текстом (Debian, Ubuntu);
//   - /etc/localtime — символическая ссылка на файл в zoneinfo, из пути
//     которого имя и достаётся (systemd, RHEL, Alpine).
//
// Имя зоны, а не смещение: смещение меняется дважды в год, и запомненное
// при старте агента к осени станет неверным.
//
// hostRoot пустой означает «спросить у своей же системы» — так агент,
// запущенный без контейнера, читает настоящий /etc.
func DetectLocation(hostRoot string) (*time.Location, bool) {
	for _, name := range []string{
		zoneFromFile(filepath.Join(hostRoot, "etc", "timezone")),
		zoneFromSymlink(filepath.Join(hostRoot, "etc", "localtime")),
	} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, true
		}
	}
	return time.UTC, false
}

// zoneFromFile читает имя зоны из /etc/timezone.
func zoneFromFile(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return validZoneName(strings.TrimSpace(string(body)))
}

// zoneFromSymlink достаёт имя зоны из пути, на который смотрит
// /etc/localtime: «/usr/share/zoneinfo/Europe/Berlin» — это «Europe/Berlin».
func zoneFromSymlink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	target = filepath.ToSlash(target)

	marker := "zoneinfo/"
	index := strings.LastIndex(target, marker)
	if index < 0 {
		return ""
	}
	// В некоторых дистрибутивах между zoneinfo и зоной стоит ещё каталог
	// вида posix или right — он к имени зоны не относится.
	name := strings.TrimPrefix(target[index+len(marker):], "posix/")
	name = strings.TrimPrefix(name, "right/")
	return validZoneName(name)
}

// validZoneName отсеивает то, что именем зоны быть не может.
//
// Файл на чужом сервере пишем не мы: в нём может оказаться что угодно,
// вплоть до пути с переходами вверх, а имя уходит в LoadLocation, который
// открывает файл по нему.
func validZoneName(name string) string {
	if name == "" || len(name) > 64 || strings.Contains(name, "..") ||
		strings.HasPrefix(name, "/") {
		return ""
	}
	for _, char := range name {
		switch {
		case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z',
			char >= '0' && char <= '9',
			char == '/', char == '_', char == '-', char == '+':
		default:
			return ""
		}
	}
	return name
}
