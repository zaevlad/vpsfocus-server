// Package health сводит собранное в один ответ на вопрос «всё ли в порядке».
//
// Считает агент, а не экран. Причина та же, по которой у него живут пороги
// Core Web Vitals и таблица `severityByCode` в SEO: экранов, спрашивающих
// «как дела у клиента», будет несколько — карточка проекта, портфель,
// чеклист, письмо, — и посчитай каждый по-своему, они однажды разойдутся.
// Наружу уходят коды и уровни, порядок уже посчитан.
//
// **Уровней два, и они не смешиваются.** У сервера свой (диск, память,
// свежесть замеров), у проекта свой (домен, сертификат, ссылки, SEO). Проект
// ссылается на сервер, а не копирует его беду себе: кончилось место на VPS с
// тремя проектами — это одна запись, одно действие и три спокойные карточки,
// а не три одинаковых красных.
//
// **Свод отвечает на вопрос «нужно ли что-то делать сегодня»,** а не «есть
// ли замечания вообще». Поэтому гигиенические находки SEO — предупреждения и
// замечания — уровень не поднимают: проект с двенадцатью замечаниями нижнего
// уровня светился бы жёлтым вечно, а отчёт присылал бы те же двенадцать
// строк каждую неделю. Та же беда, из-за которой письмо о чёрных списках
// уходит только на переходе состояния.
package health

import (
	"sort"
	"strings"
	"time"

	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/links"
	"agent/internal/logs"
	"agent/internal/metrics"
	"agent/internal/probe"
	"agent/internal/seo"
	"agent/internal/store"
	"agent/internal/vitals"
)

// Level — насколько всё плохо.
//
// Три уровня, а не пять: между «в порядке» и «горит» человеку нужна ровно
// одна ступень — «посмотри, когда дойдут руки». Больше ступеней означает
// спор о том, какая из них у этой находки, и он никогда не кончается.
type Level string

const (
	// LevelOK — делать сегодня нечего.
	LevelOK Level = "ok"
	// LevelAttention — стоит посмотреть, но сайт работает.
	LevelAttention Level = "attention"
	// LevelProblem — уже сломано или сломается со дня на день.
	LevelProblem Level = "problem"
)

