// Package config читает настройки агента из окружения и список сайтов с
// диска.
//
// Разделение неслучайно. Секреты — токен API и пароли уведомлений — живут в
// `agent.env` с правами 600 и попадают в контейнер переменными окружения:
// их смена редка и стоит перезапуска. А список сайтов меняется каждый раз,
// когда агентство заводит клиенту новый домен, и перезапускать ради этого
// агента незачем — он перечитывает файл сам.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent/internal/backup"
	"agent/internal/domains"
	"agent/internal/logs"
	"agent/internal/probe"
	"agent/internal/s3"
)

// Config — то, что задаётся при установке и меняется редко.
type Config struct {
	// Токен доступа к HTTP API. Пустой означает, что агент отказывается
	// стартовать: открытый API даже на петле — это дыра для любого
	// процесса на сервере клиента.
	Token string

	// Где слушать. Только петля: наружу агент не выходит никогда.
	Listen string

	// Куда писать базу замеров.
	DatabasePath string

	// Откуда читать список сайтов.
	SitesPath string

	// Откуда читать состав проектов. Пишет его приложение, читает агент —
	// тем же способом, что и список сайтов, и по той же причине: состав
	// меняется, когда агентство заводит клиенту домен, и перезапускать
	// ради этого агента незачем.
	ProjectsPath string

	// Корень хостовой файловой системы внутри контейнера. Агент живёт в
	// контейнере, а рассказывать должен про сервер, а не про себя.
	HostProc string
	HostRoot string

	// Как часто снимать замеры и как долго их хранить.
	SampleInterval time.Duration
	Retention      time.Duration

	// Как часто перепроверять домены и сертификаты. Раз в сутки достаточно:
	// сроки измеряются днями, а whois-серверы не любят частых гостей.
	CheckInterval time.Duration

	SMTP     SMTP
	Telegram Telegram
	Webhooks Webhooks
	// Человек выключил уведомления с этого сервера (`NOTIFY_MUTED=1`): каналы
	// общие на все серверы, а писать должен не каждый. Агент при этом следит
	// как обычно — молчат только тревоги и недельный отчёт.
	NotifyMuted bool

	Alerts Alerts

	// Проверка битых ссылок: расписание и пределы нагрузки.
	Links Links
	// Путь к бинарнику lychee.
	Lychee string

	// Core Web Vitals: ключ Google, расписание и пределы расхода квоты.
	Vitals Vitals

	// Разбор логов веб-сервера клиента: расписание, пределы, порог тревоги.
	Logs Logs

	// Обход сайта для SEO-аудита: расписание и пределы нагрузки.
	Seo Seo

	// Локальная диагностика: ключевые адреса проектов и свой же стек.
	Probes Probes

	// Копия базы в хранилище, которое оплачивает клиент.
	//
	// Единственное место, где агенту нужен пароль от базы: `pg_dump` без
	// него не работает, а сокет docker агенту не дадут ни ради какой
	// функции. Правило «Postgres опрашивается соединением» писалось про
	// диагностику и там остаётся верным.
	Backup backup.Config

	// Еженедельный отчёт о состоянии.
	Digest Digest

	// Язык, на котором агент разговаривает с человеком: отчёт, тревоги,
	// проверочное сообщение. Два языка, как и везде в продукте: `ru` и `en`.
	//
	// Сначала язык был только у отчёта, а тревоги считались русскими по
	// правилу «у агента спросить некого». Спросить было кого — язык уже
	// выбирался для отчёта, — и с 0.15.0 он общий (checks.Words).
	Locale string
}

// Digest — еженедельный отчёт о состоянии сервера и его проектов.
//
// Отчёт уходит **по серверу**, а не по агентству: собрать один на все VPS
// может только тот, кто видит их все, — приложение, которое по условию
// закрыто, иначе письмо не нужно. Агентство с десятью серверами получит
// десять писем, и сервер назван в теме каждого.
type Digest struct {
	Enabled bool
	// День недели: 0 — воскресенье, как в time.Weekday.
	Weekday time.Weekday
	// Час по времени сервера. Отчёт в три ночи воскресенья бесполезен,
	// поэтому день и час задаёт человек, а не мы.
	Hour int
}

// Seo — обход сайта со снятием структуры страниц.
//
// Отдельно от Links, хотя оба ходят по сайту: тот обход собирает адреса для
// lychee и ничего о страницах не помнит, а этот хранит извлечённую
// структуру. Пределы у них тоже разные — SEO-аудиту нужна вся карта сайта,
// проверке ссылок хватает того, что видно с главной.
//
// robots.txt соблюдается всегда и выключателя не имеет: агент — бот на
// живом сервере, пусть сервер и принадлежит агентству.
type Seo struct {
	// Выключатель. Обход сайта целиком — это сотни запросов к чужому
	// проду, и «нам это не нужно» здесь законный ответ.
	Enabled bool
	// Как часто обходить все сайты.
	Interval time.Duration
	// Глубина обхода от главной страницы.
	MaxDepth int
	// Потолок страниц на сайт за обход.
	MaxPages int
	// Сколько запросов в минуту разрешено сайту. Отсюда считается пауза
	// между обращениями; Crawl-delay из robots.txt, если он больше,
	// заменяет её собой.
	RequestsPerMinute int
	// Таймаут одного запроса.
	Timeout time.Duration
	// Сколько байт читать со страницы.
	MaxPageBytes int64
	// Читать ли карту сайта: в ней лежат страницы, на которые не ведёт ни
	// одна ссылка.
	UseSitemap bool
	// Куда не ходить помимо robots.txt: регулярки через точку с запятой —
	// не через запятую, по той же причине, что и у ссылок.
	Exclude []string
}

