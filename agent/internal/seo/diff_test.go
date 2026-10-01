package seo

import (
	"os"
	"regexp"
	"testing"
)

func scanCodes(when string, pages map[string][]string) ScanCodes {
	return ScanCodes{CheckedAt: when, Pages: pages}
}

func entry(entries []DiffEntry, code string) (DiffEntry, bool) {
	for _, item := range entries {
		if item.Code == code {
			return item, true
		}
	}
	return DiffEntry{}, false
}

func TestCompareAppearedAndFixed(t *testing.T) {
	previous := scanCodes("2026-09-03T10:00:00Z", map[string][]string{
		"https://site.test/a": {IssueTitleMissing, IssueDescriptionMissing},
		"https://site.test/b": {IssueDescriptionMissing},
	})
	current := scanCodes("2026-09-04T10:00:00Z", map[string][]string{
		"https://site.test/a": {IssueDescriptionMissing},
		"https://site.test/b": {IssueDescriptionMissing, IssueH1Missing},
	})

	diff := Compare("site.test", current, previous)

	if !diff.Comparable {
		t.Fatal("сравнение объявлено невозможным")
	}
	fixed, ok := entry(diff.Fixed, IssueTitleMissing)
	if !ok || fixed.Count != 1 || fixed.Pages[0] != "https://site.test/a" {
		t.Fatalf("исправленное: %+v", diff.Fixed)
	}
	appeared, ok := entry(diff.Appeared, IssueH1Missing)
	if !ok || appeared.Count != 1 {
		t.Fatalf("появившееся: %+v", diff.Appeared)
	}
	// Находка, которая была и осталась, не должна попасть ни в один список.
	if _, ok := entry(diff.Appeared, IssueDescriptionMissing); ok {
		t.Fatal("неизменная находка объявлена новой")
	}
	if _, ok := entry(diff.Fixed, IssueDescriptionMissing); ok {
		t.Fatal("неизменная находка объявлена исправленной")
	}
}

func TestCompareGonePageIsNotFixed(t *testing.T) {
	// У снесённой страницы исправлять было нечего, и записывать её удаление
	// в достижения нельзя.
	previous := scanCodes("2026-09-03T10:00:00Z", map[string][]string{
		"https://site.test/old": {IssueTitleMissing},
	})
	current := scanCodes("2026-09-04T10:00:00Z", map[string][]string{
		"https://site.test/new": {},
	})

	diff := Compare("site.test", current, previous)

	if len(diff.Fixed) != 0 {
		t.Fatalf("исправленным записана пропавшая страница: %+v", diff.Fixed)
	}
	if diff.GoneCount != 1 || diff.NewCount != 1 {
		t.Fatalf("страниц пришло %d, ушло %d", diff.NewCount, diff.GoneCount)
	}
	if len(diff.GonePages) != 1 || diff.GonePages[0] != "https://site.test/old" {
		t.Fatalf("пропавшие: %v", diff.GonePages)
	}
}

func TestCompareNewPageIssuesAreAppeared(t *testing.T) {
	// Находка на новой странице — всё равно новая находка сайта.
	previous := scanCodes("2026-09-03T10:00:00Z", map[string][]string{
		"https://site.test/": {},
	})
	current := scanCodes("2026-09-04T10:00:00Z", map[string][]string{
		"https://site.test/":     {},
		"https://site.test/new":  {IssueTitleMissing},
		"https://site.test/new2": {IssueTitleMissing},
	})

	diff := Compare("site.test", current, previous)

	appeared, ok := entry(diff.Appeared, IssueTitleMissing)
	if !ok || appeared.Count != 2 {
		t.Fatalf("появившееся: %+v", diff.Appeared)
	}
	if diff.SamePages != 1 {
		t.Fatalf("страниц осталось: %d", diff.SamePages)
	}
}