// Коды поводов. Наружу уходят они, перевод живёт в приложении — как и
// везде в проекте.
const (
	// ── Сервер ───────────────────────────────────────────────────────────
	// Диск заполнен выше порога, при котором агент начинает беспокоить.
	CodeDiskFilling = "healthDiskFilling"
	// Свободного места почти не осталось: пять процентов от порога тревоги
	// или меньше пяти процентов диска.
	CodeDiskFull = "healthDiskFull"
	// Памяти занято столько, что следующий всплеск уйдёт в swap.
	CodeMemoryTight = "healthMemoryTight"
	// Замеров нет вовсе: агент только поставили или он не пишет в базу.
	CodeMetricsMissing = "healthMetricsMissing"
	// Последний замер сильно старше, чем должен быть.
	CodeMetricsStale = "healthMetricsStale"

	// ── Проект ───────────────────────────────────────────────────────────
	CodeDomainExpiring = "healthDomainExpiring"
	CodeDomainExpired  = "healthDomainExpired"
	// Срок домена выяснить не удалось. Повод не про сайт, а про то, что мы
	// перестали видеть: молчать об ослепшей проверке нельзя.
	CodeDomainUnknown = "healthDomainUnknown"
	CodeCertExpiring  = "healthCertExpiring"
	CodeCertExpired   = "healthCertExpired"
	// Сертификат не принят системными корнями или не подходит домену.
	CodeCertInvalid = "healthCertInvalid"
	// На сайте есть битые ссылки.
	CodeLinksBroken = "healthLinksBroken"
	// Круг проверки ссылок не состоялся.
	CodeLinksFailed = "healthLinksFailed"
	// В SEO-аудите есть критичные находки: страница не попадёт в поиск или
	// вместо неё попадёт другая.
	CodeSeoCritical = "healthSeoCritical"
	// Обход сайта не состоялся.
	CodeSeoFailed = "healthSeoFailed"
	// Вход на сайт разъехался: http отдаёт содержимое вместо перехода на
	// https, www и адрес без него живут двумя сайтами или зациклены.
	//
	// Отдельный повод, а не «критичная находка SEO»: те считаются по
	// страницам, а этот — про сайт целиком, и число страниц у него не
	// существует. Смешай их в один счётчик, и «критичных находок: 3» стало
	// бы неправдой в обе стороны.
	CodeSeoCanonical = "healthSeoCanonical"
	// robots.txt закрывает сайт целиком от Google, Bing или Яндекса.
	// Уточнение — имена закрытых поисковиков.
	//
	// Отдельный повод по той же причине, что и вход на сайт: это находка о
	// сайте, а не о страницах, и в «критичных находок: N» она не входит.
	// Прочие находки о сайте (ИИ-боты, карта сайта) — гигиена, и свод они
	// не красят: они видны на экране SEO.
	CodeSeoBlocked = "healthSeoBlocked"
	// Живые посетители видят плохие Core Web Vitals.
	//
	// Только field-данные: lab — это одна загрузка в лаборатории Google, и
	// поднимать по ней статус значит будить человека из-за того, чего его
	// посетители могли не заметить. Field ранжирует Google.
	CodeVitalsPoor = "healthVitalsPoor"

	// Ключевой адрес проекта не работает так, как должен: страница не
	// отдаётся, API вернул не JSON, на странице нет нужного текста.
	//
	// Это самый прямой повод из всех: он про то, ради чего сайт и
	// существует, а не про его окрестности.
	CodeProbeFailing = "healthProbeFailing"

	// Веб-сервер клиента отдаёт ошибки: за окно тревоги их набралось больше
	// порога.
	//
	// Всплеск, а не сумма: «двести пятисоток за сутки» у большого сайта —
	// это фон, «двести за пятнадцать минут» — авария. Тем же порогом уходит
	// письмо, и второго мнения о том, пора ли волноваться, здесь нет.
	CodeErrorSpike = "healthErrorSpike"
	// Веб-сервер клиента отдаёт «страницы нет» чаще, чем это похоже на фон.
	//
	// Уровень ниже, чем у ошибок сервера, и порог выше: четыреста четвёртая
	// — это ещё и боты, перебирающие чужие админки. Зато после выкатки,
	// снёсшей раздел, других следов может не остаться вовсе.
	CodeNotFoundSpike = "healthNotFoundSpike"

	// ── Стек ─────────────────────────────────────────────────────────────
	// Сервис стека не отвечает на своём порту.
	CodeServiceDown = "healthServiceDown"
	// Скрипт трекинга не отдаётся: счётчик молчит на всех сайтах сразу.
	//
	// Уровень ниже, чем у мёртвого сервиса, и это осознанно: сайты клиента
	// работают, теряется статистика. Беда настоящая, но не та, ради которой
	// бросают ужин.
	CodeTrackerMissing = "healthTrackerMissing"

	// ── Копии базы ───────────────────────────────────────────────────────
	// Последняя попытка копии не удалась.
	//
	// Повод про сервер: копия снимается со стека, а не с проекта. Уровень
	// «посмотреть» — одна неудачная ночь данных не теряет, — но замолчать
	// её нельзя: следующая неудачная ночь тоже будет молчать.
	CodeBackupFailed = "healthBackupFailed"
	// Копий нет ни одной: круги идут, а удачного среди них не было.
	//
	// Отдельно от протухшей копии, и это не дробление: «последняя копия
	// пять дней назад» и «копий нет вовсе» — разные новости, и вторая
	// означает, что страховки не появлялось никогда. Ноль дней в тексте
	// про возраст был бы неправдой в самую тревожную сторону.
	CodeBackupMissing = "healthBackupMissing"
	// Свежей копии нет дольше, чем положено.
	//
	// Отдельно от неудачи, и второй поверх первой не встаёт: «попытка
	// провалилась» означает «смотри ошибку», а «копий нет» — «страховки у
	// тебя сейчас нет вовсе». Разные первые шаги — разные поводы, та же
	// линия, что у сторожа задач.
	//
	// Уровень тот же, «посмотреть», и это осознанно: красный в своде
	// означает «сайт клиента сломан», и мешать с ним отсутствующую копию
	// значит обесценивать красный. Зато повод не гаснет сам — как и
	// `healthNoChannels`, он висит, пока человек не починит или не
	// выключит.
	CodeBackupStale = "healthBackupStale"

	// ── Сам агент ────────────────────────────────────────────────────────
	// Каналов уведомлений нет ни одного: агент следит и молчит.
	//
	// Повод про сервер, а не про проект, и он честно первый в списке дел:
	// сторож, который никого не может позвать, — это не сторож. В своде он
	// появился вместе с еженедельным отчётом, которому уходить точно так же
	// некуда.
	CodeNoChannels = "healthNoChannels"
)

// levelByCode — уровень каждого повода, одной таблицей.
//
// Не рядом с местом, где повод рождается: уровень нужен и сводке, и
// чеклисту, и письму, а два места назначения важности однажды разойдутся.
// Ровно тем же соображением живёт `severityByCode` в SEO. Полнота таблицы
// закреплена тестом: забытый код виден сразу, а не в отчёте о баге.
var levelByCode = map[string]Level{
	CodeDiskFilling:    LevelAttention,
	CodeDiskFull:       LevelProblem,
	CodeMemoryTight:    LevelAttention,
	CodeMetricsMissing: LevelAttention,
	CodeMetricsStale:   LevelAttention,

	CodeDomainExpiring: LevelAttention,
	CodeDomainExpired:  LevelProblem,
	CodeDomainUnknown:  LevelAttention,
	CodeCertExpiring:   LevelAttention,
	CodeCertExpired:    LevelProblem,
	CodeCertInvalid:    LevelProblem,
	CodeLinksBroken:    LevelAttention,
	CodeLinksFailed:    LevelAttention,
	CodeSeoCritical:    LevelAttention,
	CodeSeoFailed:      LevelAttention,
	CodeSeoCanonical:   LevelAttention,
	CodeSeoBlocked:     LevelAttention,
	CodeVitalsPoor:     LevelAttention,
	CodeProbeFailing:   LevelProblem,
	CodeErrorSpike:     LevelProblem,
	CodeNotFoundSpike:  LevelAttention,

	CodeServiceDown:    LevelProblem,
	CodeTrackerMissing: LevelAttention,

	CodeBackupFailed:  LevelAttention,
	CodeBackupMissing: LevelAttention,
	CodeBackupStale:   LevelAttention,

	CodeNoChannels: LevelAttention,
}

