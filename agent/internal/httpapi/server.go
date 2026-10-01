// Package httpapi отдаёт собранное приложению.
//
// Слушает внутри контейнера, наружу его публикует docker только на петлю
// сервера — снаружи API не существует. Токен при этом обязателен всё равно:
// в docker-сети рядом стоят чужие по смыслу контейнеры, и «мы за фаерволом»
// — не аргумент.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent/internal/notify"
)

// Data — то, что API умеет отдавать. Интерфейсом, а не структурой, чтобы
// сборка агента не зависела от порядка инициализации.
type Data interface {
	Snapshot(hours int) (any, error)
	Checks() (any, error)
	RunChecks() (any, error)
	// Проверочное сообщение в один канал (`smtp`, `telegram`) или, при
	// пустом имени, во все настроенные. Отвечает, куда оно ушло.
	SendTest(channel string) ([]string, error)
	// Состояние проверки ссылок и её ручной запуск. Запуск отвечает
	// сразу: краулинг идёт минутами и в HTTP-таймаут не помещается.
	Links() (any, error)
	RunLinks() (any, error)
	// Замеры Core Web Vitals и их ручной запуск. Запуск тоже отвечает
	// сразу: PSI открывает каждую страницу браузером и думает десятками
	// секунд, а страниц у круга десятки.
	Vitals() (any, error)
	RunVitals() (any, error)
	// Ошибки из логов веб-сервера за период и внеплановый проход по ним.
	// Этот, в отличие от прочих, отвечает сделанным: проход дочитывает
	// хвосты файлов и укладывается в секунды.
	Logs(hours int) (any, error)
	// Роботы по логам (трек V): кто назвался роботом, сколько раз и когда.
	Robots(days int) (any, error)
	RunLogScan(hours int) (any, error)
	// Обход сайтов для SEO: сводка, окно страниц последнего обхода и
	// ручной запуск. Запуск отвечает сразу — обход идёт десятками минут.
	// Свод состояния: сервер и проекты одним ответом.
	Health() (any, error)
	// Приглушение находки и его снятие. Оба отвечают пересчитанным сводом:
	// экран нажимает кнопку и сразу видит результат, а не запрашивает
	// состояние вторым запросом, между которыми оно успевает разойтись.
	Suppress(code, target string, days int, note string) (any, error)
	Unsuppress(code, target string) (any, error)
	// Окно обслуживания: агент молчит, пока человек чинит.
	StartMaintenance(minutes int, note string) (any, error)
	EndMaintenance() (any, error)
	// Локальная диагностика: ключевые адреса проектов и свой же стек.
	//
	// Запуск отвечает сразу: круг короткий, но его дёргает и хук
	// «я задеплоил» — деплой-скрипту клиента незачем ждать нашего обхода.
	Probes() (any, error)
	RunProbes() (any, error)
	// Хук «я задеплоил». Токен свой, отдельный от токена API: строку с ним
	// человек вставляет в деплой-скрипт, а тот живёт в репозитории.
	Deployed(token string) (any, error)
	// Лента изменений сервера и разбор аварии.
	//
	// Оба принимают время приложения: часы сервера клиента уезжают на
	// минуты, а сравнить их не с чем, пока вторая половина не приехала. Без
	// неё расхождение считается нулём — и так и говорится наружу.
	Events(hours int, now time.Time) (any, error)
	Around(at time.Time, minutes int, now time.Time) (any, error)
	// Копии базы: что уехало в хранилище клиента и что там лежит сейчас.
	//
	// Запуск отвечает сразу — дамп, архив и выгрузка идут минутами, — а
	// ссылка нужна восстановлению: приложение с хранилищем не разговаривает,
	// сервер качает копию сам.
	Backups(prefix string) (any, error)
	RunBackup() (any, error)
	BackupLink(key string) (any, error)
	Seo() (any, error)
	SeoPages(domain, severity, code string, offset int) (any, error)
	// Сравнение последнего обхода с предыдущим: что появилось и что
	// исправилось.
	SeoDiff(domain string) (any, error)
	RunSeo(domain string) (any, error)
}

type Server struct {
	token string
	data  Data
}

