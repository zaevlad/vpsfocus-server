package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/events"
	"agent/internal/probe"
	"agent/internal/store"
)

// Локальная диагностика: ключевые адреса проектов и свой же стек.
//
// **Подписывается это честно.** Проверка идёт с самого VPS и не видит
// облачного файрвола провайдера — правило «финальная проверка достижимости
// делается снаружи» появилось именно из-за этого. Ответ здесь — «приложение
// отвечает», а не «посетитель это видит».
//
// **Состояние сервисов не спрашивается у docker.** Дать агенту
// `/var/run/docker.sock` значит дать root на сервере клиента через
// контейнер, смонтированный строго `:ro`. Агент стучится к соседям по
// docker-сети: сервис, не отвечающий на своём порту, не работает, как бы
// docker его ни называл. Перезапуск живёт в приложении и идёт по SSH.

// Ключи, под которыми состояние стека лежит в таблице проверок.
//
// Там же, где сроки доменов и сертификатов: «что мы в прошлый раз выяснили
// про эту цель» — один и тот же вопрос, и заводить под него вторую таблицу
// незачем.
const (
	kindService   = "service"
	kindAnalytics = "analytics"
	// Цель у проверки трекинга одна на сервер: стек здесь один.
	targetTracker = "tracker"
)

// Пауза между адресами внутри круга.
//
// Круг идёт по чужому проду, и три запроса в одну секунду с сервера, на
// котором этот прод и живёт, — не то, чем стоит его удивлять. Секунда на
// адрес растягивает круг из трёх адресов на три секунды и не заметна.
const probeSpacing = time.Second

func (a *Agent) probeLoop(ctx context.Context) {
	// Первый круг — не в первую секунду. Разнос стартов общий для всех
	// кругов агента: см. `startDelay`.
	if !wait(ctx, startDelay(taskProbes)) {
		return
	}
	a.startProbeRun()

	ticker := time.NewTicker(a.cfg.Probes.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.startProbeRun()
		}
	}
}

// startProbeRun запускает круг и не ждёт его конца.
//
// Не ждёт по той же причине, что круги ссылок и обхода: его дёргает и
// хук «я задеплоил», а тот приходит из деплой-скрипта клиента, которому
// незачем стоять и ждать, пока мы обойдём три адреса.
func (a *Agent) startProbeRun() bool {
	if !a.probeRunning.CompareAndSwap(false, true) {
		return false
	}

	a.background.Add(1)
	go func() {
		defer a.background.Done()
		defer a.probeRunning.Store(false)

		ctx, cancel := context.WithTimeout(a.rootCtx, probeRunTimeout)
		defer cancel()
		a.runProbes(ctx)
	}()
	return true
}

// Потолок круга. Три адреса с паузой и таймаутом ответа плюс опрос стека —
// это секунды; минута с запасом закрывает даже сервер, думающий над каждым
// запросом до последнего.
const probeRunTimeout = time.Minute

func (a *Agent) runProbes(ctx context.Context) {
	// Файл не прочитался — это не «адресов больше нет».
	//
	// Разница существенная: дальше по пустому списку живых адресов уборка
	// стёрла бы все прошлые исходы, а вместе с ними и то, чем меряется
	// перелом. Следующий круг увидел бы пустоту, счёл её первым кругом и
	// промолчал о сломавшемся адресе — то есть неудача чтения конфига
	// отменила бы тревогу, ради которой круг и заведён. Стек при этом
	// диагностируется по-прежнему: он от этого файла не зависит.
	profiles, err := a.cfg.ProbeProfiles()
	if err != nil {
		log.Printf("ключевые адреса: %v", err)
		a.checkStack(ctx)
		return
	}

	client := probe.Client(a.cfg.Probes.Timeout)
	alive := make(map[store.ProbeKey]bool, len(profiles))

	for index, profile := range profiles {
		if ctx.Err() != nil {
			return
		}
		if index > 0 && !wait(ctx, probeSpacing) {
			return
		}

		result := probe.Run(ctx, client, profile)
		alive[store.KeyOfProbe(result)] = true

		a.noteProbeChange(ctx, result)
		if err := a.saving(func(save context.Context) error {
			return a.db.SaveProbeResult(save, result)
		}); err != nil {
			log.Printf("запись проверки адреса: %v", err)
		}
	}

	if err := a.saving(func(save context.Context) error {
		return a.db.ForgetProbeResults(save, alive)
	}); err != nil {
		log.Printf("уборка проверок адресов: %v", err)
	}

	a.checkStack(ctx)
}

