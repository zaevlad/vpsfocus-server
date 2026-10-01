package digest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent/internal/health"
)

// codes читает список кодов поводов прямо из исходника свода.
//
// Перечислить их здесь значило бы завести третью копию перечня — после
// таблицы уровней и двух словарей. Тот же приём, которым в приложении
// сверяются коды ошибок в четырёх местах.
func codes(t *testing.T) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "health", "health.go"), nil, 0)
	if err != nil {
		t.Fatalf("разбор свода: %v", err)
	}

	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		if !strings.HasPrefix(spec.Names[0].Name, "Code") {
			return true
		}
		literal, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		found = append(found, strings.Trim(literal.Value, `"`))
		return true
	})

	if len(found) < 10 {
		t.Fatalf("разбор дал %d кодов — сломался разбор, а не список", len(found))
	}
	return found
}

// У каждого повода есть текст на обоих языках.
//
// Забытый повод уехал бы человеку кодом вида `healthCertExpired` посреди
// письма — и не в двуязычный интерфейс, где такое видно сразу, а в ящик,
// куда мы больше не заглядываем.
func TestEveryCodeHasBothLanguages(t *testing.T) {
	all := codes(t)

	for name, words := range map[string]phrases{"ru": russian, "en": english} {
		for _, code := range all {
			if _, ok := words.reasons[code]; !ok {
				t.Errorf("%s: поводу %s нет текста — он уедет человеку кодом", name, code)
			}
		}
		for code := range words.reasons {
			found := false
			for _, known := range all {
				if known == code {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: текст для %s есть, а такого повода нет", name, code)
			}
		}
	}
}

// Сервер назван в теме письма.
//
// Агентство с десятью серверами получит десять отчётов, и без имени сервера
// в теме это выглядит как сломанный продукт, рассылающий одно и то же.
func TestSubjectNamesTheServer(t *testing.T) {
	text := Build("ru", "vps-клиента", health.Snapshot{})

	if !strings.Contains(text.Subject, "vps-клиента") {
		t.Errorf("сервера нет в теме: %q", text.Subject)
	}
}

// Отчёт уходит и тогда, когда всё в порядке.
//
// Еженедельный отчёт, замолкающий от отсутствия новостей, неотличим от
// агента, который умер, — то же соображение, из-за которого пустой обход
// считается отказом, а не «всё хорошо».
func TestReportSpeaksWhenAllIsWell(t *testing.T) {
	text := Build("ru", "vps", health.Snapshot{
		Server: health.ServerState{Level: health.LevelOK, Reasons: []health.Reason{}},
		Projects: []health.ProjectState{
			{Name: "example.com", Level: health.LevelOK, Reasons: []health.Reason{}},
		},
	})

	if !strings.Contains(text.Body, "всё в порядке") {
		t.Errorf("отчёт молчит о том, что всё хорошо: %q", text.Body)
	}
	if !strings.Contains(text.Body, "example.com") {
		t.Errorf("проект не назван: %q", text.Body)
	}
}

// Приглушённая находка в письмо не попадает, но её число называется.
//
// Молчать о приглушённых совсем нельзя: через полгода никто не вспомнит,
// что приглушение вообще ставили. Перечислять их — значит вернуть в письмо
// ровно тот шум, ради которого приглушение и заводили.
func TestMutedFindingsAreCountedNotListed(t *testing.T) {
	snapshot := health.Snapshot{
		Server: health.ServerState{Level: health.LevelOK},
		Projects: []health.ProjectState{{
			Name:  "example.com",
			Level: health.LevelOK,
			Reasons: []health.Reason{{
				Code:       health.CodeSeoCritical,
				Level:      health.LevelAttention,
				Domain:     "example.com",
				Count:      12,
				Suppressed: true,
			}},
		}},
	}

	text := Build("ru", "vps", snapshot)

	if strings.Contains(text.Body, "критичных находок SEO") {
		t.Errorf("приглушённая находка попала в письмо: %q", text.Body)
	}
	if !strings.Contains(text.Body, "Приглушено находок: 1") {
		t.Errorf("о приглушённых не сказано вовсе: %q", text.Body)
	}
	if !strings.Contains(text.Subject, "всё в порядке") {
		t.Errorf("приглушённая находка подняла тему письма: %q", text.Subject)
	}
}

// Числа подставляются, а не показываются заглушками.
func TestNumbersAreFilledIn(t *testing.T) {
	left := 3
	snapshot := health.Snapshot{
		Server: health.ServerState{
			Level: health.LevelAttention,
			Reasons: []health.Reason{
				{Code: health.CodeDiskFilling, Level: health.LevelAttention, Count: 88},
			},
		},
		Projects: []health.ProjectState{{
			Name:  "example.com",
			Level: health.LevelProblem,
			Reasons: []health.Reason{
				{
					Code:   health.CodeCertExpiring,
					Level:  health.LevelAttention,
					Domain: "example.com",
					Days:   &left,
				},
				{
					Code:   health.CodeLinksFailed,
					Level:  health.LevelAttention,
					Domain: "example.com",
					Detail: "linksRobotsForbidden",
				},
			},
		}},
	}

	text := Build("ru", "vps", snapshot)

	// Уточнение — словами, а не кодом: письмо читает человек, а не
	// двуязычный интерфейс, и «(linksRobotsForbidden)» посреди фразы
	// выглядит поломкой у нас. Раньше этот тест требовал ровно кода — то
	// есть охранял ту самую беду, которую теперь чинит.
	for _, want := range []string{"88", "3 дн.", russian.details[linkRobotsForbidden]} {
		if !strings.Contains(text.Body, want) {
			t.Errorf("в письме нет %q: %s", want, text.Body)
		}
	}
	if strings.Contains(text.Body, linkRobotsForbidden) {
		t.Errorf("код уточнения уехал человеку как есть: %s", text.Body)
	}
	if strings.Contains(text.Body, "{count}") || strings.Contains(text.Body, "{days}") ||
		strings.Contains(text.Body, "{detail}") {
		t.Errorf("заглушка осталась незаполненной: %s", text.Body)
	}
}

// У каждого уточнения есть текст на обоих языках.
//
// Половина переведённой карты — это письмо, в котором половина строк
// по-английски: беда та же, что с забытым поводом, и ловится так же.
func TestDetailsHaveBothLanguages(t *testing.T) {
	for code := range russian.details {
		if _, ok := english.details[code]; !ok {
			t.Errorf("уточнению %s нет английского текста", code)
		}
	}
	for code := range english.details {
		if _, ok := russian.details[code]; !ok {
			t.Errorf("уточнению %s нет русского текста", code)
		}
	}
}

// Коды неудачной проверки ссылок совпадают с теми, что собирает агент.
//
// Собирает их `cmd/agent`, а импортировать его отсюда нельзя — это он
// импортирует нас. Поэтому копия, и поэтому же тест читает исходник: тем же
// приёмом приложение сверяет список составных зон и коды ошибок.
func TestLinkDetailCodesMatchTheAgent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agent", "main.go"))
	if err != nil {
		t.Fatalf("исходник агента: %v", err)
	}
	source := string(raw)

	for _, code := range []string{
		linkRobotsUnavailable, linkRobotsForbidden,
		lycheeUnavailable, linkScanTimeout, linkScanFailed,
	} {
		if !strings.Contains(source, `"`+code+`"`) {
			t.Errorf("агент больше не отдаёт %s: уточнение в письме останется кодом", code)
		}
	}
}