// Delay — пауза между запросами к одному сайту.
func (s Seo) Delay() time.Duration {
	if s.RequestsPerMinute <= 0 {
		return time.Second
	}
	return time.Minute / time.Duration(s.RequestsPerMinute)
}

// Probes — локальная диагностика.
//
// Проверяет не «отвечает ли сайт», а «работает ли то, ради чего он есть»:
// ключевые адреса проекта по профилю (страница, JSON, перенаправление,
// текст) и свой же стек — соседей по docker-сети.
//
// **Подписывается это честно.** Проверка идёт с самого VPS и не видит
// облачного файрвола провайдера: её ответ — «приложение отвечает», а не
// «посетитель это видит». Ровно поэтому финальная проверка достижимости при
// установке делается снаружи, с backend, а не отсюда.
type Probes struct {
	// Как часто ходить по ключевым адресам.
	Interval time.Duration
	// Сколько ждать ответа.
	Timeout time.Duration
	// Откуда читать профили. Файл лежит рядом со списком сайтов и правится
	// так же — приложением, без перезапуска агента.
	ProfilesPath string
	// Порт, которым стек смотрит в интернет, и домен трекинга: по ним
	// проверяется, отдаётся ли скрипт аналитики.
	EdgePort       int
	TrackingDomain string
	// Край стека на самоподписанном сертификате (`SELF_SIGNED_TLS=1`): его
	// сертификат браузер не примет никогда, и следить за его сроком незачем.
	EdgeSelfSigned bool
	// Токен хука «я задеплоил».
	//
	// Отдельный от токена API намеренно: строку из хука человек вставляет
	// в свой деплой-скрипт, а тот живёт в репозитории. Токен API открывает
	// всё, что агент знает о клиентах агентства; этот — только «сходи по
	// ключевым адресам сейчас». Пусто — хук выключен.
	DeployToken string
}

// Сколько ключевых адресов разрешено проекту.
//
// Три, а не сколько угодно: это чужой прод, и круг ходит по ним каждые
// несколько минут. Ключевых адресов у проекта и не бывает больше — если их
// десять, то это уже не «ради чего сайт есть», а полный обход, который у
// агента и так есть.
//
// Число живёт здесь, а приложение читает его из этого исходника тестом —
// тем же приёмом, что список составных зон и коды ошибок.
const MaxProbesPerProject = 3

// Logs — сбор ошибок из логов веб-сервера клиента.
//
// Веб-сервер этот нам не принадлежит и настраивали его не мы. Всё, что
// делает агент, — дочитывает уже существующие файлы, и только на чтение.
// Хранит он при этом не строки, а счётчики: лог за сутки — это гигабайты на
// чужом диске, а в каждой строке access-лога стоит адрес посетителя.
type Logs struct {
	// Как часто дочитывать логи. Не раз в сутки, как ссылки: всплеск
	// пятисоток — это авария, и узнавать о ней завтра поздно.
	Interval time.Duration
	// Сколько суток хранить счётчики.
	RetentionDays int
	// Сколько байт читать за один проход с одного файла. Остаток дочитает
	// следующий проход: агент не имеет права занимать сервер собой.
	MaxBytes int64
	// Столько ошибок за окно — повод написать человеку. Ноль выключает
	// тревогу совсем, и это законный выбор.
	AlertThreshold int
	AlertWindow    time.Duration
	// Столько ответов «страницы нет» за то же окно — повод показать это в
	// своде. Порог свой и выше: четыреста четвёртая — это ещё и фон от
	// ботов, перебирающих чужие админки, а пятисотка фоном не бывает.
	//
	// Письма по нему не уходят намеренно: пропавшая страница не будит
	// ночью, а лишнее письмо стоит доверия ко всем остальным.
	NotFoundThreshold int
	// Искать ли логи в известных местах. Пустое значение в `agent.env`
	// означает «искать» — как и у прочих признаков агента: приложение
	// всегда пишет сюда 1 или 0, и умолчание существует только для файла,
	// собранного руками.
	Autodiscover bool
	// Откуда читать пути, заданные человеком.
	SourcesPath string
}

// Vitals — измерение производительности сайтов через API Google.
//
// Ключ агентства — такой же секрет, как пароль от почты: приезжает в
// agent.env с правами 600 и на backend разработчика не уходит никогда. Без
// ключа функция выключена целиком: PSI отвечает и без него, но с квотой,
// которой хватает на пару запросов в минуту с адреса, — молча упереться в
// неё хуже, чем честно сказать «ключа нет».
type Vitals struct {
	APIKey string
	// Как часто мерить все страницы всех сайтов.
	Interval time.Duration
	// Типы устройств: mobile, desktop или оба. Google ранжирует их
	// отдельно, поэтому и меряются они отдельно.
	Strategies []string
	// Потолок обращений к API Google за сутки с этого сервера. Квота у
	// ключа общая на всё агентство (25 000 запросов в день у PSI), и один
	// сервер не должен съедать её целиком.
	DailyLimit int
	// Таймаут одного обращения. PSI по-настоящему открывает страницу в
	// браузере и думает десятками секунд.
	Timeout time.Duration
	// Откуда читать список страниц, которые меряем помимо главной.
	PagesPath string
	// Потолок страниц на сайт вместе с главной.
	MaxPages int
}

