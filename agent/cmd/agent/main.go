// Команда agent — то, что крутится на VPS клиента рядом с его сайтом.
//
// Занятия, каждое в своём ритме:
//
//   - раз в минуту снимает замер сервера и кладёт в SQLite;
//   - раз в несколько часов проверяет сроки доменов и сертификатов;
//   - раз в сутки обходит сайты и ищет битые ссылки;
//   - раз в сутки спрашивает у Google, что с производительностью сайтов;
//   - раз в сутки обходит сайты и снимает структуру их страниц;
//   - раз в минуту дочитывает логи веб-сервера и считает в них ошибки;
//   - раз в несколько минут ходит по ключевым адресам проектов и опрашивает
//     соседей по docker-сети;
//   - когда что-то не так, сам пишет письмо или в Telegram.
//
// Приложение агентства ходит к нему по HTTP через SSH-туннель. Наружу агент
// не смотрит и ничего никуда не отправляет, кроме уведомлений, адреса
// которых задал сам пользователь.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"agent/internal/bots"
	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/digest"
	"agent/internal/events"
	"agent/internal/health"
	"agent/internal/httpapi"
	"agent/internal/links"
	"agent/internal/logs"
	"agent/internal/metrics"
	"agent/internal/notify"
	"agent/internal/outage"
	"agent/internal/probe"
	"agent/internal/seo"
	"agent/internal/store"
	"agent/internal/vitals"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("настройка: %v", err)
	}

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("хранилище: %v", err)
	}
	defer db.Close()

	// Часовой пояс сервера клиента: nginx и Apache пишут в error-лог
	// местное время без смещения, и без зоны каждое событие уезжало бы на
	// смещение сервера. Не определился — остаёмся в UTC и говорим об этом:
	// молча считать чужой сервер UTC-шным нельзя.
	if zone, ok := logs.DetectLocation(cfg.HostRoot); ok {
		logs.SetLocation(zone)
		log.Printf("часовой пояс сервера: %s", zone)
	} else {
		log.Printf("часовой пояс сервера не определён, времена логов читаются как UTC")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agent := &Agent{
		cfg:       cfg,
		db:        db,
		collector: metrics.NewCollector(cfg.HostProc, cfg.HostRoot),
		domains:   checks.NewDomainChecker(),
		notifier:  notify.New(cfg),
		rootCtx:   ctx,
	}

	// Первая отметка в журнале — собственный запуск.
	//
	// Сам по себе агент не перезапускается: его запуск — это либо
	// обновление стека, либо перезагрузка сервера, либо падение контейнера.
	// В разборе аварии это первое, на что смотрят, а больше об этом
	// рассказать некому: приложение в этот момент закрыто.
	agent.note(events.KindAgentStarted, "", "")

	server := httpapi.Listen(cfg.Listen, httpapi.New(cfg.Token, agent).Handler())

	var wg sync.WaitGroup
	wg.Add(9)
	go func() { defer wg.Done(); agent.sampleLoop(ctx) }()
	go func() { defer wg.Done(); agent.checkLoop(ctx) }()
	go func() { defer wg.Done(); agent.linksLoop(ctx) }()
	go func() { defer wg.Done(); agent.vitalsLoop(ctx) }()
	go func() { defer wg.Done(); agent.logsLoop(ctx) }()
	go func() { defer wg.Done(); agent.seoLoop(ctx) }()
	go func() { defer wg.Done(); agent.probeLoop(ctx) }()
	go func() { defer wg.Done(); agent.digestLoop(ctx) }()
	go func() { defer wg.Done(); agent.backupLoop(ctx) }()

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	log.Printf("агент слушает %s, база %s", cfg.Listen, cfg.DatabasePath)

	// API не поднялся — агент останавливается целиком, а не работает
	// глухим.
	//
	// Занятый порт или отобранное право слушать означают, что приложение
	// больше никогда до нас не достучится: экран покажет «агент не
	// отвечает» про живого агента, снимающего замеры и шлющего письма, и
	// единственным следом останется строка в логе на сервере клиента —
	// то есть человек получит **неверный диагноз** и пойдёт чинить не то.
	//
	// Выходим с ошибкой: docker перезапустит контейнер и покажет причину в
	// его состоянии — там её увидит и `docker ps`, и наша же диагностика
	// стека. Круги при этом гасятся по-честному, через отмену контекста:
	// оборванный посреди записи круг копии или обхода стоил бы больше, чем
	// секунда ожидания.
	failed := false
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http: %v", err)
		failed = true
		stop()
	}

	wg.Wait()
	// Ручной или плановый круг — ссылок или замеров производительности —
	// мог быть в процессе: он живёт вне wg, потому что HTTP-обработчик и
	// тикер запускают его в разное время. Ждём его отдельно, чтобы
	// db.Close() выше не оборвал запись отчёта.
	agent.background.Wait()

	if failed {
		log.Print("агент остановлен: без API он бесполезен")
		// Закрываем базу руками: до `defer db.Close()` выход по коду не
		// доходит, а недописанный SQLite — это потерянная история замеров.
		db.Close()
		os.Exit(1)
	}

	log.Print("агент остановлен")
}

// Agent сводит вместе всё, что делает агент.
type Agent struct {
	cfg       config.Config
	db        *store.Store
	collector *metrics.Collector
	domains   *checks.DomainChecker
	notifier  *notify.Notifier

	// Контекст, который гасится по SIGINT/SIGTERM. Циклы получают его
	// параметром явно, но круг ссылок стартует и из HTTP-обработчика — ему
	// неоткуда взять ctx запроса, поэтому он держит его здесь.
	rootCtx context.Context

	// Проверки идут по сети и бывают долгими: не даём запустить два круга
	// сразу, когда пользователь нажал «проверить» посреди планового круга.
	checking sync.Mutex

	// Краулинг идёт минуты: ручной запуск отвечает сразу, а экран
	// опрашивает состояние, пока крутится этот флаг.
	linkRunning atomic.Bool

	// То же и с замерами Core Web Vitals: PSI открывает каждую страницу
	// настоящим браузером и думает десятками секунд.
	vitalsRunning atomic.Bool

	// И с обходом сайта: сотни страниц с паузой между запросами — это
	// десятки минут, а не секунды.
	seoRunning atomic.Bool
	// Какой сайт обходится прямо сейчас. Экран показывает сайты вкладками
	// и обязан сказать, чья проверка идёт, — иначе кнопка соседнего сайта
	// просто не нажималась бы без объяснений.
	seoDomain atomic.Value

	// Круг ключевых адресов короткий, но его дёргает и хук «я задеплоил»:
	// деплой-скрипт клиента может позвать нас чаще, чем идёт круг.
	probeRunning atomic.Bool

	// Проход по логам короткий, но читать один файл вдвоём нельзя: плановый
	// проход и нажатая кнопка посчитали бы одни и те же строки дважды.
	logScanning sync.Mutex

	// Копия базы идёт минутами: дамп, архив, выгрузка по каналу VPS. Два
	// круга разом означали бы два pg_dump на одной базе и вдвое больше
	// трафика клиента.
	backupRunning atomic.Bool

	// Фоновые задачи вне циклов, которые основной wg не отслеживает —
	// сейчас это круг проверки ссылок. Без ожидания их отдельно остановка
	// агента обрывала бы недописанный отчёт вместе с закрытием базы.
	background sync.WaitGroup
}

// saving выполняет запись итога круга отдельным контекстом.
//
// Он отсчитывается от корневого, а не от истёкшего: круг, не уложившийся в
// отведённое время, обязан сохранить то, что успел. Иначе потолок круга
// съедает и частичный результат, и код `*Timeout`, ради которого его
// заводили: писать становится нечем ровно в тот момент, когда есть что
// сказать. Остановку агента такой контекст слушает по-прежнему — писать в
// закрывающуюся базу незачем.
//
// Один на все три круга — ссылки, замеры, обход. Три места, делающие одно и
// то же по-разному, однажды разойдутся, и разойдётся то, про которое
// забыли: этой правкой чинится ровно такое расхождение.
func (a *Agent) saving(write func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(a.rootCtx, saveTimeout)
	defer cancel()
	return write(ctx)
}

// ── Разнос стартов ───────────────────────────────────────────────────────

// Круги агента ходят по чужому проду, и стартовать залпом им нельзя.
//
// Раньше проверки, ссылки, замеры и обход начинали первый круг в первую же
// секунду после запуска — то есть после каждого обновления стека сайт
// клиента получал полный обход, проверку всех ссылок и круг замеров разом.
// Здесь эти старты разведены, и таблица одна на всех: разнос, размазанный
// по шести функциям, однажды сойдётся обратно, и заметит это сайт клиента,
// а не мы.
//
// Числа малы намеренно. Это не «реже беспокоить», а «не всё сразу»:
// человек, только что обновивший стек, вправе увидеть свежие данные в
// ближайшие минуты, а не завтра.
type task int

const (
	taskChecks task = iota
	taskProbes
	taskLinks
	taskVitals
	taskSeo
	taskBackup
)

func startDelay(which task) time.Duration {
	switch which {
	case taskChecks:
		// Проверки идут первыми и почти сразу: они спрашивают реестр и
		// TLS-порт, а не сайт клиента, и данные о сроках нужны экрану
		// немедленно.
		return 5 * time.Second
	case taskProbes:
		// Ключевые адреса — три запроса, и именно их хотят увидеть первыми
		// после обновления.
		return 30 * time.Second
	case taskLinks:
		return 3 * time.Minute
	case taskVitals:
		// Замеры идут к Google, а не к клиенту, но квота у ключа общая на
		// всё агентство, и десять серверов, стартовавших вместе, съедали
		// бы её залпом.
		return 6 * time.Minute
	case taskSeo:
		// Обход самый тяжёлый: сотни страниц с паузой между запросами.
		return 9 * time.Minute
	case taskBackup:
		// Копия идёт последней: она читает базу целиком и льёт мегабайты в
		// сеть с сервера клиента. Сайту клиента она при этом не мешает
		// вовсе — потому и стоит после кругов, которые по нему ходят.
		return 12 * time.Minute
	}
	return 0
}

// ── Журнал сервера ───────────────────────────────────────────────────────

