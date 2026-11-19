package main

import "agent/internal/bots"

// Наша таблица — первая по старшинству (Р51). Каждая строка с причиной:
// таблица пополняется только так.
type ownRow struct {
	name       string
	role       string
	family     string
	legacy     bool
	robotsOnly bool
	match      []bots.Match
	why        string
}

func token(text string) []bots.Match { return []bots.Match{{Text: text, Rule: bots.RuleToken}} }

var ownRows = []ownRow{
	// Прежние 14 (Р19): имя в базе и роль не меняются — по ним идёт история
	// визитов, отказы и «пропал». Иначе GPTBot по ai-robots стал бы «другим
	// роботом ИИ», а OAI-SearchBot по PostHog — тоже.
	{name: "Googlebot", role: bots.SearchEngine, legacy: true, match: token("Googlebot"), why: "прежний"},
	{name: "Bingbot", role: bots.SearchEngine, legacy: true, match: token("Bingbot"), why: "прежний"},
	{name: "YandexBot", role: bots.SearchEngine, legacy: true, match: token("YandexBot"), why: "прежний"},
	{name: "OAI-SearchBot", role: bots.AISearch, legacy: true, match: token("OAI-SearchBot"), why: "прежний"},
	{name: "Claude-SearchBot", role: bots.AISearch, legacy: true, match: token("Claude-SearchBot"), why: "прежний"},
	{name: "PerplexityBot", role: bots.AISearch, legacy: true, match: token("PerplexityBot"), why: "прежний"},
	{name: "GPTBot", role: bots.AITraining, legacy: true, match: token("GPTBot"), why: "прежний"},
	{name: "ClaudeBot", role: bots.AITraining, legacy: true, match: token("ClaudeBot"), why: "прежний"},
	{name: "Google-Extended", role: bots.AITraining, legacy: true, robotsOnly: true, match: token("Google-Extended"),
		why: "прежний; только правило robots.txt, ходит под именем Googlebot"},
	{name: "Applebot-Extended", role: bots.AITraining, legacy: true, robotsOnly: true, match: token("Applebot-Extended"),
		why: "прежний; только правило robots.txt, ходит под именем Applebot"},
	{name: "CCBot", role: bots.AITraining, legacy: true, match: token("CCBot"), why: "прежний"},
	{name: "ChatGPT-User", role: bots.AIUser, legacy: true, match: token("ChatGPT-User"), why: "прежний"},
	{name: "Claude-User", role: bots.AIUser, legacy: true, match: token("Claude-User"), why: "прежний"},
	{name: "Perplexity-User", role: bots.AIUser, legacy: true, match: token("Perplexity-User"), why: "прежний"},

	// Семейство Googlebot (Р60): до трека подстрока складывала их в
	// Googlebot, и счёт Googlebot не должен упасть в день обновления.
	{name: "Googlebot-Image", role: bots.SearchEngine, family: "Googlebot", match: token("Googlebot-Image"),
		why: "семейство Googlebot, Р60"},
	{name: "Googlebot-Video", role: bots.SearchEngine, family: "Googlebot", match: token("Googlebot-Video"),
		why: "семейство Googlebot, Р60"},
	{name: "Googlebot-News", role: bots.SearchEngine, family: "Googlebot", match: token("Googlebot-News"),
		why: "семейство Googlebot, Р60"},

	// Имена botscent с видом crawler или fetcher, которых не знает никто
	// старше (Р51): роль — руками, по описанию владельца.
	{name: "AdsBot-Google-Mobile", role: bots.SearchEngine, match: token("AdsBot-Google-Mobile"),
		why: "проверка рекламных страниц поисковика Google, как AdsBot-Google"},
	{name: "Mediapartners-Google", role: bots.SearchEngine, match: token("Mediapartners-Google"),
		why: "AdSense читает страницу под рекламу; служба поисковика Google"},
	{name: "AdIdxBot", role: bots.SearchEngine, match: token("adidxbot"),
		why: "проверка рекламных страниц поисковика Bing"},
	{name: "BingVideoPreview", role: bots.SearchEngine, match: token("BingVideoPreview"),
		why: "превью видео в выдаче Bing"},
	{name: "GoogleProducer", role: bots.SearchEngine, match: token("GoogleProducer"),
		why: "Google Publisher Center читает ленты для Google Новостей"},
	{name: "APIs-Google", role: bots.Script, match: token("APIs-Google"),
		why: "доставка уведомлений API Google, не обход"},
	{name: "Google-CWS", role: bots.Script, match: token("Google-CWS"),
		why: "Chrome Web Store проверяет ссылки расширения, не обход"},
	{name: "Google-Pinpoint", role: bots.Script, match: []bots.Match{{Text: "Google-Pinpoint", Rule: bots.RuleExact}},
		why: "Pinpoint скачивает документ по просьбе человека, не обход"},
	{name: "YandexUserproxy", role: bots.Script, match: token("YandexUserproxy"),
		why: "сервисы Яндекса (перевод и др.) по просьбе человека, не обход"},
	{name: "Apple Podcasts", role: bots.Script, match: []bots.Match{{Text: "iTMS", Rule: bots.RuleExact}},
		why: "Apple Podcasts читает ленту подкаста, не обход"},

	// Записи ai-robots с оговоркой «не знаем, связан ли с ИИ» (aiRobotsUnsure):
	// их категории не верим.
	{name: "Awario", role: bots.SEOTool, match: []bots.Match{{Text: "Awario", Rule: bots.RuleToken}, {Text: "AwarioBot", Rule: bots.RuleToken}},
		why: "сервис упоминаний бренда (awario.com/bots.html), не обучение нейросетей"},
	{name: "Echobot Bot", role: bots.SEOTool, match: token("Echobot Bot"),
		why: "мониторинг упоминаний в СМИ (Echobot Media), не обучение нейросетей"},

	// У PostHog — слово «Pinterest», а так же подписывается встроенный
	// браузер приложения («[Pinterest/iOS]»), где ходит человек. Робот
	// превью пишет «Pinterestbot» или «Pinterest/0.2 (+…bot.html)».
	{name: "Pinterest", role: bots.LinkPreview,
		match: []bots.Match{{Text: "Pinterestbot", Rule: bots.RuleToken}, {Text: `Pinterest/0.d`, Rule: bots.RuleRegex}},
		why:   "превью ссылок Pinterest без встроенного браузера приложения"},

	// Gatus по умолчанию пишет «Gatus/1.0» (константа GatusUserAgent,
	// v5.36.0) — так называет себя чужой мониторинг на Gatus. Наш backend
	// ставит своим проверкам `vpsfocus-uptime` (Р58).
	{name: "Gatus", role: bots.Monitoring, match: []bots.Match{{Text: "Gatus", Rule: bots.RuleVersioned}},
		why: "мониторинг доступности на Gatus, Р58"},

	// Наши обходы и проверки (Р22, Р58): `vpsfocus-seo`, `-links`, `-probe`,
	// `-compliance`, `-uptime` — всё, что начинается с `vpsfocus-`.
	{name: "vpsFocus", role: bots.Self, match: []bots.Match{{Text: "vpsfocus-", Rule: bots.RulePrefix}},
		why: "мониторинг vpsFocus, Р58"},
}

