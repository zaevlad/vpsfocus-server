package logs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseAccessCombined(t *testing.T) {
	line := `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET /catalog/128374?sort=price HTTP/1.1" 502 1234 "https://example.com/" "Mozilla/5.0"`

	event, ok := ParseAccess(line)
	if !ok {
		t.Fatal("строка комбинированного формата не разобрана")
	}
	if event.Status != 502 || event.Method != "GET" {
		t.Errorf("код и метод: %+v", event)
	}
	// Параметры отброшены, номер товара свёрнут: иначе у каждого товара
	// своя строка в списке групп, и список читать невозможно.
	if event.Path != "/catalog/{n}" {
		t.Errorf("путь: %q", event.Path)
	}
	if event.At.Format(time.RFC3339) != "2026-09-03T12:00:05Z" {
		t.Errorf("время: %s", event.At.Format(time.RFC3339))
	}
	// Адрес посетителя не сохраняется нигде: агрегату он не нужен, а
	// хранить чужие персональные данные ради красивого примера мы не станем.
	if strings.Contains(event.Sample, "203.0.113.9") || event.Host == "203.0.113.9" {
		t.Errorf("адрес посетителя попал в событие: %+v", event)
	}
}

func TestParseAccessVhost(t *testing.T) {
	line := `shop.example.com:443 203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "POST /checkout HTTP/1.1" 500 0 "-" "-"`

	event, ok := ParseAccess(line)
	if !ok {
		t.Fatal("формат с виртуальным хостом не разобран")
	}
	if event.Host != "shop.example.com" {
		t.Errorf("домен: %q", event.Host)
	}
	if event.Method != "POST" || event.Path != "/checkout" || event.Status != 500 {
		t.Errorf("событие: %+v", event)
	}
}

// Чужой формат — не повод считать это ошибкой: просто пропускаем строку.
func TestParseAccessRejectsForeignFormats(t *testing.T) {
	for _, line := range []string{
		"",
		"просто строка без всего",
		`{"level":"info","msg":"json-лог caddy"}`,
		`203.0.113.9 - - [не время] "GET / HTTP/1.1" 500 0`,
	} {
		if _, ok := ParseAccess(line); ok {
			t.Errorf("строка не должна была разобраться: %q", line)
		}
	}
}

func TestParseNginxError(t *testing.T) {
	line := `2026/09/03 12:00:05 [error] 1234#1234: *56 FastCGI sent in stderr: "PHP message: Uncaught Error" ` +
		`while reading response header from upstream, client: 203.0.113.9, server: example.com, ` +
		`request: "GET /cart/98765 HTTP/1.1", upstream: "fastcgi://unix:/run/php.sock", host: "shop.example.com"`

	event, ok := ParseError(line)
	if !ok {
		t.Fatal("строка error-лога nginx не разобрана")
	}
	if event.Level != "error" || event.Kind != KindError {
		t.Errorf("уровень: %+v", event)
	}
	if event.Host != "shop.example.com" || event.Path != "/cart/{n}" {
		t.Errorf("домен и путь: %+v", event)
	}
	if strings.Contains(event.Sample, "203.0.113.9") {
		t.Errorf("адрес посетителя остался в примере: %q", event.Sample)
	}
	// Номера процесса и соединения в подпись не входят: с ними одна и та же
	// беда рассыпается на сотни групп.
	if strings.Contains(event.Signature, "1234") || strings.Contains(event.Signature, "56") {
		t.Errorf("подпись содержит номера: %q", event.Signature)
	}
}

// warn и notice в nginx — будни работающего сайта. Собирать их значит
// утопить настоящую беду в шуме.
func TestParseErrorSkipsNoise(t *testing.T) {
	for _, line := range []string{
		`2026/09/03 12:00:05 [warn] 1#1: конец соединения`,
		`2026/09/03 12:00:05 [notice] 1#1: сигнал получен`,
		`[Wed Sep 03 12:00:00.123456 2026] [php:notice] [pid 1] сообщение`,
	} {
		if _, ok := ParseError(line); ok {
			t.Errorf("строка не должна была разобраться: %q", line)
		}
	}
}

