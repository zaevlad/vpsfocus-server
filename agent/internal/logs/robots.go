package logs

import (
	"time"

	"agent/internal/bots"
)

// Счётчики роботов по логам (трек V, экран «Агенты и ИИ», Р20).
//
// Хранятся не строки и не строки User-agent, а число «сутки, сайт, имя» —
// то же правило, что у ошибок: адрес посетителя не сохраняется нигде, а из
// строки User-agent наружу выходит только имя робота из закрытого списка
// (`bots.Classify`). Кроме роботов, в той же таблице — служебные счётчики,
// без которых «роботы не приходили» нельзя отличить от «не знаем».
const (
	// Строки access-лога, в которых поле User-agent есть. Ноль при живом
	// сайте — формат лога его не пишет, и ответ экрана — «не знаем».
	MetaLines = "@lines"
	// Строки без поля User-agent.
	MetaNoAgent = "@noagent"
	// Обращения к адресам, которые перебирают сканеры.
	MetaScan = "@scan"
	// Адрес с секретом (`.env`, `.git`) ответил 200.
	MetaExposed = "@exposed"
	// Строки роботов, которых можно проверить по адресу (трек AI, Р8), и
	// почему из них не проверилось: адрес частный (веб-сервер в Docker),
	// адрес Cloudflare (сайт за прокси), адреса в строке нет. Больше
	// половины — экран называет причину один раз на блок (Р44).
	MetaAddrCheckable = "@addr"
	MetaAddrPrivate   = "@addrPrivate"
	MetaAddrProxy     = "@addrProxy"
	MetaAddrNone      = "@addrNone"
)

// RobotCheck — исходы проверки имени по адресу за сутки (Р7). Адреса нет и
// здесь: только три числа.
type RobotCheck struct {
	Day        int64
	Host       string
	Name       string
	Verified   int
	Unverified int
	Unknown    int
}

// RobotCount — одно число таблицы: сколько раз за сутки на сайте.
type RobotCount struct {
	// Начало суток по UTC, секунды.
	Day int64
	// Домен, если формат лога его пишет; пусто — сервер целиком.
	Host   string
	Name   string
	Count  int
	LastAt time.Time
}

// NamesPerHostDay — потолок имён роботов на сайт в сутки (Р10): в списке
// сотни имён, и всё, что сверх, уходит в «прочих». Считается за проход:
// проход — это минута лога, а за минуту двести разных роботов — уже не
// сайт, а перебор.
const NamesPerHostDay = 200

// Robots сводит строки одного прохода в счётчики.
type Robots struct {
	counts     map[[3]string]*RobotCount
	names      map[[2]string]int
	refusals   map[refusalKey]*RobotRefusal
	traffic    map[trafficKey]*SiteTraffic
	violations map[violationKey]*RobotViolation
	checks     map[[3]string]*RobotCheck
	// Отказы и заходы в закрытое тем, кто назвался роботом с чужого адреса
	// (Р9, Р24, Р25): прежние таблицы пишутся как раньше, а эти вычитаются
	// из них при чтении — так вывод не зависит от того, какой агент писал
	// прежние сутки.
	impostorRefusals   map[refusalKey]*RobotRefusal
	impostorViolations map[violationKey]*RobotViolation
	// Страницы роботов ИИ по просьбе людей (Р11) и сколько разных путей у
	// робота за сутки — для потолка.
	pages     map[pageKey]*RobotPage
	pagePaths map[[3]string]int
	policy    Policy
}

type refusalKey struct {
	day       int64
	host      string
	name      string
	status    int
	robotsTxt bool
}

func NewRobots() *Robots {
	return &Robots{
		counts:     map[[3]string]*RobotCount{},
		names:      map[[2]string]int{},
		refusals:   map[refusalKey]*RobotRefusal{},
		traffic:    map[trafficKey]*SiteTraffic{},
		violations: map[violationKey]*RobotViolation{},
		checks:     map[[3]string]*RobotCheck{},

		impostorRefusals:   map[refusalKey]*RobotRefusal{},
		impostorViolations: map[violationKey]*RobotViolation{},
		pages:              map[pageKey]*RobotPage{},
		pagePaths:          map[[3]string]int{},
	}
}

// UseRules задаёт правила robots.txt сайтов, с которыми сверяются заходы
// роботов. Без них заходы в закрытое не считаются — «не знаем».
func (r *Robots) UseRules(policy Policy) *Robots {
	r.policy = policy
	return r
}

func dayOf(at time.Time) int64 {
	unix := at.Unix()
	return unix - mod(unix, 86_400)
}

func (r *Robots) bump(event Event, name string) {
	day := dayOf(event.At)
	key := [3]string{time.Unix(day, 0).UTC().Format("2006-01-02"), event.Host, name}
	if count, ok := r.counts[key]; ok {
		count.Count++
		if event.At.After(count.LastAt) {
			count.LastAt = event.At
		}
		return
	}
	r.counts[key] = &RobotCount{Day: day, Host: event.Host, Name: name, Count: 1, LastAt: event.At}
}

