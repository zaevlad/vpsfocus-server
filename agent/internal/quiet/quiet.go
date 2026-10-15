// Package quiet — тихие часы: ночью сразу уходит только беда, остальное
// ждёт утра (идея 6 трека AB, Р19–Р21).
//
// Здесь только вопрос «тихо ли сейчас». Как тихие часы глушат тревогу,
// решает `dispatch` агента: тем же приёмом, что окно работ, — молчит и не
// записывает состояние, и после тихих часов тревога уходит как свежая.
package quiet

import (
	"strconv"
	"strings"
	"time"
)

// Коды предупреждений: значение задано, но разобрать его нельзя. Тихих
// часов тогда нет вовсе — промолчать о тревоге из-за опечатки хуже, чем
// разбудить, — а экран называет причину кодом.
const (
	ErrHours    = "quietHoursInvalid"
	ErrTimezone = "quietTimezoneInvalid"
)

// Hours — тихие часы. Нулевое значение — выключены.
type Hours struct {
	// С какого часа и до какого, по часам пояса Location. Конец не входит:
	// «23-08» — с 23:00 до 07:59.
	Start, End int
	Location   *time.Location
	// Задано ли вообще. Пустой `QUIET_HOURS` — тихих часов нет, и код идёт
	// прежним путём.
	Enabled bool
	// Задано, но не разобралось: код предупреждения, тихих часов нет.
	Problem string
	// Как задано — экрану, чтобы показать то, что лежит на сервере.
	Raw, Zone string
}

// Parse разбирает `QUIET_HOURS` («23-08») и `NOTIFY_TZ` (имя пояса IANA).
//
// Часы целые: минуты человеку в этой настройке не нужны, а формат проще
// проверить. Начало и конец не совпадают — «0-0» значило бы либо «всегда
// тихо», либо «никогда», и угадывать нельзя.
func Parse(hours, zone string) Hours {
	hours = strings.TrimSpace(hours)
	zone = strings.TrimSpace(zone)
	out := Hours{Raw: hours, Zone: zone}
	if hours == "" {
		return out
	}

	start, end, ok := parseRange(hours)
	if !ok {
		out.Problem = ErrHours
		return out
	}
	// Пояс обязателен: приложение пишет его всегда, а «по UTC» без спроса
	// сдвинуло бы ночь человека на несколько часов.
	if zone == "" {
		out.Problem = ErrTimezone
		return out
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		out.Problem = ErrTimezone
		return out
	}

	out.Start, out.End, out.Location, out.Enabled = start, end, location, true
	return out
}

func parseRange(value string) (int, int, bool) {
	left, right, found := strings.Cut(value, "-")
	if !found {
		return 0, 0, false
	}
	start, ok := parseHour(left)
	if !ok {
		return 0, 0, false
	}
	end, ok := parseHour(right)
	if !ok || start == end {
		return 0, 0, false
	}
	return start, end, true
}

func parseHour(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2 {
		return 0, false
	}
	hour, err := strconv.Atoi(value)
	if err != nil || hour < 0 || hour > 23 {
		return 0, false
	}
	return hour, true
}

// Active — тихо ли в момент now.
//
// Промежуток через полночь («23-08») — два куска суток, обычный («1-6») —
// один.
func (h Hours) Active(now time.Time) bool {
	if !h.Enabled {
		return false
	}
	hour := now.In(h.Location).Hour()
	if h.Start < h.End {
		return hour >= h.Start && hour < h.End
	}
	return hour >= h.Start || hour < h.End
}