// Configured — есть ли чем мерить.
func (v Vitals) Configured() bool { return strings.TrimSpace(v.APIKey) != "" }

// Links — пределы краулинга и проверки ссылок.
//
// Всё консервативно: агент стоит на проде клиента, и его работа не имеет
// права мешать работе сайта, ради которого его поставили.
//
// robots.txt соблюдается и здесь, как в SEO-обходе, и выключателя не имеет:
// правило «агент остаётся ботом на живом сервере» общее, и выполняться одним
// краулером из двух оно не может. Решение 2026-09-04.
type Links struct {
	// Как часто гонять полный круг по всем сайтам.
	Interval time.Duration
	// Глубина обхода от главной страницы.
	MaxDepth int
	// Потолок числа страниц на сайт за один круг.
	MaxPages int
	// Сколько запросов в минуту разрешено сайту при обходе. Отсюда
	// считается пауза между обращениями; Crawl-delay из robots.txt, если он
	// больше, заменяет её собой.
	//
	// Про обход, а не про проверку ссылок: проверку ведёт lychee, и её
	// скорость задаётся Concurrency.
	RequestsPerMinute int
	// Одновременные запросы при проверке ссылок.
	Concurrency int
	// Таймаут одного запроса при проверке ссылок.
	Timeout time.Duration
	// Куда не ходить и что не проверять: регулярки, одна строка через точку
	// с запятой. Не запятая: она сама частый символ регулярки — квантификатор
	// вида `{2,4}` — и запятая-разделитель разрезала бы такую регулярку
	// пополам.
	Exclude []string
}

// Delay — пауза между запросами к одному сайту при обходе.
func (l Links) Delay() time.Duration {
	if l.RequestsPerMinute <= 0 {
		return time.Second
	}
	return time.Minute / time.Duration(l.RequestsPerMinute)
}

// SMTP — параметры почты. Агент шлёт письма сам, с сервера клиента: письмо,
// отправленное с десктопа, не уйдёт, когда приложение закрыто.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	To       []string
}

func (s SMTP) Configured() bool {
	return s.Host != "" && s.From != "" && len(s.To) > 0
}

type Telegram struct {
	BotToken string
	ChatID   string
}

func (t Telegram) Configured() bool {
	return t.BotToken != "" && t.ChatID != ""
}

// Webhooks — адреса каналов, в которые пишут POST-запросом: Slack, Discord
// и произвольный вебхук (агент 0.21.0). Пусто — канал выключен.
//
// Адрес целиком — секрет, как токен бота: кто его знает, тот пишет в чат
// агентства. Годность адреса проверяет `notify.CheckHookURL`, не этот пакет.
type Webhooks struct {
	Slack   string
	Discord string
	Custom  string
}

// Alerts — пороги, при которых агент начинает беспокоить человека.
type Alerts struct {
	// Процент занятого диска, выше которого это уже проблема.
	DiskPercent int
	// За сколько дней предупреждать об истечении сертификата.
	CertDays int
	// Ступени напоминаний о сроке домена, от дальней к ближней.
	//
	// Список, а не одно число: продлевает домен человек у регистратора, а
	// не мы, и до этого дня он ничего не может сделать нашими руками.
	// Одного порога хватало, чтобы письмо уходило каждые сутки все тридцать
	// дней подряд, — а письма, которые приходят тридцать раз, перестают
	// читать вместе со всеми остальными.
	//
	// Сертификата это не касается: его продлевает Caddy сам за тридцать
	// дней до срока, и тревога за четырнадцать означает, что продление уже
	// не сработало. Там повтор — не шум, а настоящая незакрытая беда.
	DomainSteps []int
	// Как часто напоминать о том, что уже сообщали. Без этого одна и та же
	// беда пишет письмо каждые пять минут, и её перестают читать.
	RepeatAfter time.Duration
}

// Site — сайт, за которым следит агент.
type Site struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

// Project — группа доменов одного клиента.
//
// Сервера в ней нет намеренно: агент живёт на нём и другого не знает. Нет и
// признака ручной правки — им распоряжается приложение, а агенту он ничего
// не объясняет. Чего не отдали, то с сервера клиента и не утечёт.
type Project struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

// HostName — имя сервера, каким его знает человек.
//
// Не `os.Hostname()`: агент живёт в контейнере, и там это короткий номер
// контейнера (`3f2a9c1b7d4e`), а не имя сервера. С ним еженедельный отчёт,
// проверочное сообщение и поле `server` вебхука называли бы сервер
// бессмысленной строкой (найдено сверкой трека V, спринт 119). Имя сервера
// лежит в `/etc/hostname` корня сервера, который смонтирован агенту на
// чтение; не прочиталось — имя контейнера, а за ним «vps».
func HostName(hostRoot string) string {
	if raw, err := os.ReadFile(filepath.Join(hostRoot, "etc", "hostname")); err == nil {
		if name := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]); name != "" {
			return name
		}
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return "vps"
}

