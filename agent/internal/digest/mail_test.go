package digest

import (
	"regexp"
	"strings"
	"testing"

	"agent/internal/health"
	"agent/internal/logs"
	"agent/internal/mailhtml"
)

// fullSnapshot — отчёт, в котором есть все поводы свода: на сервере и в
// проекте, оба уровня.
func fullSnapshot(t *testing.T) health.Snapshot {
	var server, project []health.Reason
	for index, code := range codes(t) {
		level := health.LevelAttention
		if index%2 == 0 {
			level = health.LevelProblem
		}
		reason := health.Reason{Code: code, Level: level, Count: 91, Domain: "shop.example.com"}
		server = append(server, reason)
		project = append(project, reason)
	}
	return health.Snapshot{
		Server:   health.ServerState{Level: health.LevelProblem, Reasons: server},
		Projects: []health.ProjectState{{Name: "shop.example.com", Level: health.LevelProblem, Reasons: project}, {Name: "blog.example.com", Level: health.LevelOK}},
	}
}

// Отчёт оформляется без потерь (трек AC, Р20, Р24): строка проекта —
// заголовком, пункты с отступом — списком, метки — цветом, ни одно слово не
// пропало.
func TestReportKeepsItsTextInHTML(t *testing.T) {
	robots := []logs.RobotVerdict{
		{Host: "shop.example.com", Name: "OAI-SearchBot", Verdict: logs.VerdictRefused, Level: logs.VerdictAttention,
			Visits: 140, Refused: 120, Code: 403, RobotsTxt: 3},
	}
	for _, locale := range []string{"ru", "en"} {
		text := BuildWithRobots(locale, "web-1", fullSnapshot(t), robots)
		page, err := mailhtml.Render(mailhtml.Letter{
			Lang: locale, Subject: text.Subject, Text: text.Body, Tone: text.Tone, Band: text.Band,
		})
		if err != nil {
			t.Fatal(err)
		}
		if lost := mailhtml.Missing(text.Body, page); len(lost) > 0 {
			t.Errorf("%s: HTML потерял слова %q", locale, lost)
		}
		for _, want := range []string{"mark-problem", "mark-warning", ">shop.example.com</p>"} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: в HTML нет %q", locale, want)
			}
		}
	}
}

// Тон отчёта — по худшему пункту, а не всегда «всё в порядке».
func TestReportToneFollowsTheWorstFinding(t *testing.T) {
	if text := Build("ru", "web-1", fullSnapshot(t)); text.Tone != mailhtml.Problem || text.Band != "Требует вмешательства" {
		t.Errorf("отчёт с проблемами: %q %q", text.Tone, text.Band)
	}
	attention := health.Snapshot{Server: health.ServerState{Level: health.LevelAttention, Reasons: []health.Reason{
		{Code: health.CodeMemoryTight, Level: health.LevelAttention, Count: 91},
	}}}
	if text := Build("en", "web-1", attention); text.Tone != mailhtml.Warning || text.Band != "Worth a look" {
		t.Errorf("отчёт со «стоит посмотреть»: %q %q", text.Tone, text.Band)
	}
	if text := Build("ru", "web-1", health.Snapshot{}); text.Tone != mailhtml.OK || text.Band != "Всё в порядке" {
		t.Errorf("спокойный отчёт: %q %q", text.Tone, text.Band)
	}
}

// В отчёте нет слов «агент» и «стек» — внутренних имён мониторинга.
func TestReportAvoidsJargon(t *testing.T) {
	jargon := regexp.MustCompile(`(?i)(^|[^\p{L}])(агент|стек)\p{L}*|\b(agent|stack)\b|сайт клиента`)
	for _, locale := range []string{"ru", "en"} {
		body := Build(locale, "web-1", fullSnapshot(t)).Body
		if found := jargon.FindString(body); found != "" {
			t.Errorf("%s: «%s» в отчёте", locale, strings.TrimSpace(found))
		}
	}
}