// checkStack опрашивает соседей по docker-сети и свой же трекинг.
func (a *Agent) checkStack(ctx context.Context) {
	for _, service := range probe.CheckServices(ctx, a.cfg.Probes.EdgePort, a.cfg.Probes.Timeout) {
		if err := a.saving(func(save context.Context) error {
			return a.db.SaveCheck(save, kindService, service.Name, service)
		}); err != nil {
			log.Printf("запись состояния сервиса: %v", err)
		}
	}

	// Домена трекинга нет — проверять нечего: так бывает у стека, который
	// поставили до того, как этот раздел появился. Записать сюда неудачу
	// значило бы объявить аварией собственную необновлённость.
	if a.cfg.Probes.TrackingDomain == "" {
		return
	}

	state := probe.CheckAnalytics(ctx,
		a.cfg.Probes.TrackingDomain, a.cfg.Probes.EdgePort, a.cfg.Probes.Timeout)
	if err := a.saving(func(save context.Context) error {
		return a.db.SaveCheck(save, kindAnalytics, targetTracker, state)
	}); err != nil {
		log.Printf("запись проверки трекинга: %v", err)
	}
}

// noteProbeChange отмечает перелом и зовёт человека.
//
// Отметка в журнале и письмо идут через ту же машинерию, что и тревоги о
// диске и сертификатах: о новой беде говорим сразу, о старой — не чаще, чем
// раз в сутки, о починившемся — один раз. Ключевой адрес, переставший
// работать, — ровно то, ради чего агент и живёт на сервере клиента: он
// сообщает об этом, когда приложение закрыто.
func (a *Agent) noteProbeChange(ctx context.Context, result probe.Result) {
	// Личность проверки — четыре поля, а не пара: две проверки одного адреса
	// («главная отвечает 200» и «на главной есть слово Каталог») — осмысленная
	// пара, и сравнение свежего исхода с чужим прошлым объявляло бы перелом на
	// ровном месте.
	previous, err := a.db.ProbeResults(ctx, map[store.ProbeKey]bool{
		store.KeyOfProbe(result): true,
	})
	if err != nil {
		log.Printf("прошлый исход проверки: %v", err)
		return
	}
	// Первый круг переломом не считается: агент только что запустился, и
	// рассказывать человеку про наш собственный старт незачем.
	if len(previous) == 0 {
		return
	}
	if previous[0].OK == result.OK {
		return
	}

	kind := events.KindProbeFailing
	if result.OK {
		kind = events.KindProbeRecovered
	}
	a.note(kind, result.URL, result.Code)

	// Тревога адресуется личностью проверки, а не адресом: проверок на
	// одном адресе бывает несколько, и общая строка `alerts` означала бы,
	// что беда второй молчит сутки, пока горит первая, а отбой первой гасит
	// состояние второй. В журнале сервера тревога при этом называется
	// адресом — склейка четырёх полей человеку ничего не говорит.
	alert := checks.Alert{
		Kind:    checks.KindProbe,
		Target:  store.ProbeAlertTarget(store.KeyOfProbe(result)),
		Label:   result.URL,
		Subject: fmt.Sprintf(a.words().ProbeSubject, result.Name),
		Body:    fmt.Sprintf(a.words().ProbeBody, result.Name, result.URL, result.Code),
	}
	if result.OK {
		alert.Subject = fmt.Sprintf(a.words().ProbeOKSubject, result.Name)
		alert.Body = fmt.Sprintf(a.words().ProbeOKBody, result.Name, result.URL)
		a.dispatch(ctx, nil, []checks.Alert{alert})
		return
	}
	a.dispatch(ctx, []checks.Alert{alert}, nil)
}

// ── То, что видит приложение ─────────────────────────────────────────────