// Load собирает конфигурацию из окружения.
func Load() (Config, error) {
	cfg := Config{
		Token:          os.Getenv("AGENT_TOKEN"),
		Listen:         envOr("AGENT_LISTEN", "0.0.0.0:9101"),
		DatabasePath:   envOr("AGENT_DATABASE", "/data/agent.db"),
		SitesPath:      envOr("AGENT_SITES", "/config/sites.json"),
		ProjectsPath:   envOr("AGENT_PROJECTS", "/config/projects.json"),
		HostProc:       envOr("AGENT_HOST_PROC", "/host/proc"),
		HostRoot:       envOr("AGENT_HOST_ROOT", "/host/root"),
		SampleInterval: envDuration("AGENT_SAMPLE_INTERVAL", time.Minute),
		Retention:      envDuration("AGENT_RETENTION", 30*24*time.Hour),
		CheckInterval:  envDuration("AGENT_CHECK_INTERVAL", 6*time.Hour),
		SMTP: SMTP{
			Host:     os.Getenv("SMTP_HOST"),
			Port:     envInt("SMTP_PORT", 587),
			User:     os.Getenv("SMTP_USER"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     os.Getenv("SMTP_FROM"),
			To:       splitList(os.Getenv("SMTP_TO")),
		},
		Telegram: Telegram{
			BotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
			ChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		},
		// Ключей в шаблоне бандла нет: строки дописывает приложение, как
		// `NOTIFY_MUTED`. Нет ключа — канал выключен.
		Webhooks: Webhooks{
			Slack:   os.Getenv("SLACK_WEBHOOK_URL"),
			Discord: os.Getenv("DISCORD_WEBHOOK_URL"),
			Custom:  os.Getenv("WEBHOOK_URL"),
		},
		// Пусто — не выключены: так выглядит и файл, написанный до 0.20.0.
		NotifyMuted: envBool("NOTIFY_MUTED", false),
		Alerts: Alerts{
			DiskPercent: envInt("ALERT_DISK_PERCENT", 85),
			CertDays:    envInt("ALERT_CERT_DAYS", 14),
			DomainSteps: envSteps("ALERT_DOMAIN_DAYS", []int{30, 14, 7, 1}),
			RepeatAfter: envDuration("ALERT_REPEAT_AFTER", 24*time.Hour),
		},
		Links: Links{
			// Интервал приходит в часах с экрана приложения, а не в
			// формате Go.
			Interval:          time.Duration(envIntClamped("LINK_CHECK_INTERVAL_HOURS", 24, 1)) * time.Hour,
			MaxDepth:          envIntClamped("LINK_MAX_DEPTH", 3, 0),
			MaxPages:          envIntClamped("LINK_MAX_PAGES", 500, 1),
			RequestsPerMinute: envIntClamped("LINK_REQUESTS_PER_MINUTE", 60, 1),
			Concurrency:       envIntClamped("LINK_MAX_CONCURRENCY", 4, 1),
			Timeout:           time.Duration(envIntClamped("LINK_TIMEOUT_SECONDS", 20, 5)) * time.Second,
			Exclude:           splitExcludes(os.Getenv("LINK_EXCLUDE")),
		},
		Lychee: envOr("LYCHEE_BIN", "/usr/local/bin/lychee"),
		Logs: Logs{
			// Секунды, а не формат Go: значение приходит с экрана
			// приложения, как и всё остальное здесь.
			Interval:       time.Duration(envIntClamped("LOG_SCAN_INTERVAL_SECONDS", 60, 15)) * time.Second,
			RetentionDays:  envIntClamped("LOG_RETENTION_DAYS", 14, 1),
			MaxBytes:       int64(envIntClamped("LOG_MAX_BYTES", 8<<20, 64<<10)),
			AlertThreshold: envIntClamped("LOG_ALERT_THRESHOLD", 25, 0),
			AlertWindow:    time.Duration(envIntClamped("LOG_ALERT_WINDOW_MINUTES", 15, 1)) * time.Minute,
			// Вчетверо выше порога пятисоток: столько четыреста четвёртых
			// за четверть часа не набирает ни один живой сайт без
			// происшествия — а бот, перебирающий адреса, набирает
			// десятками.
			NotFoundThreshold: envIntClamped("LOG_NOT_FOUND_THRESHOLD", 100, 0),
			Autodiscover:      envBool("LOG_AUTODISCOVER", true),
			SourcesPath:       envOr("AGENT_LOG_SOURCES", "/config/logs.json"),
		},
		Probes: Probes{
			// Пять минут: ключевой адрес — это то, о чём хотят узнать
			// сегодня, а не завтра, но каждая минута означала бы триста
			// запросов в сутки к чужому проду на каждый адрес.
			Interval:     time.Duration(envIntClamped("PROBE_INTERVAL_MINUTES", 5, 1)) * time.Minute,
			Timeout:      time.Duration(envIntClamped("PROBE_TIMEOUT_SECONDS", 15, 3)) * time.Second,
			ProfilesPath: envOr("AGENT_PROBES", "/config/probes.json"),
			// Приезжают из stack.env через файл компоновки: приложение их
			// уже знает, и спрашивать заново незачем.
			EdgePort:       envInt("AGENT_EDGE_PORT", 0),
			TrackingDomain: strings.TrimSpace(os.Getenv("AGENT_TRACKING_DOMAIN")),
			EdgeSelfSigned: envBool("AGENT_EDGE_SELF_SIGNED", false),
			DeployToken:    strings.TrimSpace(os.Getenv("DEPLOY_TOKEN")),
		},
		Seo: Seo{
			Enabled: envBool("SEO_ENABLED", true),
			// Как и у ссылок с замерами, интервал приходит в часах с
			// экрана приложения, а не в формате Go.
			Interval:          time.Duration(envIntClamped("SEO_INTERVAL_HOURS", 24, 1)) * time.Hour,
			MaxDepth:          envIntClamped("SEO_MAX_DEPTH", 5, 0),
			MaxPages:          envIntClamped("SEO_MAX_PAGES", 500, 1),
			RequestsPerMinute: envIntClamped("SEO_REQUESTS_PER_MINUTE", 60, 1),
			Timeout:           time.Duration(envIntClamped("SEO_TIMEOUT_SECONDS", 20, 5)) * time.Second,
			MaxPageBytes:      int64(envIntClamped("SEO_MAX_PAGE_KB", 2048, 32)) << 10,
			UseSitemap:        envBool("SEO_SITEMAP", true),
			Exclude:           splitExcludes(os.Getenv("SEO_EXCLUDE")),
		},
		Backup: backup.Config{
			Enabled: envBool("BACKUP_ENABLED", false),
			// Раз в сутки: копия — это чтение базы целиком и мегабайты в
			// сеть с сервера клиента, и чаще она нужна разве что тому, кто
			// готов за это платить трафиком.
			Interval: time.Duration(envIntClamped("BACKUP_INTERVAL_HOURS", 24, 1)) * time.Hour,
			// Семь копий: неделя — это срок, за который замечают порчу
			// данных. Ноль означает «не удалять ничего»: правилами
			// жизненного цикла бакета человек вправе распорядиться сам, и
			// затирать их нашей ротацией нельзя.
			Keep:    envIntAtLeast("BACKUP_KEEP", 7, 0),
			Timeout: time.Duration(envIntClamped("BACKUP_TIMEOUT_MINUTES", 60, 5)) * time.Minute,
			// Каталоги не спрашиваются: том агента и его же конфиги лежат
			// там, где уже заданы. Лишний вопрос — это лишний способ
			// ошибиться в том, о чём знать неоткуда.
			// path, а не filepath: это пути внутри контейнера, они всегда
			// со слэшем. На разборе под Windows filepath дал бы `\data`,
			// и тест поймал бы это раньше, чем живой сервер, — но чинить
			// надо не тест.
			WorkDir:   path.Dir(envOr("AGENT_DATABASE", "/data/agent.db")),
			ConfigDir: path.Dir(envOr("AGENT_SITES", "/config/sites.json")),
			StackDir:  envOr("BACKUP_STACK_DIR", "/host/root/opt/vpsfocus"),
			Postgres:  postgres(os.Getenv("AGENT_POSTGRES_URL")),
			Storage: s3.Config{
				Endpoint:  strings.TrimSpace(os.Getenv("BACKUP_S3_ENDPOINT")),
				Region:    strings.TrimSpace(os.Getenv("BACKUP_S3_REGION")),
				Bucket:    strings.TrimSpace(os.Getenv("BACKUP_S3_BUCKET")),
				KeyID:     strings.TrimSpace(os.Getenv("BACKUP_S3_KEY_ID")),
				Secret:    strings.TrimSpace(os.Getenv("BACKUP_S3_SECRET")),
				PathStyle: strings.TrimSpace(os.Getenv("BACKUP_S3_PATH_STYLE")),
			},
			Prefix: strings.Trim(strings.TrimSpace(os.Getenv("BACKUP_S3_PREFIX")), "/"),
		},
		Digest: Digest{
			Enabled: envBool("DIGEST_ENABLED", false),
			Weekday: weekday(os.Getenv("DIGEST_WEEKDAY")),
			// Десять утра: рабочее время в любом часовом поясе сервера, и
			// у человека есть день, чтобы что-то сделать.
			Hour: envHour("DIGEST_HOUR", 10),
		},
		Locale: locale(os.Getenv("AGENT_LOCALE")),
		Vitals: Vitals{
			APIKey: strings.TrimSpace(os.Getenv("VITALS_API_KEY")),
			// Как и у ссылок, интервал приходит в часах с экрана
			// приложения, а не в формате Go.
			Interval:   time.Duration(envIntClamped("VITALS_INTERVAL_HOURS", 24, 1)) * time.Hour,
			Strategies: strategies(os.Getenv("VITALS_STRATEGIES")),
			DailyLimit: envIntClamped("VITALS_DAILY_LIMIT", 200, 1),
			Timeout:    time.Duration(envIntClamped("VITALS_TIMEOUT_SECONDS", 120, 30)) * time.Second,
			PagesPath:  envOr("AGENT_VITALS_PAGES", "/config/vitals.json"),
			MaxPages:   envIntClamped("VITALS_MAX_PAGES", 5, 1),
		},
	}

	// Агент слушает внутри контейнера на всех адресах, а наружу его
	// публикует docker только на петлю хоста. Токен при этом обязателен:
	// внутри docker-сети сосед по стеку до него дотянется.
	if strings.TrimSpace(cfg.Token) == "" {
		return Config{}, fmt.Errorf("AGENT_TOKEN не задан: без него API открыт всем в docker-сети")
	}

	return cfg, nil
}

// Sites читает список сайтов. Отсутствие файла — не ошибка: агент полезен и
// без сайтов, он всё равно следит за самим сервером.
func (c Config) Sites() ([]Site, error) {
	raw, err := os.ReadFile(c.SitesPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("список сайтов: %w", err)
	}

	var sites []Site
	if err := json.Unmarshal(raw, &sites); err != nil {
		return nil, fmt.Errorf("список сайтов: %w", err)
	}

	kept := sites[:0]
	for _, site := range sites {
		site.Domain = strings.TrimSpace(strings.ToLower(site.Domain))
		if site.Domain == "" {
			continue
		}
		if site.Name == "" {
			site.Name = site.Domain
		}
		kept = append(kept, site)
	}
	return kept, nil
}

// Projects читает состав проектов, а если файла нет — складывает их сам.
//
// Складывает по регистрируемому домену, тем же правилом, каким это делает
// приложение: `example.com`, `www.example.com` и `shop.example.com` — один
// клиент. Запасной путь нужен ровно один раз в жизни каждого сервера — от
// обновления стека до первой раздачи состава приложением, — но без него в
// эту минуту еженедельный отчёт ушёл бы вообще без проектов, а свод оказался
// бы пуст. Молчаливый ноль хуже честного приближения.
//
// Домен, которого нет ни в одном проекте (приложение его ещё не разложило),
// заводит собственный проект по тому же правилу: сайт, о котором никто не
// рассказал, всё равно обязан попасть в свод.
func (c Config) Projects() ([]Project, error) {
	sites, err := c.Sites()
	if err != nil {
		return nil, err
	}

	projects, err := c.storedProjects()
	if err != nil {
		return nil, err
	}

	known := map[string]bool{}
	kept := make([]Project, 0, len(projects))
	for _, project := range projects {
		domains := make([]string, 0, len(project.Domains))
		for _, domain := range project.Domains {
			domain = strings.TrimSpace(strings.ToLower(domain))
			// Домена нет среди сайтов сервера — состав разошёлся с тем, что
			// стоит на сервере. Верим серверу: карточка про домен, которого
			// здесь нет, рассказывала бы о пустоте.
			if domain == "" || !hasDomain(sites, domain) || known[domain] {
				continue
			}
			known[domain] = true
			domains = append(domains, domain)
		}
		if len(domains) == 0 {
			continue
		}
		if project.Name == "" {
			project.Name = domains[0]
		}
		project.Domains = domains
		kept = append(kept, project)
	}

	// Всё, что приложение не разложило, раскладываем сами.
	byGroup := map[string]int{}
	for index, project := range kept {
		if len(project.Domains) > 0 {
			byGroup[domains.Registrable(project.Domains[0])] = index
		}
	}
	for _, site := range sites {
		if known[site.Domain] {
			continue
		}
		known[site.Domain] = true

		group := domains.Registrable(site.Domain)
		if index, ok := byGroup[group]; ok {
			kept[index].Domains = append(kept[index].Domains, site.Domain)
			continue
		}
		byGroup[group] = len(kept)
		kept = append(kept, Project{ID: group, Name: group, Domains: []string{site.Domain}})
	}

	return kept, nil
}

func hasDomain(sites []Site, domain string) bool {
	for _, site := range sites {
		if site.Domain == domain {
			return true
		}
	}
	return false
}

// storedProjects читает файл состава. Его отсутствие — не ошибка: приложение
// могло его ещё не написать.
func (c Config) storedProjects() ([]Project, error) {
	raw, err := os.ReadFile(c.ProjectsPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("состав проектов: %w", err)
	}

	var projects []Project
	if err := json.Unmarshal(raw, &projects); err != nil {
		return nil, fmt.Errorf("состав проектов: %w", err)
	}
	return projects, nil
}

// Pages читает список страниц, которые меряем помимо главной.
//
// Файл лежит рядом со списком сайтов и правится так же — приложением, без
// перезапуска агента: страницу добавляют, когда у клиента появился важный
// раздел, и простой ради этого никому не нужен.
//
// Отсутствие файла — не ошибка: мерить главную страницу каждого сайта агент
// умеет и без него.
func (c Config) Pages() (map[string][]string, error) {
	raw, err := os.ReadFile(c.Vitals.PagesPath)
	if os.IsNotExist(err) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("список страниц: %w", err)
	}

	var entries []struct {
		Domain string   `json:"domain"`
		Paths  []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("список страниц: %w", err)
	}

	pages := make(map[string][]string, len(entries))
	for _, entry := range entries {
		domain := strings.TrimSpace(strings.ToLower(entry.Domain))
		if domain == "" {
			continue
		}

		seen := map[string]bool{"/": true}
		var paths []string
		for _, path := range entry.Paths {
			path = strings.TrimSpace(path)
			if path == "" || strings.Contains(path, " ") {
				continue
			}
			// Только путь на самом сайте. Полный адрес означал бы, что
			// агент по просьбе из файла меряет чужой сайт нашим ключом —
			// а квота у ключа общая на всё агентство.
			if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
				continue
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
		pages[domain] = paths
	}
	return pages, nil
}

// LogSources читает пути логов, заданные человеком.
//
// Файл лежит рядом со списком сайтов и правится так же — приложением, без
// перезапуска агента: путь к логу выясняется уже после установки, когда
// человек увидел, что автопоиск ничего не нашёл.
//
// Путь, не прошедший проверку, молча выбрасывается: агенту смонтирован весь
// корень сервера, и строка в конфиге не должна превращаться в «покажи мне
// содержимое любого файла».
func (c Config) LogSources() ([]string, error) {
	raw, err := os.ReadFile(c.Logs.SourcesPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("список логов: %w", err)
	}

	var entries []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("список логов: %w", err)
	}

	var paths []string
	seen := map[string]bool{}
	for _, entry := range entries {
		path := strings.TrimSpace(entry.Path)
		if path == "" || seen[path] || !logs.CheckPath(path) {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths, nil
}

// ProbeProfiles читает ключевые адреса проектов.
//
// Отсутствие файла — не ошибка: ключевые адреса задаёт человек, и пустой
// список означает «пока не задал», а не поломку.
//
// Профиль без проекта выбрасывается: свод состояния поднимает уровень
// именно проекту, и находка, которой некуда лечь, никого не разбудит.
// Лишние адреса сверх потолка отбрасываются здесь же — приложение их и не
// даст завести, но конфиг переживает выпуски, а чужой прод один.
func (c Config) ProbeProfiles() ([]probe.Profile, error) {
	raw, err := os.ReadFile(c.Probes.ProfilesPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ключевые адреса: %w", err)
	}

	var profiles []probe.Profile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, fmt.Errorf("ключевые адреса: %w", err)
	}

	perProject := map[string]int{}
	kept := profiles[:0]
	for _, profile := range profiles {
		profile.URL = strings.TrimSpace(profile.URL)
		profile.ProjectID = strings.TrimSpace(profile.ProjectID)
		if profile.URL == "" || profile.ProjectID == "" {
			continue
		}
		if !ProbeURLAllowed(profile.URL) {
			continue
		}
		if perProject[profile.ProjectID] >= MaxProbesPerProject {
			continue
		}
		perProject[profile.ProjectID]++

		if profile.Name == "" {
			profile.Name = profile.URL
		}
		kept = append(kept, profile)
	}
	return kept, nil
}

// ProbeURLAllowed отвечает, годится ли адрес ключевой проверки.
//
// Схема — только http и https: адрес приходит из файла, а не из кода, и
// `file://` превратил бы проверку в чтение чужих файлов чужими глазами.
//
// Учётных данных (`user:pass@host`) и строки запроса в адресе нет, и это
// запрет, а не длина. Заголовок с токеном профилю разрешён законно — он
// живёт на сервере клиента, в файле с правами 640, и токен там собственный, —
// но это разрешение на **заголовок**: заголовок не показывается нигде, а
// адрес уходит в письмо о беде, в журнал сервера и в базу агента, откуда его
// читает экран. Тот же запрет и по той же причине стоит у внешней проверки
// (`monitoring.ValidatePath`: «в строке запроса лежат токены закрытых
// разделов клиентов») и у переноса из чужого сервиса
// (`import.rs::split_address`).
//
// Отбор двойной: первым негодный адрес отвергает приложение — оно
// единственный писатель `probes.json` и может объяснить человеку причину, —
// а здесь стоит вторая линия. Файл переживает выпуски приложения, а чужой
// прод один.
func ProbeURLAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	if parsed.User != nil {
		return false
	}
	// Пустой хвостовой `?` — не строка запроса, а мусор копирования: адрес
	// от него не меняется, и отказывать по нему было бы придиркой. Ровно
	// так же на него смотрят перенос из чужого сервиса и первая линия
	// отбора в приложении; разойдись они здесь, приложение принимало бы
	// адрес, который агент молча выбрасывает.
	if parsed.RawQuery != "" {
		return false
	}
	return true
}