func New(token string, data Data) *Server {
	return &Server{token: token, data: data}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Проверка живости — без токена: ей отвечают на вопрос «ты запустился?»,
	// а не «расскажи о сервере». Ничего, кроме факта запуска, она не выдаёт.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Свод состояния. Отдельно от `GET /health` выше и назван иначе
	// намеренно: тот отвечает docker на вопрос «ты запустился?» и потому
	// живёт без токена, а этот рассказывает о сервере и о клиентах
	// агентства — и без токена существовать не вправе.
	mux.Handle("GET /status", s.authorized(s.handleStatus))

	mux.Handle("POST /suppressions", s.authorized(s.handleSuppress))
	mux.Handle("DELETE /suppressions", s.authorized(s.handleUnsuppress))
	mux.Handle("POST /maintenance", s.authorized(s.handleStartMaintenance))
	mux.Handle("DELETE /maintenance", s.authorized(s.handleEndMaintenance))

	// Журнал сервера и срез по времени вокруг аварии. Оба живут под
	// токеном по той же причине, что и свод: они рассказывают о сервере и
	// о клиентах агентства.
	mux.Handle("GET /events", s.authorized(s.handleEvents))
	mux.Handle("GET /around", s.authorized(s.handleAround))

	mux.Handle("GET /probes", s.authorized(s.handleProbes))
	mux.Handle("POST /probes/run", s.authorized(s.handleRunProbes))

	// Хук «я задеплоил» — единственный раздел со своим токеном.
	//
	// Общий токен сюда не годится: строку с ним человек вставляет в свой
	// деплой-скрипт, а тот живёт в репозитории. Токен API открывает всё,
	// что агент знает о клиентах агентства, — этот только просит сходить по
	// ключевым адресам.
	mux.HandleFunc("POST /deployed", s.handleDeployed)

	mux.Handle("GET /metrics", s.authorized(s.handleMetrics))
	mux.Handle("GET /checks", s.authorized(s.handleChecks))
	mux.Handle("POST /checks/run", s.authorized(s.handleRunChecks))
	mux.Handle("POST /notify/test", s.authorized(s.handleTest))
	mux.Handle("GET /links", s.authorized(s.handleLinks))
	mux.Handle("POST /links/run", s.authorized(s.handleRunLinks))
	mux.Handle("GET /vitals", s.authorized(s.handleVitals))
	mux.Handle("POST /vitals/run", s.authorized(s.handleRunVitals))
	mux.Handle("GET /logs", s.authorized(s.handleLogs))
	mux.Handle("POST /logs/scan", s.authorized(s.handleLogScan))
	mux.Handle("GET /robots", s.authorized(s.handleRobots))
	mux.Handle("GET /backups", s.authorized(s.handleBackups))
	mux.Handle("POST /backups/run", s.authorized(s.handleRunBackup))
	mux.Handle("POST /backups/link", s.authorized(s.handleBackupLink))
	mux.Handle("GET /seo", s.authorized(s.handleSeo))
	mux.Handle("GET /seo/pages", s.authorized(s.handleSeoPages))
	mux.Handle("GET /seo/diff", s.authorized(s.handleSeoDiff))
	mux.Handle("POST /seo/run", s.authorized(s.handleRunSeo))

	return mux
}

// authorized сверяет токен за постоянное время: обычное сравнение строк
// выдаёт длину общего префикса задержкой, и токен подбирается по одному
// символу.
func (s *Server) authorized(next func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get("Authorization")
		expected := "Bearer " + s.token

		if subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.data.Health()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// Тело запросов на приглушение и обслуживание.
//
// Одной структурой на четыре обработчика: поля не пересекаются, а два
// почти одинаковых типа рядом — это две формы, которые однажды разойдутся.
type request struct {
	Code string `json:"code"`
	// Ключ копии в хранилище: на него выдаётся подписанная ссылка.
	Key    string `json:"key"`
	Target string `json:"target"`
	// Ноль — бессрочно.
	Days int `json:"days"`
	// На сколько минут открыть окно обслуживания.
	Minutes int    `json:"minutes"`
	Note    string `json:"note"`
}

// decode читает тело запроса.
//
// Разбор ограничен по размеру: тело приходит из приложения через туннель,
// но открытый на петле порт видит и любой процесс на сервере клиента, а
// заметка человека в мегабайт — это не заметка.
func decode(w http.ResponseWriter, r *http.Request) (request, bool) {
	var body request
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return request{}, false
	}
	return body, true
}