func TestParseApacheError(t *testing.T) {
	line := `[Wed Sep 03 12:00:00.123456 2026] [php:error] [pid 1234] [client 203.0.113.9:52341] ` +
		`PHP Fatal error: Allowed memory size of 134217728 bytes exhausted`

	event, ok := ParseError(line)
	if !ok {
		t.Fatal("строка error-лога Apache не разобрана")
	}
	if event.Level != "error" {
		t.Errorf("уровень: %q", event.Level)
	}
	if strings.Contains(event.Sample, "203.0.113.9") {
		t.Errorf("адрес посетителя остался в примере: %q", event.Sample)
	}
	if !strings.Contains(event.Sample, "PHP Fatal error") {
		t.Errorf("сообщение потерялось: %q", event.Sample)
	}
	// Числа в подписи заменены: иначе каждое значение памяти давало бы свою
	// группу, и «одна и та же ошибка» превращалась бы в сотню разных.
	if strings.Contains(event.Signature, "134217728") {
		t.Errorf("подпись содержит числа: %q", event.Signature)
	}
}

// Одна и та же беда обязана попадать в одну строку экрана, даже когда
// числа в сообщении каждый раз новые.
func TestSignatureGroupsSameTrouble(t *testing.T) {
	first := Signature("PHP Fatal error: memory of 134217728 bytes exhausted in /var/www/app.php on line 42")
	second := Signature("PHP Fatal error: memory of 268435456 bytes exhausted in /var/www/app.php on line 91")

	if first != second {
		t.Errorf("подписи разошлись:\n%q\n%q", first, second)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"/":                      "/",
		"":                       "/",
		"/catalog?utm_source=ya": "/catalog",
		"/product/12345":         "/product/{n}",
		"/order/9c858901-8a57-4791-81fe-4c455b099bc9": "/order/{id}",
		"https://example.com/blog/7":                  "/blog/{n}",
	}

	for raw, want := range cases {
		if got := NormalizePath(raw); got != want {
			t.Errorf("%q: получили %q, ждали %q", raw, got, want)
		}
	}
}

// Агенту смонтирован весь корень сервера. Строка в конфиге не должна
// превращаться в «покажи мне содержимое любого файла».
func TestCheckPathRefusesEverythingButLogs(t *testing.T) {
	allowed := []string{
		"/var/log/nginx/access.log",
		"/home/site/logs/error_log",
		"/opt/panel/logs/site-access.log",
	}
	refused := []string{
		"/etc/shadow",
		"/root/.ssh/id_rsa",
		"var/log/nginx/access.log",
		"/var/log/../../etc/passwd",
		"/var/log/nginx/",
		"/var/log/nginx/access.log\nSMTP_TO=чужой@example.com",
		// Кавычки и подстановки: тот же путь уезжает в shell-команду выдачи
		// доступа, и отбор здесь обязан быть не слабее, чем в приложении.
		`/var/log/nginx/access.log; rm -rf /`,
		`/var/log/"nginx"/access.log`,
		`/var/log/$(whoami)/access.log`,
		"/var/log/`id`/access.log",
		`/var/log/nginx/access.log & cat /etc/shadow`,
		`/var/log/nginx/acc|ess.log`,
		`/var/log/nginx/acc\ess.log`,
		"/var/log/nginx/access\vlog",
		"/var/log/nginx/" + strings.Repeat("x", 300) + ".log",
	}

	for _, path := range allowed {
		if !CheckPath(path) {
			t.Errorf("путь должен был пройти: %q", path)
		}
	}
	for _, path := range refused {
		if CheckPath(path) {
			t.Errorf("путь не должен был пройти: %q", path)
		}
	}
}

