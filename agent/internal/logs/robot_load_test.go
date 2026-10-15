package logs

import "testing"

func access(t *testing.T, line string) Event {
	t.Helper()
	event, ok := ParseAccess(line)
	if !ok {
		t.Fatalf("строка не разобрана: %s", line)
	}
	return event
}

// Размер ответа — поле за кодом; «-» у Apache — ноль; формат без размера —
// «не знаем», а не ноль.
func TestParseAccessSize(t *testing.T) {
	sized := access(t, `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET /a?x=1 HTTP/1.1" 200 5120 "-" "Mozilla/5.0"`)
	if !sized.HasBytes || sized.Bytes != 5120 || sized.Target != "/a?x=1" {
		t.Fatalf("с размером: %+v", sized)
	}
	dash := access(t, `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 304 - "-" "Mozilla/5.0"`)
	if !dash.HasBytes || dash.Bytes != 0 {
		t.Fatalf("прочерк: %+v", dash)
	}
	none := access(t, `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 "-" "Mozilla/5.0"`)
	if none.HasBytes {
		t.Fatalf("без размера: %+v", none)
	}
}

// Поле размера и строка параметров не дробят группы ошибок: прежние итоги
// экранов ошибок не меняются.
func TestSizeAndQueryDoNotSplitGroups(t *testing.T) {
	batch := NewBatch(0)
	batch.AddInteresting(access(t, `1.2.3.4 - - [03/Sep/2026:12:00:05 +0000] "GET /p/1?a=1 HTTP/1.1" 502 100 "-" "-"`))
	batch.AddInteresting(access(t, `1.2.3.4 - - [03/Sep/2026:12:00:06 +0000] "GET /p/2?b=2 HTTP/1.1" 502 900 "-" "-"`))
	groups := batch.Groups()
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("группы: %+v", groups)
	}
}

const shopRobots = `User-agent: *
Disallow: /admin/
Allow: /admin/public

User-agent: GPTBot
Disallow: /
Allow: /blog/
Disallow: /*.pdf$
Disallow: /*?sort=
`

func TestRobotsFilesDecide(t *testing.T) {
	files := NewRobotsFiles(map[string][]byte{
		"shop.example": []byte(shopRobots),
		"open.example": {},
	})
	cases := []struct {
		host, robot, target string
		known, closed       bool
	}{
		// Группа по имени вытесняет звёздочку целиком.
		{"shop.example", "GPTBot", "/catalog", true, true},
		{"shop.example", "GPTBot", "/blog/post", true, false},
		// Длиннейшее совпадение: Disallow /*.pdf$ длиннее Allow /blog/.
		{"shop.example", "GPTBot", "/blog/a.pdf", true, true},
		{"shop.example", "GPTBot", "/blog/a.pdf?x", true, false},
		// Правило на параметры сверяется с адресом как пришёл.
		{"shop.example", "GPTBot", "/blog/?sort=price", true, true},
		// Allow поверх Disallow при более длинном правиле.
		{"shop.example", "Googlebot", "/admin/public", true, false},
		{"shop.example", "Googlebot", "/admin/users", true, true},
		{"shop.example", "Googlebot", "/catalog", true, false},
		// Абсолютный адрес в строке запроса.
		{"shop.example", "Googlebot", "http://shop.example/admin/x", true, true},
		// С «www.» — тот же файл.
		{"www.shop.example", "Googlebot", "/admin/x", true, true},
		// Файла на сайте нет — открыто всё.
		{"open.example", "GPTBot", "/anything", true, false},
		// Правил не знаем.
		{"other.example", "GPTBot", "/", false, false},
	}
	for _, c := range cases {
		known, closed := files.Disallowed(c.host, c.robot, c.target)
		if known != c.known || closed != c.closed {
			t.Errorf("%s %s %s: known=%v closed=%v, ждали %v %v",
				c.host, c.robot, c.target, known, closed, c.known, c.closed)
		}
	}
}

func vhostLine(host, target, agent, size string) string {
	return host + ` 1.2.3.4 - - [03/Sep/2026:12:00:05 +0000] "GET ` + target + ` HTTP/1.1" 200 ` + size + ` "-" "` + agent + `"`
}

func TestRobotsCountTrafficAndTrespass(t *testing.T) {
	files := NewRobotsFiles(map[string][]byte{"shop.example": []byte(shopRobots)})
	robots := NewRobots().UseRules(files)

	gpt := "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)"
	user := "Mozilla/5.0 (compatible; ChatGPT-User/1.0; +https://openai.com/bot)"
	for _, l := range []string{
		vhostLine("shop.example", "/catalog/123?token=secret", gpt, "3000"),
		vhostLine("shop.example", "/catalog/7", gpt, "1000"),
		vhostLine("shop.example", "/blog/x", gpt, "500"),
		// По просьбе человека — не нарушитель (Р23), но в нагрузке есть.
		vhostLine("shop.example", "/catalog", user, "200"),
		vhostLine("shop.example", "/", "Mozilla/5.0 (Windows NT 10.0)", "300"),
		vhostLine("shop.example", "/", "SomeCrawler/1.0 (+http://x)", "-"),
		// Без домена в логе — правил не знаем (Р24).
		`1.2.3.4 - - [03/Sep/2026:12:00:05 +0000] "GET /catalog HTTP/1.1" 200 10 "-" "` + gpt + `"`,
	} {
		robots.Add(access(t, l))
	}

	traffic := map[[2]string]SiteTraffic{}
	for _, item := range robots.Traffic() {
		traffic[[2]string{item.Host, item.Kind}] = item
	}
	if all := traffic[[2]string{"shop.example", TrafficAll}]; all.Requests != 6 || all.Bytes != 5000 || all.Sized != 6 {
		t.Errorf("все запросы: %+v", all)
	}
	if training := traffic[[2]string{"shop.example", "aiTraining"}]; training.Requests != 3 || training.Bytes != 4500 {
		t.Errorf("обучение: %+v", training)
	}
	if other := traffic[[2]string{"shop.example", TrafficOther}]; other.Requests != 1 || other.Bytes != 0 {
		t.Errorf("прочие: %+v", other)
	}
	if aiUser := traffic[[2]string{"shop.example", "aiUser"}]; aiUser.Requests != 1 {
		t.Errorf("по просьбе человека: %+v", aiUser)
	}
	if server := traffic[[2]string{"", TrafficAll}]; server.Requests != 1 {
		t.Errorf("сервер целиком: %+v", server)
	}

	violations := robots.Violations()
	if len(violations) != 1 {
		t.Fatalf("нарушения: %+v", violations)
	}
	v := violations[0]
	if v.Host != "shop.example" || v.Name != "GPTBot" || v.Count != 2 {
		t.Errorf("нарушение: %+v", v)
	}
	// Пример — без параметров и с номерами, свёрнутыми в {n}.
	if v.Example != "/catalog/{n}" {
		t.Errorf("пример: %q", v.Example)
	}
}