// needCode отсекает запрос без повода.
//
// Пустой код — это «приглушить всё» или «снять всё»: разрешить его значит
// однажды выключить мониторинг целиком опечаткой в теле запроса. Цель при
// этом пустой быть вправе — это «повод на всём сервере».
func needCode(w http.ResponseWriter, body request) bool {
	if strings.TrimSpace(body.Code) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return false
	}
	return true
}

func (s *Server) handleSuppress(w http.ResponseWriter, r *http.Request) {
	body, ok := decode(w, r)
	if !ok || !needCode(w, body) {
		return
	}
	payload, err := s.data.Suppress(body.Code, body.Target, body.Days, body.Note)
	answer(w, payload, err)
}

func (s *Server) handleUnsuppress(w http.ResponseWriter, r *http.Request) {
	body, ok := decode(w, r)
	if !ok || !needCode(w, body) {
		return
	}
	payload, err := s.data.Unsuppress(body.Code, body.Target)
	answer(w, payload, err)
}

func (s *Server) handleStartMaintenance(w http.ResponseWriter, r *http.Request) {
	body, ok := decode(w, r)
	if !ok {
		return
	}
	// Окно без срока — это выключенный навсегда мониторинг, о котором
	// через месяц никто не вспомнит. Час по умолчанию, сутки потолком:
	// дольше суток чинят уже не «сейчас», и такое молчание надо продлить
	// осознанно, второй кнопкой.
	minutes := body.Minutes
	if minutes <= 0 {
		minutes = 60
	}
	if minutes > 24*60 {
		minutes = 24 * 60
	}
	payload, err := s.data.StartMaintenance(minutes, body.Note)
	answer(w, payload, err)
}

func (s *Server) handleEndMaintenance(w http.ResponseWriter, r *http.Request) {
	payload, err := s.data.EndMaintenance()
	answer(w, payload, err)
}

// answer отдаёт результат или ошибку. Четыре обработчика подряд повторяли
// бы одни и те же пять строк.
func answer(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleProbes отдаёт состояние ключевых адресов и стека.
func (s *Server) handleProbes(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.Probes()
	answer(w, state, err)
}

// handleRunProbes гоняет круг прямо сейчас и не ждёт его конца.
func (s *Server) handleRunProbes(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.RunProbes()
	answer(w, state, err)
}

// handleDeployed принимает хук «я задеплоил».
//
// Токен читается из заголовка так же, как основной, но сверяет его сам
// агент: у хука свой секрет, и подставлять сюда общую проверку значило бы
// открыть ему весь API.
func (s *Server) handleDeployed(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}

	state, err := s.data.Deployed(token)
	if err != nil {
		// Выключенный хук и неверный токен — разные новости для того, кто
		// настраивает деплой, и коды у них разные.
		status := http.StatusInternalServerError
		var coded interface{ Status() int }
		if errors.As(err, &coded) {
			status = coded.Status()
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleEvents отдаёт ленту изменений сервера.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.Events(logHours(r), appTime(r))
	answer(w, state, err)
}

// handleAround отдаёт то, что было на сервере вокруг указанного времени.
//
// Без `at` отвечать нечем: срез по времени без времени — это запрос,
// понятый неверно, а не запрос с умолчанием. Молча подставить «сейчас»
// значило бы показать текущее состояние под заголовком «во время аварии».
func (s *Server) handleAround(w http.ResponseWriter, r *http.Request) {
	at, ok := unixTime(r, "at")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}

	// Окно вокруг события. Час по умолчанию: авария длиной в минуты
	// становится видна только вместе с тем, что было до и после неё.
	minutes, err := strconv.Atoi(r.URL.Query().Get("minutes"))
	if err != nil || minutes < 10 {
		minutes = 60
	}
	if minutes > 24*60 {
		minutes = 24 * 60
	}

	state, failure := s.data.Around(at, minutes, appTime(r))
	answer(w, state, failure)
}

// unixTime читает время из запроса.
//
// Секундами эпохи, а не строкой RFC3339: у приложения нет библиотеки дат, и
// собирать её ради двух параметров значило бы завести ещё одно место, где
// формат времени может разойтись. Наружу времена по-прежнему уходят в
// RFC3339 — их читает браузер, а не наш же разбор.
func unixTime(r *http.Request, name string) (time.Time, bool) {
	seconds, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

// appTime — который час у приложения.
//
// Пустое или неразборчивое значение означает «не сказали»: расхождение
// часов тогда считается нулём, окно не сдвигается, и наружу это уходит
// честным нулём, а не догадкой. Отказывать здесь нельзя — старое
// приложение о вопросе не знает, а лента ему нужна не меньше.
func appTime(r *http.Request) time.Time {
	at, ok := unixTime(r, "now")
	if !ok {
		return time.Time{}
	}
	return at
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	hours, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil || hours <= 0 || hours > 24*30 {
		hours = 24
	}

	snapshot, err := s.data.Snapshot(hours)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	checks, err := s.data.Checks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, checks)
}

// handleRunChecks гоняет проверки прямо сейчас.
//
// Нужен экрану: пользователь продлил домен и хочет увидеть это немедленно,
// а не через шесть часов до следующего круга.
func (s *Server) handleRunChecks(w http.ResponseWriter, r *http.Request) {
	checks, err := s.data.RunChecks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, checks)
}

