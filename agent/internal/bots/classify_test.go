package bots

import "testing"

// Сторож Р5: ни одна строка настоящего браузера не узнаётся роботом.
//
// Человек, принятый за робота, пропадает из «людей» в нагрузке и врёт в
// выводах — это хуже пропущенного робота. Набор написан нами, чужие наборы
// людей не копируются. Новая сборка списка (`cmd/bots-build`), которая
// роняет этот тест, не выпускается: правка — в `skippedTokens` сборщика.
var browsers = []string{
	// Компьютеры.
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/115.0.0.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 YaBrowser/24.10.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Vivaldi/7.0.3495.15",
	// Встроенные браузеры редакторов и приложений (Р3, Р66): наша
	// аудитория смотрит свой сайт как раз в них.
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Code/1.95.3 Chrome/128.0.6613.186 Electron/32.2.6 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Cursor/0.42.5 Chrome/124.0.6367.243 Electron/30.5.1 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Trae/1.0.9 Chrome/128.0.6613.186 Electron/32.2.6 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Slack/4.41.97 Chrome/130.0.6723.137 Electron/33.2.0 Safari/537.36 Sonic Slack_SSB/4.41.97",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) discord/1.0.9171 Chrome/128.0.6613.186 Electron/32.2.7 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Asana/2.3.0 Chrome/120.0.6099.291 Electron/28.3.3 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) obsidian/1.7.7 Chrome/130.0.6723.137 Electron/33.2.1 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Notion/4.2.0 Chrome/130.0.6723.137 Electron/33.2.1 Safari/537.36",
	// Телефоны и планшеты.
	"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/133.0 Mobile/15E148 Safari/605.1.15",
	"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 12; Cubot Note 20) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 10; CUBOT_X30 Build/QP1A) Chrome/120",
	"Mozilla/5.0 (Linux; Android 11; CUBOT KINGKONG 5 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 10; Toast POS Device) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 12; NOH-NX9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/114.0.0.0 HuaweiBrowser/15.0.4.312 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; U; Android 13; ru-ru; 2201117TY Build/TKQ1.221114.001) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/112.0.5615.136 Mobile Safari/537.36 XiaoMi/MiuiBrowser/14.8.0-gn",
	"Mozilla/5.0 (Linux; Android 13; SM-A515F) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36 YaApp_Android/24.110.1 YaSearchBrowser/24.110.1 BroPP/1.0 SA/3",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 DuckDuckGo/7 Safari/605.1.15",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) GSA/341.0.684441017 Mobile/15E148 Safari/604.1",
	// Встроенные браузеры приложений на телефоне.
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 [FBAN/FBIOS;FBAV/470.0.0.40.108;FBBV/640000000;FBDV/iPhone15,2;FBMD/iPhone;FBSN/iOS;FBSV/17.5;FBSS/3;FBCR/;FBID/phone;FBLC/en_US;FBOP/80]",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Instagram 350.0.0.30.89 (iPhone15,2; iOS 17_5; en_US; en; scale=3.00; 1179x2556; 640000000)",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 [Pinterest/iOS]",
	"Mozilla/5.0 (Linux; Android 13; SM-A515F) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36 Viber/22.6.0.0",
	"Mozilla/5.0 (Linux; Android 13; SM-A515F) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.39 Mobile Safari/537.36 Telegram-Android/11.4.2 (Samsung SM-A515F; Android 13; SDK 33; AVERAGE)",
	"Mozilla/5.0 (Linux; Android 13; SM-A515F Build/TP1A.220624.014; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/131.0.6778.39 Mobile Safari/537.36 [LinkedInApp]/9.30.1234",
	"Mozilla/5.0 (Linux; Android 13; SM-A515F Build/TP1A.220624.014; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/131.0.6778.39 Mobile Safari/537.36 trill_370504 JsSdk/1.0 NetType/WIFI Channel/googleplay AppName/musical_ly app_version/37.5.4 ByteLocale/en BytedanceWebview/d8a21c6",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Safari Line/14.19.1",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 KAKAOTALK 10.8.5",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.50(0x1800323a) NetType/WIFI Language/zh_CN",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 Ecosia ios@11.3.0.2381",
	// Телевизор и приставка.
	"Mozilla/5.0 (SMART-TV; Linux; Tizen 7.0) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/5.0 Chrome/94.0.4606.31 TV Safari/537.36",
	"Mozilla/5.0 (PlayStation; PlayStation 5/8.20) AppleWebKit/605.1.15 (KHTML, like Gecko)",
}

func TestBrowsersAreNotRobots(t *testing.T) {
	for _, agent := range browsers {
		if got := Classify(agent); got != "" {
			t.Errorf("человек принят за робота %q:\n  %s", got, agent)
		}
	}
}