// note записывает отметку в журнал.
//
// Неудача записи никого не останавливает: журнал — это объяснение задним
// числом, и терять из-за него замер, письмо или круг проверок нельзя.
//
// Контекст тот же, которым круги сохраняют свои итоги: отметка о круге,
// упёршемся в собственный потолок времени, обязана дойти до базы — иначе в
// ленте не окажется как раз того события, ради которого её открыли.
func (a *Agent) note(kind, target, detail string) {
	if err := a.saving(func(ctx context.Context) error {
		return a.db.SaveEvent(ctx, events.Event{
			At:     time.Now().UTC(),
			Kind:   kind,
			Target: target,
			Detail: detail,
		})
	}); err != nil {
		log.Printf("журнал: %v", err)
	}
}

// noteRound отмечает круг, ходивший на сайты клиента.
//
// Пустая ошибка — круг дошёл до конца. Подробности того, что именно не
// заладилось на каком сайте, живут в отчёте самого круга и на его экране:
// лента отвечает на вопрос «что происходило», а не «почему не получилось».
func (a *Agent) noteRound(kind string, err error) {
	detail := ""
	if err != nil {
		detail = events.DetailInterrupted
	}
	a.note(kind, "", detail)
}

// ── Замеры ───────────────────────────────────────────────────────────────

func (a *Agent) sampleLoop(ctx context.Context) {
	// Первый замер процессора не с чем сравнивать: снимаем его сразу, чтобы
	// следующий, через минуту, уже был осмысленным.
	if _, err := a.collector.Collect(); err != nil {
		log.Printf("первый замер: %v", err)
	}

	// Время работы сервера из последнего сохранённого замера. Нужно, чтобы
	// заметить перезагрузку: другого её следа у агента нет, а в разборе
	// аварии «сервер перезагрузился в 3:14» — половина ответа.
	var previousUptime uint64
	knownUptime := false
	if sample, ok, err := a.db.LatestSample(ctx); err != nil {
		log.Printf("последний замер: %v", err)
	} else if ok {
		previousUptime, knownUptime = sample.UptimeSeconds, true
	}

	ticker := time.NewTicker(a.cfg.SampleInterval)
	defer ticker.Stop()

	rotate := time.NewTicker(6 * time.Hour)
	defer rotate.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			sample, err := a.collector.Collect()
			if err != nil {
				log.Printf("замер: %v", err)
				continue
			}
			if err := a.db.SaveSample(ctx, sample); err != nil {
				log.Printf("запись замера: %v", err)
				continue
			}
			// Время работы сервера стало меньше, чем было, — значит его
			// перезагрузили. Проверка стоит после записи замера: отметка
			// без замера рядом с ней объясняет ровно ничего.
			if knownUptime && sample.UptimeSeconds < previousUptime {
				a.note(events.KindServerRebooted, "", "")
			}
			previousUptime, knownUptime = sample.UptimeSeconds, true
			a.notifyDisk(ctx, sample)

		case <-rotate.C:
			removed, err := a.db.Rotate(ctx, a.cfg.Retention)
			if err != nil {
				log.Printf("ротация: %v", err)
				continue
			}
			if removed > 0 {
				log.Printf("ротация: удалено замеров %d", removed)
			}
			// Журнал живёт ровно столько же, сколько замеры: событие,
			// к которому нечего приложить, ничего не объясняет.
			if removed, err := a.db.RotateEvents(ctx, a.cfg.Retention); err != nil {
				log.Printf("ротация журнала: %v", err)
			} else if removed > 0 {
				log.Printf("ротация: удалено событий %d", removed)
			}
			// История копий живёт по тому же сроку: запись о круге, к
			// которой нечего приложить, ничего не объясняет.
			if removed, err := a.db.RotateBackupRuns(ctx, a.cfg.Retention); err != nil {
				log.Printf("ротация истории копий: %v", err)
			} else if removed > 0 {
				log.Printf("ротация: удалено записей о копиях %d", removed)
			}
		}
	}
}

// notifyDisk смотрит только на диск: он единственный из показателей сервера
// не приходит в норму сам.
func (a *Agent) notifyDisk(ctx context.Context, sample metrics.Sample) {
	firing, resolved := checks.Evaluate(a.cfg.Alerts, a.words(), sample, nil, nil)
	a.dispatch(ctx, firing, resolved)
}

// ── Проверки ─────────────────────────────────────────────────────────────

func (a *Agent) checkLoop(ctx context.Context) {
	// Первый круг почти сразу: сервер мог простоять выключенным, и данные
	// нужны приложению немедленно, а не через шесть часов. Пауза здесь —
	// часть общего разноса стартов, а не осторожность: проверки ходят в
	// реестр и на TLS-порт, а не по страницам клиента.
	if !wait(ctx, startDelay(taskChecks)) {
		return
	}
	if _, err := a.runChecks(ctx); err != nil {
		log.Printf("проверки: %v", err)
	}

	ticker := time.NewTicker(a.cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := a.runChecks(ctx); err != nil {
				log.Printf("проверки: %v", err)
			}
		}
	}
}

// Result — то, что агент знает о сайтах.
type Result struct {
	Domains []checks.DomainStatus `json:"domains"`
	Certs   []checks.TLSStatus    `json:"certs"`
	// Когда закончился последний круг проверок.
	CheckedAt time.Time `json:"checkedAt"`
	// Настроенные каналы уведомлений: экрану надо показать, куда придут
	// письма, и предупредить, если никуда.
	Channels []string `json:"channels"`
	// Уведомления с этого сервера выключены человеком: каналы настроены, а
	// писем не будет, и экран сроков обязан это сказать (0.20.0).
	NotifyMuted bool `json:"notifyMuted"`
	// За сколько дней до срока агент начинает беспокоить. Экран красит
	// строки этими же числами, а не своей копией: порог настраивается, и
	// жёлтая строка при молчащем агенте (или наоборот) — вопрос «кому из
	// них верить» (трек U).
	DomainDays int `json:"domainDays"`
	CertDays   int `json:"certDays"`
}

// thresholds вписывает пороги сроков — те же, что у свода и у писем.
func (a *Agent) thresholds(result *Result) {
	result.DomainDays = checks.LargestStep(a.cfg.Alerts.DomainSteps)
	result.CertDays = a.cfg.Alerts.CertDays
}

func (a *Agent) runChecks(ctx context.Context) (Result, error) {
	a.checking.Lock()
	defer a.checking.Unlock()

	sites, err := a.cfg.Sites()
	if err != nil {
		return Result{}, err
	}

	result := Result{
		CheckedAt:   time.Now().UTC(),
		Channels:    a.notifier.Channels(),
		NotifyMuted: a.notifier.Muted(),
		Domains:     []checks.DomainStatus{},
		Certs:       []checks.TLSStatus{},
	}
	a.thresholds(&result)
	alive := make(map[string]bool, len(sites))

	for _, site := range sites {
		alive[site.Domain] = true

		// Проверки идут последовательно: сайтов у сервера единицы, а
		// параллельные обращения к whois быстро приводят к отказу.
		domain := a.domains.Check(ctx, site.Domain)
		cert := checks.CheckTLS(ctx, site.Domain, 443)

		result.Domains = append(result.Domains, domain)
		result.Certs = append(result.Certs, cert)

		if err := a.db.SaveCheck(ctx, "domain", site.Domain, domain); err != nil {
			log.Printf("сохранение проверки домена: %v", err)
		}
		if err := a.db.SaveCheck(ctx, "cert", site.Domain, cert); err != nil {
			log.Printf("сохранение проверки сертификата: %v", err)
		}
	}

	// Край собственного стека — такая же проверка, как сайты клиента
	// (свой сертификат мониторинга). В режиме своего сертификата продлевает
	// его человек, и молчаливое протухание оборвало бы сбор статистики со
	// всех сайтов сервера разом. В обычном режиме Caddy продлевает сам —
	// и тогда эта же проверка показывает, что автопродление работает: не
	// сработало, письмо придёт за две недели, а не через три месяца.
	//
	// Самоподписанный край (`tls internal`, стенд и локальные домены) не
	// проверяем: браузер ему не доверяет по определению, и тревога
	// «сертификат: проблема» висела бы на нём вечно (аудит 2026-09-22).
	if edge := a.cfg.Probes.TrackingDomain; edge != "" && !a.cfg.Probes.EdgeSelfSigned {
		port := a.cfg.Probes.EdgePort
		if port == 0 {
			port = 443
		}
		// Прямо к своему Caddy, а не туда, куда смотрит DNS: за прокси
		// Cloudflare иначе проверялся бы чужой сертификат.
		cert := checks.CheckTLSAt(ctx, edge, port,
			net.JoinHostPort(probe.ServiceCaddy, strconv.Itoa(port)))
		alive[cert.Target()] = true
		result.Certs = append(result.Certs, cert)

		if err := a.db.SaveCheck(ctx, "cert", cert.Target(), cert); err != nil {
			log.Printf("сохранение проверки сертификата стека: %v", err)
		}
	}

	// Сайт удалили — его проверки и его тревоги больше не наши.
	for _, kind := range []string{"domain", "cert"} {
		if err := a.db.ForgetChecks(ctx, kind, alive); err != nil {
			log.Printf("уборка проверок: %v", err)
		}
	}

	firing, resolved := checks.Evaluate(a.cfg.Alerts, a.words(), metrics.Sample{}, result.Domains, result.Certs)
	a.dispatch(ctx, firing, resolved)

	return result, nil
}

// ── Оповещения ───────────────────────────────────────────────────────────