// strategies разбирает список типов устройств.
//
// Неизвестные значения выбрасываются, пустой список превращается в мобильный:
// им Google меряет чаще всего, и «ничего не мерить» — не то, чего просят,
// заполняя это поле.
func strategies(raw string) []string {
	var picked []string
	seen := map[string]bool{}
	for _, item := range splitList(raw) {
		value := strings.ToLower(strings.TrimSpace(item))
		if value != "mobile" && value != "desktop" {
			continue
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		picked = append(picked, value)
	}
	if len(picked) == 0 {
		return []string{"mobile"}
	}
	return picked
}

// weekday разбирает день недели: число 0–6 или английское имя.
//
// Умолчание — понедельник: отчёт про неделю, которая начинается, полезнее
// отчёта про неделю, которая кончилась.
func weekday(raw string) time.Weekday {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return time.Monday
	}
	if number, err := strconv.Atoi(raw); err == nil && number >= 0 && number <= 6 {
		return time.Weekday(number)
	}
	for day := time.Sunday; day <= time.Saturday; day++ {
		if strings.ToLower(day.String()) == raw {
			return day
		}
	}
	return time.Monday
}

// envHour читает час суток. Бессмыслица заменяется умолчанием, а не
// обрезается: «25» — это опечатка, и угадывать за человека, имел ли он в
// виду час ночи или одиннадцать вечера, мы не станем.
func envHour(name string, fallback int) int {
	hour := envInt(name, fallback)
	if hour < 0 || hour > 23 {
		return fallback
	}
	return hour
}