// Просроченный срок пишется днями без минуса: «истёк 5 дней назад», а не
// «через -5 дней».
func TestExpiredDaysAreNotNegative(t *testing.T) {
	ago := -5
	text := Build("ru", "vps", health.Snapshot{
		Projects: []health.ProjectState{{
			Name:  "example.com",
			Level: health.LevelProblem,
			Reasons: []health.Reason{{
				Code:   health.CodeDomainExpiring,
				Level:  health.LevelAttention,
				Domain: "example.com",
				Days:   &ago,
			}},
		}},
	})

	if strings.Contains(text.Body, "-5") {
		t.Errorf("минус уехал человеку: %s", text.Body)
	}
}

// Английский отчёт — английский целиком, а не наполовину.
func TestEnglishReportIsEnglish(t *testing.T) {
	text := Build("en", "vps", health.Snapshot{
		Server: health.ServerState{
			Level: health.LevelAttention,
			Reasons: []health.Reason{
				{Code: health.CodeNoChannels, Level: health.LevelAttention},
			},
		},
	})

	for _, russianWord := range []string{"Сервер", "Отчёт", "порядке"} {
		if strings.Contains(text.Body, russianWord) {
			t.Errorf("в английском отчёте русское слово %q: %s", russianWord, text.Body)
		}
	}
	if !strings.Contains(text.Body, "no notification channel") {
		t.Errorf("повод не переведён: %s", text.Body)
	}
}

// Незнакомый код не теряется: письмо покажет его как есть.
//
// Свод к этому моменту уже посчитан, и пропустить повод из-за отсутствия
// перевода значило бы промолчать о беде — что хуже некрасивой строки.
func TestUnknownCodeIsShownNotDropped(t *testing.T) {
	text := Build("ru", "vps", health.Snapshot{
		Server: health.ServerState{
			Level: health.LevelProblem,
			Reasons: []health.Reason{
				{Code: "healthSomethingNew", Level: health.LevelProblem},
			},
		},
	})

	if !strings.Contains(text.Body, "healthSomethingNew") {
		t.Errorf("незнакомый повод потерялся: %s", text.Body)
	}
}
