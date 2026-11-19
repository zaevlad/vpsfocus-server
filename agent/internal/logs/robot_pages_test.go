package logs

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func TestRobotPagesOnlyWhatAIReadForPeople(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)
	robots := NewRobots()
	add := func(robot, method, path string, status int, addr string) {
		event := Event{Kind: KindAccess, At: at, Host: "shop.example", Robot: robot, Method: method,
			Path: path, Target: path, Status: status, HasAgent: true}
		if addr != "" {
			event.Addr = netip.MustParseAddr(addr)
		}
		robots.Add(event)
	}
	// Claude-User адресов не публикует — «не проверить», страница пишется.
	add("Claude-User", "GET", "/pricing", 200, "")
	add("Claude-User", "GET", "/pricing", 200, "")
	add("Claude-User", "GET", "/old-page", 404, "")
	add("Claude-User", "GET", "/blog/{n}", 301, "")
	// Не страницы (Р37) и статика (Р11).
	add("Claude-User", "HEAD", "/pricing", 200, "")
	add("Claude-User", "GET", "/robots.txt", 200, "")
	add("Claude-User", "GET", "/static/app.css", 200, "")
	add("Claude-User", "GET", "/logo.PNG", 200, "")
	add("Claude-User", "GET", "/.env", 404, "")
	// ChatGPT-User с чужого адреса — самозванец, его «чтения» нет.
	add("ChatGPT-User", "GET", "/pricing", 200, "8.8.8.8")
	// Роботы других ролей страниц не пишут.
	add("GPTBot", "GET", "/pricing", 200, "")
	add("Googlebot", "GET", "/pricing", 200, "")

	got := map[string]int{}
	for _, page := range robots.Pages() {
		got[fmt.Sprintf("%s %s %d", page.Name, page.Path, page.Status)] = page.Count
	}
	want := map[string]int{
		"Claude-User /pricing 200":  2,
		"Claude-User /old-page 404": 1,
		"Claude-User /blog/{n} 301": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("страницы: %v", got)
	}
	for key, count := range want {
		if got[key] != count {
			t.Errorf("%s: %d, ждали %d", key, got[key], count)
		}
	}
}

func TestRobotPagesAreCapped(t *testing.T) {
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)
	robots := NewRobots()
	for i := 0; i < PagesPerRobotDay+7; i++ {
		robots.Add(Event{Kind: KindAccess, At: at, Host: "shop.example", Robot: "Claude-User", Method: "GET",
			Path: fmt.Sprintf("/page-%d", i), Status: 200, HasAgent: true})
	}
	// Уже записанный путь сверх потолка — своей строкой, не в «остальные».
	robots.Add(Event{Kind: KindAccess, At: at, Host: "shop.example", Robot: "Claude-User", Method: "GET",
		Path: "/page-0", Status: 200, HasAgent: true})
	paths, other := 0, 0
	for _, page := range robots.Pages() {
		if page.Path == OtherPath {
			other = page.Count
			continue
		}
		paths++
		if page.Path == "/page-0" && page.Count != 2 {
			t.Errorf("/page-0: %d", page.Count)
		}
	}
	if paths != PagesPerRobotDay || other != 7 {
		t.Fatalf("путей %d, остальных %d", paths, other)
	}
}

func TestPageOutcome(t *testing.T) {
	for status, want := range map[int]string{200: PageOpened, 304: PageRedirected, 301: PageRedirected,
		404: PageNotFound, 410: PageNotFound, 403: PageNotFound, 502: PageFailed} {
		if got := PageOutcome(status); got != want {
			t.Errorf("%d → %s, ждали %s", status, got, want)
		}
	}
}