// Reason — один повод для беспокойства.
//
// Числа отдельными полями, а не в тексте: текст собирает приложение на языке
// человека, а «осталось 3 дня» и «осталось 30» — это одна и та же строка
// перевода с разной подстановкой.
type Reason struct {
	Code  string `json:"code"`
	Level Level  `json:"level"`
	// О чём речь: домен, адрес ключевой проверки или имя сервиса стека.
	// Пусто — повод про сервер целиком или про весь проект сразу.
	//
	// Одно поле, а не три: это цель находки, и ею же её приглушают. Три
	// поля означали бы три ключа приглушения и три места, где их путают.
	Domain string `json:"domain,omitempty"`
	// Сколько дней осталось. Отрицательное — срок уже прошёл.
	Days *int `json:"days,omitempty"`
	// Сколько штук: битых ссылок, критичных находок, процентов диска.
	Count int `json:"count,omitempty"`
	// Код беды от проверки, которая не состоялась. Тоже код, а не текст.
	Detail string `json:"detail,omitempty"`
	// За сколько минут посчитан всплеск. Только у поводов из логов.
	//
	// Своим полем, а не в `Detail`: тот несёт код, который приложение ищет
	// по словарям проверок, и число в нём читалось бы как незнакомый код.
	// И не одним значением на весь свод: повод обязан объясняться сам —
	// «двести за пятнадцать минут» и «двести за сутки» это разные новости,
	// а строку читают в письме, в чеклисте и на карточке порознь.
	WindowMinutes int `json:"windowMinutes,omitempty"`
	// Находку приглушили: «знаю, так и задумано».
	//
	// Приглушённая находка уровень не поднимает и в отчёт не попадает, но
	// из свода не исчезает. Исчезни она совсем — снять приглушение стало бы
	// нечем, и через полгода никто бы не вспомнил, что его вообще ставили.
	Suppressed bool `json:"suppressed,omitempty"`
	// До какого времени приглушено, RFC3339. Пусто — бессрочно.
	SuppressedUntil string `json:"suppressedUntil,omitempty"`
	// Чем именно погашено: цель приглушения, а не домен находки.
	//
	// Они расходятся, когда приглушён повод целиком: находка про
	// `example.com`, а приглушение — пустая цель. Сними приложение
	// приглушение по домену находки, оно не сняло бы ничего, и кнопка
	// «вернуть» молча не работала бы. Отдаём ключ, которым гасили.
	SuppressedTarget string `json:"suppressedTarget,omitempty"`
}

// ServerState — состояние самого VPS.
type ServerState struct {
	Level   Level    `json:"level"`
	Reasons []Reason `json:"reasons"`
	// Когда снят последний замер. Пусто — замеров нет вовсе.
	SampledAt string `json:"sampledAt,omitempty"`
}

// ProjectState — состояние одного проекта.
//
// Уровня сервера здесь нет намеренно: проект на него ссылается, а не
// копирует его себе.
type ProjectState struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Level   Level    `json:"level"`
	Reasons []Reason `json:"reasons"`
	// Когда проект последний раз чем-нибудь проверяли: самая свежая из дат
	// проверки домена, сертификата, ссылок и обхода. Пусто — ещё ничем.
	CheckedAt string `json:"checkedAt,omitempty"`
	// Показатели для карточки: скорость, SEO, ссылки, сроки (`figures.go`).
	// Всегда четыре; чего не знаем — `unknown`, а не ноль.
	Figures []Figure `json:"figures"`
}

// Snapshot — то, что уезжает приложению.
type Snapshot struct {
	Server ServerState `json:"server"`
	// Проекты, отсортированные по тому, за какой браться первым.
	Projects []ProjectState `json:"projects"`
	// Идёт обслуживание. Пусто — не идёт.
	//
	// Уровни при этом считаются честно: человек, открывший экран во время
	// собственного обновления, должен видеть, что происходит, а не гладкое
	// «всё хорошо». Приглушается исходящее — тревоги и еженедельный
	// отчёт, — потому что будить оно должно не того, кто и так смотрит.
	Maintenance *MaintenanceWindow `json:"maintenance,omitempty"`
}

// MaintenanceWindow — окно, в котором агент молчит.
type MaintenanceWindow struct {
	Until   string `json:"until"`
	Note    string `json:"note,omitempty"`
	Started string `json:"started"`
}