// dispatch решает, о чём человеку сообщать, а о чём он уже знает.
//
// Правило простое: о новой беде говорим сразу, о старой — не чаще, чем раз в
// сутки, о том, что починилось, — один раз. Без этого одна и та же проблема
// пишет письмо каждую минуту, и письма перестают читать — а вместе с ними
// пропускают и настоящую беду.
func (a *Agent) dispatch(ctx context.Context, firing, resolved []checks.Alert) {
	if a.notifier.Silent() {
		return
	}

	// Идёт обслуживание — молчим. Иначе обновление стека объявит само себя
	// аварией: контейнеры перезапускаются, сертификат на секунду
	// недоступен, замеры не снимаются.
	//
	// Состояние тревоги при этом **не записывается**, и это важно: когда
	// окно кончится, беда, которая не рассосалась, будет выглядеть свежей и
	// уйдёт письмом. Запиши мы её сейчас как «сообщено», человек не узнал
	// бы о ней вовсе — приглушение превратилось бы в потерю.
	if a.maintaining(ctx) {
		return
	}

	for _, alert := range firing {
		state, err := a.db.AlertState(ctx, alert.Kind, alert.Target)
		if err != nil {
			log.Printf("состояние тревоги: %v", err)
			continue
		}

		// Ступенчатая тревога живёт по своему правилу: письмо на каждой
		// ступени и ни одного между ними. Общий повтор раз в сутки для неё
		// означал бы тридцать писем про домен, который кончается через
		// тридцать дней, — то есть ровно то, от чего ступени и заводились.
		if alert.Step > 0 {
			told, err := a.db.AlertStep(ctx, alert.Kind, alert.Target)
			if err != nil {
				log.Printf("ступень напоминания: %v", err)
				continue
			}
			// Ступени идут по убыванию: о ступени говорим, только если она
			// ближе той, о которой уже говорили. Ноль означает, что не
			// говорили ни разу.
			if told > 0 && alert.Step >= told {
				continue
			}
		} else {
			fresh := !state.Firing
			stale := state.Firing && time.Since(state.LastSent) >= a.cfg.Alerts.RepeatAfter
			if !fresh && !stale {
				continue
			}
		}

		if err := a.notifier.Send(ctx, alert.Subject, alert.Body); err != nil {
			log.Printf("отправка уведомления: %v", err)
			continue
		}
		if err := a.db.SaveAlertState(ctx, alert.Kind, alert.Target,
			store.AlertState{Firing: true, LastSent: time.Now().UTC()}); err != nil {
			log.Printf("запись состояния тревоги: %v", err)
		}
		if alert.Step > 0 {
			if err := a.db.SaveAlertStep(ctx, alert.Kind, alert.Target, alert.Step); err != nil {
				log.Printf("запись ступени напоминания: %v", err)
			}
		}
		// Отметка ставится там же, где ставится состояние: «агент позвал
		// человека» — это факт, а не второй пересчёт беды. Заведи мы
		// журналу собственное состояние тревоги, два состояния однажды
		// разошлись бы, и разошлось бы то, про которое забыли.
		a.note(events.KindAlertFiring, alert.Shown(), alert.Kind)
	}

	for _, alert := range resolved {
		state, err := a.db.AlertState(ctx, alert.Kind, alert.Target)
		if err != nil || !state.Firing {
			// Не горело — и сообщать не о чем.
			continue
		}

		if err := a.notifier.Send(ctx, alert.Subject, alert.Body); err != nil {
			log.Printf("отправка уведомления: %v", err)
			continue
		}
		if err := a.db.SaveAlertState(ctx, alert.Kind, alert.Target,
			store.AlertState{Firing: false, LastSent: time.Now().UTC()}); err != nil {
			log.Printf("запись состояния тревоги: %v", err)
		}
		// Беда кончилась — счёт ступеней начинается заново. Продлённый
		// домен через год обязан напомнить о себе с самой дальней ступени,
		// а не молчать до последней.
		if err := a.db.ForgetAlertStep(ctx, alert.Kind, alert.Target); err != nil {
			log.Printf("сброс ступени напоминания: %v", err)
		}
		a.note(events.KindAlertResolved, alert.Shown(), alert.Kind)
	}
}

// maintaining — идёт ли сейчас обслуживание.
//
// Неудачу чтения считаем «не идёт»: сломанная база не повод замолчать о
// кончающемся диске. Ошибиться в эту сторону значит прислать лишнее письмо,
// в другую — промолчать о настоящей беде.
func (a *Agent) maintaining(ctx context.Context) bool {
	_, active, err := a.db.MaintenanceWindow(ctx, time.Now())
	if err != nil {
		log.Printf("окно обслуживания: %v", err)
		return false
	}
	return active
}

// ── Еженедельный отчёт ───────────────────────────────────────────────────

// Как часто смотреть на часы. Пятнадцать минут — потому что отчёт привязан
// к часу, а не к минуте: попасть в нужный час так нельзя только на сервере,
// который эти пятнадцать минут не работал.
const digestTick = 15 * time.Minute

// Ключ, под которым помнится отправка отчёта.
//
// В той же таблице, что и тревоги: «когда мы последний раз писали человеку»
// — один и тот же вопрос, и заводить под него вторую таблицу незачем.
const (
	digestKind   = "digest"
	digestTarget = "weekly"
)

// digestLoop раз в неделю рассказывает, как дела.
//
// День и час задаёт человек, часовой пояс берётся у сервера — тот самый,
// который агент уже определил для разбора логов. Отчёт в три ночи
// воскресенья бесполезен, а «по UTC» на московском сервере означает ровно
// это.
func (a *Agent) digestLoop(ctx context.Context) {
	if !a.cfg.Digest.Enabled {
		log.Print("еженедельный отчёт выключен")
		return
	}

	log.Printf("еженедельный отчёт: %s, %d:00 по времени сервера",
		a.cfg.Digest.Weekday, a.cfg.Digest.Hour)

	ticker := time.NewTicker(digestTick)
	defer ticker.Stop()

	// Первый круг — не сразу, а через тик. Агент, только что запущенный,
	// ещё не снял ни одного замера, и отчёт, ушедший в эту секунду, честно
	// сообщил бы «замеров нет вовсе» — то есть рассказал бы человеку про
	// наш собственный перезапуск вместо недели. Поймано живым прогоном на
	// стенде: письмо ушло через десять секунд после старта именно с этой
	// строкой.
	//
	// Цена — агент, запущенный в последние пятнадцать минут выбранного
	// часа, пропустит отчёт этой недели. Это лучше, чем еженедельно
	// рассылать отчёт о перезапуске: у первого случай редкий, у второго —
	// каждое обновление стека.
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.maybeSendDigest(ctx)
		}
	}
}

// maybeSendDigest отправляет отчёт, если пришло время и на этой неделе его
// ещё не было.
func (a *Agent) maybeSendDigest(ctx context.Context) {
	now := time.Now().In(logs.Location())
	if now.Weekday() != a.cfg.Digest.Weekday || now.Hour() != a.cfg.Digest.Hour {
		return
	}

	state, err := a.db.AlertState(ctx, digestKind, digestTarget)
	if err != nil {
		log.Printf("состояние отчёта: %v", err)
		return
	}
	// Шесть суток, а не семь: круг тикает каждые пятнадцать минут, и ровно
	// семь суток означали бы, что отчёт, ушедший в 10:00, на следующей
	// неделе не уйдёт в 10:00 — он не «старше семи суток» ни на минуту.
	if !state.LastSent.IsZero() && time.Since(state.LastSent) < 6*24*time.Hour {
		return
	}

	// Обслуживание гасит и отчёт: человек, обновляющий стек в воскресенье
	// утром, не должен получить письмо о том, что его же обновление сломало.
	if a.maintaining(ctx) {
		log.Print("еженедельный отчёт отложен: идёт обслуживание")
		return
	}

	if a.notifier.Silent() {
		// Каналов нет или сервер выключен — отправлять некуда, и отметку
		// ставить нельзя: иначе первый настроенный канал будет ждать отчёта
		// неделю.
		return
	}

	snapshot, err := a.snapshot(ctx)
	if err != nil {
		log.Printf("еженедельный отчёт: %v", err)
		return
	}

	text := digest.Build(a.cfg.Locale, config.HostName(a.cfg.HostRoot), snapshot)

	if err := a.notifier.SendReport(ctx, text.Subject, text.Body); err != nil {
		log.Printf("отправка отчёта: %v", err)
		return
	}
	// Отметка ставится после отправки, а не вместе с решением отправить, —
	// то же правило, по которому отметка об уведомлении о чёрных списках
	// двигается только тем, кто написал. Неудачная отправка не должна
	// съедать неделю.
	if err := a.db.SaveAlertState(ctx, digestKind, digestTarget,
		store.AlertState{Firing: false, LastSent: time.Now().UTC()}); err != nil {
		log.Printf("запись отправки отчёта: %v", err)
	}
	a.note(events.KindDigestSent, "", "")
	log.Print("еженедельный отчёт отправлен")
}

// ── То, что видит приложение ─────────────────────────────────────────────

// Snapshot — состояние сервера сейчас и его история.
type Snapshot struct {
	Current *metrics.Sample  `json:"current,omitempty"`
	History []metrics.Sample `json:"history"`
	// Пороги, при которых агент начинает беспокоить: экран показывает их
	// рядом с числами, чтобы «85%» не выглядело выдумкой.
	DiskAlertPercent int `json:"diskAlertPercent"`
}

func (a *Agent) Snapshot(hours int) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	history, err := a.db.Samples(ctx, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return nil, err
	}

	// Пустой срез, а не nil: Go сериализует nil как null, и разбор на
	// стороне приложения спотыкается о него на ровном месте. Обещали
	// список — отдаём список, пусть и пустой.
	if history == nil {
		history = []metrics.Sample{}
	}

	snapshot := Snapshot{History: history, DiskAlertPercent: a.cfg.Alerts.DiskPercent}
	if len(history) > 0 {
		snapshot.Current = &history[len(history)-1]
	}
	return snapshot, nil
}

func (a *Agent) Checks() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result := Result{
		Channels:    a.notifier.Channels(),
		NotifyMuted: a.notifier.Muted(),
		Domains:     []checks.DomainStatus{},
		Certs:       []checks.TLSStatus{},
	}
	a.thresholds(&result)

	domains, err := a.db.Checks(ctx, "domain")
	if err != nil {
		return nil, err
	}
	certs, err := a.db.Checks(ctx, "cert")
	if err != nil {
		return nil, err
	}

	for _, raw := range domains {
		var status checks.DomainStatus
		if err := json.Unmarshal(raw, &status); err == nil {
			result.Domains = append(result.Domains, status)
			if status.CheckedAt.After(result.CheckedAt) {
				result.CheckedAt = status.CheckedAt
			}
		}
	}
	for _, raw := range certs {
		var status checks.TLSStatus
		if err := json.Unmarshal(raw, &status); err == nil {
			result.Certs = append(result.Certs, status)
		}
	}

	return result, nil
}

func (a *Agent) RunChecks() (any, error) {
	// Круг проверок ходит в whois и по TLS: минуты, а не секунды.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()

	return a.runChecks(ctx)
}

// SendTest отправляет проверочное сообщение в названный канал (пусто — во
// все настроенные) и отвечает, куда оно ушло.
func (a *Agent) SendTest(channel string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	host := config.HostName(a.cfg.HostRoot)
	return a.notifier.SendTo(ctx, channel, a.words().TestSubject, fmt.Sprintf(a.words().TestBody, host))
}

// ── Свод состояния ───────────────────────────────────────────────────────

