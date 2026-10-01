package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Регулярка с квантификатором вида {2,4} содержит запятую сама по себе.
// Раздели список по запятой — и такая регулярка развалится на две половинки,
// каждая из которых не компилируется. Список ссылок делится точкой с
// запятой ровно поэтому, в отличие от splitList для адресов почты.
func TestSplitExcludesKeepsCommasInsidePatterns(t *testing.T) {
	got := splitExcludes(" /blog/.* ; a{2,4}\\.html ")
	want := []string{"/blog/.*", "a{2,4}\\.html"}

	if len(got) != len(want) {
		t.Fatalf("список: %+v, ожидали %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("список: %+v, ожидали %+v", got, want)
		}
	}
}

func TestSplitExcludesDropsEmptyParts(t *testing.T) {
	if got := splitExcludes(" ; ; /a ;; "); len(got) != 1 || got[0] != "/a" {
		t.Fatalf("пустые части не отфильтровались: %+v", got)
	}
	if got := splitExcludes(""); got != nil {
		t.Fatalf("пустая строка должна дать пустой список: %+v", got)
	}
}

// Список страниц правит человек, а меряются они ключом агентства, у которого
// квота одна на всех. Полный адрес в списке означал бы, что нашим ключом
// меряют чужой сайт: только пути на самом сайте.
func TestPagesKeepOnlyOwnPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vitals.json")
	if err := os.WriteFile(path, []byte(`[
	  {"domain": " Site.TEST ", "paths": [
	    "/catalog", "catalog-no-slash", "https://evil.test/x", "//evil.test/x",
	    "/with space", "/catalog", " ", "/pricing"
	  ]},
	  {"domain": "", "paths": ["/ignored"]}
	]`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Vitals: Vitals{PagesPath: path}}
	pages, err := cfg.Pages()
	if err != nil {
		t.Fatal(err)
	}

	got := pages["site.test"]
	want := []string{"/catalog", "/pricing"}
	if len(got) != len(want) {
		t.Fatalf("пути: %+v, ожидали %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("пути: %+v, ожидали %+v", got, want)
		}
	}
	if _, ok := pages[""]; ok {
		t.Error("сайт без домена не должен попадать в список")
	}
}

// Файла нет — мерить главные страницы это не мешает: список дополнительных
// страниц необязателен, и его отсутствие не должно выглядеть поломкой.
func TestPagesWithoutFile(t *testing.T) {
	cfg := Config{Vitals: Vitals{PagesPath: filepath.Join(t.TempDir(), "нет.json")}}

	pages, err := cfg.Pages()
	if err != nil {
		t.Fatalf("отсутствие файла — не ошибка: %v", err)
	}
	if len(pages) != 0 {
		t.Fatalf("ожидали пустой список: %+v", pages)
	}
}

// Пустая или испорченная строка типов устройств не должна означать «не мерить
// вовсе»: заполняя это поле, просят обратного.
func TestStrategiesFallBackToMobile(t *testing.T) {
	cases := map[string][]string{
		"":                      {"mobile"},
		"  ":                    {"mobile"},
		"планшет":               {"mobile"},
		"desktop":               {"desktop"},
		" Mobile , desktop ":    {"mobile", "desktop"},
		"mobile,mobile,desktop": {"mobile", "desktop"},
	}

	for raw, want := range cases {
		got := strategies(raw)
		if len(got) != len(want) {
			t.Fatalf("%q: получили %+v, ждали %+v", raw, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q: получили %+v, ждали %+v", raw, got, want)
			}
		}
	}
}

func TestSeoDelayFromRequestsPerMinute(t *testing.T) {
	// Пауза между запросами задаётся человеку понятной величиной —
	// «запросов в минуту», — а краулеру нужна пауза.
	cases := []struct {
		perMinute int
		expected  time.Duration
	}{
		{60, time.Second},
		{30, 2 * time.Second},
		{120, 500 * time.Millisecond},
		// Бессмыслица в настройке не должна превращаться в обход без пауз.
		{0, time.Second},
	}

	for _, item := range cases {
		got := Seo{RequestsPerMinute: item.perMinute}.Delay()
		if got != item.expected {
			t.Errorf("%d запросов в минуту: получено %v, ожидалось %v",
				item.perMinute, got, item.expected)
		}
	}
}

// ── Проекты ──────────────────────────────────────────────────────────────