// handleTest шлёт проверочное уведомление.
//
// Отправляет агент, а не приложение: проверять надо тот путь, которым пойдут
// настоящие письма. Успешная отправка с ноутбука ничего не доказывает про
// сервер клиента.
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	// Канал — в строке запроса (агент 0.20.0). Агент старше её не читал и
	// слал во все каналы; поэтому ответ называет, куда сообщение ушло, — по
	// отсутствию списка приложение узнаёт старого агента и не обещает
	// человеку проверку одного канала.
	channels, err := s.data.SendTest(r.URL.Query().Get("channel"))
	if errors.Is(err, notify.ErrUnknownChannel) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "sent", "channels": channels})
}

// handleLinks отдаёт состояние проверки ссылок: идёт ли круг и последний
// отчёт по каждому сайту.
func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.Links()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleRunLinks запускает круг проверок ссылок и не ждёт его конца:
// краулинг минутами идёт, а приложение держит туннель с конечным таймаутом.
func (s *Server) handleRunLinks(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.RunLinks()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleVitals отдаёт последние замеры производительности и состояние квоты.
func (s *Server) handleVitals(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.Vitals()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleRunVitals запускает круг замеров и не ждёт его конца.
func (s *Server) handleRunVitals(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.RunVitals()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleLogs отдаёт ошибки из логов за период.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.Logs(logHours(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleRobots отдаёт роботов по логам за `days` суток (по умолчанию — весь
// срок хранения логов).
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	state, err := s.data.Robots(days)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleLogScan дочитывает логи прямо сейчас и отдаёт то, что получилось.
func (s *Server) handleLogScan(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.RunLogScan(logHours(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	// Префикс чужого сервера — законный запрос: переезд начинается с того,
	// что человек ищет копии старого VPS, а бакет у агентства один.
	state, err := s.data.Backups(r.URL.Query().Get("prefix"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	started, err := s.data.RunBackup()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, started)
}

func (s *Server) handleBackupLink(w http.ResponseWriter, r *http.Request) {
	body, ok := decode(w, r)
	if !ok {
		return
	}

	link, err := s.data.BackupLink(body.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, link)
}

// handleSeo отдаёт сводку последнего обхода по каждому сайту.
func (s *Server) handleSeo(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.Seo()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleSeoPages отдаёт страницы последнего обхода окном: их сотни, и
// тащить все сразу через SSH-туннель незачем.
func (s *Server) handleSeoPages(w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}

	state, err := s.data.SeoPages(
		r.URL.Query().Get("domain"),
		r.URL.Query().Get("severity"),
		r.URL.Query().Get("code"),
		offset,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleSeoDiff отдаёт разницу между последним обходом и предыдущим.
func (s *Server) handleSeoDiff(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.SeoDiff(r.URL.Query().Get("domain"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleRunSeo запускает обход и не ждёт его конца. `domain` — обойти один
// сайт; пусто — все.
func (s *Server) handleRunSeo(w http.ResponseWriter, r *http.Request) {
	state, err := s.data.RunSeo(r.URL.Query().Get("domain"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// logHours разбирает период. Потолок тот же, что у метрик: дольше месяца
// данных всё равно нет — ротация их убирает.
func logHours(r *http.Request) int {
	hours, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil || hours <= 0 || hours > 24*30 {
		return 24
	}
	return hours
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// Listen поднимает сервер с разумными таймаутами.
func Listen(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Проверки доменов ходят в сеть и бывают медленными.
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
