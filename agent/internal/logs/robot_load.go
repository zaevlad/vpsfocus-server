package logs

import (
	"strings"
	"sync"

	"agent/internal/bots"
	"agent/internal/robots"
)

// Доля нагрузки от роботов и соблюдение robots.txt (идея 7 трека AB,
// Р22–Р24, Р31).
//
// Как и у остальных счётчиков роботов, хранятся только числа «сутки, сайт,
// вид» — ни строк лога, ни адресов посетителей. Подделку имени не ловим и
// не обещаем: «назвался GPTBot» — всё, что видно по логу.

// Виды в счётчике нагрузки: роли из `bots`, «прочие роботы» и «все запросы».
const (
	TrafficAll   = "all"
	TrafficOther = bots.Other
)

// SiteTraffic — запросы и байты одного вида за сутки на сайте.
type SiteTraffic struct {
	Day  int64
	Host string
	// TrafficAll, роль робота из bots или TrafficOther.
	Kind     string
	Requests int64
	// Байты — только по строкам, где формат пишет размер; Sized — сколько
	// таких строк. Ноль Sized — доля байтов «не знаем», а не ноль.
	Bytes int64
	Sized int64
}

// RobotViolation — сколько раз за сутки робот ходил туда, куда ему запрещено
// robots.txt сайта, и пример адреса — без строки параметров (в адресах
// бывают секреты) и с номерами, заменёнными на {n}, как на экране ошибок.
type RobotViolation struct {
	Day     int64
	Host    string
	Name    string
	Count   int
	Example string
}

// Policy отвечает, закрыт ли адрес роботу правилами robots.txt сайта.
// known=false — правил сайта мы не знаем: обхода SEO не было, или домен в
// логе не назван (Р24).
type Policy interface {
	Disallowed(host, robot, target string) (known, disallowed bool)
}

// Обязан ли робот слушать robots.txt (Р23): роботы поиска и обучения —
// да; робот по просьбе человека (ChatGPT-User и др.) — по правилам своих
// компаний нет, и «нарушителем» его называть нельзя.
func bound(name string) bool {
	kind, ok := botKind(name)
	return ok && kind != bots.AIUser
}

// trafficKind — вид строки для счётчика нагрузки; пусто — человек.
func trafficKind(robot string) string {
	if robot == "" {
		return ""
	}
	if robot == bots.Other {
		return TrafficOther
	}
	if kind, ok := botKind(robot); ok {
		return kind
	}
	return TrafficOther
}

type trafficKey struct {
	day  int64
	host string
	kind string
}

type violationKey struct {
	day  int64
	host string
	name string
}

func (r *Robots) addTraffic(event Event, kind string) {
	key := trafficKey{dayOf(event.At), event.Host, kind}
	item, ok := r.traffic[key]
	if !ok {
		item = &SiteTraffic{Day: key.day, Host: key.host, Kind: kind}
		r.traffic[key] = item
	}
	item.Requests++
	if event.HasBytes {
		item.Bytes += event.Bytes
		item.Sized++
	}
}

func (r *Robots) checkRules(event Event) {
	if r.policy == nil || event.Host == "" || !bound(event.Robot) {
		return
	}
	known, disallowed := r.policy.Disallowed(event.Host, event.Robot, event.Target)
	if !known || !disallowed {
		return
	}
	key := violationKey{dayOf(event.At), event.Host, event.Robot}
	if item, ok := r.violations[key]; ok {
		item.Count++
		return
	}
	r.violations[key] = &RobotViolation{Day: key.day, Host: key.host, Name: key.name,
		Count: 1, Example: truncate(event.Path, 200)}
}

// Traffic — счётчики нагрузки за проход, для таблицы site_traffic.
func (r *Robots) Traffic() []SiteTraffic {
	out := make([]SiteTraffic, 0, len(r.traffic))
	for _, item := range r.traffic {
		out = append(out, *item)
	}
	return out
}

// Violations — заходы роботов в закрытое за проход, для robot_violations.
func (r *Robots) Violations() []RobotViolation {
	out := make([]RobotViolation, 0, len(r.violations))
	for _, item := range r.violations {
		out = append(out, *item)
	}
	return out
}

// RobotsFiles — правила robots.txt сайтов из последних обходов SEO (Р31):
// домен → текст файла. Пустой текст — файла на сайте нет, и закрыто
// ничего не было.
//
// Разбор на каждую строку лога дорог, поэтому правила разбираются один
// раз на пару «сайт, робот» и помнятся до конца прохода.
type RobotsFiles struct {
	bodies map[string][]byte
	mu     sync.Mutex
	parsed map[[2]string]*robots.Rules
}

func NewRobotsFiles(bodies map[string][]byte) *RobotsFiles {
	return &RobotsFiles{bodies: bodies, parsed: map[[2]string]*robots.Rules{}}
}

// body ищет файл сайта: домен как в логе, а если нет — без «www.» или с
// ним. Обход SEO ходит на домен из списка сайтов, а nginx пишет тот хост,
// что прислал браузер.
func (f *RobotsFiles) body(host string) ([]byte, bool) {
	domain, ok := RobotsDomain(host, func(name string) bool {
		_, found := f.bodies[name]
		return found
	})
	if !ok {
		return nil, false
	}
	return f.bodies[domain], true
}

// RobotsDomain — под каким доменом лежит robots.txt хоста из лога: как
// есть, без «www.» или с ним.
func RobotsDomain(host string, has func(string) bool) (string, bool) {
	host = strings.ToLower(host)
	candidates := []string{host, "www." + host}
	if bare, found := strings.CutPrefix(host, "www."); found {
		candidates = []string{host, bare}
	}
	for _, name := range candidates {
		if has(name) {
			return name, true
		}
	}
	return "", false
}

func (f *RobotsFiles) Disallowed(host, robot, target string) (bool, bool) {
	if f == nil {
		return false, false
	}
	body, ok := f.body(host)
	if !ok {
		return false, false
	}
	if len(body) == 0 {
		// Файла нет — открыто всё и всем.
		return true, false
	}

	key := [2]string{strings.ToLower(host), robot}
	f.mu.Lock()
	rules, ok := f.parsed[key]
	if !ok {
		rules = robots.Parse(body, robot)
		f.parsed[key] = rules
	}
	f.mu.Unlock()

	return true, !rules.Allowed(requestPath(target))
}

// requestPath — путь со строкой параметров, как его сверяет поисковик:
// без схемы и хоста (они бывают у прокси и ботов) и без якоря.
func requestPath(target string) string {
	if scheme := strings.Index(target, "://"); scheme >= 0 {
		rest := target[scheme+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			target = rest[slash:]
		} else {
			target = "/"
		}
	}
	if cut := strings.IndexByte(target, '#'); cut >= 0 {
		target = target[:cut]
	}
	if target == "" || target[0] != '/' {
		return "/"
	}
	return target
}