// Через сколько замер считается несвежим.
//
// Пять интервалов — это уже не «мы пропустили один круг», а «агент встал».
// Минимум в четверть часа держит порог осмысленным на серверах, где замеры
// снимают редко.
func (a *Agent) metricsStaleAfter() time.Duration {
	stale := 5 * a.cfg.SampleInterval
	if stale < 15*time.Minute {
		return 15 * time.Minute
	}
	return stale
}

// Health отдаёт свод: состояние сервера и состояние каждого проекта.
//
// Нового источника данных здесь нет ни одного — всё уже лежит в базе агента.
// Новое только одно: собранное названо словами, о которых можно говорить
// целиком, и порядок «за что браться первым» посчитан здесь, а не на экране.
func (a *Agent) Health() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	return a.snapshot(ctx)
}

// snapshot считает свод.
//
// Отдельно от Health, потому что читателей у свода двое: API приложения и
// еженедельный отчёт. Посчитай отчёт по-своему — и человек получил бы
// письмо, не совпадающее с экраном, на который он посмотрит через минуту.
func (a *Agent) snapshot(ctx context.Context) (health.Snapshot, error) {
	projects, err := a.cfg.Projects()
	if err != nil {
		return health.Snapshot{}, err
	}

	// Час снимка берётся один раз и переиспользуется: окно всплеска
	// выравнивается по нему же, и «за сколько минут сосчитано» обязано
	// отвечать тому же мгновению, что и остальной свод.
	now := time.Now()

	in := health.Input{
		Now:               now,
		DiskAlertPercent:  a.cfg.Alerts.DiskPercent,
		MetricsStaleAfter: a.metricsStaleAfter(),
		CertDays:          a.cfg.Alerts.CertDays,
		// Своду ступени не нужны: он отвечает на вопрос «пора ли
		// беспокоиться», а не «пора ли писать письмо». Порог у него самый
		// дальний из ступеней — тот же день, с которого начинает
		// беспокоиться и агент.
		DomainDays:           checks.LargestStep(a.cfg.Alerts.DomainSteps),
		LogErrorThreshold:    a.cfg.Logs.AlertThreshold,
		LogNotFoundThreshold: a.cfg.Logs.NotFoundThreshold,
		LogWindowMinutes:     logs.WindowMinutes(now, a.cfg.Logs.AlertWindow),
		Projects:             projects,
		Channels:             a.notifier.Channels(),
		NotifyMuted:          a.notifier.Muted(),
		Backup:               a.backupState(ctx),
	}

	// Всплески читаются за то же окно, за какое уходит письмо: два окна
	// означали бы, что экран и письмо спорят о том, авария это или фон.
	if spikes, err := a.db.LogSpikes(ctx, logs.WindowStart(now, a.cfg.Logs.AlertWindow)); err != nil {
		log.Printf("свод: ошибки в логах: %v", err)
	} else {
		in.LogSpikes = spikes
	}

	if sample, ok, err := a.db.LatestSample(ctx); err != nil {
		return health.Snapshot{}, err
	} else if ok {
		in.Sample = &sample
	}

	// Приглушения и обслуживание читаются вместе со всем остальным: свод
	// обязан быть одним ответом, а не экраном, который сначала показывает
	// красное, а через секунду его гасит.
	if suppressions, err := a.db.Suppressions(ctx, in.Now); err != nil {
		log.Printf("свод: приглушения: %v", err)
	} else {
		in.Suppressions = suppressions
	}
	if window, active, err := a.db.MaintenanceWindow(ctx, in.Now); err != nil {
		log.Printf("свод: окно обслуживания: %v", err)
	} else if active {
		in.Maintenance = &window
	}

	// Проверки, ссылки и обход читаются каждая своим запросом, и неудача
	// любой из них не отменяет остальные: свод без SEO полезнее, чем
	// отсутствие свода. Молчать о такой неудаче нельзя, поэтому она уходит
	// в лог агента — а на экране это будет видно как исчезнувший повод, не
	// как «всё хорошо»: пустой раздел свода означает, что о нём ничего не
	// известно, и приложение говорит об этом прямо.
	if raw, err := a.db.Checks(ctx, "domain"); err != nil {
		log.Printf("свод: сроки доменов: %v", err)
	} else {
		for _, item := range raw {
			var status checks.DomainStatus
			if err := json.Unmarshal(item, &status); err == nil {
				in.Domains = append(in.Domains, status)
			}
		}
	}
	if raw, err := a.db.Checks(ctx, "cert"); err != nil {
		log.Printf("свод: сертификаты: %v", err)
	} else {
		for _, item := range raw {
			var status checks.TLSStatus
			if err := json.Unmarshal(item, &status); err == nil {
				in.Certs = append(in.Certs, status)
			}
		}
	}

	alive := map[string]bool{}
	for _, project := range projects {
		for _, domain := range project.Domains {
			alive[domain] = true
		}
	}
	if reports, err := a.db.LinkReports(ctx, alive); err != nil {
		log.Printf("свод: битые ссылки: %v", err)
	} else {
		in.Links = reports
	}
	if scans, err := a.db.SeoScans(ctx, alive); err != nil {
		log.Printf("свод: обход сайтов: %v", err)
	} else {
		in.Seo = scans
	}
	if reports, err := a.db.VitalsReports(ctx, alive); err != nil {
		log.Printf("свод: замеры производительности: %v", err)
	} else {
		in.Vitals = reports
	}

	// Ключевые адреса живут по проектам, а не по доменам: их заводят по
	// одному-три на клиента, и лежать они могут на любом его домене.
	keys := map[store.ProbeKey]bool{}
	if profiles, err := a.cfg.ProbeProfiles(); err != nil {
		log.Printf("свод: ключевые адреса: %v", err)
	} else {
		for _, profile := range profiles {
			keys[store.KeyOfProfile(profile)] = true
		}
	}
	if results, err := a.db.ProbeResults(ctx, keys); err != nil {
		log.Printf("свод: проверки ключевых адресов: %v", err)
	} else {
		in.Probes = results
	}

	if raw, err := a.db.Checks(ctx, kindService); err != nil {
		log.Printf("свод: сервисы стека: %v", err)
	} else {
		for _, item := range raw {
			var service probe.Service
			if err := json.Unmarshal(item, &service); err == nil {
				in.Services = append(in.Services, service)
			}
		}
	}
	if raw, err := a.db.Checks(ctx, kindAnalytics); err != nil {
		log.Printf("свод: трекинг: %v", err)
	} else if item, ok := raw[targetTracker]; ok {
		var analytics probe.Analytics
		if err := json.Unmarshal(item, &analytics); err == nil {
			in.Analytics = &analytics
		}
	}

	return health.Compute(in), nil
}

// ── Лента изменений и разбор аварии ──────────────────────────────────────

// Сколько отметок отдавать за раз. Лента читается сверху, и человек,
// добравшийся до двухсотой строки, ищет уже не «что случилось вчера».
const eventsLimit = 200

// Сколько групп ошибок показывать в окне аварии.
//
// Меньше, чем на экране ошибок: там разбирают, что именно сыпется, здесь —
// узнают, сыпалось ли вообще. Длинный список в разборе аварии только
// заслоняет замеры и отметки журнала.
const outageGroupLimit = 20

// Journal — лента изменений сервера.
type Journal struct {
	// Время агента в UTC и расхождение с часами приложения.
	//
	// Без него ленту нельзя свести с журналом приложения: события сервера
	// записаны по его часам, события установки — по нашим, и разъезжаются
	// они на минуты. Сдвигает приложение, а считаем расхождение здесь —
	// только у агента есть обе половины.
	AgentTime   time.Time      `json:"agentTime"`
	SkewSeconds int            `json:"skewSeconds"`
	Events      []events.Event `json:"events"`
}

// Events отдаёт журнал сервера за последние часы.
func (a *Agent) Events(hours int, now time.Time) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	agentNow := time.Now().UTC()
	list, err := a.db.Events(ctx,
		agentNow.Add(-time.Duration(hours)*time.Hour), agentNow, eventsLimit)
	if err != nil {
		return nil, err
	}

	return Journal{
		AgentTime:   agentNow,
		SkewSeconds: int(outage.Skew(agentNow, now).Seconds()),
		Events:      list,
	}, nil
}

// Around собирает то, что было на сервере вокруг указанного времени.
//
// `at` приходит по часам приложения — инцидент записала наша инфраструктура
// снаружи, — а всё, что лежит здесь, записано по часам сервера. Поэтому
// окно сдвигается на расхождение часов, и наружу уходит как сдвиг, так и
// окно, которое агент действительно посмотрел: промахнуться мимо данных
// молча эта функция не вправе.
func (a *Agent) Around(at time.Time, minutes int, now time.Time) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	agentNow := time.Now().UTC()
	skew := outage.Skew(agentNow, now)

	half := time.Duration(minutes) * time.Minute / 2
	center := at.Add(skew)
	from := center.Add(-half).UTC()
	to := center.Add(half).UTC()

	window := outage.Window{
		AgentTime:   agentNow,
		SkewSeconds: int(skew.Seconds()),
		From:        from,
		To:          to,
		Samples:     []metrics.Sample{},
	}

	samples, err := a.db.SamplesBetween(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if samples != nil {
		window.Samples = samples
	}

	// Последний замер перед окном — то единственное, что известно об
	// аварии, в которой агент лежал вместе с сервером.
	if before, ok, err := a.db.SampleBefore(ctx, from); err != nil {
		return nil, err
	} else if ok {
		window.Before = &before
	}

	// Провалы считаются до «сейчас», а не до конца окна: будущее — это
	// будущее, а не молчание сервера.
	edge := to
	if edge.After(agentNow) {
		edge = agentNow
	}
	window.Gaps = outage.Gaps(samples, from, edge, a.cfg.SampleInterval)

	if window.LogGroups, err = a.db.LogGroupsBetween(ctx, from, to, outageGroupLimit); err != nil {
		return nil, err
	}
	if window.LogSeries, err = a.db.LogSeriesBetween(ctx, from, to); err != nil {
		return nil, err
	}
	if window.Events, err = a.db.Events(ctx, from, to, eventsLimit); err != nil {
		return nil, err
	}

	return window, nil
}

// ── Приглушение и обслуживание: то, что делает приложение ────────────────

// Suppress приглушает находку. Ноль дней — бессрочно.
func (a *Agent) Suppress(code, target string, days int, note string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var until time.Time
	if days > 0 {
		until = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	}
	if err := a.db.Suppress(ctx, code, target, until, note); err != nil {
		return nil, err
	}
	return a.snapshot(ctx)
}

