package logs

import (
	"testing"
	"time"
)

var verdictNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// daysAgo — начало суток n дней назад.
func daysAgo(n int) int64 {
	return dayOf(verdictNow) - int64(n)*86_400
}

func visit(n int, host, name string, count int) RobotCount {
	return RobotCount{Day: daysAgo(n), Host: host, Name: name, Count: count}
}

func find(verdicts []RobotVerdict, host, name string) *RobotVerdict {
	for i := range verdicts {
		if verdicts[i].Host == host && verdicts[i].Name == name {
			return &verdicts[i]
		}
	}
	return nil
}

// Отказ — больше половины запросов робота за неделю и не меньше десяти
// запросов (Р14); 401 и 503 отказом не считаются.
func TestRefusalNeedsShareAndVolume(t *testing.T) {
	if !Refusal(403) || !Refusal(429) || !Refusal(444) || Refusal(401) || Refusal(503) || Refusal(200) {
		t.Fatal("коды отказа не те")
	}

	visits := []RobotCount{
		visit(0, "a.test", "OAI-SearchBot", 14),
		visit(0, "b.test", "OAI-SearchBot", 9),
		visit(0, "c.test", "OAI-SearchBot", 20),
	}
	refusals := []RobotRefusal{
		{Day: daysAgo(0), Host: "a.test", Name: "OAI-SearchBot", Status: 403, Count: 10},
		{Day: daysAgo(0), Host: "a.test", Name: "OAI-SearchBot", Status: 429, Count: 2},
		{Day: daysAgo(0), Host: "b.test", Name: "OAI-SearchBot", Status: 403, Count: 9},
		{Day: daysAgo(0), Host: "c.test", Name: "OAI-SearchBot", Status: 403, Count: 10},
	}
	verdicts, _ := AssessRobots(verdictNow, 14, visits, refusals)

	a := find(verdicts, "a.test", "OAI-SearchBot")
	if a == nil || a.Verdict != VerdictRefused || a.Refused != 12 || a.Visits != 14 || a.Code != 403 || a.Level != VerdictAttention {
		t.Errorf("a.test: %+v", a)
	}
	if find(verdicts, "b.test", "OAI-SearchBot") != nil {
		t.Error("девять запросов — не повод для вывода")
	}
	if find(verdicts, "c.test", "OAI-SearchBot") != nil {
		t.Error("ровно половина — ещё не «больше половины»")
	}
}

// Отказ старше недели в вывод не входит.
func TestRefusalCountsOnlyTheWeek(t *testing.T) {
	visits := []RobotCount{visit(0, "", "Googlebot", 12), visit(8, "", "Googlebot", 50)}
	refusals := []RobotRefusal{{Day: daysAgo(8), Name: "Googlebot", Status: 403, Count: 50}}
	verdicts, _ := AssessRobots(verdictNow, 14, visits, refusals)
	if v := find(verdicts, "", "Googlebot"); v != nil {
		t.Fatalf("отказ двухнедельной давности: %+v", v)
	}
}

// Роботу обучения отказ может быть выбором человека: справка без уровня.
// Отказ на robots.txt — первым.
func TestTrainingBotsGetNoLevelAndRobotsTxtComesFirst(t *testing.T) {
	visits := []RobotCount{visit(0, "", "GPTBot", 20), visit(0, "", "PerplexityBot", 20)}
	refusals := []RobotRefusal{
		{Day: daysAgo(0), Name: "PerplexityBot", Status: 403, Count: 20},
		{Day: daysAgo(0), Name: "GPTBot", Status: 403, Count: 15},
		{Day: daysAgo(0), Name: "GPTBot", Status: 403, RobotsTxt: true, Count: 5},
	}
	verdicts, _ := AssessRobots(verdictNow, 14, visits, refusals)
	if len(verdicts) != 2 {
		t.Fatalf("выводы: %+v", verdicts)
	}
	if verdicts[0].Name != "GPTBot" || verdicts[0].RobotsTxt != 5 || verdicts[0].Level != "" {
		t.Errorf("первым должен быть отказ на robots.txt, без уровня у робота обучения: %+v", verdicts[0])
	}
	if verdicts[1].Level != VerdictAttention {
		t.Errorf("робот ИИ-поиска — «посмотри»: %+v", verdicts[1])
	}
}