// Input — всё, из чего свод считается.
//
// Структурой, а не походом в базу: свод — это разбор уже собранного, и
// собирать ради него нечего. Заодно пакет проверяется без SQLite и без сети.
type Input struct {
	Now time.Time

	// Последний замер и пороги, при которых агент начинает беспокоить.
	Sample           *metrics.Sample
	DiskAlertPercent int
	// Через сколько замер считается несвежим.
	MetricsStaleAfter time.Duration

	// За сколько дней предупреждать. Те же числа, что у уведомлений: две
	// проверки, спорящие о том, пора ли волноваться, — это гарантированный
	// вопрос «кому из них верить».
	CertDays   int
	DomainDays int

	Domains []checks.DomainStatus
	Certs   []checks.TLSStatus
	Links   []links.Report
	Seo     []seo.Scan
	Vitals  []vitals.Report

	Projects []config.Project

	// Ключевые адреса проектов: чем кончилась последняя проверка.
	Probes []probe.Result
	// Сервисы стека и то, отдаётся ли скрипт трекинга. Оба про сервер, а не
	// про проект: стек на нём один, и мёртвая Umami — это одна запись и
	// одно действие, а не по красной карточке на каждого клиента.
	Services  []probe.Service
	Analytics *probe.Analytics

	// Всплески ошибок в логах веб-сервера клиента и пороги, при которых
	// они считаются всплеском. Ноль-порог выключает повод совсем.
	//
	// Логи читаются у веб-сервера, который нам не принадлежит, и домен в
	// строке есть не всегда: формат `combined` у nginx его не пишет. Есть
	// домен и он знаком — повод уезжает проекту; нет — остаётся у сервера.
	// Промолчать про «ничей» всплеск нельзя: это тот же молчаливый ноль.
	LogSpikes            []logs.Spike
	LogErrorThreshold    int
	LogNotFoundThreshold int
	LogWindowMinutes     int

	// Копии базы: что известно о последнем круге и о том, что лежит в
	// хранилище клиента.
	Backup BackupState

	// Приглушённые находки: «знаю, так и задумано».
	Suppressions []store.Suppression
	// Идущее обслуживание, если оно идёт.
	Maintenance *store.Maintenance
	// Настроенные каналы уведомлений. Пустой список — повод сам по себе:
	// агент, которому некуда написать, следит впустую.
	Channels []string
	// Уведомления с этого сервера выключены человеком. Это его решение, а
	// не беда: повода «некуда написать» у такого сервера нет.
	NotifyMuted bool
}

// BackupState — что свод знает о копиях.
//
// Структурой, а не походом в базу: свод — разбор уже собранного, и
// собирать ради него нечего. Пустое `Configured` означает «хранилище не
// задано», и повода из этого не рождается: не делать копий — законный
// выбор, а не находка.
type BackupState struct {
	Configured bool
	// Как часто копия должна появляться.
	Interval time.Duration
	// Был ли хоть один круг и чем кончился последний.
	Attempted bool
	LastOK    bool
	LastCode  string
	// Когда сделана самая свежая копия. Спрошено у хранилища, если оно
	// отвечало; иначе — наша запись о последнем успехе.
	Freshest time.Time
}

// Через сколько интервалов копия считается несвежей.
//
// Два, а не один: круг, сдвинувшийся на час из-за перезапуска агента, не
// повод объявлять страховку потерянной, — а вот двое суток без копии при
// суточном расписании это уже не сдвиг.
const backupStaleIntervals = 2

// backupReasons считает поводы про копии.
func backupReasons(in Input) []Reason {
	state := in.Backup
	if !state.Configured || !state.Attempted {
		return nil
	}

	interval := state.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	stale := state.Freshest.IsZero() ||
		in.Now.Sub(state.Freshest) > time.Duration(backupStaleIntervals)*interval

	// Код последней беды едет рядом с обоими поводами: «копий нет» без «а
	// почему» заставляет открывать второй экран ради одной строки.
	if state.Freshest.IsZero() {
		return []Reason{reason(CodeBackupMissing, "", nil, 0, state.LastCode)}
	}
	if stale {
		days := int(in.Now.Sub(state.Freshest).Hours() / 24)
		return []Reason{reason(CodeBackupStale, "", nil, days, state.LastCode)}
	}

	if !state.LastOK {
		return []Reason{reason(CodeBackupFailed, "", nil, 0, state.LastCode)}
	}

	return nil
}

// Compute считает свод.
func Compute(in Input) Snapshot {
	snapshot := Snapshot{
		Server:   serverState(in),
		Projects: projectStates(in),
	}
	if snapshot.Projects == nil {
		snapshot.Projects = []ProjectState{}
	}

	// Приглушение применяется последним шагом, а не при рождении повода:
	// повод обязан родиться в любом случае, иначе его нечем будет вернуть
	// в свод, когда приглушение снимут. Здесь он только перестаёт поднимать
	// уровень.
	apply(in.Suppressions, in.Now, &snapshot)

	if in.Maintenance != nil {
		snapshot.Maintenance = &MaintenanceWindow{
			Until:   in.Maintenance.Until.UTC().Format(time.RFC3339),
			Note:    in.Maintenance.Note,
			Started: in.Maintenance.Started.UTC().Format(time.RFC3339),
		}
	}

	return snapshot
}