// Unsuppress возвращает находку в свод и в отчёт.
func (a *Agent) Unsuppress(code, target string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.db.Unsuppress(ctx, code, target); err != nil {
		return nil, err
	}
	return a.snapshot(ctx)
}

// StartMaintenance открывает окно обслуживания на указанное число минут.
func (a *Agent) StartMaintenance(minutes int, note string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.db.StartMaintenance(ctx,
		time.Now().UTC().Add(time.Duration(minutes)*time.Minute), note); err != nil {
		return nil, err
	}
	// Окно обслуживания — как раз то, что объясняет тишину в замерах и
	// перезапуск контейнеров. Без отметки в ленте плановое обновление
	// выглядит через неделю точно так же, как авария.
	a.note(events.KindMaintenanceStarted, "", "")
	return a.snapshot(ctx)
}

// EndMaintenance закрывает окно досрочно.
func (a *Agent) EndMaintenance() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.db.EndMaintenance(ctx); err != nil {
		return nil, err
	}
	a.note(events.KindMaintenanceEnded, "", "")
	return a.snapshot(ctx)
}

// ── Битые ссылки ─────────────────────────────────────────────────────────

func (a *Agent) linksLoop(ctx context.Context) {
	// Первый круг — после своей паузы в общем разносе стартов: обход всех
	// сайтов сразу после запуска агента означал бы, что каждое обновление
	// стека бьёт по чужому проду полным краулингом.
	if !wait(ctx, startDelay(taskLinks)) {
		return
	}
	a.startLinkScan()

	ticker := time.NewTicker(a.cfg.Links.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.startLinkScan()
		}
	}
}

// startLinkScan запускает полный круг по всем сайтам в фоне.
//
// Круг идёт минуты и не должен держать ни цикл агента, ни HTTP-запрос
// приложения: таймауты у обоих короче. Возвращает false, если круг уже
// идёт — второй запуск ничего не сломает, но и смысла не имеет.
//
// Контекст круга — потомок rootCtx, а не голый Background: без этого
// остановка агента (SIGTERM при обновлении стека) не прерывала бы обход и
// проверку чужого сайта, а main() успевал бы закрыть базу под ещё пишущим
// в неё кругом. background.Wait() в main() дожидается его здесь же.
func (a *Agent) startLinkScan() bool {
	if !a.linkRunning.CompareAndSwap(false, true) {
		return false
	}

	a.background.Add(1)
	go func() {
		defer a.background.Done()
		defer a.linkRunning.Store(false)

		ctx, cancel := context.WithTimeout(a.rootCtx, 30*time.Minute)
		defer cancel()
		err := a.scanAllSites(ctx)
		if err != nil {
			log.Printf("проверка ссылок: %v", err)
		}
		a.noteRound(events.KindLinksScan, err)
	}()
	return true
}

// scanSite обходит один сайт и проверяет найденные страницы.
//
// Ошибка круга кладётся в сам отчёт, а не возвращается наверх: экран должен
// увидеть «не получилось», а не молчание о том, что данных нет.
func (a *Agent) scanSite(ctx context.Context, client *http.Client, domain string, excludes []*regexp.Regexp) links.Report {
	report, err := links.Scan(ctx, client, "https://"+domain+"/",
		links.Limits{
			MaxDepth: a.cfg.Links.MaxDepth,
			MaxPages: a.cfg.Links.MaxPages,
			Delay:    a.cfg.Links.Delay(),
			Exclude:  excludes,
			// Потолок чтения страницы общий с SEO-обходом: два числа для
			// одного и того же однажды разошлись бы.
			MaxBytes: a.cfg.Seo.MaxPageBytes,
		},
		links.Options{
			Binary:         a.cfg.Lychee,
			Concurrency:    a.cfg.Links.Concurrency,
			TimeoutSeconds: int(a.cfg.Links.Timeout / time.Second),
			Exclude:        a.cfg.Links.Exclude,
		})
	if err != nil {
		report.Error = linkErrorCode(ctx, err)
	}
	return report
}

// linkErrorCode сводит ошибку круга к стабильному коду для интерфейса.
// Текст ошибки может уехать в лог, но не в перевод: интерфейс двуязычный.
//
// Контекст круга спрашивается наравне с самой ошибкой, и спрашивается
// последним. Lychee, убитый нашим же таймаутом, возвращает `*exec.ExitError`
// («signal: killed»), а не ошибку контекста: `Cmd.Wait` подставляет ошибку
// контекста, только если процесс завершился успешно. Из-за этого круг, не
// уложившийся во время, показывался общим `linkScanFailed`, а ошибка
// контекста доезжала лишь в одном случае — когда время вышло **до** запуска
// lychee и её вернул `Cmd.Start`. Последним — потому что истёкший контекст
// не должен маскировать причины, которые мы называем точнее: «robots.txt не
// отдался» остаётся собой, даже если следом кончилось время.
func linkErrorCode(ctx context.Context, err error) string {
	message := err.Error()
	switch {
	case errors.Is(err, links.ErrRobotsUnavailable):
		return "linksRobotsUnavailable"
	case errors.Is(err, links.ErrRobotsForbidden):
		return "linksRobotsForbidden"
	case strings.Contains(message, "executable file not found"):
		return "lycheeUnavailable"
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "linkScanTimeout"
	default:
		return "linkScanFailed"
	}
}

// scanAllSites обходит и проверяет все сайты по очереди.
//
// Сайтов у сервера единицы, а краулинг нагружает их хозяина: параллелить
// обходы значит бить по чужому проду всеми сразу.
func (a *Agent) scanAllSites(ctx context.Context) error {
	sites, err := a.cfg.Sites()
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		return nil
	}

	excludes := links.CompileExcludes(a.cfg.Links.Exclude)
	client := links.HTTPClient(a.cfg.Links.Timeout)
	alive := make(map[string]bool, len(sites))

	for _, site := range sites {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		alive[site.Domain] = true

		report := a.scanSite(ctx, client, site.Domain, excludes)
		if report.Error != "" {
			log.Printf("проверка ссылок %s: %s", site.Domain, report.Error)
		}
		// Записываем отдельным контекстом, а не тем, которым обходили:
		// круг, упёршийся в свой потолок, иначе терял отчёт целиком —
		// вместе с кодом `linkScanTimeout`, который только что и поставил.
		if err := a.saving(func(save context.Context) error {
			return a.db.SaveLinkReport(save, report)
		}); err != nil {
			log.Printf("запись отчёта ссылок: %v", err)
		}
	}

	// Сайт удалили — его история больше не наша забота.
	if err := a.saving(func(save context.Context) error {
		return a.db.ForgetLinkReports(save, alive)
	}); err != nil {
		log.Printf("уборка отчётов ссылок: %v", err)
	}
	return nil
}

// ── Битые ссылки: то, что видит приложение ───────────────────────────────

// LinksState — состояние проверки ссылок для экрана.
type LinksState struct {
	// Идёт ли круг прямо сейчас. Ручной запуск отвечает мгновенно, и без
	// этого флага экран не отличил бы «работает» от «ничего не делаем».
	Running bool           `json:"running"`
	Reports []links.Report `json:"reports"`
}

func (a *Agent) Links() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sites, err := a.cfg.Sites()
	if err != nil {
		return nil, err
	}
	alive := make(map[string]bool, len(sites))
	for _, site := range sites {
		alive[site.Domain] = true
	}

	reports, err := a.db.LinkReports(ctx, alive)
	if err != nil {
		return nil, err
	}
	if reports == nil {
		reports = []links.Report{}
	}
	return LinksState{Running: a.linkRunning.Load(), Reports: reports}, nil
}

func (a *Agent) RunLinks() (any, error) {
	started := a.startLinkScan()
	return map[string]bool{"started": started}, nil
}

// ── Core Web Vitals ──────────────────────────────────────────────────────
//
// Меряем не мы: страницу открывает Google своим браузером по запросу PSI, а
// данные живых посетителей отдаёт CrUX. Агент только спрашивает, считает
// расход квоты и хранит историю — но спрашивает он с сервера клиента и
// ключом агентства, чтобы ни домены, ни ключ не проходили через нашу
// инфраструктуру.

// Один замер стоит до трёх обращений: PSI плюс CrUX по странице и, если её
// данных нет, по сайту целиком.
const requestsPerMeasure = 3

// Круг меряет десятки страниц по полминуты каждую. Потолок щедрый: круг
// всё равно ограничен суточной квотой, а обрывать его на середине значит
// оставить половину сайтов без свежих цифр.
const vitalsRunTimeout = 2 * time.Hour

func (a *Agent) vitalsLoop(ctx context.Context) {
	if !a.cfg.Vitals.Configured() {
		log.Print("замеры производительности выключены: нет ключа Google")
		return
	}

	// Первый круг — только если прошлый был давно, и всё равно не сразу. В
	// отличие от проверок доменов, замер стоит квоты и минут: перезапуск
	// агента после правки настроек не повод мерить всё заново.
	if !wait(ctx, startDelay(taskVitals)) {
		return
	}
	if a.vitalsStale(ctx) {
		a.startVitalsRun()
	}

	ticker := time.NewTicker(a.cfg.Vitals.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.startVitalsRun()
		}
	}
}

// vitalsStale — правда ли, что с прошлого круга прошло больше интервала.
func (a *Agent) vitalsStale(ctx context.Context) bool {
	latest, ok, err := a.db.LatestVitalsAt(ctx)
	if err != nil {
		log.Printf("время прошлого замера: %v", err)
		return true
	}
	return !ok || time.Since(latest) >= a.cfg.Vitals.Interval
}

// startVitalsRun запускает круг замеров в фоне.
//
// Как и круг ссылок: HTTP-обработчик отвечает сразу, а контекст берётся от
// rootCtx, чтобы остановка агента прерывала круг, а не оставляла его писать
// в закрытую базу.
func (a *Agent) startVitalsRun() bool {
	if !a.cfg.Vitals.Configured() {
		return false
	}
	if !a.vitalsRunning.CompareAndSwap(false, true) {
		return false
	}

	a.background.Add(1)
	go func() {
		defer a.background.Done()
		defer a.vitalsRunning.Store(false)

		ctx, cancel := context.WithTimeout(a.rootCtx, vitalsRunTimeout)
		defer cancel()
		err := a.measureAllSites(ctx)
		if err != nil {
			log.Printf("замеры производительности: %v", err)
		}
		a.noteRound(events.KindVitalsScan, err)
	}()
	return true
}