// writeConfigFiles кладёт рядом список сайтов и состав проектов.
func writeConfigFiles(t *testing.T, sites, projects string) Config {
	t.Helper()

	dir := t.TempDir()
	cfg := Config{
		SitesPath:    filepath.Join(dir, "sites.json"),
		ProjectsPath: filepath.Join(dir, "projects.json"),
	}
	if err := os.WriteFile(cfg.SitesPath, []byte(sites), 0o600); err != nil {
		t.Fatal(err)
	}
	if projects != "" {
		if err := os.WriteFile(cfg.ProjectsPath, []byte(projects), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// Состав, раздаваемый приложением, — главный источник: только оно знает про
// ручные переносы, а агент про них знать не должен и не может.
func TestProjectsComeFromTheApp(t *testing.T) {
	cfg := writeConfigFiles(t,
		`[{"domain":"example.com"},{"domain":"staging.example.com"}]`,
		`[
		  {"id":"prod","name":"Клиент","domains":["example.com"]},
		  {"id":"staging","name":"Стенд","domains":["staging.example.com"]}
		]`)

	projects, err := cfg.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("проекты слиплись: %+v", projects)
	}
	if projects[0].Name != "Клиент" {
		t.Errorf("имя проекта потеряно: %+v", projects[0])
	}
}

// Файла нет — агент раскладывает домены сам, по регистрируемому.
//
// Так он живёт ровно один промежуток: от обновления стека до первой раздачи
// состава приложением. Промолчи он в эту минуту, еженедельный отчёт ушёл бы
// вообще без проектов, а свод оказался бы пуст — молчаливый ноль хуже
// честного приближения.
func TestProjectsFallBackToRegistrableDomain(t *testing.T) {
	cfg := writeConfigFiles(t,
		`[{"domain":"www.example.com"},{"domain":"shop.example.com"},{"domain":"another.org"}]`,
		"")

	projects, err := cfg.Projects()
	if err != nil {
		t.Fatalf("отсутствие файла — не ошибка: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("ожидали два проекта: %+v", projects)
	}
	for _, project := range projects {
		if project.Name == "example.com" && len(project.Domains) != 2 {
			t.Errorf("поддомены не сложились: %+v", project)
		}
	}
}

// Домен, о котором приложение ещё не рассказало, всё равно попадает в свод.
func TestProjectsPickUpUnknownDomains(t *testing.T) {
	cfg := writeConfigFiles(t,
		`[{"domain":"example.com"},{"domain":"fresh.org"}]`,
		`[{"id":"prod","name":"Клиент","domains":["example.com"]}]`)

	projects, err := cfg.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("новый сайт потерялся: %+v", projects)
	}
}

// Состав разошёлся с сервером — верим серверу.
//
// Карточка про домен, которого на этом сервере нет, рассказывала бы о
// пустоте: ни проверок, ни обхода по нему не делалось и не будет.
func TestProjectsDropDomainsTheServerDoesNotHave(t *testing.T) {
	cfg := writeConfigFiles(t,
		`[{"domain":"example.com"}]`,
		`[{"id":"prod","name":"Клиент","domains":["example.com","gone.com"]},
		  {"id":"ghost","name":"Призрак","domains":["vanished.com"]}]`)

	projects, err := cfg.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("опустевший проект остался: %+v", projects)
	}
	if len(projects[0].Domains) != 1 || projects[0].Domains[0] != "example.com" {
		t.Errorf("домен, которого нет на сервере, остался: %+v", projects[0])
	}
}

// ── Ступени напоминания о домене ─────────────────────────────────────────

// Список разбирается и приводится к убывающему порядку: на него полагается
// `reachedStep`, а перевёрнутый список молча сломал бы правило «напомнили на
// ступени — дальше молчим до следующей».
func TestDomainStepsComeSortedAndUnique(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "есть")
	t.Setenv("ALERT_DOMAIN_DAYS", "7, 30,14, 7 ,1")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := []int{30, 14, 7, 1}
	if len(cfg.Alerts.DomainSteps) != len(want) {
		t.Fatalf("ступени: %v, ждали %v", cfg.Alerts.DomainSteps, want)
	}
	for i, step := range want {
		if cfg.Alerts.DomainSteps[i] != step {
			t.Fatalf("ступени: %v, ждали %v", cfg.Alerts.DomainSteps, want)
		}
	}
}

// Одно число — это тот же список из одной ступени. Конфиг, собранный руками
// до тридцатого спринта, обязан продолжать работать: агент обновляется
// вместе со стеком, а `agent.env` на сервере остаётся прежним до следующей
// записи из приложения.
func TestSingleDomainThresholdStillParses(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "есть")
	t.Setenv("ALERT_DOMAIN_DAYS", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Alerts.DomainSteps) != 1 || cfg.Alerts.DomainSteps[0] != 30 {
		t.Fatalf("ступени: %v, ждали [30]", cfg.Alerts.DomainSteps)
	}
}

// Мусор в поле не должен выключать напоминания вовсе: пустой список означал
// бы, что о кончающемся домене не скажут ни разу, — и молчание это выглядело
// бы как «всё в порядке».
func TestBrokenDomainStepsFallBackToDefaults(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "есть")

	for _, raw := range []string{"", "abc", "0", "-5"} {
		t.Setenv("ALERT_DOMAIN_DAYS", raw)

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Alerts.DomainSteps) == 0 {
			t.Fatalf("%q: ступеней не осталось — о домене не скажут ни разу", raw)
		}
	}
}

// Порог всплеска «страницы нет» свой и выше порога пятисоток: четыреста
// четвёртая — это ещё и фон от ботов, а пятисотка фоном не бывает.
func TestNotFoundThresholdIsItsOwn(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "есть")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logs.NotFoundThreshold <= cfg.Logs.AlertThreshold {
		t.Errorf("порог «страницы нет» (%d) не выше порога пятисоток (%d)",
			cfg.Logs.NotFoundThreshold, cfg.Logs.AlertThreshold)
	}

	// Ноль законен: «не показывать мне всплески четыреста четвёртых» —
	// осознанный выбор, а не пропущенное поле.
	t.Setenv("LOG_NOT_FOUND_THRESHOLD", "0")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logs.NotFoundThreshold != 0 {
		t.Errorf("ноль не пережил приведение: %d", cfg.Logs.NotFoundThreshold)
	}
}