func TestCompareWithoutPreviousScan(t *testing.T) {
	// Первый обход сравнивать не с чем, и показывать «всё появилось»
	// нельзя: это не изменение, а первое знакомство с сайтом.
	current := scanCodes("2026-09-04T10:00:00Z", map[string][]string{
		"https://site.test/": {IssueTitleMissing},
	})

	diff := Compare("site.test", current, ScanCodes{})

	if diff.Comparable {
		t.Fatal("сравнение с пустотой объявлено возможным")
	}
	if len(diff.Appeared) != 0 || len(diff.Fixed) != 0 {
		t.Fatalf("первый обход выдан за изменения: %+v %+v", diff.Appeared, diff.Fixed)
	}
}

func TestCompareOrdersBySeverityThenCount(t *testing.T) {
	// Список читают сверху вниз — он обязан начинаться с того, что чинят
	// первым.
	previous := scanCodes("2026-09-03T10:00:00Z", map[string][]string{
		"https://site.test/a": {},
		"https://site.test/b": {},
		"https://site.test/c": {},
	})
	current := scanCodes("2026-09-04T10:00:00Z", map[string][]string{
		"https://site.test/a": {IssueLangMissing, IssueTitleMissing},
		"https://site.test/b": {IssueLangMissing},
		"https://site.test/c": {IssueLangMissing, IssueH1Missing},
	})

	diff := Compare("site.test", current, previous)

	if len(diff.Appeared) != 3 {
		t.Fatalf("находок: %+v", diff.Appeared)
	}
	if diff.Appeared[0].Code != IssueTitleMissing {
		t.Fatalf("первым идёт %s, а не критичное", diff.Appeared[0].Code)
	}
	if diff.Appeared[1].Code != IssueH1Missing {
		t.Fatalf("вторым идёт %s, а не предупреждение", diff.Appeared[1].Code)
	}
	// Замечание последнее, даже когда страниц с ним втрое больше.
	if diff.Appeared[2].Code != IssueLangMissing || diff.Appeared[2].Count != 3 {
		t.Fatalf("замечание: %+v", diff.Appeared[2])
	}
}

func TestCompareCapsExampleList(t *testing.T) {
	pages := map[string][]string{}
	before := map[string][]string{}
	for _, suffix := range []string{"a", "b", "c", "d", "e"} {
		pages["https://site.test/"+suffix] = []string{IssueTitleMissing}
		before["https://site.test/"+suffix] = []string{}
	}

	diff := Compare("site.test",
		scanCodes("2026-09-04T10:00:00Z", pages),
		scanCodes("2026-09-03T10:00:00Z", before))

	appeared, _ := entry(diff.Appeared, IssueTitleMissing)
	if appeared.Count != 5 {
		t.Fatalf("число страниц: %d", appeared.Count)
	}
	if len(appeared.Pages) != diffExamples {
		t.Fatalf("примеров: %d — список из пятисот адресов никто не читает", len(appeared.Pages))
	}
}

func TestEveryIssueCodeHasSeverity(t *testing.T) {
	// Незнакомый код получает «замечание» молча, и опечатка в новой проверке
	// прошла бы незамеченной до первого отчёта клиенту. Коды читаются из
	// исходников, а не перечнем здесь: забытый в перечне код тест бы и не
	// заметил.
	all := issueConstants(t, "audit.go", "canonical.go", "site.go")
	if len(all) < 40 {
		t.Fatalf("разбор исходников дал %d кодов — сломался разбор, а не список", len(all))
	}

	if len(severityByCode) != len(all) {
		t.Fatalf("в таблице уровней %d кодов, в списке %d — один из них забыт",
			len(severityByCode), len(all))
	}
	for _, code := range all {
		if _, ok := severityByCode[code]; !ok {
			t.Errorf("у кода %s нет уровня", code)
		}
		if KnownCode(code) != code {
			t.Errorf("код %s не проходит отбор для запроса", code)
		}
	}
	if KnownCode("../../etc/passwd") != "" {
		t.Fatal("отбор кодов пропускает чужую строку")
	}
}

// issueConstants — значения констант `Issue*` из исходников пакета.
func issueConstants(t *testing.T, files ...string) []string {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^\s*Issue\w+\s*=\s*"(\w+)"`)
	var codes []string
	for _, name := range files {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllSubmatch(source, -1) {
			codes = append(codes, string(match[1]))
		}
	}
	return codes
}
