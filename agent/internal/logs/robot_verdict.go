package logs

import (
	"sort"
	"time"

	"agent/internal/bots"
)

// Отказы роботам и «робот пропал» (идея 10 трека AB, Р14–Р17).
//
// Человек хочет, чтобы сайт находили через ChatGPT и Perplexity, а сервер
// отказывает их роботам — плагин безопасности, правило nginx от старой
// атаки, общий ограничитель частоты. По логам видно, что отказал сам
// сервер; отказ Cloudflare или другого прокси на краю до сервера не доходит,
// и тогда остаётся второй признак — робот ходил и пропал.
//
// Подделку имени не ловим и не обещаем: отказ «GPTBot» мог получить и
// самозванец. Экран так и говорит.

// Refusal — считается ли код ответа отказом роботу (Р14): 403, 429 и 444
// (nginx закрыл соединение). 401 — закрытый раздел, это законно; 503 —
// перегрузка, другая беда, она видна в ошибках.
func Refusal(status int) bool {
	return status == 403 || status == 429 || status == 444
}

// RobotRefusal — сколько раз за сутки сервер отказал роботу этим кодом.
// Отдельно — отказы на /robots.txt: самый сильный признак (Р14), робот не
// может прочитать даже правила.
type RobotRefusal struct {
	Day       int64
	Host      string
	Name      string
	Status    int
	RobotsTxt bool
	Count     int
}

// Пороги вывода — постоянными (Р7).
const (
	// Меньше стольких запросов робота за неделю — не повод для вывода.
	refusalMinVisits = 10
	// «Пропал» — только у постоянных гостей (Р15): был в стольких из семи
	// прошлых дней…
	goneMinDaysSeen = 5
	// …а за столько последних дней — ни разу.
	goneQuietDays = 3
	// Окно, на котором «пропал» считается: три тихих дня и семь до них.
	// Логи хранятся меньше — «не знаем» (Р16).
	GoneNeedsDays = goneQuietDays + 7
)

// Что сказали о роботе.
const (
	VerdictRefused = "refused"
	VerdictGone    = "gone"
	// Уровень «посмотри» — у роботов поиска и ответов. Роботу обучения
	// отказ может быть выбором человека: справка без уровня.
	VerdictAttention = "attention"
)

