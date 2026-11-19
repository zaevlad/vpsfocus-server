package bots

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Сторожа вшитого списка: он собран руками (`cmd/bots-build`) и закоммичен,
// поэтому всё, что сборщик обязан был проверить, проверяется и здесь — на
// случай правки файла руками.

func TestCatalogLoadsAndIsDated(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse("2006-01-02", catalog.Built); err != nil {
		t.Fatalf("дата сборки %q: %v", catalog.Built, err)
	}
	if ListDate() != catalog.Built {
		t.Fatal("ListDate не дата сборки")
	}
	if len(catalog.Robots) < 100 {
		t.Fatalf("роботов %d — список урезан", len(catalog.Robots))
	}
	if len(catalog.Sources) != 3 {
		t.Fatalf("источников %d", len(catalog.Sources))
	}
	// Лицензии источников лежат рядом с данными как есть (Р1, Р15).
	for _, source := range catalog.Sources {
		if source.Commit == "" || source.License == "" {
			t.Errorf("%s без коммита или лицензии", source.ID)
			continue
		}
		if _, err := os.Stat(filepath.Join("data", source.License)); err != nil {
			t.Errorf("%s: %v", source.ID, err)
		}
	}
}

func TestCatalogRolesAndNames(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]bool{}
	for _, role := range Roles {
		roles[role] = true
	}
	rules := map[string]bool{RuleToken: true, RuleVersioned: true, RuleExact: true, RuleWord: true, RulePrefix: true, RuleRegex: true}
	seen := map[string]bool{}
	for _, robot := range catalog.Robots {
		if !roles[robot.Role] {
			t.Errorf("%s: роль %q", robot.Name, robot.Role)
		}
		if seen[robot.Name] {
			t.Errorf("%s дважды", robot.Name)
		}
		seen[robot.Name] = true
		if len(robot.Match) == 0 {
			t.Errorf("%s без правил", robot.Name)
		}
		for _, match := range robot.Match {
			if !rules[match.Rule] || match.Text == "" {
				t.Errorf("%s: правило %+v", robot.Name, match)
			}
		}
	}
}

// Прежние 14 в списке — те же имена и роли, что в List (Р19). Пока
// `Classify` живёт на List (до спринта 272), две копии сверяются этим
// тестом.
func TestCatalogKeepsLegacyRobots(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]Robot{}
	for _, robot := range catalog.Robots {
		if robot.Legacy {
			legacy[robot.Name] = robot
		}
	}
	if len(legacy) != len(List) {
		t.Fatalf("прежних в списке %d, в List %d", len(legacy), len(List))
	}
	for _, bot := range List {
		robot, ok := legacy[bot.Name]
		if !ok {
			t.Errorf("%s потерян", bot.Name)
			continue
		}
		if robot.Role != bot.Kind || robot.RobotsOnly == bot.InLogs || robot.Family != "" {
			t.Errorf("%s: %+v против %+v", bot.Name, robot, bot)
		}
	}
}

func TestRangesMatchCatalog(t *testing.T) {
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ranges, err := LoadRanges()
	if err != nil {
		t.Fatal(err)
	}
	if ranges.Built != catalog.Built {
		t.Fatalf("диапазоны от %s, список от %s — собраны не вместе", ranges.Built, catalog.Built)
	}
	built, _ := time.Parse("2006-01-02", ranges.Built)
	byName := map[string]Robot{}
	for _, robot := range catalog.Robots {
		byName[robot.Name] = robot
	}
	owner := map[string]string{}
	for _, set := range ranges.Sets {
		if len(set.Names) == 0 {
			t.Errorf("%s: ни одного имени", set.ID)
		}
		for _, name := range set.Names {
			robot, ok := byName[name]
			if !ok || robot.RobotsOnly {
				t.Errorf("%s: имени %q нет в списке", set.ID, name)
			}
			if other, dup := owner[name]; dup {
				t.Errorf("%s в двух наборах: %s и %s", name, other, set.ID)
			}
			owner[name] = set.ID
		}
		created, err := time.Parse("2006-01-02", set.Created)
		if err != nil {
			t.Errorf("%s: дата %q", set.ID, set.Created)
			continue
		}
		// Р54: старше 12 месяцев — «не проверить».
		if stale := built.Sub(created) > 365*24*time.Hour; stale != set.Stale {
			t.Errorf("%s: файл от %s, пометка «старый» %v", set.ID, set.Created, set.Stale)
		}
		if len(set.V4)+len(set.V6) == 0 {
			t.Errorf("%s пуст", set.ID)
		}
		checkPrefixes(t, set.ID, set.V4, true, 16)
		checkPrefixes(t, set.ID, set.V6, false, 32)
	}
	if len(ranges.Cloudflare.V4) == 0 || len(ranges.Cloudflare.V6) == 0 {
		t.Error("диапазонов Cloudflare нет")
	}
	checkPrefixes(t, "cloudflare", ranges.Cloudflare.V4, true, 0)
	checkPrefixes(t, "cloudflare", ranges.Cloudflare.V6, false, 0)
}

// checkPrefixes — префиксы разбираются, нужного семейства, не шире пола
// (Р32) и записаны в каноническом виде.
func checkPrefixes(t *testing.T, id string, items []string, v4 bool, floor int) {
	t.Helper()
	for _, item := range items {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			t.Errorf("%s: %q: %v", id, item, err)
			continue
		}
		if prefix.Addr().Is4() != v4 || prefix.Bits() < floor || prefix.Masked() != prefix {
			t.Errorf("%s: %s", id, item)
		}
	}
}
