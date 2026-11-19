package bots

import (
	"embed"
	"encoding/json"
	"fmt"
)

// Список роботов и диапазоны их адресов (трек AI, спринт 271, Р1).
//
// Собирает их `cmd/bots-build` руками перед выпуском агента из открытых
// списков по закреплённым коммитам и из файлов, которые публикуют сами
// владельцы роботов. Результат лежит в `data/` и коммитится (Р29): CI его
// только вшивает, поэтому образ из тега повторяется, а агент на сервере
// клиента за списками в интернет не ходит никогда.
//
// Типы ниже — одна копия формата: сборщик пишет ими, агент ими читает.

//go:embed data/robots.json data/ranges.json
var data embed.FS

// Правила совпадения имени со строкой User-agent (Р4). Применяет их
// `Classify` (спринт 272); сборщик только записывает, какое у имени правило.
const (
	// Токен целиком: слева разделитель ` ;(,+` или начало строки, справа
	// `/ ;),` или конец. Текст может быть из нескольких слов.
	RuleToken = "token"
	// Только «Имя/версия».
	RuleVersioned = "versioned"
	// Вся строка User-agent целиком.
	RuleExact = "exact"
	// Подстрока на границе слова (буквальные шаблоны PostHog).
	RuleWord = "word"
	// Начало токена (`vpsfocus-` — все наши обходы, Р58).
	RulePrefix = "prefix"
	// Регулярное выражение RE2 (настоящие регулярки PostHog).
	RuleRegex = "regex"
)

// Роли сверх прежних четырёх (Р3, Р51, Р52, Р58).
const (
	// Робот ИИ, чьё назначение источники не дают разобрать надёжно. На
	// экране — «Другие роботы ИИ» (Р52).
	AICrawler   = "aiCrawler"
	SEOTool     = "seoTool"
	LinkPreview = "linkPreview"
	Monitoring  = "monitoring"
	// Общие имена: `curl`, `python-requests`, безголовые браузеры. Уступают
	// любому конкретному имени.
	Script = "script"
	// Наши собственные обходы и проверки: не люди и не чужие роботы (Р22).
	Self = "self"
)

// Roles — все роли, которые бывают в списке. Сборщик отказывает роли не из
// этого перечня.
var Roles = []string{SearchEngine, AISearch, AITraining, AIUser, AICrawler, SEOTool, LinkPreview, Monitoring, Script, Self}

// Catalog — `data/robots.json`.
type Catalog struct {
	// День сборки по UTC, `2006-01-02`: «список роботов от <даты>».
	Built   string   `json:"built"`
	Sources []Source `json:"sources"`
	Robots  []Robot  `json:"robots"`
}

// Source — откуда взяты имена.
type Source struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Commit string `json:"commit"`
	// Файл лицензии источника в `data/`, как есть.
	License string `json:"license"`
}

// Robot — один робот списка.
type Robot struct {
	// Имя в базе и на экране. У прежних 14 — прежнее (Р19).
	Name string `json:"name"`
	Role string `json:"role"`
	// Семейство, под именем которого пишется счётчик (Р20, Р60): пусто —
	// своё имя.
	Family string `json:"family,omitempty"`
	// Один из прежних 14: у них «пока не приходил» (Р18) и проверка
	// robots.txt в SEO-аудите (Р2).
	Legacy bool `json:"legacy,omitempty"`
	// Только правило robots.txt, в логах под этим именем не ходят
	// (Google-Extended, Applebot-Extended).
	RobotsOnly bool `json:"robotsOnly,omitempty"`
	// Откуда роль: `own` (наша таблица) или идентификатор источника.
	RoleFrom string  `json:"roleFrom"`
	Match    []Match `json:"match"`
}

// Match — одно правило совпадения.
type Match struct {
	Text string `json:"text"`
	Rule string `json:"rule"`
}

// Ranges — `data/ranges.json`: адреса, с которых ходят роботы, по словам
// их владельцев.
type Ranges struct {
	Built string `json:"built"`
	Sets  []Set  `json:"sets"`
	// Диапазоны Cloudflare: адрес из них — прокси, а не посетитель (Р8).
	// Вторая копия списка из `Caddyfile.tpl` бандла, сверяется тестом.
	Cloudflare Proxy `json:"cloudflare"`
}

// Set — один файл владельца робота.
type Set struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Имена из списка роботов, которых проверяют по этому файлу.
	Names []string `json:"names"`
	// Дата файла (`creationTime`, у SAIL — `modified`); нет поля — день
	// скачивания, и тогда `createdFrom` = `download`.
	Created     string `json:"created"`
	CreatedFrom string `json:"createdFrom"`
	// Файл старше 12 месяцев на день сборки: имена считаются «не
	// проверить», а не «назвался» (Р54).
	Stale bool     `json:"stale,omitempty"`
	V4    []string `json:"v4"`
	V6    []string `json:"v6"`
}

// Proxy — диапазоны прокси.
type Proxy struct {
	URLs []string `json:"urls"`
	V4   []string `json:"v4"`
	V6   []string `json:"v6"`
}

// LoadCatalog читает вшитый список роботов.
func LoadCatalog() (Catalog, error) {
	var out Catalog
	raw, err := data.ReadFile("data/robots.json")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("robots.json: %w", err)
	}
	return out, nil
}

// LoadRanges читает вшитые диапазоны адресов.
func LoadRanges() (Ranges, error) {
	var out Ranges
	raw, err := data.ReadFile("data/ranges.json")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("ranges.json: %w", err)
	}
	return out, nil
}

// ListDate — день сборки вшитого списка, для подписи «список роботов от
// <даты>». Пусто, если список не читается (такой агент не пройдёт тестов).
func ListDate() string {
	catalog, err := LoadCatalog()
	if err != nil {
		return ""
	}
	return catalog.Built
}