// vitalsPages — какие страницы сайта меряем: главная всегда, дальше то, что
// задало агентство, до потолка.
func (a *Agent) vitalsPages(domain string, extra map[string][]string) []string {
	pages := []string{"/"}
	for _, path := range extra[domain] {
		if len(pages) >= a.cfg.Vitals.MaxPages {
			break
		}
		pages = append(pages, path)
	}
	return pages
}

// measureAllSites обходит сайты, страницы и типы устройств по очереди.
//
// Последовательно, как и всё остальное у агента: параллельные обращения
// быстро упираются в ограничение частоты Google (у CrUX это 150 запросов в
// минуту на проект), а спешить круг, идущий раз в сутки, некуда.
func (a *Agent) measureAllSites(ctx context.Context) error {
	sites, err := a.cfg.Sites()
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		return nil
	}

	extra, err := a.cfg.Pages()
	if err != nil {
		// Испорченный список страниц не повод не мерить главные: они и есть
		// то, ради чего экран заводят.
		log.Printf("список страниц: %v", err)
		extra = map[string][]string{}
	}

	// Сутки спрашиваются заново на каждом замере, а не один раз на круг:
	// круг живёт до двух часов и легко переваливает за полночь UTC. С одним
	// значением на круг расход после полуночи писался бы во вчерашний
	// счёт — и завтрашняя квота начиналась бы уже початой, а вчерашняя
	// переполненной.
	day := store.Today()
	spent, err := a.db.VitalsSpent(ctx, day)
	if err != nil {
		return err
	}

	// Список составляется целиком до первого запроса, а не по ходу круга.
	// Иначе уборка в конце считала бы живыми только те страницы, до которых
	// круг успел дойти, и упёршийся в квоту круг стирал бы историю
	// остальных — собственным лимитом.
	type target struct{ domain, path, strategy string }
	var targets []target
	alive := map[string]bool{}

	for _, site := range sites {
		for _, path := range a.vitalsPages(site.Domain, extra) {
			for _, strategy := range a.cfg.Vitals.Strategies {
				targets = append(targets, target{site.Domain, path, strategy})
				alive[store.VitalsKey(site.Domain, path, strategy)] = true
			}
		}
	}

	// Сайт удалили или страницу убрали из списка — их история больше не
	// наша забота, и ждать конца круга, чтобы это заметить, незачем.
	if err := a.saving(func(save context.Context) error {
		return a.db.ForgetVitalsReports(save, alive)
	}); err != nil {
		log.Printf("уборка замеров: %v", err)
	}

	client := vitals.HTTPClient(a.cfg.Vitals.Timeout)

	for _, item := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Полночь UTC внутри круга: Google сбросил квоту, и мы вместе с
		// ним. Счёт нового дня читается из базы, а не обнуляется на месте:
		// вчерашний круг мог его уже начать.
		if today := store.Today(); today != day {
			day = today
			if spent, err = a.db.VitalsSpent(ctx, day); err != nil {
				return err
			}
		}

		// Квота кончилась — круг останавливается, а прошлые замеры
		// остаются как были. Затирать их отчётом «не мерили» значило бы
		// потерять историю из-за нашего же лимита.
		if spent+requestsPerMeasure > a.cfg.Vitals.DailyLimit {
			log.Printf("замеры производительности: суточный лимит %d обращений исчерпан",
				a.cfg.Vitals.DailyLimit)
			return nil
		}

		report, requests := vitals.Measure(ctx, client, vitals.Options{
			Key:      a.cfg.Vitals.APIKey,
			Strategy: item.strategy,
			Timeout:  a.cfg.Vitals.Timeout,
			Locale:   a.cfg.Locale,
		}, item.domain, item.path)

		spent += requests
		// Расход и сам замер пишутся отдельным контекстом, а не тем,
		// которым мерили. Круг живёт до двух часов, и упереться в потолок
		// он может ровно на последнем замере: с прежним рисунком запросы к
		// Google были потрачены, а суточный счёт их не видел — завтрашняя
		// квота начиналась заниженной, то есть наоборот тому, ради чего
		// расход и считается.
		if err := a.saving(func(save context.Context) error {
			return a.db.AddVitalsSpent(save, day, requests)
		}); err != nil {
			log.Printf("учёт квоты: %v", err)
		}
		if report.Error != "" {
			log.Printf("замер %s (%s): %s", report.URL, item.strategy, report.Error)
		}
		if err := a.saving(func(save context.Context) error {
			return a.db.SaveVitalsReport(save, report)
		}); err != nil {
			log.Printf("запись замера: %v", err)
		}
	}

	return nil
}

// ── Core Web Vitals: то, что видит приложение ────────────────────────────

// VitalsQuota — расход квоты Google за сутки.
type VitalsQuota struct {
	// Сутки UTC: по ним Google сбрасывает свою квоту.
	Day   string `json:"day"`
	Used  int    `json:"used"`
	Limit int    `json:"limit"`
}

// VitalsPage — сайт и страницы, которые у него меряются.
type VitalsPage struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	// Пути помимо главной: она меряется всегда и в список не входит.
	Paths []string `json:"paths"`
}

// VitalsState — состояние замеров для экрана.
type VitalsState struct {
	Running bool `json:"running"`
	// Настроен ли ключ. Без него экран говорит, чего не хватает, вместо
	// того чтобы показывать пустоту.
	KeyConfigured bool `json:"keyConfigured"`
	// Типы устройств и частота — экран показывает их рядом с цифрами,
	// чтобы «замер вчерашний» не выглядело поломкой.
	Strategies    []string        `json:"strategies"`
	IntervalHours int             `json:"intervalHours"`
	MaxPages      int             `json:"maxPages"`
	Quota         VitalsQuota     `json:"quota"`
	Pages         []VitalsPage    `json:"pages"`
	Reports       []vitals.Report `json:"reports"`
}

func (a *Agent) Vitals() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sites, err := a.cfg.Sites()
	if err != nil {
		return nil, err
	}
	extra, err := a.cfg.Pages()
	if err != nil {
		log.Printf("список страниц: %v", err)
		extra = map[string][]string{}
	}

	alive := map[string]bool{}
	pages := make([]VitalsPage, 0, len(sites))
	for _, site := range sites {
		paths := a.vitalsPages(site.Domain, extra)
		for _, path := range paths {
			for _, strategy := range a.cfg.Vitals.Strategies {
				alive[store.VitalsKey(site.Domain, path, strategy)] = true
			}
		}
		// Главная в списке страниц не показывается: она меряется всегда, и
		// убрать её нельзя — строка, которую нельзя удалить, только
		// путает.
		pages = append(pages, VitalsPage{
			Domain: site.Domain,
			Name:   site.Name,
			Paths:  append([]string{}, paths[1:]...),
		})
	}

	day := store.Today()
	used, err := a.db.VitalsSpent(ctx, day)
	if err != nil {
		return nil, err
	}

	reports, err := a.db.VitalsReports(ctx, alive)
	if err != nil {
		return nil, err
	}
	if reports == nil {
		reports = []vitals.Report{}
	}

	return VitalsState{
		Running:       a.vitalsRunning.Load(),
		KeyConfigured: a.cfg.Vitals.Configured(),
		Strategies:    a.cfg.Vitals.Strategies,
		IntervalHours: int(a.cfg.Vitals.Interval / time.Hour),
		MaxPages:      a.cfg.Vitals.MaxPages,
		Quota:         VitalsQuota{Day: day, Used: used, Limit: a.cfg.Vitals.DailyLimit},
		Pages:         pages,
		Reports:       reports,
	}, nil
}

func (a *Agent) RunVitals() (any, error) {
	return map[string]bool{"started": a.startVitalsRun()}, nil
}

// ── Логи веб-сервера ─────────────────────────────────────────────────────
//
// Веб-сервер клиента нам не принадлежит: мы его не ставили, не настраивали и
// подстраиваться под него не собираемся. Агент дочитывает файлы, которые уже
// лежат на диске, и только на чтение — а хранит из них не строки, а
// счётчики: лог за сутки это гигабайты на чужом диске, и в каждой строке
// access-лога стоит адрес посетителя.

func (a *Agent) logsLoop(ctx context.Context) {
	// Первый проход сразу: он берёт хвост каждого файла, и экран не пустует
	// первые минуты после установки.
	a.scanLogs(ctx)

	ticker := time.NewTicker(a.cfg.Logs.Interval)
	defer ticker.Stop()

	rotate := time.NewTicker(6 * time.Hour)
	defer rotate.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.scanLogs(ctx)
		case <-rotate.C:
			removed, err := a.db.RotateLogEvents(ctx, a.cfg.Logs.RetentionDays)
			if err != nil {
				log.Printf("ротация логов: %v", err)
				continue
			}
			if removed > 0 {
				log.Printf("ротация логов: удалено групп %d", removed)
			}
		}
	}
}

// logSources собирает список файлов: найденное плюс заданное человеком.
//
// Заданное человеком главнее: если он указал путь, который мы и так нашли,
// в списке должна остаться одна строка, и пометка «задан вручную» на ней.
func (a *Agent) logSources() []logs.Source {
	var sources []logs.Source
	seen := map[string]bool{}

	manual, err := a.cfg.LogSources()
	if err != nil {
		log.Printf("список логов: %v", err)
	}
	for _, path := range manual {
		seen[path] = true
		sources = append(sources, logs.Source{
			Path:   path,
			Kind:   logs.KindOf(path),
			Manual: true,
		})
	}

	if a.cfg.Logs.Autodiscover {
		for _, found := range logs.Discover(a.cfg.HostRoot, listNames) {
			if seen[found.Path] {
				continue
			}
			seen[found.Path] = true
			sources = append(sources, found)
		}
	}

	return sources
}