// apply помечает приглушённые находки и пересчитывает уровни без них.
func apply(suppressions []store.Suppression, now time.Time, snapshot *Snapshot) {
	if len(suppressions) == 0 {
		return
	}

	// Ключ парой, а не склейкой строк: разделитель в ключе — это лишний
	// вопрос «а если он встретится в домене», на который не хочется
	// отвечать.
	index := make(map[[2]string]store.Suppression, len(suppressions))
	for _, item := range suppressions {
		if !item.Until.IsZero() && !item.Until.After(now) {
			continue
		}
		index[[2]string{item.Code, item.Target}] = item
	}

	mark := func(reasons []Reason) []Reason {
		for i := range reasons {
			// Приглушить можно точечно — «этот сертификат» — или повод
			// целиком. Пустая цель во втором случае: сказать «сроки доменов
			// меня не интересуют» человек вправе один раз, а не по домену.
			item, ok := index[[2]string{reasons[i].Code, reasons[i].Domain}]
			if !ok {
				item, ok = index[[2]string{reasons[i].Code, ""}]
			}
			if !ok {
				continue
			}
			reasons[i].Suppressed = true
			reasons[i].SuppressedTarget = item.Target
			if !item.Until.IsZero() {
				reasons[i].SuppressedUntil = item.Until.UTC().Format(time.RFC3339)
			}
		}
		return reasons
	}

	snapshot.Server.Reasons = mark(snapshot.Server.Reasons)
	snapshot.Server.Level = worst(snapshot.Server.Reasons)
	for i := range snapshot.Projects {
		snapshot.Projects[i].Reasons = mark(snapshot.Projects[i].Reasons)
		snapshot.Projects[i].Level = worst(snapshot.Projects[i].Reasons)
	}

	// Порядок проектов считался до приглушения и после него мог устареть:
	// проект, у которого приглушили единственную беду, обязан уехать вниз.
	sortProjects(snapshot.Projects)
}

// empty подставляет пустой срез вместо nil.
//
// Go сериализует nil-срез в `null`, а не в `[]`, и разбор на стороне
// приложения спотыкается о него ровно там, где всё хорошо: у сервера без
// единого повода. Обещали список — отдаём список, пусть и пустой. Грабли
// уже наступленные — на истории замеров в седьмом спринте.
func empty(reasons []Reason) []Reason {
	if reasons == nil {
		return []Reason{}
	}
	return reasons
}

func serverState(in Input) ServerState {
	var state ServerState

	// Сторож, которому некуда позвать, — не сторож. Повод стоит первым и
	// проверяется до всего остального: он не зависит ни от замеров, ни от
	// того, дожил ли агент до первого круга проверок.
	if len(in.Channels) == 0 && !in.NotifyMuted {
		state.Reasons = append(state.Reasons, reason(CodeNoChannels, "", nil, 0, ""))
	}

	// Сервисы стека и трекинг — про сервер, а не про проект: стек здесь
	// один на всех клиентов.
	//
	// Считаются до замеров и не зависят от них. Порядок этот не случайный:
	// в первую минуту после запуска агента замеров ещё нет, а мёртвый
	// postgres уже есть, и промолчать о нём из-за отсутствия цифр про
	// процессор значило бы спрятать беду за её же соседкой. Найдено живым
	// прогоном.
	for _, service := range in.Services {
		if !service.Up {
			state.Reasons = append(state.Reasons,
				reason(CodeServiceDown, service.Name, nil, 0, service.Code))
		}
	}
	// Молчание проверки трекинга поводом не считается: `Analytics` пуст
	// ровно до первого круга, и объявлять этим счётчик сломанным значило бы
	// пугать человека собственным перезапуском агента.
	if in.Analytics != nil && !in.Analytics.Tracker {
		state.Reasons = append(state.Reasons,
			reason(CodeTrackerMissing, "", nil, 0, in.Analytics.Code))
	}

	// Копии базы. Про сервер, а не про проект: копия снимается со стека,
	// один на все проекты этого VPS.
	state.Reasons = append(state.Reasons, backupReasons(in)...)

	// Всплеск ошибок в логах, который не удалось отнести ни к одному
	// проекту. Считается до замеров и от них не зависит — по той же
	// причине, что и сервисы стека: логи читаются, даже когда метрики ещё
	// не снялись.
	state.Reasons = append(state.Reasons, serverSpikes(in)...)

	// Замеров нет вовсе. Повод честный: сервер без единого замера,
	// объявленный здоровым, — это молчаливый ноль. Но у него есть законный
	// случай — первая минута после запуска агента, — и именно поэтому
	// еженедельный отчёт не уходит сразу при старте.
	if in.Sample == nil {
		state.Reasons = append(state.Reasons, reason(CodeMetricsMissing, "", nil, 0, ""))
		state.Level = worst(state.Reasons)
		sortReasons(state.Reasons)
		state.Reasons = empty(state.Reasons)
		return state
	}

	state.SampledAt = in.Sample.At.UTC().Format(time.RFC3339)

	if in.MetricsStaleAfter > 0 && in.Now.Sub(in.Sample.At) > in.MetricsStaleAfter {
		minutes := int(in.Now.Sub(in.Sample.At).Minutes())
		state.Reasons = append(state.Reasons, reason(CodeMetricsStale, "", nil, minutes, ""))
	}

	// Порог диска — тот же, по которому уходит уведомление. «Почти
	// кончился» отличается от «заполняется» на пять процентных пунктов:
	// между 85 и 90 ещё можно спокойно почистить логи, после 95 сервер
	// начинает ломаться сам.
	if disk := in.Sample.DiskUsedPercent(); in.DiskAlertPercent > 0 && disk >= in.DiskAlertPercent {
		code := CodeDiskFilling
		if disk >= 95 || disk >= in.DiskAlertPercent+5 {
			code = CodeDiskFull
		}
		state.Reasons = append(state.Reasons, reason(code, "", nil, disk, ""))
	}

	// Порог памяти не настраивается и не уведомляет: занятая память сама по
	// себе не беда — Linux отдаёт её под кеш, — и будить человека по ней
	// нельзя. А в своде она объясняет, почему сервер начал тормозить.
	if mem := in.Sample.MemUsedPercent(); mem >= memoryTightPercent {
		state.Reasons = append(state.Reasons, reason(CodeMemoryTight, "", nil, mem, ""))
	}

	state.Level = worst(state.Reasons)
	// Порядок тот же, что у проектов: сначала то, что сломано. Считаются
	// поводы в другом порядке — сперва немые каналы, потом сервисы, потом
	// замеры, — и это порядок вычисления, а не показа: экранов у свода
	// несколько, и разберись каждый сам, они однажды разойдутся.
	sortReasons(state.Reasons)
	state.Reasons = empty(state.Reasons)
	return state
}

