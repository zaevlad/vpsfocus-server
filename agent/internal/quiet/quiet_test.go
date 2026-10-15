package quiet

import (
	"testing"
	"time"
)

func at(t *testing.T, zone string, hour, minute int) time.Time {
	t.Helper()
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("пояс %s: %v", zone, err)
	}
	return time.Date(2026, 10, 15, hour, minute, 0, 0, location)
}

func TestEmptyMeansOff(t *testing.T) {
	h := Parse("", "Europe/Moscow")
	if h.Enabled || h.Problem != "" {
		t.Fatalf("пустое значение — выключено без предупреждения: %+v", h)
	}
	if h.Active(at(t, "Europe/Moscow", 3, 0)) {
		t.Fatal("выключенные тихие часы не глушат ничего")
	}
}

func TestAcrossMidnight(t *testing.T) {
	h := Parse("23-08", "Europe/Moscow")
	if !h.Enabled {
		t.Fatalf("разбор: %+v", h)
	}
	cases := []struct {
		hour, minute int
		want         bool
	}{
		{22, 59, false},
		{23, 0, true},
		{0, 0, true},
		{7, 59, true},
		{8, 0, false},
		{12, 0, false},
	}
	for _, c := range cases {
		if got := h.Active(at(t, "Europe/Moscow", c.hour, c.minute)); got != c.want {
			t.Errorf("%02d:%02d — %v, ждали %v", c.hour, c.minute, got, c.want)
		}
	}
}

func TestWithinOneDay(t *testing.T) {
	h := Parse("1-6", "UTC")
	if h.Active(at(t, "UTC", 0, 59)) || !h.Active(at(t, "UTC", 1, 0)) ||
		!h.Active(at(t, "UTC", 5, 59)) || h.Active(at(t, "UTC", 6, 0)) {
		t.Fatal("промежуток внутри суток: границы")
	}
}

// Часы считаются по поясу человека, а не сервера: то же мгновение в UTC —
// ночь в Москве и вечер в Нью-Йорке.
func TestZoneDecides(t *testing.T) {
	moment := time.Date(2026, 10, 15, 21, 30, 0, 0, time.UTC) // 00:30 в Москве, 17:30 в Нью-Йорке
	if !Parse("23-08", "Europe/Moscow").Active(moment) {
		t.Fatal("в Москве ночь")
	}
	if Parse("23-08", "America/New_York").Active(moment) {
		t.Fatal("в Нью-Йорке вечер")
	}
}

func TestBadValuesWarnAndStayLoud(t *testing.T) {
	for _, value := range []string{"23", "23-", "-8", "24-08", "23-23", "a-b", "23:00-08:00", "123-1"} {
		h := Parse(value, "UTC")
		if h.Enabled || h.Problem != ErrHours {
			t.Errorf("%q: ждали %s без тихих часов, вышло %+v", value, ErrHours, h)
		}
	}
	for _, zone := range []string{"", "Mars/Olympus"} {
		h := Parse("23-08", zone)
		if h.Enabled || h.Problem != ErrTimezone {
			t.Errorf("пояс %q: ждали %s, вышло %+v", zone, ErrTimezone, h)
		}
		if h.Active(at(t, "UTC", 3, 0)) {
			t.Errorf("пояс %q: неразобранные тихие часы не глушат", zone)
		}
	}
}
