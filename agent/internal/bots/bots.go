// Package bots — роботы, о которых агент что-то знает: кому сайт открыт в
// robots.txt (SEO-аудит, трек S) и кто из них на самом деле приходил по
// логам веб-сервера (экран «Агенты и ИИ», трек V, Р20).
//
// Одна копия списка на оба применения: до трека V он жил в `seo/site.go`, и
// вторая копия для логов разошлась бы с первой при первом же пополнении.
package bots

import "strings"

// Кто этот робот для сайта.
const (
	// Поисковик: закрыт — сайта нет в поиске.
	SearchEngine = "searchEngine"
	// ИИ-поиск: закрыт — сайт не цитируют в ответах со ссылкой.
	AISearch = "aiSearch"
	// Обучение нейросетей: закрыт — тексты не идут в обучение.
	AITraining = "aiTraining"
	// Робот, которого запускает сам человек своим вопросом к ИИ: «прочитай
	// эту страницу». Его владельцы пишут, что robots.txt к нему может не
	// применяться, поэтому в проверку robots.txt он не входит — «закрыт»
	// про него было бы неправдой. В логах он виден, как и остальные.
	AIUser = "aiUser"
)

// Bot — один робот.
type Bot struct {
	// Имя, как его пишут в User-agent: и в robots.txt, и в строке лога.
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Бывает ли он в логах. Google-Extended и Applebot-Extended — только
	// правила в robots.txt: ходят под именами Googlebot и Applebot, и
	// «пока не приходил» про них было бы неправдой навсегда.
	InLogs bool `json:"inLogs"`
}

// List — одна копия списка.
//
// Имена сверены 2026-09-29 с официальными страницами владельцев: OpenAI
// (developers.openai.com/api/docs/bots), Anthropic (support.claude.com),
// Perplexity (docs.perplexity.ai/guides/bots), Google (google-common-
// crawlers: Google-Extended поиск не затрагивает), Apple (support.apple.com
// 119829: Applebot-Extended — только обучение).
//
// Список устаревает; обновляется он с выпуском агента.
var List = []Bot{
	{Name: "Googlebot", Kind: SearchEngine, InLogs: true},
	{Name: "Bingbot", Kind: SearchEngine, InLogs: true},
	// «YandexBot», а не «Yandex»: правило пишут на «Yandex», и совпадение
	// по началу имени его находит.
	{Name: "YandexBot", Kind: SearchEngine, InLogs: true},
	{Name: "OAI-SearchBot", Kind: AISearch, InLogs: true},
	{Name: "Claude-SearchBot", Kind: AISearch, InLogs: true},
	{Name: "PerplexityBot", Kind: AISearch, InLogs: true},
	{Name: "GPTBot", Kind: AITraining, InLogs: true},
	{Name: "ClaudeBot", Kind: AITraining, InLogs: true},
	{Name: "Google-Extended", Kind: AITraining},
	{Name: "Applebot-Extended", Kind: AITraining},
	{Name: "CCBot", Kind: AITraining, InLogs: true},
	{Name: "ChatGPT-User", Kind: AIUser, InLogs: true},
	{Name: "Claude-User", Kind: AIUser, InLogs: true},
	{Name: "Perplexity-User", Kind: AIUser, InLogs: true},
}

// ForRobots — роботы, о которых спрашивают robots.txt: все, кроме тех,
// кого запускает человек своим вопросом.
func ForRobots() []Bot {
	out := make([]Bot, 0, len(List))
	for _, bot := range List {
		if bot.Kind != AIUser {
			out = append(out, bot)
		}
	}
	return out
}

// Other — имя счётчика «прочих роботов»: назвался роботом, но в списке его
// нет. Одной строкой, а не по имени: имён тысячи, и каждое в базе — это
// строка без смысла для человека.
const Other = "other"

// Приметы, по которым строку User-agent узнают как робота. «bot» — только с
// разделителем после: роботы пишут «AhrefsBot/7.0», «MJ12bot;», а у людей
// слово встречается внутри названия телефона («Cubot Note 20», «CUBOT_X30»).
// «+http» — ссылка на страницу о роботе, которую ставят почти все они.
var robotWords = []string{"bot/", "bot;", "bot)", "bot-", "bot,", "crawler", "spider", "slurp", "fetcher", "+http"}

// Classify — какой робот назвался этой строкой User-agent.
//
// Сама строка нигде не сохраняется: из функции выходит только имя из списка,
// [Other] или пусто — «похоже на человека». Подлинность не проверяется:
// назваться Googlebot может кто угодно, и экран так и пишет — «назвался».
//
// Поиск — по подстроке без учёта регистра: имя стоит внутри длинной строки
// («Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)»).
// Более длинные имена проверяются раньше: «Claude-SearchBot» не должен
// посчитаться за «ClaudeBot».
func Classify(agent string) string {
	if agent == "" || agent == "-" {
		return ""
	}
	lower := strings.ToLower(agent)
	best := ""
	for _, bot := range List {
		if !bot.InLogs {
			continue
		}
		if strings.Contains(lower, strings.ToLower(bot.Name)) && len(bot.Name) > len(best) {
			best = bot.Name
		}
	}
	if best != "" {
		return best
	}
	for _, word := range robotWords {
		if strings.Contains(lower, word) {
			return Other
		}
	}
	if strings.HasSuffix(lower, "bot") {
		return Other
	}
	return ""
}

// Адреса, которые перебирают сканеры уязвимостей. Обращение к ним — не
// беда: их видит любой сайт в интернете, и экран показывает число, а не
// тревогу. Беда — если такой адрес с секретом ответил 200 (см. [Secret]).
var scannerPaths = []string{
	"/.env", "/.git/", "/wp-login.php", "/xmlrpc.php", "/phpmyadmin", "/pma/",
	"/.aws/", "/actuator", "/.ds_store", "/config.php", "/server-status",
}

// Scanner — похож ли путь на перебор сканером.
func Scanner(path string) bool {
	lower := strings.ToLower(path)
	for _, probe := range scannerPaths {
		if lower == strings.TrimSuffix(probe, "/") || strings.HasPrefix(lower, probe) {
			return true
		}
	}
	return false
}

// Secret — адрес, который не должен отдаваться никогда: файл с паролями и
// код сайта. Ответ 200 на него — повод для красной строки со ссылкой на
// «Безопасность», где проверка сайта скажет, отдаётся ли он сейчас.
func Secret(path string) bool {
	lower := strings.ToLower(path)
	return lower == "/.env" || lower == "/.git" || strings.HasPrefix(lower, "/.git/")
}