// Порог, за которым занятая память становится поводом посмотреть.
const memoryTightPercent = 90

// spikeReasons — поводы из одного набора всплесков.
//
// Одна функция на оба уровня: у сервера и у проекта пороги общие, и
// разойдись они, «двести ошибок» на карточке проекта означало бы не то же
// самое, что «двести ошибок» на карточке сервера.
func spikeReasons(in Input, domain string, serverErrors, notFound int) []Reason {
	var out []Reason

	if in.LogErrorThreshold > 0 && serverErrors >= in.LogErrorThreshold {
		item := reason(CodeErrorSpike, domain, nil, serverErrors, "")
		item.WindowMinutes = in.LogWindowMinutes
		out = append(out, item)
	}
	if in.LogNotFoundThreshold > 0 && notFound >= in.LogNotFoundThreshold {
		item := reason(CodeNotFoundSpike, domain, nil, notFound, "")
		item.WindowMinutes = in.LogWindowMinutes
		out = append(out, item)
	}
	return out
}

// serverSpikes — всплески, которые не оказались ничьими в отдельности.
//
// Сюда попадает всё, у чего в логе нет домена, и всё, чей домен не значится
// ни в одном проекте: чужой сайт на том же сервере — законный случай, мы
// стоим на чужом проде. Складываются они в один повод, а не в десять: это
// одна беда веб-сервера и одно действие.
func serverSpikes(in Input) []Reason {
	known := map[string]bool{}
	for _, project := range in.Projects {
		for _, domain := range project.Domains {
			known[strings.ToLower(domain)] = true
		}
	}

	serverErrors, notFound := 0, 0
	for _, spike := range in.LogSpikes {
		if known[strings.ToLower(spike.Host)] {
			continue
		}
		serverErrors += spike.ServerErrors
		notFound += spike.NotFound
	}
	return spikeReasons(in, "", serverErrors, notFound)
}