// locale — язык, на котором агент пишет человеку.
//
// Языка два, как и в интерфейсе. Русский по умолчанию: сервер, на котором
// язык не задан, пишет так же, как писал до перевода тревог.
func locale(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "en") {
		return "en"
	}
	return "ru"
}

// postgres разбирает адрес базы, который приезжает из docker-compose.
//
// Одной строкой, а не пятью переменными: строку собирает тот же файл
// компоновки, который уже собирает её для Umami, и разойтись двум записям
// одного адреса негде.
//
// Разбор свой, а не `url.Parse`: тот отвергает всю строку целиком, если в
// пароле окажется символ, недопустимый в userinfo, — кириллица, пробел,
// собака. Наши пароли генерируются буквами и цифрами, но `agent.env`
// человек правит руками, и «копии молча не делаются, потому что в пароле
// русская буква» — это полдня поисков не там. Собака ищется последняя:
// в пароле она законна, в имени сервера — нет.
//
// Пароль отсюда уезжает окружением дочернего процесса, а не аргументом
// команды: `/proc/<pid>/cmdline` читает любой пользователь сервера.
func postgres(raw string) backup.Postgres {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return backup.Postgres{}
	}

	if index := strings.Index(raw, "://"); index >= 0 {
		raw = raw[index+3:]
	}

	credentials := ""
	if index := strings.LastIndex(raw, "@"); index >= 0 {
		credentials, raw = raw[:index], raw[index+1:]
	}

	database := ""
	if index := strings.Index(raw, "/"); index >= 0 {
		raw, database = raw[:index], raw[index+1:]
	}
	// Параметры запроса вроде `?sslmode=disable` до нас не относятся:
	// внутри docker-сети TLS между контейнерами не настраивается.
	if index := strings.Index(database, "?"); index >= 0 {
		database = database[:index]
	}

	host, port := raw, 5432
	if index := strings.LastIndex(raw, ":"); index >= 0 {
		if number, err := strconv.Atoi(raw[index+1:]); err == nil && number > 0 {
			host, port = raw[:index], number
		}
	}

	if host == "" {
		return backup.Postgres{}
	}

	user, password := credentials, ""
	if index := strings.Index(credentials, ":"); index >= 0 {
		user, password = credentials[:index], credentials[index+1:]
	}

	// Экранирование в пароле разворачиваем, если оно есть: скопированный из
	// панели хостера адрес приходит и таким. Не развернулось — берём как
	// есть: пароль с процентом законен.
	if decoded, err := url.QueryUnescape(password); err == nil {
		password = decoded
	}

	return backup.Postgres{
		Host:     host,
		Port:     port,
		User:     user,
		Database: database,
		Password: password,
	}
}

