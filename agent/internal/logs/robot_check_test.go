package logs

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"agent/internal/bots"
)

// gptAddr — адрес GPTBot из вшитого снимка (Р33), а не прописанный руками.
func gptAddr(t *testing.T) string {
	t.Helper()
	loaded, err := bots.LoadRanges()
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range loaded.Sets {
		for _, name := range set.Names {
			if name == "GPTBot" && len(set.V4) > 0 {
				return netip.MustParsePrefix(set.V4[0]).Addr().Next().String()
			}
		}
	}
	t.Fatal("у GPTBot нет адресов в снимке")
	return ""
}

const gptAgent = `Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)`

func TestParseAccessTakesTheServerAddress(t *testing.T) {
	gpt := gptAddr(t)
	cases := []struct {
		name, line, outcome string
	}{
		{"combined, адрес OpenAI",
			gpt + ` - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `"`, bots.Verified},
		{"combined, чужой адрес",
			`8.8.8.8 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `"`, bots.Unverified},
		// Р26: адрес OpenAI в X-Forwarded-For последним полем nginx `main`
		// не делает самозванца настоящим — читается только поле сервера.
		{"XFF в конце строки",
			`8.8.8.8 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `" "` + gpt + `"`, bots.Unverified},
		{"vhost_combined",
			`shop.example:443 ` + gpt + ` - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `"`, bots.Verified},
		{"IPv4 в записи IPv6",
			`::ffff:` + gpt + ` - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `"`, bots.Verified},
		{"веб-сервер в Docker видит шлюз",
			`172.18.0.1 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `"`, bots.Unknown},
		{"имя хоста вместо адреса",
			`crawl.example.net - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + gptAgent + `"`, bots.Unknown},
	}
	for _, tc := range cases {
		event, ok := ParseAccess(tc.line)
		if !ok || event.Robot != "GPTBot" {
			t.Errorf("%s: не разобрано (%v, %q)", tc.name, ok, event.Robot)
			continue
		}
		if outcome, _ := bots.Verify(event.Robot, event.Addr); outcome != tc.outcome {
			t.Errorf("%s: %s, ждали %s", tc.name, outcome, tc.outcome)
		}
	}
}

// Р28: у людей и у роботов, которых проверить нельзя, адрес не разбирается
// вовсе — он не нужен и не должен попадать даже в память события.
func TestAddressOnlyForCheckableRobots(t *testing.T) {
	for _, agent := range []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)",
	} {
		event, ok := ParseAccess(`8.8.8.8 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "` + agent + `"`)
		if !ok || event.Addr.IsValid() {
			t.Errorf("адрес разобран у %q", agent)
		}
	}
}

func TestRobotsCountChecksAndImpostors(t *testing.T) {
	gpt := netip.MustParseAddr(gptAddr(t))
	fake := netip.MustParseAddr("8.8.8.8")
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)
	robots := NewRobots().UseRules(denyAll{})
	add := func(addr netip.Addr, status int, path string) {
		robots.Add(Event{Kind: KindAccess, At: at, Host: "shop.example", Robot: "GPTBot", Path: path, Target: path,
			Status: status, HasAgent: true, Addr: addr})
	}
	add(gpt, 200, "/")
	add(gpt, 403, "/")
	add(fake, 403, "/")
	add(fake, 403, "/robots.txt")
	add(netip.Addr{}, 200, "/")
	add(netip.MustParseAddr("104.16.1.1"), 200, "/")

	checks := robots.Checks()
	if len(checks) != 1 || checks[0].Verified != 2 || checks[0].Unverified != 2 || checks[0].Unknown != 2 {
		t.Fatalf("исходы: %+v", checks)
	}
	meta := map[string]int{}
	for _, count := range robots.Counts() {
		meta[count.Name] = count.Count
	}
	if meta[MetaAddrCheckable] != 6 || meta[MetaAddrNone] != 1 || meta[MetaAddrProxy] != 1 || meta[MetaAddrPrivate] != 0 {
		t.Fatalf("причины: %v", meta)
	}

	total, impostors := 0, 0
	for _, item := range robots.Refusals() {
		total += item.Count
	}
	for _, item := range robots.ImpostorRefusals() {
		impostors += item.Count
	}
	if total != 3 || impostors != 2 {
		t.Fatalf("отказов %d, из них самозванцам %d", total, impostors)
	}
	// Заходы в закрытое (denyAll закрывает всё): шесть, из них два — чужих.
	if v := robots.Violations(); len(v) != 1 || v[0].Count != 6 {
		t.Fatalf("заходы: %+v", v)
	}
	if v := robots.ImpostorViolations(); len(v) != 1 || v[0].Count != 2 {
		t.Fatalf("заходы самозванцев: %+v", v)
	}
}

type denyAll struct{}

func (denyAll) Disallowed(host, robot, target string) (bool, bool) { return true, true }

// Р9: отказ тому, кто назвался GPTBot с чужого адреса, не делает вывода
// «сервер отказывает GPTBot».
func TestImpostorsDoNotMakeAVerdict(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	day := dayOf(now)
	visits := []RobotCount{
		{Day: day, Host: "shop.example", Name: MetaLines, Count: 100},
		{Day: day, Host: "shop.example", Name: "GPTBot", Count: 40},
	}
	refusals := []RobotRefusal{{Day: day, Host: "shop.example", Name: "GPTBot", Status: 403, Count: 30}}
	checks := []RobotCheck{{Day: day, Host: "shop.example", Name: "GPTBot", Verified: 10, Unverified: 30}}
	impostors := []RobotRefusal{{Day: day, Host: "shop.example", Name: "GPTBot", Status: 403, Count: 30}}

	if verdicts, _ := AssessRobots(now, 30, visits, refusals); len(verdicts) != 1 || verdicts[0].Verdict != VerdictRefused {
		t.Fatalf("без проверки — отказ: %+v", verdicts)
	}
	cleanVisits, cleanRefusals := WithoutImpostors(visits, refusals, checks, impostors)
	if verdicts, _ := AssessRobots(now, 30, cleanVisits, cleanRefusals); len(verdicts) != 0 {
		t.Fatalf("самозванцы дали вывод: %+v", verdicts)
	}
	if cleanVisits[1].Count != 10 || len(cleanRefusals) != 0 {
		t.Fatalf("вычитание: %+v %+v", cleanVisits, cleanRefusals)
	}
}

// Р10: больше NamesPerHostDay разных имён на сайт в сутки — «прочие».
func TestRobotNamesAreCapped(t *testing.T) {
	catalog, err := bots.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	robots := NewRobots()
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)
	added := 0
	for _, robot := range catalog.Robots {
		if robot.Legacy || robot.RobotsOnly || robot.Family != "" {
			continue
		}
		robots.Add(Event{Kind: KindAccess, At: at, Host: "shop.example", Robot: robot.Name, Path: "/", Status: 200, HasAgent: true})
		added++
	}
	if added <= NamesPerHostDay {
		t.Skipf("в списке %d новых имён — потолок не достать", added)
	}
	names := 0
	other := 0
	for _, count := range robots.Counts() {
		switch {
		case count.Name == bots.Other:
			other = count.Count
		case !strings.HasPrefix(count.Name, "@"):
			names++
		}
	}
	if names != NamesPerHostDay || other != added-NamesPerHostDay {
		t.Fatalf("имён %d, прочих %d из %d", names, other, added)
	}
	// Прежние потолка не знают.
	robots.Add(Event{Kind: KindAccess, At: at, Host: "shop.example", Robot: "GPTBot", Path: "/", Status: 200, HasAgent: true})
	for _, count := range robots.Counts() {
		if count.Name == "GPTBot" {
			return
		}
	}
	t.Fatal("GPTBot ушёл в прочие")
}

// П5, П6: скрипты и наши обходы в нагрузке — своими видами, а не людьми.
func TestTrafficKindsForNewRoles(t *testing.T) {
	for name, want := range map[string]string{
		"vpsFocus": bots.Self, "curl": bots.Script, "Ahrefs": bots.SEOTool, "GPTBot": bots.AITraining,
		bots.Other: TrafficOther, "": "",
	} {
		if got := trafficKind(name); got != want {
			t.Errorf("%q → %q, ждали %q", name, got, want)
		}
	}
}