func TestDiscoverSkipsRotatedFiles(t *testing.T) {
	files := map[string][]string{
		filepath.Join("root", "/var/log/nginx"): {
			"access.log", "error.log", "access.log.1", "access.log.2.gz", "old.log.gz", "README",
		},
	}

	found := Discover("root", func(dir string) ([]string, error) {
		names, ok := files[dir]
		if !ok {
			return nil, os.ErrNotExist
		}
		return names, nil
	})

	if len(found) != 2 {
		t.Fatalf("нашли не то: %+v", found)
	}
	if found[0].Path != "/var/log/nginx/access.log" || found[0].Kind != KindAccess {
		t.Errorf("первый источник: %+v", found[0])
	}
	if found[1].Path != "/var/log/nginx/error.log" || found[1].Kind != KindError {
		t.Errorf("второй источник: %+v", found[1])
	}
}

// ── Дочитывание файла ────────────────────────────────────────────────────

func accessLine(at string, status int, path string) string {
	return `203.0.113.9 - - [` + at + ` +0000] "GET ` + path + ` HTTP/1.1" ` +
		string(rune('0'+status/100)) + string(rune('0'+status/10%10)) + string(rune('0'+status%10)) + ` 100`
}

func TestReadContinuesFromPosition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	first := accessLine("03/Sep/2026:12:00:05", 500, "/a") + "\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}

	var seen []Event
	position, failure := Read(path, Position{}, 0, func(event Event) { seen = append(seen, event) })
	if failure != "" {
		t.Fatalf("чтение не удалось: %s", failure)
	}
	if len(seen) != 1 {
		t.Fatalf("событий: %d", len(seen))
	}

	// Дописали строку — второй проход обязан прочитать только её.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(accessLine("03/Sep/2026:12:01:05", 503, "/b") + "\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()

	seen = nil
	position, failure = Read(path, position, 0, func(event Event) { seen = append(seen, event) })
	if failure != "" {
		t.Fatalf("второе чтение не удалось: %s", failure)
	}
	if len(seen) != 1 || seen[0].Status != 503 {
		t.Fatalf("второй проход прочитал не то: %+v", seen)
	}
	if position.Offset == 0 {
		t.Error("позиция не сдвинулась")
	}
}

// Лог повернули — под тем же именем лежит другой файл, и читать его надо с
// начала, а не с прошлого смещения.
func TestReadStartsOverAfterRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	long := accessLine("03/Sep/2026:12:00:05", 500, "/старая-длинная-строка-которая-была-до-поворота") + "\n"
	if err := os.WriteFile(path, []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}

	position, _ := Read(path, Position{}, 0, func(Event) {})

	// Файл заменили коротким — это и есть поворот с точки зрения читателя.
	short := accessLine("03/Sep/2026:13:00:05", 502, "/после") + "\n"
	if err := os.WriteFile(path, []byte(short), 0o600); err != nil {
		t.Fatal(err)
	}

	var seen []Event
	if _, failure := Read(path, position, 0, func(event Event) { seen = append(seen, event) }); failure != "" {
		t.Fatalf("чтение после поворота: %s", failure)
	}
	if len(seen) != 1 || seen[0].Status != 502 {
		t.Fatalf("после поворота прочитали не то: %+v", seen)
	}
}

func TestReadReportsMissingFile(t *testing.T) {
	_, failure := Read(filepath.Join(t.TempDir(), "нет.log"), Position{}, 0, func(Event) {})
	if failure != ErrNotFound {
		t.Errorf("ждали %s, получили %q", ErrNotFound, failure)
	}
}