// постоянный — робот, бывший в days из семи дней до тихих.
func regular(host, name string, days int) []RobotCount {
	var out []RobotCount
	for i := 0; i < days; i++ {
		out = append(out, visit(3+i, host, name, 10))
	}
	return out
}

// «Пропал» — был в пяти из семи прошлых дней, а за три последних ни разу
// (Р15); и только если лог сайта за эти три дня читался.
func TestGoneOnlyForRegularGuests(t *testing.T) {
	lines := []RobotCount{visit(0, "a.test", MetaLines, 500), visit(0, "b.test", MetaLines, 500)}

	visits := append(regular("a.test", "Googlebot", 5), lines...)
	visits = append(visits, regular("b.test", "Googlebot", 4)...)
	verdicts, known := AssessRobots(verdictNow, 14, visits, nil)
	if !known {
		t.Fatal("при хранении 14 дней «пропал» считается")
	}
	gone := find(verdicts, "a.test", "Googlebot")
	if gone == nil || gone.Verdict != VerdictGone || gone.Before != 50 || gone.DaysSeen != 5 || gone.Level != VerdictAttention {
		t.Errorf("a.test: %+v", gone)
	}
	if find(verdicts, "b.test", "Googlebot") != nil {
		t.Error("робот, бывший в четырёх днях из семи, — не постоянный гость")
	}

	// Хоть один визит за тихие дни — на месте.
	back := append(regular("a.test", "Googlebot", 7), lines...)
	back = append(back, visit(2, "a.test", "Googlebot", 1))
	if v, _ := AssessRobots(verdictNow, 14, back, nil); find(v, "a.test", "Googlebot") != nil {
		t.Error("робот приходил позавчера — не пропал")
	}
}

// Лог сайта за тихие дни не читался — «пропал» не выводится: это мы
// перестали видеть, а не робот перестал ходить.
func TestGoneNeedsTheLogToBeRead(t *testing.T) {
	visits := regular("a.test", "Googlebot", 7)
	if v, _ := AssessRobots(verdictNow, 14, visits, nil); len(v) != 0 {
		t.Fatalf("без строк лога за тихие дни: %+v", v)
	}
}

// Счётчики живут меньше десяти дней — «пропал» не считается, и ответ
// говорит «не знаем» (Р16). Отказы при этом считаются.
func TestGoneUnknownWithShortRetention(t *testing.T) {
	visits := append(regular("a.test", "Googlebot", 7), visit(0, "a.test", MetaLines, 100))
	verdicts, known := AssessRobots(verdictNow, 9, visits, nil)
	if known || len(verdicts) != 0 {
		t.Fatalf("при хранении 9 дней: известно %v, выводы %+v", known, verdicts)
	}
	if _, known := AssessRobots(verdictNow, GoneNeedsDays, nil, nil); !known {
		t.Error("десяти дней хватает")
	}
}

// Служебные счётчики и «прочие роботы» выводов не получают.
func TestOnlyListedBotsAreAssessed(t *testing.T) {
	visits := []RobotCount{visit(0, "", "other", 100), visit(0, "", MetaScan, 100)}
	refusals := []RobotRefusal{{Day: daysAgo(0), Name: "other", Status: 403, Count: 100}}
	if v, _ := AssessRobots(verdictNow, 14, visits, refusals); len(v) != 0 {
		t.Fatalf("выводы: %+v", v)
	}
}

// Проход по логу считает отказы роботам из списка — по коду и отдельно
// /robots.txt; «прочих» и людей не считает.
func TestRobotsCountRefusals(t *testing.T) {
	robots := NewRobots()
	at := verdictNow
	add := func(robot, path string, status int) {
		robots.Add(Event{Kind: KindAccess, At: at, Robot: robot, Path: path, Status: status, HasAgent: true})
	}
	add("GPTBot", "/", 403)
	add("GPTBot", "/blog", 403)
	add("GPTBot", "/robots.txt", 403)
	add("GPTBot", "/", 200)
	add("GPTBot", "/private", 401)
	add("other", "/", 403)
	add("", "/", 403)

	refusals := robots.Refusals()
	total, robotsTxt := 0, 0
	for _, item := range refusals {
		if item.Name != "GPTBot" || item.Status != 403 {
			t.Errorf("лишний отказ: %+v", item)
		}
		total += item.Count
		if item.RobotsTxt {
			robotsTxt += item.Count
		}
	}
	if total != 3 || robotsTxt != 1 {
		t.Fatalf("отказов %d, из них robots.txt %d", total, robotsTxt)
	}
}