// Шаблоны PostHog, которые не берём. Это приметы «странного браузера», а не
// имена робота: у настоящего человека в старом или необычном браузере они
// тоже бывают, а человек, принятый за робота, хуже пропущенного робота (Р5).
var postHogSkipped = map[string]string{
	`\) AppleWebKit/537\.36 Chrome/`: "примета браузера, не имя",
	`Chrome/1\d\d.*Edge/1[2-8]\.`:    "примета браузера, не имя",
	`Safari/537\.3$`:                 "примета браузера, не имя",
	`(^\s|\s$)`:                      "примета строки, не имя",
	`^Mozilla/5\.0$`:                 "примета строки, не имя",
	`Chrome/\d+\.\d+\.\d+\.\d{4,} (Mobile )?Safari/537\.36$`: "примета браузера, не имя",
}

// Записи ai-robots с оговоркой «не знаем, связан ли с ИИ», чьё назначение
// неизвестно и нам: в список не берём, такие строки остаются «прочими
// роботами» — честнее, чем приписать роль.
var aiRobotsSkipped = map[string]string{
	"Datenbank Crawler": "назначение неизвестно",
	"WARDBot":           "назначение неизвестно",
}

// Тексты правил, которые не берём ни из одного источника (кроме нашей
// таблицы): так же подписываются программы, где ходит человек руками, —
// встроенные браузеры редакторов и приложений (Р3: встроенный браузер
// ИИ-приложения роботом не считаем). Человек, принятый за робота, хуже
// пропущенного робота (Р5). Ключ — в нижнем регистре.
var skippedTokens = map[string]string{
	"code":      "VS Code: «Code/1.95 Chrome/… Electron/…» — встроенный браузер редактора",
	"cursor":    "Cursor: встроенный браузер редактора; запросы самого Cursor узнаёт регулярка PostHog «^Cursor/»",
	"trae":      "Trae: редактор на Electron, «Trae/…» во встроенном браузере",
	"asana":     "Asana: приложение на Electron, «Asana/…»",
	"pinterest": "встроенный браузер приложения Pinterest; робот — строкой Pinterest в нашей таблице",
	"viber":     "встроенный браузер Viber; робота превью по этому слову не отличить",
}