// Успешные ответы в базу не попадают: счётчики «сайт работает» — это
// гигабайты строк, о которых и так есть кому рассказать. Остаются ошибки
// сервера и «страницы нет» — обе про то, что посетитель не получил, за чем
// пришёл.
func TestBatchKeepsOnlyErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	body := accessLine("03/Sep/2026:12:00:04", 200, "/ok") + "\n" +
		accessLine("03/Sep/2026:12:00:05", 301, "/переехало") + "\n" +
		accessLine("03/Sep/2026:12:00:06", 404, "/нет") + "\n" +
		accessLine("03/Sep/2026:12:00:07", 500, "/беда") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// Читается всё — роботов считают и по ответам 200, — а в группы ошибок
	// попадают только ошибки.
	var seen []Event
	batch := NewBatch(0)
	Read(path, Position{}, 0, func(event Event) {
		seen = append(seen, event)
		batch.AddInteresting(event)
	})

	if len(seen) != 4 {
		t.Fatalf("прочитано строк %d, ждали все четыре", len(seen))
	}
	statuses := map[int]bool{}
	for _, group := range batch.Groups() {
		statuses[group.Status] = true
	}
	if len(statuses) != 2 || !statuses[404] || !statuses[500] {
		t.Fatalf("в группы попало не то: %v", statuses)
	}
}

// Из строки берётся только имя робота из закрытого списка; сама строка
// User-agent и адрес посетителя в событие не попадают.
func TestParseAccessNamesTheRobot(t *testing.T) {
	line := `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET /blog HTTP/1.1" 200 512 "https://chatgpt.com/" "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot"`
	event, ok := ParseAccess(line)
	if !ok || !event.HasAgent || event.Robot != "GPTBot" {
		t.Fatalf("робот не узнан: %+v", event)
	}
	if strings.Contains(event.Sample+event.Path+event.Host, "Mozilla") {
		t.Fatalf("строка User-agent попала в событие: %+v", event)
	}

	// Формат без User-agent — «не знаем», а не «человек».
	short := `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512`
	event, ok = ParseAccess(short)
	if !ok || event.HasAgent || event.Robot != "" {
		t.Fatalf("строка без User-agent: %+v", event)
	}

	// Кавычка внутри поля не сдвигает разбор.
	escaped := `203.0.113.9 - - [03/Sep/2026:12:00:05 +0000] "GET / HTTP/1.1" 200 512 "-" "Mozilla/5.0 \"x\" (compatible; ClaudeBot/1.0)"`
	if event, _ := ParseAccess(escaped); event.Robot != "ClaudeBot" {
		t.Fatalf("экранированная кавычка сбила разбор: %+v", event)
	}
}

// Счётчики роботов: по суткам и сайту, со служебными строками и без
// строки User-agent в хранимом.
func TestRobotsCountByDayHostAndName(t *testing.T) {
	robots := NewRobots()
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)
	add := func(host, robot, path string, status int, agent bool) {
		robots.Add(Event{Kind: KindAccess, At: at, Host: host, Robot: robot, Path: path, Status: status, HasAgent: agent})
	}
	add("shop.example", "GPTBot", "/", 200, true)
	add("shop.example", "GPTBot", "/about", 200, true)
	add("shop.example", "", "/.env", 404, true)
	add("shop.example", "", "/.env", 200, true)
	add("", "", "/", 200, false)
	robots.Add(Event{Kind: KindError, At: at})

	got := map[string]int{}
	for _, count := range robots.Counts() {
		got[count.Host+"|"+count.Name] = count.Count
		if count.Day != time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC).Unix() {
			t.Fatalf("сутки: %d", count.Day)
		}
	}
	want := map[string]int{
		"shop.example|GPTBot":   2,
		"shop.example|@lines":   4,
		"shop.example|@scan":    2,
		"shop.example|@exposed": 1,
		"|@noagent":             1,
	}
	for key, count := range want {
		if got[key] != count {
			t.Errorf("%s: %d, ждали %d (всё: %v)", key, got[key], count, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("лишние счётчики: %v", got)
	}
}

// Перебор ботами не имеет права вытеснить настоящие пятисотки: у «страницы
// нет» свой потолок групп, и упирается в него только она.
func TestNotFoundDoesNotCrowdOutServerErrors(t *testing.T) {
	batch := NewBatch(0)
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)

	for i := 0; i < DefaultNotFoundLimit*3; i++ {
		batch.Add(Event{
			Kind: KindAccess, At: at, Status: StatusNotFound,
			Method: "GET", Path: "/бот/" + strconv.Itoa(i),
		})
	}
	batch.Add(Event{
		Kind: KindAccess, At: at, Status: 500,
		Method: "POST", Path: "/checkout",
	})

	var checkout, overflow bool
	for _, group := range batch.Groups() {
		if group.Status == 500 && group.Path == "/checkout" {
			checkout = true
		}
		if group.Status == StatusNotFound && group.Path == OverflowPath {
			overflow = true
		}
	}
	if !checkout {
		t.Error("пятисотка потерялась под перебором четыреста четвёртых")
	}
	if !overflow {
		t.Error("лишние четыреста четвёртые не ушли в группу «прочее»")
	}
	if batch.Total() != DefaultNotFoundLimit*3+1 {
		t.Errorf("потеряли события: всего %d", batch.Total())
	}
}