// RobotVerdict — вывод о роботе на сайте.
//
// Считает агент, экран только рисует — правило проекта «оценку ставит
// агент».
type RobotVerdict struct {
	// Сайт — для своих (отчёт, группировка); наружу уходит внутри сайта.
	Host string `json:"-"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// `refused` или `gone`.
	Verdict string `json:"verdict"`
	// `attention` или пусто — справка без тревоги.
	Level string `json:"level,omitempty"`
	// Запросов робота за последние семь дней.
	Visits int `json:"visits"`
	// Из них отказов и самый частый код отказа.
	Refused int `json:"refused,omitempty"`
	Code    int `json:"code,omitempty"`
	// Отказов на /robots.txt за неделю.
	RobotsTxt int `json:"robotsTxt,omitempty"`
	// У пропавшего: запросов за семь дней до тихих и в скольких днях он был.
	Before   int `json:"before,omitempty"`
	DaysSeen int `json:"daysSeen,omitempty"`
}

// AssessRobots выводит, кому из роботов отказывают и кто пропал.
//
// visits — суточные счётчики `robot_visits` (с служебными строками),
// refusals — суточные отказы; оба за последние GoneNeedsDays дней или
// дольше. retentionDays — сколько живут счётчики: меньше GoneNeedsDays —
// «пропал» не считается вовсе, и второй ответ говорит об этом (Р16).
func AssessRobots(now time.Time, retentionDays int, visits []RobotCount, refusals []RobotRefusal) ([]RobotVerdict, bool) {
	today := dayOf(now)
	const day = 86_400
	weekFrom := today - 6*day
	quietFrom := today - (goneQuietDays-1)*day
	beforeFrom := quietFrom - 7*day
	goneKnown := retentionDays >= GoneNeedsDays

	type key struct{ host, name string }
	type tally struct {
		week, quiet, before int
		seen                map[int64]bool
	}
	tallies := map[key]*tally{}
	// Строки лога сайта за тихие дни: без них «робот пропал» значит «мы
	// перестали читать лог», а не «робот не приходил» (молчаливый ноль).
	readQuiet := map[string]bool{}

	for _, count := range visits {
		if count.Name == MetaLines && count.Day >= quietFrom && count.Count > 0 {
			readQuiet[count.Host] = true
		}
		if _, ok := botKind(count.Name); !ok {
			continue
		}
		k := key{count.Host, count.Name}
		t, ok := tallies[k]
		if !ok {
			t = &tally{seen: map[int64]bool{}}
			tallies[k] = t
		}
		if count.Day >= weekFrom {
			t.week += count.Count
		}
		switch {
		case count.Day >= quietFrom:
			t.quiet += count.Count
		case count.Day >= beforeFrom:
			t.before += count.Count
			if count.Count > 0 {
				t.seen[count.Day] = true
			}
		}
	}

	type refusalTally struct {
		total, robotsTxt int
		byCode           map[int]int
	}
	refused := map[key]*refusalTally{}
	for _, item := range refusals {
		if item.Day < weekFrom {
			continue
		}
		k := key{item.Host, item.Name}
		r, ok := refused[k]
		if !ok {
			r = &refusalTally{byCode: map[int]int{}}
			refused[k] = r
		}
		r.total += item.Count
		r.byCode[item.Status] += item.Count
		if item.RobotsTxt {
			r.robotsTxt += item.Count
		}
	}

	var verdicts []RobotVerdict
	for k, t := range tallies {
		kind, _ := botKind(k.name)
		level := VerdictAttention
		if kind == bots.AITraining {
			level = ""
		}
		base := RobotVerdict{Host: k.host, Name: k.name, Kind: kind, Level: level, Visits: t.week}

		if r := refused[k]; r != nil && t.week >= refusalMinVisits && r.total*2 > t.week {
			verdict := base
			verdict.Verdict = VerdictRefused
			verdict.Refused = r.total
			verdict.RobotsTxt = r.robotsTxt
			verdict.Code = commonest(r.byCode)
			verdicts = append(verdicts, verdict)
			continue
		}

		if goneKnown && readQuiet[k.host] && t.quiet == 0 && len(t.seen) >= goneMinDaysSeen {
			verdict := base
			verdict.Verdict = VerdictGone
			verdict.Before = t.before
			verdict.DaysSeen = len(t.seen)
			verdicts = append(verdicts, verdict)
		}
	}

	// Первыми — отказ на robots.txt (Р14), затем «посмотри», затем по числу
	// запросов; дальше по имени, чтобы порядок не прыгал между ответами.
	sort.Slice(verdicts, func(i, j int) bool {
		a, b := verdicts[i], verdicts[j]
		if (a.RobotsTxt > 0) != (b.RobotsTxt > 0) {
			return a.RobotsTxt > 0
		}
		if (a.Level == VerdictAttention) != (b.Level == VerdictAttention) {
			return a.Level == VerdictAttention
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Visits+a.Before != b.Visits+b.Before {
			return a.Visits+a.Before > b.Visits+b.Before
		}
		return a.Name < b.Name
	})
	return verdicts, goneKnown
}

// botKind — роль робота из списка; служебные счётчики и «прочие» — не
// роботы для вывода.
func botKind(name string) (string, bool) {
	for _, bot := range bots.List {
		if bot.Name == name && bot.InLogs {
			return bot.Kind, true
		}
	}
	return "", false
}

// commonest — самый частый код; при равенстве — меньший, чтобы ответ не
// зависел от порядка обхода карты.
func commonest(byCode map[int]int) int {
	best, bestCount := 0, -1
	for code, count := range byCode {
		if count > bestCount || (count == bestCount && code < best) {
			best, bestCount = code, count
		}
	}
	return best
}
