package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"agent/internal/bots"
)

func ownCandidates() []candidate {
	out := make([]candidate, 0, len(ownRows))
	for _, row := range ownRows {
		out = append(out, candidate{source: "own", name: row.name, role: row.role, roleRank: rankOwn,
			match: row.match, family: row.family, legacy: row.legacy, robotsOnly: row.robotsOnly})
	}
	return out
}

// merge сводит имена всех источников в один список.
//
// Один робот — это все записи, у которых совпадает хотя бы один текст
// правила (без учёта регистра): «GPTBot» у ai-robots, у PostHog и у нас —
// один робот. Регулярки не сливаются ни с чем, кроме записи с тем же именем.
// Имя и роль — по старшинству источников (Р51). Робот без роли — отказ
// сборки со списком имён: роль даётся строкой нашей таблицы, а не догадкой.
func merge(all []candidate) ([]bots.Robot, error) {
	robots, err := mergeNoCheck(all)
	if err != nil {
		return nil, err
	}
	return robots, check(robots)
}

// mergeNoCheck — сведение без сторожей готового списка (их проверяет merge).
func mergeNoCheck(all []candidate) ([]bots.Robot, error) {
	all = withoutSkipped(all)
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			if ra < rb {
				parent[rb] = ra
			} else {
				parent[ra] = rb
			}
		}
	}

	byText := map[string]int{}
	for i, item := range all {
		for _, match := range item.match {
			if match.Rule == bots.RuleRegex {
				continue
			}
			key := strings.ToLower(match.Text)
			if j, ok := byText[key]; ok {
				union(i, j)
			} else {
				byText[key] = i
			}
		}
	}

	groups := map[int][]candidate{}
	roots := []int{}
	for i, item := range all {
		root := find(i)
		if _, ok := groups[root]; !ok {
			roots = append(roots, root)
		}
		groups[root] = append(groups[root], item)
	}

	robots := []bots.Robot{}
	var noRole []string
	for _, root := range roots {
		robot, ok := fold(groups[root])
		if !ok {
			noRole = append(noRole, describe(groups[root]))
			continue
		}
		robots = append(robots, robot)
	}
	if len(noRole) > 0 {
		sort.Strings(noRole)
		return nil, fmt.Errorf("у %d роботов нет роли — дать её строкой в own.go:\n  %s",
			len(noRole), strings.Join(noRole, "\n  "))
	}

	// Одинаковые имена с разными правилами: та же роль — один робот, разные —
	// отказ, решать руками.
	byName := map[string]int{}
	merged := []bots.Robot{}
	var clash []string
	for _, robot := range robots {
		key := strings.ToLower(robot.Name)
		if i, ok := byName[key]; ok {
			prev := &merged[i]
			if prev.Role != robot.Role {
				clash = append(clash, fmt.Sprintf("%s: %s (%s, %s) и %s (%s, %s)", robot.Name,
					prev.Role, prev.RoleFrom, texts(prev.Match), robot.Role, robot.RoleFrom, texts(robot.Match)))
				continue
			}
			prev.Match = uniqueMatches(append(prev.Match, robot.Match...))
			prev.Legacy = prev.Legacy || robot.Legacy
			continue
		}
		byName[key] = len(merged)
		merged = append(merged, robot)
	}

	if len(clash) > 0 {
		return nil, fmt.Errorf("%d имён с двумя ролями — решить в own.go:\n  %s", len(clash), strings.Join(clash, "\n  "))
	}

	sort.Slice(merged, func(i, j int) bool {
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	return merged, nil
}

// withoutSkipped убирает правила из skippedTokens у источников; запись без
// правил выпадает целиком.
func withoutSkipped(all []candidate) []candidate {
	out := make([]candidate, 0, len(all))
	for _, item := range all {
		if item.source == "own" {
			out = append(out, item)
			continue
		}
		kept := []bots.Match{}
		for _, match := range item.match {
			if _, skip := skippedTokens[strings.ToLower(match.Text)]; skip && match.Rule != bots.RuleRegex {
				continue
			}
			kept = append(kept, match)
		}
		if len(kept) == 0 {
			continue
		}
		item.match = kept
		out = append(out, item)
	}
	return out
}

// fold — один робот из записей разных источников.
func fold(items []candidate) (bots.Robot, bool) {
	byName := append([]candidate(nil), items...)
	sort.SliceStable(byName, func(i, j int) bool { return nameRank(byName[i].source) < nameRank(byName[j].source) })
	robot := bots.Robot{Name: byName[0].name}

	best := 0
	matches := []bots.Match{}
	for _, item := range items {
		matches = append(matches, item.match...)
		if item.source == "own" {
			robot.Family = item.family
			robot.Legacy = item.legacy
			robot.RobotsOnly = item.robotsOnly
		}
		if item.role != "" && (best == 0 || item.roleRank < best) {
			best = item.roleRank
			robot.Role = item.role
			robot.RoleFrom = item.source
		}
	}
	robot.Match = uniqueMatches(matches)
	return robot, robot.Role != ""
}

func uniqueMatches(in []bots.Match) []bots.Match {
	seen := map[bots.Match]bool{}
	out := []bots.Match{}
	for _, match := range in {
		key := bots.Match{Text: strings.ToLower(match.Text), Rule: match.Rule}
		if match.Rule == bots.RuleRegex {
			key.Text = match.Text
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Text < out[j].Text
	})
	return out
}

func texts(matches []bots.Match) string {
	out := []string{}
	for _, match := range matches {
		out = append(out, match.Text)
	}
	return strings.Join(out, ", ")
}

func describe(items []candidate) string {
	parts := []string{}
	for _, item := range items {
		texts := []string{}
		for _, match := range item.match {
			texts = append(texts, match.Text)
		}
		parts = append(parts, fmt.Sprintf("%s (%s: %s)", item.name, item.source, strings.Join(texts, ", ")))
	}
	return strings.Join(parts, "; ")
}

// check — сторожа готового списка.
func check(robots []bots.Robot) error {
	roles := map[string]bool{}
	for _, role := range bots.Roles {
		roles[role] = true
	}
	names := map[string]bots.Robot{}
	for _, robot := range robots {
		if !roles[robot.Role] {
			return fmt.Errorf("%s: роль %q не из перечня bots.Roles", robot.Name, robot.Role)
		}
		if len(robot.Match) == 0 {
			return fmt.Errorf("%s: нет ни одного правила", robot.Name)
		}
		for _, match := range robot.Match {
			if match.Rule == bots.RuleRegex {
				if _, err := regexp.Compile(match.Text); err != nil {
					return fmt.Errorf("%s: RE2 не понимает %q: %w", robot.Name, match.Text, err)
				}
			}
		}
		names[robot.Name] = robot
	}
	for _, robot := range robots {
		if robot.Family == "" {
			continue
		}
		family, ok := names[robot.Family]
		if !ok || family.Family != "" {
			return fmt.Errorf("%s: семейство %q не робот верхнего уровня", robot.Name, robot.Family)
		}
	}
	// Прежние 14 — на месте, с прежними ролями и именами (Р19).
	legacy := 0
	for _, row := range ownRows {
		if !row.legacy {
			continue
		}
		legacy++
		robot, ok := names[row.name]
		if !ok || robot.Role != row.role || !robot.Legacy {
			return fmt.Errorf("прежний робот %s потерян или сменил роль", row.name)
		}
	}
	if legacy != 14 {
		return fmt.Errorf("прежних роботов %d, а не 14", legacy)
	}
	return nil
}