func projectStates(in Input) []ProjectState {
	domains := map[string]checks.DomainStatus{}
	for _, status := range in.Domains {
		domains[strings.ToLower(status.Domain)] = status
	}
	certs := map[string]checks.TLSStatus{}
	for _, status := range in.Certs {
		certs[strings.ToLower(status.Domain)] = status
	}
	linkReports := map[string]links.Report{}
	for _, report := range in.Links {
		linkReports[strings.ToLower(report.Domain)] = report
	}
	scans := map[string]seo.Scan{}
	for _, scan := range in.Seo {
		scans[strings.ToLower(scan.Domain)] = scan
	}
	// Замеров у домена несколько — по странице на тип устройства, — и
	// повод из них один: перечислять «мобильная главная», «десктопная
	// главная», «мобильный каталог» отдельными строками значит утопить
	// сводку в подробностях, за которыми есть свой экран.
	measured := map[string][]vitals.Report{}
	for _, report := range in.Vitals {
		key := strings.ToLower(report.Domain)
		measured[key] = append(measured[key], report)
	}

	// Всплески по доменам. Домен в строке лога есть не всегда, и то, что
	// без него, разбирает `serverSpikes`.
	spikes := map[string]logs.Spike{}
	for _, spike := range in.LogSpikes {
		if spike.Host == "" {
			continue
		}
		key := strings.ToLower(spike.Host)
		known := spikes[key]
		known.ServerErrors += spike.ServerErrors
		known.NotFound += spike.NotFound
		spikes[key] = known
	}

	// Ключевые адреса привязаны к проекту, а не к домену: их и заводят по
	// одному-три на клиента, а лежать они могут на любом его домене.
	probes := map[string][]probe.Result{}
	for _, result := range in.Probes {
		probes[result.ProjectID] = append(probes[result.ProjectID], result)
	}

	states := make([]ProjectState, 0, len(in.Projects))
	for _, project := range in.Projects {
		state := ProjectState{
			ID:      project.ID,
			Name:    project.Name,
			Domains: project.Domains,
		}

		var newest time.Time
		for _, domain := range project.Domains {
			domain = strings.ToLower(domain)

			if status, ok := domains[domain]; ok {
				state.Reasons = append(state.Reasons, domainReasons(status, in.DomainDays)...)
				newest = later(newest, status.CheckedAt)
			}
			if status, ok := certs[domain]; ok {
				state.Reasons = append(state.Reasons, certReasons(status, in.CertDays)...)
				newest = later(newest, status.CheckedAt)
			}
			if report, ok := linkReports[domain]; ok {
				state.Reasons = append(state.Reasons, linkReasons(report)...)
				newest = later(newest, parseTime(report.CheckedAt))
			}
			if scan, ok := scans[domain]; ok {
				state.Reasons = append(state.Reasons, seoReasons(scan)...)
				newest = later(newest, parseTime(scan.CheckedAt))
			}
			if reports, ok := measured[domain]; ok {
				state.Reasons = append(state.Reasons, vitalsReasons(domain, reports)...)
				for _, report := range reports {
					newest = later(newest, parseTime(report.CheckedAt))
				}
			}
			if spike, ok := spikes[domain]; ok {
				state.Reasons = append(state.Reasons,
					spikeReasons(in, domain, spike.ServerErrors, spike.NotFound)...)
			}
		}

		for _, result := range probes[project.ID] {
			newest = later(newest, result.CheckedAt)
			if result.OK {
				continue
			}
			// Целью находки идёт адрес, а не имя: приглушают конкретный
			// адрес, а имя человек в любой момент переименует.
			state.Reasons = append(state.Reasons,
				reason(CodeProbeFailing, result.URL, nil, 0, result.Code))
		}

		if !newest.IsZero() {
			state.CheckedAt = newest.UTC().Format(time.RFC3339)
		}
		state.Level = worst(state.Reasons)
		sortReasons(state.Reasons)
		state.Reasons = empty(state.Reasons)
		state.Figures = figures(project, in, figureSources{
			domains:  domains,
			certs:    certs,
			links:    linkReports,
			scans:    scans,
			measured: measured,
		})
		if state.Domains == nil {
			state.Domains = []string{}
		}
		states = append(states, state)
	}

	sortProjects(states)
	return states
}

// sortProjects упорядочивает проекты по тому, за какой браться первым.
//
// Считает агент, а не экран: то же правило, по которому упорядочен разрез
// находок SEO по кодам. Сначала уровень, потом число поводов, потом имя —
// последнее затем, чтобы список не перетасовывался при каждом обновлении.
//
// Отдельной функцией, потому что порядок приходится считать дважды: после
// первого прохода и ещё раз после приглушения, которое меняет уровни.
func sortProjects(states []ProjectState) {
	sort.SliceStable(states, func(i, j int) bool {
		if rank(states[i].Level) != rank(states[j].Level) {
			return rank(states[i].Level) > rank(states[j].Level)
		}
		if len(states[i].Reasons) != len(states[j].Reasons) {
			return len(states[i].Reasons) > len(states[j].Reasons)
		}
		return states[i].Name < states[j].Name
	})
}

func domainReasons(status checks.DomainStatus, threshold int) []Reason {
	// Проверка не смогла ответить. Не «всё хорошо»: молчаливый ноль хуже
	// отказа, и человек обязан знать, что срок домена мы больше не видим.
	if status.DaysLeft == nil {
		return []Reason{reason(CodeDomainUnknown, status.Domain, nil, 0, status.Error)}
	}

	days := *status.DaysLeft
	if days < 0 {
		return []Reason{reason(CodeDomainExpired, status.Domain, &days, 0, "")}
	}
	if threshold > 0 && days <= threshold {
		return []Reason{reason(CodeDomainExpiring, status.Domain, &days, 0, "")}
	}
	return nil
}

func certReasons(status checks.TLSStatus, threshold int) []Reason {
	if !status.Valid {
		// Истёкший сертификат — тоже невалидный, но говорить о нём надо
		// иначе: «истёк вчера» человек чинит одной командой, а «не тому
		// домену» — переустановкой.
		if status.DaysLeft != nil && *status.DaysLeft < 0 {
			days := *status.DaysLeft
			return []Reason{reason(CodeCertExpired, status.Domain, &days, 0, status.Error)}
		}
		return []Reason{reason(CodeCertInvalid, status.Domain, nil, 0, status.Error)}
	}
	if status.DaysLeft == nil {
		return nil
	}

	days := *status.DaysLeft
	if days < 0 {
		return []Reason{reason(CodeCertExpired, status.Domain, &days, 0, "")}
	}
	if threshold > 0 && days <= threshold {
		return []Reason{reason(CodeCertExpiring, status.Domain, &days, 0, "")}
	}
	return nil
}