// listNames отдаёт имена файлов каталога. Отдельной функцией, чтобы поиск
// логов проверялся тестами без настоящей файловой системы сервера.
func listNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// scanLogs дочитывает все файлы и складывает найденное в счётчики.
func (a *Agent) scanLogs(ctx context.Context) {
	if !a.logScanning.TryLock() {
		// Проход уже идёт — второй ничего не добавит, а читать один файл
		// вдвоём значит посчитать его строки дважды.
		return
	}
	defer a.logScanning.Unlock()

	sources := a.logSources()
	positions, err := a.db.LogPositions(ctx)
	if err != nil {
		log.Printf("позиции в логах: %v", err)
		return
	}

	alive := make(map[string]bool, len(sources))

	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		alive[source.Path] = true

		// Путь на сервере, а файл открывается внутри контейнера: агент
		// живёт в контейнере, а читает диск сервера.
		full := filepath.Join(a.cfg.HostRoot, source.Path)

		batch := logs.NewBatch(logs.DefaultGroupLimit)
		// Роботы считаются тем же проходом: второе чтение файла — это вдвое
		// больше работы диска чужого сервера (трек V, Р20).
		robots := logs.NewRobots()
		position, failure := logs.Read(full, positions[source.Path].Position,
			a.cfg.Logs.MaxBytes, func(event logs.Event) {
				batch.AddInteresting(event)
				robots.Add(event)
			})

		if groups := batch.Groups(); len(groups) > 0 {
			if err := a.db.AddLogGroups(ctx, source.Path, groups); err != nil {
				log.Printf("запись событий лога: %v", err)
			}
		}
		if counts := robots.Counts(); len(counts) > 0 {
			if err := a.db.AddRobotCounts(ctx, counts); err != nil {
				log.Printf("запись счётчиков роботов: %v", err)
			}
		}
		if err := a.db.SaveLogPosition(ctx, source.Path, position, failure); err != nil {
			log.Printf("позиция в логе: %v", err)
		}
		if failure != "" && positions[source.Path].Error != failure {
			// Пишем в лог агента только смену состояния: файл без прав
			// иначе жаловался бы каждую минуту, вечно.
			log.Printf("лог %s: %s", source.Path, failure)
		}
	}

	// Файл убрали из списка — его позиция больше не наша забота. Счётчики
	// при этом остаются: они уже история, и стирать её из-за того, что
	// человек перестал следить за файлом, незачем — их уберёт ротация.
	if err := a.db.ForgetLogPositions(ctx, alive); err != nil {
		log.Printf("уборка позиций: %v", err)
	}

	a.notifyLogSpike(ctx)
}

// notifyLogSpike решает, пора ли беспокоить человека всплеском пятисоток.
//
// Порог считается по окну, а не по общему счёту: «двести ошибок за сутки» у
// большого сайта — фон, «двести за пятнадцать минут» — авария.
func (a *Agent) notifyLogSpike(ctx context.Context) {
	threshold := a.cfg.Logs.AlertThreshold
	if threshold <= 0 || a.notifier.Silent() {
		return
	}

	// Окно выравнивается по границе бакета, и названо в письме будет ровно
	// то, что сосчитано. В базе лежат счётчики за пять минут: взять из
	// бакета «только последние минуты» нечем, поэтому граница честная, а не
	// круглая.
	now := time.Now().UTC()
	spikes, err := a.db.LogSpikes(ctx, logs.WindowStart(now, a.cfg.Logs.AlertWindow))
	if err != nil {
		log.Printf("счёт ошибок в логах: %v", err)
		return
	}

	// Письмо уходит по серверу целиком, а не по домену: порог человек
	// задал один, а разрез по доменам есть далеко не всегда — формат
	// `combined` у nginx домена не пишет. Кому именно плохо, показывает
	// свод и экран ошибок; письмо будит.
	//
	// Ответы «страницы нет» здесь не считаются вовсе: они не будят. Их
	// всплеск виден в своде, и это осознанная разница — письмо про
	// пропавшую страницу пришло бы ночью и ничего бы не изменило.
	count := 0
	for _, spike := range spikes {
		count += spike.ServerErrors
	}

	minutes := logs.WindowMinutes(now, a.cfg.Logs.AlertWindow)
	alert := checks.Alert{
		Kind:    checks.KindLogs,
		Target:  "5xx",
		Subject: fmt.Sprintf(a.words().LogsSubject, count, minutes),
		Body:    fmt.Sprintf(a.words().LogsBody, minutes, count, threshold),
	}

	if count >= threshold {
		a.dispatch(ctx, []checks.Alert{alert}, nil)
		return
	}

	a.dispatch(ctx, nil, []checks.Alert{{
		Kind:    checks.KindLogs,
		Target:  "5xx",
		Subject: a.words().LogsOKSubject,
		Body:    fmt.Sprintf(a.words().LogsOKBody, minutes, count, threshold),
	}})
}

// ── Логи: то, что видит приложение ───────────────────────────────────────

// LogsState — ошибки из логов для экрана.
type LogsState struct {
	Sources []logs.SourceState `json:"sources"`
	Groups  []logs.Group       `json:"groups"`
	Series  []logs.Point       `json:"series"`
	// Сколько событий всего за показанный период.
	Total int `json:"total"`
	// Порог и окно тревоги: экран показывает их рядом с графиком, чтобы
	// «двести ошибок» не выглядели выдумкой.
	AlertThreshold int `json:"alertThreshold"`
	WindowMinutes  int `json:"windowMinutes"`
	RetentionDays  int `json:"retentionDays"`
	// Настроен ли хоть один канал уведомлений: без него порог ни к чему не
	// приводит, и сказать об этом надо прямо.
	Notifying bool `json:"notifying"`
	// Уведомления с этого сервера выключены человеком: экран говорит это, а
	// не «каналы не настроены» (0.20.0).
	NotifyMuted bool `json:"notifyMuted"`
}

// Сколько групп отдавать экрану. Больше человек всё равно не прочитает, а
// туннель к агенту не резиновый.
const logGroupsShown = 200

// ── Роботы по логам: экран «Агенты и ИИ» (трек V, Р20) ───────────────────

// RobotVisit — сколько раз робот приходил на сайт и когда в последний раз.
type RobotVisit struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	LastAt string `json:"lastAt"`
}

// RobotHost — роботы одного сайта. Пустой домен — сервер целиком: формат
// лога домена не пишет, и отнести визит к сайту нечем.
type RobotHost struct {
	Host string `json:"host"`
	// Строки access-лога с полем User-agent и без него. Ноль первых при
	// живом сайте — формат лога роботов не называет: «не знаем».
	Lines   int `json:"lines"`
	NoAgent int `json:"noAgent"`
	// Роботы из списка — по имени; прочие назвавшиеся роботами — числом.
	Visits []RobotVisit `json:"visits"`
	Other  int          `json:"other"`
	// Обращения к адресам, которые перебирают сканеры, и ответы 200 на
	// адреса с секретом (`.env`, `.git`).
	Scans   int `json:"scans"`
	Exposed int `json:"exposed"`
}

// RobotsState — ответ `GET /robots`.
type RobotsState struct {
	// За сколько суток счёт: не больше срока хранения логов.
	Days int `json:"days"`
	// Список роботов с ролями — одна копия, агента; экран берёт её отсюда.
	Bots  []bots.Bot  `json:"bots"`
	Hosts []RobotHost `json:"hosts"`
	// Сколько логов читается: ноль — читать нечего, и пустой список роботов
	// значит «не знаем», а не «не приходили».
	Sources int `json:"sources"`
}

func (a *Agent) Robots(days int) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if days <= 0 || days > a.cfg.Logs.RetentionDays {
		days = a.cfg.Logs.RetentionDays
	}
	since := time.Now().UTC().AddDate(0, 0, -days)
	counts, err := a.db.RobotSummary(ctx, since)
	if err != nil {
		return nil, err
	}

	byHost := map[string]*RobotHost{}
	order := []string{}
	for _, count := range counts {
		host, ok := byHost[count.Host]
		if !ok {
			host = &RobotHost{Host: count.Host, Visits: []RobotVisit{}}
			byHost[count.Host] = host
			order = append(order, count.Host)
		}
		switch count.Name {
		case logs.MetaLines:
			host.Lines = count.Count
		case logs.MetaNoAgent:
			host.NoAgent = count.Count
		case logs.MetaScan:
			host.Scans = count.Count
		case logs.MetaExposed:
			host.Exposed = count.Count
		case bots.Other:
			host.Other = count.Count
		default:
			host.Visits = append(host.Visits, RobotVisit{
				Name: count.Name, Count: count.Count, LastAt: count.LastAt.Format(time.RFC3339),
			})
		}
	}

	hosts := make([]RobotHost, 0, len(order))
	for _, name := range order {
		hosts = append(hosts, *byHost[name])
	}
	sources := 0
	for _, source := range a.logSources() {
		if source.Kind == logs.KindAccess {
			sources++
		}
	}
	return RobotsState{Days: days, Bots: bots.List, Hosts: hosts, Sources: sources}, nil
}

func (a *Agent) Logs(hours int) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	groups, err := a.db.LogGroups(ctx, since, logGroupsShown)
	if err != nil {
		return nil, err
	}
	series, err := a.db.LogSeries(ctx, since)
	if err != nil {
		return nil, err
	}
	counts, err := a.db.LogCountsBySource(ctx, since)
	if err != nil {
		return nil, err
	}
	positions, err := a.db.LogPositions(ctx)
	if err != nil {
		return nil, err
	}

	sources := []logs.SourceState{}
	for _, source := range a.logSources() {
		state := logs.SourceState{Source: source, Count: counts[source.Path]}
		if position, ok := positions[source.Path]; ok {
			state.Error = position.Error
			if !position.ReadAt.IsZero() {
				state.ReadAt = position.ReadAt.Format(time.RFC3339)
			}
		}
		sources = append(sources, state)
	}

	total := 0
	for _, point := range series {
		total += point.Access + point.Errors
	}

	return LogsState{
		Sources:        sources,
		Groups:         groups,
		Series:         series,
		Total:          total,
		AlertThreshold: a.cfg.Logs.AlertThreshold,
		WindowMinutes:  int(a.cfg.Logs.AlertWindow / time.Minute),
		RetentionDays:  a.cfg.Logs.RetentionDays,
		Notifying:      !a.notifier.Silent(),
		NotifyMuted:    a.notifier.Muted(),
	}, nil
}

// RunLogScan дочитывает логи прямо сейчас.
//
// В отличие от круга ссылок и замеров, проход короткий: он дочитывает хвосты
// файлов, а не ходит по сети. Поэтому отвечаем, когда всё сделано, — экрану
// не нужно опрашивать состояние.
func (a *Agent) RunLogScan(hours int) (any, error) {
	ctx, cancel := context.WithTimeout(a.rootCtx, 60*time.Second)
	defer cancel()

	a.scanLogs(ctx)
	return a.Logs(hours)
}

// ── Обход сайта для SEO ──────────────────────────────────────────────────
//
// Свой краулер, отдельный от того, что ищет битые ссылки. Тот собирает
// адреса для lychee и ничего о страницах не помнит; здесь с каждой страницы
// снимается структура — заголовки, описания, canonical, alt-тексты, — и
// именно она хранится, а не HTML.