// ── Счётчики прохода ─────────────────────────────────────────────────────

func TestBatchCountsRepeats(t *testing.T) {
	batch := NewBatch(0)
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)

	for i := 0; i < 3; i++ {
		batch.Add(Event{Kind: KindAccess, At: at.Add(time.Duration(i) * time.Second),
			Status: 500, Method: "GET", Path: "/checkout"})
	}
	batch.Add(Event{Kind: KindAccess, At: at, Status: 503, Method: "GET", Path: "/checkout"})

	groups := batch.Groups()
	if len(groups) != 2 {
		t.Fatalf("групп: %d (%+v)", len(groups), groups)
	}
	if batch.Total() != 4 {
		t.Errorf("всего событий: %d", batch.Total())
	}
}

// У сайта под ботами уникальных путей десятки тысяч. Врать про количество
// нельзя, но и держать их все в памяти чужого сервера — тоже.
func TestBatchFoldsOverflowIntoOneGroup(t *testing.T) {
	batch := NewBatch(2)
	at := time.Date(2026, 9, 3, 12, 0, 5, 0, time.UTC)

	for i := 0; i < 50; i++ {
		batch.Add(Event{Kind: KindAccess, At: at, Status: 500,
			Method: "GET", Path: "/уникальный-" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}

	groups := batch.Groups()
	if len(groups) != 3 {
		t.Fatalf("групп должно быть три (две и «прочее»): %d", len(groups))
	}
	if batch.Total() != 50 {
		t.Errorf("счёт потерялся: %d вместо 50", batch.Total())
	}

	var overflow int
	for _, group := range groups {
		if group.Path == OverflowPath {
			overflow = group.Count
		}
	}
	if overflow != 48 {
		t.Errorf("в «прочее» попало %d вместо 48", overflow)
	}
}

// Окно тревоги выравнивается по бакетам, и названо будет то, что сосчитано.
//
// В базе лежат счётчики за пятиминутный бакет, и «только последние минуты»
// взять из него нечем. До спринта 36 отбор шёл по времени последнего события
// в бакете: бакет, начавшийся до окна, попадал в счёт целиком, счёт
// раздувался на треть, а письмо называло его точным числом за точные
// пятнадцать минут.
func TestWindowIsAlignedToBuckets(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 7, 43, 0, time.UTC)

	start := WindowStart(now, 15*time.Minute)
	if start.Unix()%BucketSeconds != 0 {
		t.Fatalf("начало окна %s не на границе бакета", start)
	}
	if !start.Before(now.Add(-15*time.Minute)) && !start.Equal(now.Add(-15*time.Minute)) {
		t.Fatalf("окно короче запрошенного: %s при пятнадцати минутах до %s", start, now)
	}

	// Названо будет ровно то, что сосчитано: не меньше запрошенного окна и
	// не больше, чем на один бакет.
	minutes := WindowMinutes(now, 15*time.Minute)
	if minutes < 15 || minutes > 15+BucketSeconds/60 {
		t.Fatalf("окно названо как %d минут при запрошенных пятнадцати", minutes)
	}
	if got := int(now.Sub(start) / time.Minute); got != minutes {
		t.Errorf("названо %d минут, а сосчитано %d", minutes, got)
	}
}