func linkReasons(report links.Report) []Reason {
	if report.Error != "" {
		return []Reason{reason(CodeLinksFailed, report.Domain, nil, 0, report.Error)}
	}
	if report.Broken > 0 {
		return []Reason{reason(CodeLinksBroken, report.Domain, nil, report.Broken, "")}
	}
	return nil
}

func seoReasons(scan seo.Scan) []Reason {
	var out []Reason

	// Неудачная попытка и показанный обход — разные вещи, и говорить надо
	// об обеих: `Error` заполнен, когда состоявшихся обходов не было вовсе,
	// `FailedError` — когда последняя попытка после удачного обхода упала.
	switch {
	case scan.Error != "":
		out = append(out, reason(CodeSeoFailed, scan.Domain, nil, 0, scan.Error))
	case scan.FailedError != "":
		out = append(out, reason(CodeSeoFailed, scan.Domain, nil, 0, scan.FailedError))
	}

	// Только критичные. Предупреждения и замечания — гигиена, и поднимать
	// ими статус значит держать его жёлтым вечно.
	if scan.Stats.Issues.Critical > 0 {
		out = append(out, reason(CodeSeoCritical, scan.Domain, nil, scan.Stats.Issues.Critical, ""))
	}

	// Находки входа — по одной строке на каждую, а не одним числом: их
	// не бывает больше трёх, и каждая чинится своей настройкой. «Проблем со
	// входом: 2» отправило бы человека угадывать, каких именно.
	if scan.Stats.Canonical != nil {
		for _, issue := range scan.Stats.Canonical.Issues {
			out = append(out, reason(CodeSeoCanonical, scan.Domain, nil, 0, issue.Code))
		}
	}
	if scan.Stats.Site != nil {
		for _, issue := range scan.Stats.Site.Issues {
			if issue.Code == seo.IssueSearchEnginesBlocked {
				out = append(out, reason(CodeSeoBlocked, scan.Domain, nil, 0, issue.Detail))
			}
		}
	}
	return out
}

// vitalsReasons — про то, что видят живые посетители.
//
// Только field-данные и только оценка «плохо». Lab — одна загрузка в
// лаборатории Google, и поднимать по ней статус значит будить человека
// из-за того, чего его посетители могли не заметить; ранжирует Google по
// field. «Средне» тоже молчит: это работа на потом, а свод отвечает на
// вопрос «нужно ли что-то делать сегодня».
//
// Неудача замера поводом не становится вовсе. У неё три обычные причины —
// кончилась суточная квота Google, у страницы мало посетителей, ключа нет
// совсем, — и ни одна не про сайт клиента. Экран замеров говорит о них
// прямо и подробно, а в своде они были бы шумом за нашей подписью.
func vitalsReasons(domain string, reports []vitals.Report) []Reason {
	poor := 0
	for _, report := range reports {
		if report.Field == nil {
			continue
		}
		for _, metric := range []*vitals.Metric{
			report.Field.LCP, report.Field.INP, report.Field.CLS,
		} {
			if metric != nil && metric.Rating == vitals.RatingPoor {
				poor++
			}
		}
	}
	if poor == 0 {
		return nil
	}
	return []Reason{reason(CodeVitalsPoor, domain, nil, poor, "")}
}

func reason(code, domain string, days *int, count int, detail string) Reason {
	level, ok := levelByCode[code]
	if !ok {
		// Кода нет в таблице — это ошибка программиста, и её ловит тест.
		// В рантайме честнее позвать человека, чем промолчать.
		level = LevelAttention
	}
	return Reason{Code: code, Level: level, Domain: domain, Days: days, Count: count, Detail: detail}
}

// worst — уровень худшего повода. Приглушённые не в счёт: в этом и смысл
// приглушения.
func worst(reasons []Reason) Level {
	level := LevelOK
	for _, item := range reasons {
		if item.Suppressed {
			continue
		}
		if rank(item.Level) > rank(level) {
			level = item.Level
		}
	}
	return level
}

func rank(level Level) int {
	switch level {
	case LevelProblem:
		return 2
	case LevelAttention:
		return 1
	default:
		return 0
	}
}

func sortReasons(reasons []Reason) {
	sort.SliceStable(reasons, func(i, j int) bool {
		if rank(reasons[i].Level) != rank(reasons[j].Level) {
			return rank(reasons[i].Level) > rank(reasons[j].Level)
		}
		if reasons[i].Domain != reasons[j].Domain {
			return reasons[i].Domain < reasons[j].Domain
		}
		return reasons[i].Code < reasons[j].Code
	})
}

func later(known time.Time, candidate time.Time) time.Time {
	if candidate.After(known) {
		return candidate
	}
	return known
}

func parseTime(value string) time.Time {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return at
}
