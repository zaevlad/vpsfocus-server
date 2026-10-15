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
)

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

// Robots сводит строки одного прохода в счётчики. Ключей немного — роботов
// в списке полтора десятка, — поэтому потолка, как у групп ошибок, не нужно.
type Robots struct {
	counts     map[[3]string]*RobotCount
	refusals   map[refusalKey]*RobotRefusal
	traffic    map[trafficKey]*SiteTraffic
	violations map[violationKey]*RobotViolation
	policy     Policy
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
		refusals:   map[refusalKey]*RobotRefusal{},
		traffic:    map[trafficKey]*SiteTraffic{},
		violations: map[violationKey]*RobotViolation{},
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
		r.bump(event, event.Robot)
		// Отказ роботу из списка (Р14). «Прочих» не считаем: вывода о них
		// экран не делает, а строк в таблице стало бы на порядок больше.
		if event.Robot != bots.Other && Refusal(event.Status) {
			key := refusalKey{dayOf(event.At), event.Host, event.Robot, event.Status, event.Path == "/robots.txt"}
			if item, ok := r.refusals[key]; ok {
				item.Count++
			} else {
				r.refusals[key] = &RobotRefusal{Day: key.day, Host: key.host, Name: key.name,
					Status: key.status, RobotsTxt: key.robotsTxt, Count: 1}
			}
		}
		r.checkRules(event)
	}
	if bots.Scanner(event.Path) {
		r.bump(event, MetaScan)
	}
	if event.Status == 200 && bots.Secret(event.Path) {
		r.bump(event, MetaExposed)
	}
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