// Обход сотен страниц с паузой между запросами идёт долго, а сайтов у
// сервера несколько. Потолок щедрый по той же причине, что и у замеров:
// оборвать обход на середине значит оставить часть сайтов без свежих
// данных.
const seoRunTimeout = 3 * time.Hour

// Сколько даём на запись итога круга — любого из трёх.
//
// Полтысячи страниц одной транзакцией на медленном диске — это секунды,
// минуты хватает с запасом; отчёту о ссылках и замеру хватает тем более.
const saveTimeout = time.Minute

// Сколько страниц отдаём экрану за раз.
const seoPagesLimit = 100

func (a *Agent) seoLoop(ctx context.Context) {
	if !a.cfg.Seo.Enabled {
		log.Print("обход сайтов выключен настройкой")
		return
	}

	// Первый обход — только если прошлый был давно, и в самом конце общего
	// разноса стартов. Как и с замерами: он стоит сотен запросов к чужому
	// проду, и перезапуск агента после правки настроек не повод обходить
	// всё заново.
	if !wait(ctx, startDelay(taskSeo)) {
		return
	}
	if a.seoStale(ctx) {
		a.startSeoScan("")
	}

	ticker := time.NewTicker(a.cfg.Seo.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.startSeoScan("")
		}
	}
}

// seoStale — правда ли, что с прошлого обхода прошло больше интервала.
func (a *Agent) seoStale(ctx context.Context) bool {
	latest, ok, err := a.db.LatestSeoAt(ctx)
	if err != nil {
		log.Printf("время прошлого обхода: %v", err)
		return true
	}
	return !ok || time.Since(latest) >= a.cfg.Seo.Interval
}

// startSeoScan запускает обход в фоне: всех сайтов (`only` пуст) или
// одного — того, что человек выбрал на экране.
//
// Как и круги ссылок и замеров: HTTP-обработчик отвечает сразу, а контекст
// берётся от rootCtx — остановка агента обязана прерывать обход, а не
// оставлять его писать в уже закрытую базу.
//
// Обход один за раз на сервер, чей бы он ни был: правило «один запрос за
// раз» к чужому проду.
func (a *Agent) startSeoScan(only string) bool {
	if !a.cfg.Seo.Enabled {
		return false
	}
	if !a.seoRunning.CompareAndSwap(false, true) {
		return false
	}

	a.background.Add(1)
	go func() {
		defer a.background.Done()
		defer a.seoRunning.Store(false)
		defer a.seoDomain.Store("")

		ctx, cancel := context.WithTimeout(a.rootCtx, seoRunTimeout)
		defer cancel()
		err := a.crawlSites(ctx, only)
		if err != nil {
			log.Printf("обход сайтов: %v", err)
		}
		a.noteRound(events.KindSeoScan, err)
	}()
	return true
}

// pickSeoSites — какие сайты обходить. Пусто в `only` — все; иначе ровно
// этот, и только если он в списке сайтов: чужой адрес агент не обходит,
// кто бы его ни прислал.
func pickSeoSites(sites []config.Site, only string) ([]config.Site, bool) {
	only = strings.TrimSpace(strings.ToLower(only))
	if only == "" {
		return sites, true
	}
	for _, site := range sites {
		if site.Domain == only {
			return []config.Site{site}, true
		}
	}
	return nil, false
}

// crawlSites обходит сайты по очереди — все или один.
//
// Последовательно, как и проверка ссылок: сайтов у сервера единицы, а
// параллельный обход бьёт по чужому проду всеми сразу.
func (a *Agent) crawlSites(ctx context.Context, only string) error {
	all, err := a.cfg.Sites()
	if err != nil {
		return err
	}
	sites, ok := pickSeoSites(all, only)
	if !ok || len(sites) == 0 {
		return nil
	}

	limits := seo.Limits{
		MaxDepth:   a.cfg.Seo.MaxDepth,
		MaxPages:   a.cfg.Seo.MaxPages,
		MaxBytes:   a.cfg.Seo.MaxPageBytes,
		Delay:      a.cfg.Seo.Delay(),
		Timeout:    a.cfg.Seo.Timeout,
		Exclude:    seo.CompileExcludes(a.cfg.Seo.Exclude),
		UseSitemap: a.cfg.Seo.UseSitemap,
	}
	client := seo.HTTPClient(a.cfg.Seo.Timeout)
	alive := make(map[string]bool, len(sites))

	for _, site := range sites {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		alive[site.Domain] = true
		a.seoDomain.Store(site.Domain)

		scan, pages := seo.Crawl(ctx, client, "https://"+site.Domain+"/", limits)
		// Домен берём из списка сайтов, а не из адреса: по нему хранится
		// история, и расхождение написаний развело бы её на две.
		scan.Domain = site.Domain
		if scan.Error != "" {
			log.Printf("обход %s: %s", site.Domain, scan.Error)
		}

		// Записываем отдельным контекстом, а не тем, которым обходили.
		// `Crawl` ставит ErrTimeout ровно тогда, когда отведённое время
		// вышло, — то есть следующий же запрос к базе тем же контекстом
		// падает с DeadlineExceeded. Частичный обход при этом терялся
		// целиком, а переведённый на оба языка код `seoScanTimeout` не мог
		// появиться на экране никогда: сохранять было нечего.
		if err := a.saving(func(save context.Context) error {
			return a.db.SaveSeoScan(save, scan, pages)
		}); err != nil {
			log.Printf("запись обхода: %v", err)
		}
	}

	// Сайт удалили — его снимки больше не наша забота. Только в полном
	// круге: обход одного сайта знает про один сайт, и `alive` из него
	// стёр бы снимки всех остальных.
	if only != "" {
		return nil
	}
	if err := a.saving(func(save context.Context) error {
		return a.db.ForgetSeoScans(save, alive)
	}); err != nil {
		log.Printf("уборка обходов: %v", err)
	}
	return nil
}

// ── Обход сайта: то, что видит приложение ────────────────────────────────

// SeoState — состояние обхода для экрана.
type SeoState struct {
	// Идёт ли обход прямо сейчас.
	Running bool `json:"running"`
	// Включён ли обход вообще. Выключенный — законный выбор, и экран
	// обязан сказать об этом, а не показывать пустой список.
	Enabled bool       `json:"enabled"`
	Scans   []seo.Scan `json:"scans"`
	// Сайты сервера — все, в том числе ни разу не обойдённые: экран
	// показывает их вкладками, и сайт без обхода обязан на нём быть.
	Sites []SeoSite `json:"sites"`
	// Какой сайт обходится сейчас. Пусто, если обход не идёт.
	RunningDomain string `json:"runningDomain,omitempty"`
}

// SeoSite — сайт во вкладках экрана.
type SeoSite struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

// SeoPagesPage — окно страниц одного обхода.
type SeoPagesPage struct {
	Domain string `json:"domain"`
	// По какому уровню важности и какой находке отобрано. Пусто — отбора
	// не было.
	Severity string     `json:"severity,omitempty"`
	Code     string     `json:"code,omitempty"`
	Total    int        `json:"total"`
	Offset   int        `json:"offset"`
	Pages    []seo.Page `json:"pages"`
}

func (a *Agent) Seo() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sites, err := a.cfg.Sites()
	if err != nil {
		return nil, err
	}
	alive := make(map[string]bool, len(sites))
	for _, site := range sites {
		alive[site.Domain] = true
	}

	scans, err := a.db.SeoScans(ctx, alive)
	if err != nil {
		return nil, err
	}
	if scans == nil {
		scans = []seo.Scan{}
	}
	list := make([]SeoSite, 0, len(sites))
	for _, site := range sites {
		list = append(list, SeoSite{Domain: site.Domain, Name: site.Name})
	}
	running, _ := a.seoDomain.Load().(string)
	return SeoState{
		Running:       a.seoRunning.Load(),
		Enabled:       a.cfg.Seo.Enabled,
		Scans:         scans,
		Sites:         list,
		RunningDomain: running,
	}, nil
}

// SeoPages отдаёт страницы последнего обхода сайта окном.
//
// Отбирают уровень важности («этот и тяжелее») и конкретная находка. Отбор в
// базе, а не на экране: у обхода сотни страниц, и присылать их все, чтобы
// показать двадцать, значит гонять обход целиком через SSH-туннель на каждый
// щелчок.
func (a *Agent) SeoPages(domain, severity, code string, offset int) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return SeoPagesPage{Pages: []seo.Page{}}, nil
	}
	if offset < 0 {
		offset = 0
	}

	pages, total, err := a.db.SeoPages(ctx, domain, severity, code, seoPagesLimit, offset)
	if err != nil {
		return nil, err
	}
	return SeoPagesPage{
		Domain: domain,
		// Уровень нормализуется так же, как код: незнакомый `AtLeast`
		// молча отбрасывает, отбора не происходит — и ответ, вернувший
		// уровень как пришёл, утверждал бы, что отбор был.
		Severity: seo.KnownSeverity(severity),
		Code:     seo.KnownCode(code),
		Total:    total,
		Offset:   offset,
		Pages:    pages,
	}, nil
}

// SeoDiff сравнивает последний обход сайта с предыдущим.
//
// Считает агент, а не экран: сравнение читает две сотни страниц, и присылать
// их обе через SSH-туннель ради разницы значит гонять весь обход дважды.
func (a *Agent) SeoDiff(domain string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return seo.Diff{Appeared: []seo.DiffEntry{}, Fixed: []seo.DiffEntry{}}, nil
	}

	current, previous, err := a.db.SeoScanCodes(ctx, domain)
	if err != nil {
		return nil, err
	}
	return seo.Compare(domain, current, previous), nil
}

// Код отказа: сайта с таким доменом у агента нет.
const seoUnknownSite = "seoUnknownSite"

// RunSeo запускает обход: всех сайтов или одного (`domain`).
func (a *Agent) RunSeo(domain string) (any, error) {
	if strings.TrimSpace(domain) != "" {
		sites, err := a.cfg.Sites()
		if err != nil {
			return nil, err
		}
		if _, ok := pickSeoSites(sites, domain); !ok {
			return map[string]any{"started": false, "error": seoUnknownSite}, nil
		}
	}
	started := a.startSeoScan(domain)
	return map[string]bool{"started": started}, nil
}

// words — тексты тревог на языке, выбранном в приложении (`AGENT_LOCALE`).
// Тот же язык, что у недельного отчёта: одно сообщение по-русски, а другое
// по-английски — хуже, чем оба на любом одном.
func (a *Agent) words() checks.Words {
	return checks.WordsFor(a.cfg.Locale)
}
