// Package digest собирает еженедельный отчёт о состоянии.
//
// Отчёт пишется на языке, который человек выбрал в приложении
// (`AGENT_LOCALE`), — на том же, что и тревоги (checks.Words).
//
// **Отчёт уходит по серверу, а не по агентству, и сервер назван в теме.**
// Собрать один отчёт на все VPS может только тот, кто видит их все, — то
// есть приложение, которое в момент отправки по условию закрыто, иначе
// письмо не нужно вовсе. Агентство с десятью серверами получит десять
// писем; не назвать сервер в теме значит выглядеть сломанным.
//
// **Отчёт уходит и тогда, когда всё в порядке.** Еженедельный отчёт,
// замолкающий от отсутствия новостей, неотличим от агента, который умер:
// то же соображение, из-за которого пустой обход считается отказом, а не
// «всё хорошо». Спам из него делают не «хорошие» письма, а повторение одних
// и тех же находок, — и от этого спасает приглушение, а не молчание.
package digest

import (
	"fmt"
	"sort"
	"strings"

	"agent/internal/health"
)

// Text — тема и тело письма.
type Text struct {
	Subject string
	Body    string
}

// Build собирает отчёт по своду.
//
// Приглушённые находки в него не попадают вовсе — в этом и смысл
// приглушения: свод их показывает, чтобы было что снимать, а письмо о них
// молчит, потому что человек уже сказал «знаю».
func Build(locale, host string, snapshot health.Snapshot) Text {
	words := dictionary(locale)

	server := speaking(snapshot.Server.Reasons)
	type projectLine struct {
		name    string
		level   health.Level
		reasons []health.Reason
	}

	var projects []projectLine
	for _, project := range snapshot.Projects {
		reasons := speaking(project.Reasons)
		projects = append(projects, projectLine{
			name:    project.Name,
			level:   worstOf(reasons),
			reasons: reasons,
		})
	}

	problems, attention := 0, 0
	count := func(level health.Level) {
		switch level {
		case health.LevelProblem:
			problems++
		case health.LevelAttention:
			attention++
		}
	}
	count(worstOf(server))
	for _, project := range projects {
		count(project.level)
	}

	subject := fmt.Sprintf("%s — %s: %s", words.subject, host, summary(words, problems, attention))

	var body strings.Builder
	body.WriteString(fmt.Sprintf(words.intro, host))
	body.WriteString("\n\n")
	body.WriteString(summary(words, problems, attention))
	body.WriteString("\n")

	body.WriteString("\n" + words.serverTitle + "\n")
	if len(server) == 0 {
		body.WriteString("  " + words.nothing + "\n")
	}
	for _, item := range server {
		body.WriteString("  " + line(words, item) + "\n")
	}

	if len(projects) == 0 {
		body.WriteString("\n" + words.noProjects + "\n")
	}
	for _, project := range projects {
		body.WriteString("\n" + project.name + "\n")
		if len(project.reasons) == 0 {
			body.WriteString("  " + words.nothing + "\n")
			continue
		}
		for _, item := range project.reasons {
			body.WriteString("  " + line(words, item) + "\n")
		}
	}

	// Приглушённые перечисляются числом, а не списком. Молчать о них
	// совсем нельзя — через полгода никто не вспомнит, что приглушение
	// вообще ставили, — но перечислять то, о чём человек уже сказал
	// «знаю», значит вернуть в письмо ровно тот шум, ради которого
	// приглушение и заводили.
	if muted := countMuted(snapshot); muted > 0 {
		body.WriteString("\n" + fmt.Sprintf(words.muted, muted) + "\n")
	}

	if snapshot.Maintenance != nil {
		body.WriteString("\n" + words.maintenance + "\n")
	}

	body.WriteString("\n" + words.footer + "\n")

	return Text{Subject: subject, Body: body.String()}
}

// speaking — поводы, о которых письмо говорит: не приглушённые и не
// «в порядке».
func speaking(reasons []health.Reason) []health.Reason {
	out := make([]health.Reason, 0, len(reasons))
	for _, item := range reasons {
		if item.Suppressed {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return rank(out[i].Level) > rank(out[j].Level)
	})
	return out
}

func countMuted(snapshot health.Snapshot) int {
	muted := 0
	for _, item := range snapshot.Server.Reasons {
		if item.Suppressed {
			muted++
		}
	}
	for _, project := range snapshot.Projects {
		for _, item := range project.Reasons {
			if item.Suppressed {
				muted++
			}
		}
	}
	return muted
}

func worstOf(reasons []health.Reason) health.Level {
	level := health.LevelOK
	for _, item := range reasons {
		if rank(item.Level) > rank(level) {
			level = item.Level
		}
	}
	return level
}

func rank(level health.Level) int {
	switch level {
	case health.LevelProblem:
		return 2
	case health.LevelAttention:
		return 1
	default:
		return 0
	}
}

func summary(words phrases, problems, attention int) string {
	switch {
	case problems > 0:
		return fmt.Sprintf(words.summaryProblem, problems)
	case attention > 0:
		return fmt.Sprintf(words.summaryAttention, attention)
	default:
		return words.summaryOK
	}
}

// line — одна строка отчёта.
//
// Текст собирается здесь, из кода и чисел, а не приезжает готовым: это то
// же правило, по которому уровни и коды уходят в приложение, а не строки.
// Разница лишь в том, что здесь получатель — человек, и словарь стоит
// рядом.
func line(words phrases, item health.Reason) string {
	text, ok := words.reasons[item.Code]
	if !ok {
		// Незнакомый код показывается как есть. Это честнее пустой строки
		// и сразу видно в отчёте о баге; свод при этом уже посчитан, и
		// пропускать повод из-за отсутствия перевода нельзя.
		text = item.Code
	}

	filled := strings.NewReplacer(
		"{days}", fmt.Sprint(absDays(item)),
		"{count}", fmt.Sprint(item.Count),
		"{minutes}", fmt.Sprint(item.WindowMinutes),
		"{detail}", detail(words, item.Detail),
	).Replace(text)

	prefix := words.markAttention
	if item.Level == health.LevelProblem {
		prefix = words.markProblem
	}
	if item.Domain != "" {
		return fmt.Sprintf("%s %s — %s", prefix, item.Domain, filled)
	}
	return fmt.Sprintf("%s %s", prefix, filled)
}

// detail — почему проверка не состоялась, словами.
//
// Уточнение приезжает кодом, как и всё, что агент отдаёт наружу, — но здесь
// оно попадает не в двуязычный интерфейс, а в письмо человеку:
// «(linkScanTimeout)» посреди фразы читается как поломка у нас, а не как
// беда на сервере.
//
// Незнакомое показывается как есть. У срока домена в уточнении бывает и
// текст ошибки реестра, а не код, и потерять его хуже, чем показать: тем
// же соображением незнакомый повод выводится кодом, а не пропускается.
func detail(words phrases, code string) string {
	if text, ok := words.details[code]; ok {
		return text
	}
	return code
}

func absDays(item health.Reason) int {
	if item.Days == nil {
		return 0
	}
	if *item.Days < 0 {
		return -*item.Days
	}
	return *item.Days
}