// ProbeState — локальная диагностика для экрана.
type ProbeState struct {
	// Идёт ли круг прямо сейчас.
	Running bool `json:"running"`
	// Ключевые адреса так, как они заданы.
	//
	// Отдаются наружу целиком, потому что второго списка нет: файл на
	// сервере — единственный источник правды, приложение его пишет и
	// читает. Держи мы копию у себя, список на экране и список, по которому
	// ходит круг, однажды разошлись бы, и понять, кто прав, стало бы нечем.
	Profiles []probe.Profile `json:"profiles"`
	// Ключевые адреса и их последний исход.
	Results []probe.Result `json:"results"`
	// Сервисы стека.
	Services []probe.Service `json:"services"`
	// Отдаётся ли скрипт трекинга. Пусто — ещё не проверяли.
	Analytics *probe.Analytics `json:"analytics,omitempty"`
	// Сколько ключевых адресов разрешено проекту. Отдаётся наружу, чтобы
	// экран не держал собственную копию числа.
	MaxPerProject int `json:"maxPerProject"`
	// Настроен ли хук «я задеплоил». Сам токен наружу не уезжает: он есть
	// у приложения в хранилище ОС, и второй дороги ему незачем.
	DeployHook bool `json:"deployHook"`
}

func (a *Agent) Probes() (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	profiles, err := a.cfg.ProbeProfiles()
	if err != nil {
		return nil, err
	}
	alive := make(map[store.ProbeKey]bool, len(profiles))
	for _, profile := range profiles {
		alive[store.KeyOfProfile(profile)] = true
	}

	results, err := a.db.ProbeResults(ctx, alive)
	if err != nil {
		return nil, err
	}

	if profiles == nil {
		profiles = []probe.Profile{}
	}

	state := ProbeState{
		Running:       a.probeRunning.Load(),
		Profiles:      profiles,
		Results:       results,
		Services:      []probe.Service{},
		MaxPerProject: config.MaxProbesPerProject,
		DeployHook:    a.cfg.Probes.DeployToken != "",
	}

	if raw, err := a.db.Checks(ctx, kindService); err != nil {
		log.Printf("состояние сервисов: %v", err)
	} else {
		for _, item := range raw {
			var service probe.Service
			if json.Unmarshal(item, &service) == nil {
				state.Services = append(state.Services, service)
			}
		}
	}
	if raw, err := a.db.Checks(ctx, kindAnalytics); err != nil {
		log.Printf("состояние трекинга: %v", err)
	} else if item, ok := raw[targetTracker]; ok {
		var analytics probe.Analytics
		if json.Unmarshal(item, &analytics) == nil {
			state.Analytics = &analytics
		}
	}

	return state, nil
}

func (a *Agent) RunProbes() (any, error) {
	return map[string]bool{"started": a.startProbeRun()}, nil
}

// Deployed — хук «я задеплоил».
//
// Отдельный токен, а не общий: строку с ним человек вставляет в свой
// деплой-скрипт, а тот живёт в репозитории. Токен API открывает всё, что
// агент знает о клиентах агентства; этот — только «сходи по ключевым
// адресам сейчас», и больше ничего.
//
// Адрес виден только с самого сервера: агент слушает петлю, и наружу он не
// смотрит никогда. Дёргают его из деплой-скрипта на VPS или из шага CI,
// который и так ходит туда по SSH.
func (a *Agent) Deployed(token string) (any, error) {
	if a.cfg.Probes.DeployToken == "" {
		return nil, errHookDisabled
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.Probes.DeployToken)) != 1 {
		return nil, errHookForbidden
	}
	a.note(events.KindDeployed, "", "")
	return map[string]bool{"started": a.startProbeRun()}, nil
}

// wait ждёт паузу или остановку агента. Возвращает false, если пора
// заканчивать: круг, продолженный после SIGTERM, пишет в закрывающуюся базу.
func wait(ctx context.Context, pause time.Duration) bool {
	if pause <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Ошибки хука. Их различает HTTP-слой: выключенный хук и неверный токен —
// разные новости для того, кто настраивает деплой.
var (
	errHookDisabled  = &hookError{status: http.StatusNotFound, text: "hook disabled"}
	errHookForbidden = &hookError{status: http.StatusUnauthorized, text: "unauthorized"}
)

type hookError struct {
	status int
	text   string
}

func (e *hookError) Error() string { return e.text }

// Status — какой код ответа отдать. Читается HTTP-слоем через интерфейс,
// чтобы тот не знал про наши типы.
func (e *hookError) Status() int { return e.status }