// Прежние 14 узнаются, как раньше, и под теми же именами (Р19).
func TestLegacyRobotsKeepTheirNames(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)":                                                                           "Googlebot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Googlebot/2.1; +http://www.google.com/bot.html) Chrome/131.0.6778.108 Safari/537.36": "Googlebot",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)":                                                                            "Bingbot",
		"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)":                                                                                   "YandexBot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; OAI-SearchBot/1.0; +https://openai.com/searchbot":                                   "OAI-SearchBot",
		"Mozilla/5.0 (compatible; Claude-SearchBot/1.0; +https://www.anthropic.com)":                                                                         "Claude-SearchBot",
		"Mozilla/5.0 (compatible; PerplexityBot/1.0; +https://perplexity.ai/perplexitybot)":                                                                  "PerplexityBot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)":                                             "GPTBot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)":                                            "ClaudeBot",
		"CCBot/2.0 (https://commoncrawl.org/faq/)":                                                                                                           "CCBot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot":                                          "ChatGPT-User",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-User/1.0; +Claude-User@anthropic.com)":                                        "Claude-User",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Perplexity-User/1.0; +https://perplexity.ai/perplexity-user)":                        "Perplexity-User",
		// Семейство (Р20, Р60): до трека подстрока складывала их в Googlebot.
		"Googlebot-Image/1.0": "Googlebot",
		"Googlebot-Video/1.0": "Googlebot",
		"Googlebot-News":      "Googlebot",
	}
	for agent, want := range cases {
		if got := Classify(agent); got != want {
			t.Errorf("%q → %q, ждали %q", agent, got, want)
		}
	}
	// Правила только для robots.txt в логах не ищутся.
	if got := Classify("Mozilla/5.0 (compatible; Google-Extended)"); got == "Google-Extended" {
		t.Error("Google-Extended найден в логе")
	}
}

func TestClassifyNewRolesAndPriority(t *testing.T) {
	cases := []struct {
		agent, name, role string
	}{
		// Наши обходы (Р22, Р58) — «мониторинг vpsFocus», а не люди.
		{"vpsfocus-seo/0.31.0 (+https://vpsfocus.xyz/server-code)", "vpsFocus", Self},
		{"vpsfocus-links/0.31.0", "vpsFocus", Self},
		{"vpsfocus-uptime/1 (+https://vpsfocus.xyz/server-code)", "vpsFocus", Self},
		// Общие имена — скрипты (П5): раньше они считались людьми.
		{"curl/8.5.0", "curl", Script},
		{"python-requests/2.32.3", "", Script},
		// Конкретное имя побеждает общее: робот, написанный на Python, —
		// робот, а не «скрипт».
		{"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/) python-requests/2.31", "Ahrefs", SEOTool},
		{"TelegramBot (like TwitterBot)", "", LinkPreview},
		// Чужой Gatus — мониторинг; наш пишет `vpsfocus-uptime` (Р58).
		{"Gatus/1.0", "", Monitoring},
		// Робот с хвостом через дефис, которого нет в списке, — тот же робот.
		{"Mozilla/5.0 (compatible; SemrushBot-ZZ/1.0; +http://www.semrush.com/bot.html)", "", SEOTool},
		// Регулярка PostHog: запросы самого приложения Claude.
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Claude/0.9.3 Chrome/128.0.6613.186 Electron/32.2.5 Safari/537.36", "", AIUser},
	}
	for _, tc := range cases {
		got := Classify(tc.agent)
		if got == "" || got == Other {
			t.Errorf("%q не узнан (%q)", tc.agent, got)
			continue
		}
		if tc.name != "" && got != tc.name {
			t.Errorf("%q → %q, ждали %q", tc.agent, got, tc.name)
		}
		if role, _ := KindOf(got); role != tc.role {
			t.Errorf("%q → %s с ролью %q, ждали %q", tc.agent, got, role, tc.role)
		}
	}
}

func TestLegacyHelper(t *testing.T) {
	if !Legacy("GPTBot") || Legacy("Ahrefs") || Legacy("nobody") {
		t.Fatal("Legacy путает прежних с новыми")
	}
}

// Замер Р59: прежний Classify на этом наборе — 650 нс на строку;
// новый — не медленнее 1300 нс (`go test -run XXX -bench Classify
// -benchmem ./internal/bots/`).
var benchAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 12; Cubot Note 20) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)",
	"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
	"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
	"curl/8.5.0",
}

func BenchmarkClassify(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, agent := range benchAgents {
			Classify(agent)
		}
	}
}