// Add учитывает строку access-лога; строки error-лога пропускаются.
func (r *Robots) Add(event Event) {
	if event.Kind != KindAccess {
		return
	}
	if event.HasAgent {
		r.bump(event, MetaLines)
		// Нагрузка считается только по строкам, где формат называет
		// посетителя: без поля User-agent долю роботов не узнать.
		r.addTraffic(event, TrafficAll)
		if kind := trafficKind(event.Robot); kind != "" {
			r.addTraffic(event, kind)
		}
	} else {
		r.bump(event, MetaNoAgent)
	}
	if event.Robot != "" {
		event.Robot = r.capped(event)
		r.bump(event, event.Robot)
		outcome := ""
		if event.Robot != bots.Other {
			outcome = r.check(event)
			r.addPage(event, outcome)
		}
		// Отказ роботу из прежних 14 (Р14, Р17). Остальных не считаем: вывода
		// о них экран не делает, а строк в таблице стало бы на порядок больше.
		if bots.Legacy(event.Robot) && Refusal(event.Status) {
			key := refusalKey{dayOf(event.At), event.Host, event.Robot, event.Status, event.Path == "/robots.txt"}
			addRefusal(r.refusals, key)
			if outcome == bots.Unverified {
				addRefusal(r.impostorRefusals, key)
			}
		}
		r.checkRules(event, outcome)
	}
	if bots.Scanner(event.Path) {
		r.bump(event, MetaScan)
	}
	if event.Status == 200 && bots.Secret(event.Path) {
		r.bump(event, MetaExposed)
	}
}

func addRefusal(to map[refusalKey]*RobotRefusal, key refusalKey) {
	if item, ok := to[key]; ok {
		item.Count++
		return
	}
	to[key] = &RobotRefusal{Day: key.day, Host: key.host, Name: key.name,
		Status: key.status, RobotsTxt: key.robotsTxt, Count: 1}
}

// check сверяет имя робота с адресом строки (Р6) и считает исход. Адрес
// после этого больше нигде не нужен.
func (r *Robots) check(event Event) string {
	outcome, reason := bots.Verify(event.Robot, event.Addr)
	if bots.Checkable(event.Robot) {
		r.bump(event, MetaAddrCheckable)
		switch reason {
		case bots.ReasonPrivate:
			r.bump(event, MetaAddrPrivate)
		case bots.ReasonProxy:
			r.bump(event, MetaAddrProxy)
		case bots.ReasonNoAddr:
			r.bump(event, MetaAddrNone)
		}
	}
	day := dayOf(event.At)
	key := [3]string{time.Unix(day, 0).UTC().Format("2006-01-02"), event.Host, event.Robot}
	item, ok := r.checks[key]
	if !ok {
		item = &RobotCheck{Day: day, Host: event.Host, Name: event.Robot}
		r.checks[key] = item
	}
	switch outcome {
	case bots.Verified:
		item.Verified++
	case bots.Unverified:
		item.Unverified++
	default:
		item.Unknown++
	}
	return outcome
}

// Checks — исходы проверки за проход, для таблицы robot_checks.
func (r *Robots) Checks() []RobotCheck {
	out := make([]RobotCheck, 0, len(r.checks))
	for _, item := range r.checks {
		out = append(out, *item)
	}
	return out
}

// ImpostorRefusals — отказы тем, кто назвался роботом с чужого адреса.
func (r *Robots) ImpostorRefusals() []RobotRefusal {
	out := make([]RobotRefusal, 0, len(r.impostorRefusals))
	for _, item := range r.impostorRefusals {
		out = append(out, *item)
	}
	return out
}

// ImpostorViolations — их же заходы в закрытое robots.txt.
func (r *Robots) ImpostorViolations() []RobotViolation {
	out := make([]RobotViolation, 0, len(r.impostorViolations))
	for _, item := range r.impostorViolations {
		out = append(out, *item)
	}
	return out
}

// capped — имя робота или «прочие», если у сайта за сутки уже
// NamesPerHostDay других имён (Р10). Прежние 14 потолка не знают.
func (r *Robots) capped(event Event) string {
	if event.Robot == bots.Other || bots.Legacy(event.Robot) {
		return event.Robot
	}
	day := time.Unix(dayOf(event.At), 0).UTC().Format("2006-01-02")
	if _, seen := r.counts[[3]string{day, event.Host, event.Robot}]; seen {
		return event.Robot
	}
	key := [2]string{day, event.Host}
	if r.names[key] >= NamesPerHostDay {
		return bots.Other
	}
	r.names[key]++
	return event.Robot
}

// Refusals — отказы роботам за проход, для таблицы robot_refusals.
func (r *Robots) Refusals() []RobotRefusal {
	out := make([]RobotRefusal, 0, len(r.refusals))
	for _, item := range r.refusals {
		out = append(out, *item)
	}
	return out
}

// Counts отдаёт собранное. Порядок не важен: дальше их складывают в базу.
func (r *Robots) Counts() []RobotCount {
	out := make([]RobotCount, 0, len(r.counts))
	for _, count := range r.counts {
		out = append(out, *count)
	}
	return out
}
