package bots

import "testing"

func TestClassifyNamesKnownRobots(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)":                                          "GPTBot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)":   "ClaudeBot",
		"Mozilla/5.0 (compatible; Claude-SearchBot/1.0; +https://www.anthropic.com)":                                "Claude-SearchBot",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot": "ChatGPT-User",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)":                                  "Googlebot",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)":                                   "Bingbot",
		"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)":                                        Other,
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126.0 Safari/537.36":                   "",
		"Mozilla/5.0 (Linux; Android 12; Cubot Note 20) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36":         "",
		"Mozilla/5.0 (Linux; Android 10; CUBOT_X30 Build/QP1A) Chrome/120":                                          "",
		"Mozilla/5.0 (compatible; MJ12bot/v1.4.8; http://mj12bot.com/)":                                             Other,
		"Mozilla/5.0 (compatible; SeznamBot/4.0; +https://o-seznam.cz/napoveda/vyhledavani/en/seznambot-crawler/)":  Other,
		"-": "",
		"":  "",
	}
	for agent, want := range cases {
		if got := Classify(agent); got != want {
			t.Errorf("%q → %q, ждали %q", agent, got, want)
		}
	}
}

// Правила robots.txt, под которыми не ходит ни один робот, в логах не
// ищутся: «пока не приходил» про них было бы неправдой навсегда.
func TestRobotsOnlyNamesNeverMatchLogs(t *testing.T) {
	if got := Classify("Mozilla/5.0 (compatible; Google-Extended)"); got == "Google-Extended" {
		t.Fatal("Google-Extended найден в логе")
	}
}

// Роботов «по вопросу человека» в проверке robots.txt нет: их владельцы
// пишут, что robots.txt к ним может не применяться.
func TestUserTriggeredRobotsAreNotAskedAboutRobotsTxt(t *testing.T) {
	for _, bot := range ForRobots() {
		if bot.Kind == AIUser {
			t.Fatalf("%s попал в проверку robots.txt", bot.Name)
		}
	}
	if len(ForRobots()) != 11 {
		t.Fatalf("в проверке robots.txt %d роботов, ждали прежние 11", len(ForRobots()))
	}
}

func TestScannerAndSecretPaths(t *testing.T) {
	for _, path := range []string{"/.env", "/.git/config", "/wp-login.php", "/phpmyadmin/index.php", "/.ENV"} {
		if !Scanner(path) {
			t.Errorf("%s не узнан как перебор", path)
		}
	}
	for _, path := range []string{"/", "/blog/.environment-tips", "/wp-content/x.png", "/envelope"} {
		if Scanner(path) {
			t.Errorf("%s принят за перебор", path)
		}
	}
	if !Secret("/.env") || !Secret("/.git/HEAD") || Secret("/wp-login.php") {
		t.Error("секретные адреса узнаются неверно")
	}
}