// envIntAtLeast — то же, что envIntClamped, но допускает ноль как значение.
//
// Отдельной функцией, а не минимумом в ноль у соседней: там значение ниже
// минимума означает «человек написал бессмыслицу, берём умолчание», а
// здесь ноль — законный ответ «не удалять ничего».
func envIntAtLeast(name string, fallback, minimum int) int {
	value := envInt(name, fallback)
	if value < minimum {
		return fallback
	}
	return value
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}

// envIntClamped не даёт настройкам с экрана стать бессмыслицей: ноль
// страниц или отрицательная глубина означали бы круг, который ничего не
// делает. Значение ниже минимума заменяется умолчанием.
func envIntClamped(name string, fallback, minimum int) int {
	value := envInt(name, fallback)
	if value < minimum {
		return fallback
	}
	return value
}

// envBool читает выключатель. Всё, кроме явного «0», «false» и «no», —
// это «да»: выключить функцию можно, но случайной опечаткой в конфиге —
// нельзя.
// envSteps читает ступени напоминания: «30,14,7,1».
//
// Одно число тоже годится — так поле выглядело до тридцатого спринта, и
// конфиг, собранный руками год назад, обязан продолжать работать. Порядок
// приводится к убывающему здесь, а не там, где считают: перевёрнутый
// список молча сломал бы правило «напомнили на ступени — дальше молчим до
// следующей».
func envSteps(name string, fallback []int) []int {
	var steps []int
	for _, part := range splitList(os.Getenv(name)) {
		value, err := strconv.Atoi(part)
		if err != nil || value <= 0 {
			continue
		}
		steps = append(steps, value)
	}
	if len(steps) == 0 {
		return fallback
	}

	sort.Sort(sort.Reverse(sort.IntSlice(steps)))

	// Дубли убираются: две одинаковые ступени означали бы два письма об
	// одном и том же дне.
	unique := steps[:1]
	for _, step := range steps[1:] {
		if step != unique[len(unique)-1] {
			unique = append(unique, step)
		}
	}
	return unique
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "":
		return fallback
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// splitList разбирает список через запятую: адреса получателей приезжают
// одной строкой, потому что окружение других форм не знает.
func splitList(raw string) []string {
	var items []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

// splitExcludes разбирает список регулярок через точку с запятой — не через
// запятую, как splitList. Запятая — частый символ внутри самой регулярки
// (квантификатор `{2,4}`), и делить список по ней порезало бы такую
// регулярку пополам.
func splitExcludes(raw string) []string {
	var items []string
	for _, part := range strings.Split(raw, ";") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}
