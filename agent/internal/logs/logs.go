// Package logs разбирает логи веб-сервера клиента и считает в них ошибки.
//
// Веб-сервер клиента нам не принадлежит: мы его не настраивали, не знаем его
// версии и не собираемся под него подстраиваться. Всё, что делает этот
// пакет, — читает файлы, которые уже лежат на диске, и только на чтение.
// Ничего не пишется, ничего не поворачивается, ни одна строка чужого лога не
// покидает сервер.
//
// Хранятся не строки, а счётчики. Во-первых, лог за сутки — это гигабайты на
// диске, который нам не принадлежит. Во-вторых, в каждой строке access-лога
// стоит адрес посетителя, и держать их у себя мы не станем: агрегату он не
// нужен, а утечь может только то, что где-то лежит.
package logs

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"agent/internal/text"
)

// Вид лога. Access говорит, что сломалось и где, error — почему.
const (
	KindAccess = "access"
	KindError  = "error"
)

// Уровни error-лога, которые мы вообще замечаем.
//
// warn и notice в nginx — это будни работающего сайта (закрытое соединение,
// отброшенный заголовок). Собирать их значит утопить настоящую беду в шуме.
var interestingLevels = map[string]bool{
	"error": true,
	"crit":  true,
	"alert": true,
	"emerg": true,
	"fatal": true,
	"panic": true,
}

// Шаг окна агрегации.
//
// Пять минут — компромисс: всплеск виден, а строк в базе на порядки меньше,
// чем событий. График рисуется по этим же окнам.
const BucketSeconds = 300

// WindowStart — начало окна тревоги, выровненное по границе бакета.
//
// Выравнивание обязательно, и вот почему. В базе лежат не события, а
// счётчики за пятиминутный бакет: «сколько пятисоток пришлось на окно с
// 12:05 по 12:10». Спроси мы «всё, что случилось за последние пятнадцать
// минут», ответить точно было бы нечем — бакет либо берётся целиком, либо
// не берётся вовсе.
//
// До спринта 36 отбор шёл по времени последнего события в бакете, и в
// пятнадцатиминутное окно попадал целиком бакет, начавшийся до него: счёт
// раздувался на треть, а письмо называло это число «за 15 минут». Теперь
// граница честная — по бакетам, — и рядом стоит `WindowMinutes`, который
// говорит, сколько минут на самом деле сосчитано.
func WindowStart(now time.Time, window time.Duration) time.Time {
	from := now.Add(-window).Unix()
	return time.Unix(from-mod(from, BucketSeconds), 0).UTC()
}

// WindowMinutes — сколько минут на самом деле попало в окно.
//
// Число для человека: оно уходит в письмо и в свод, и врать в нём нельзя.
// Всегда кратно пяти и всегда не меньше запрошенного окна.
func WindowMinutes(now time.Time, window time.Duration) int {
	return int(now.Sub(WindowStart(now, window)) / time.Minute)
}

// mod — остаток, неотрицательный и для времён до эпохи.
func mod(value, step int64) int64 {
	rest := value % step
	if rest < 0 {
		rest += step
	}
	return rest
}

// StatusNotFound — ответ «страницы нет».
//
// Считается наравне с ошибками сервера, но отдельно от них: всплеск
// пятисоток значит «сайт сломался», всплеск четыреста четвёртых — «страницы
// пропали», и чинят их разные люди разными способами.
const StatusNotFound = 404

// Interesting — стоит ли запоминать этот ответ.
//
// Двухсотки в базе — это гигабайты счётчиков ради «сайт работает», о чём и
// так есть кому рассказать. Остаются ошибки сервера и «страницы нет»: обе —
// про то, что посетитель не получил, за чем пришёл.
func Interesting(status int) bool {
	return status >= 500 || status == StatusNotFound
}

// Event — одна разобранная строка лога, уже без всего лишнего.
type Event struct {
	Kind string
	At   time.Time

	// Код ответа для access-лога; у error-лога нулевой.
	Status int
	// Уровень для error-лога; у access-лога пустой.
	Level string

	// Домен, если формат лога его содержит.
	Host string
	// Путь запроса без строки параметров.
	Path string
	// Метод запроса.
	Method string

	// Подпись группы: нормализованное сообщение error-лога. У access-лога
	// пустая — там группируют код и путь.
	Signature string
	// Пример для человека: сообщение без адреса посетителя.
	Sample string

	// Есть ли в строке access-лога поле User-agent.
	HasAgent bool
	// Робот, которым назвалась строка: имя из `bots.List`, `bots.Other` или
	// пусто. Сама строка User-agent не хранится нигде.
	Robot string
}

// Bucket — начало окна, в которое попадает событие.
func (e Event) Bucket() int64 {
	return e.At.Unix() - e.At.Unix()%BucketSeconds
}

// Key — по чему события считаются одной группой.
//
// Метод в ключе не для полноты: POST /checkout, отдающий 500, и GET того же
// адреса — разные беды, и лечить их будут по-разному.
func (e Event) Key() string {
	return strings.Join([]string{
		e.Kind, strconv.Itoa(e.Status), e.Level, e.Host, e.Method, e.Path, e.Signature,
	}, "\x00")
}

// Group — сколько раз повторилось одно и то же.
//
// Это и есть то, что хранится и показывается: строки логов остаются на
// сервере клиента, к нам — то есть в базу агента — попадают счётчики.
type Group struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	// Код ответа для access-лога, ноль для error-лога.
	Status int `json:"status"`
	// Уровень для error-лога.
	Level  string `json:"level,omitempty"`
	Host   string `json:"host,omitempty"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	// Подпись сообщения error-лога: по ней события и сведены в группу.
	Signature string `json:"signature,omitempty"`
	// Пример сообщения — без адреса посетителя.
	Sample string `json:"sample,omitempty"`

	Count   int    `json:"count"`
	FirstAt string `json:"firstAt"`
	LastAt  string `json:"lastAt"`
}

// Point — окно на графике: сколько ошибок сервера и сколько записей в
// error-логе пришлось на эти пять минут.
type Point struct {
	At     string `json:"at"`
	Access int    `json:"access"`
	Errors int    `json:"errors"`
}

// Spike — сколько ошибок набралось за окно тревоги у одного домена.
//
// Две цифры рядом, а не одна: всплеск пятисоток значит «сайт сломался»,
// всплеск «страницы нет» — «страницы пропали при выкатке». Пороги у них
// разные и чинят их по-разному, поэтому и складывать их в одно число
// нельзя.
//
// Пустой домен — законное значение: формат `combined` у nginx домена не
// пишет вовсе. Такие ошибки относятся к серверу целиком.
type Spike struct {
	Host         string `json:"host,omitempty"`
	ServerErrors int    `json:"serverErrors"`
	NotFound     int    `json:"notFound"`
}

// SourceState — источник вместе с тем, чем кончилось последнее чтение.
type SourceState struct {
	Source
	// Код беды: файла нет, прав нет, не читается. Пусто — всё в порядке.
	Error string `json:"error,omitempty"`
	// Когда читали в последний раз.
	ReadAt string `json:"readAt,omitempty"`
	// Сколько событий из него набралось за показанный период.
	Count int `json:"count"`
}

var (
	// Адреса посетителей в сообщениях error-лога. Вырезаются до записи:
	// в агрегате они не нужны, а хранить чужие персональные данные на
	// сервере клиента ради красивого примера — плохая сделка.
	nginxClient  = regexp.MustCompile(`,?\s*client: [^,]+`)
	apacheClient = regexp.MustCompile(`\[client [^\]]+\]\s*`)
	inlineIPv4   = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?\b`)
	// Групп в IPv6 требуется не меньше четырёх намеренно: с меньшим числом
	// под шаблон попадает время вида 12:00:05, которого в каждой второй
	// строке лога по нескольку штук.
	inlineIPv6 = regexp.MustCompile(`(?:[0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}`)

	// Числа внутри сообщения: pid, номер соединения, размер. Для подписи
	// они шум — из-за них одна и та же ошибка распадается на сотню групп.
	numbers = regexp.MustCompile(`\d+`)
)

// NormalizePath приводит путь запроса к виду, по которому есть смысл
// группировать: без строки параметров и без идентификаторов.
func NormalizePath(raw string) string {
	path := raw
	if cut := strings.IndexAny(path, "?#"); cut >= 0 {
		path = path[:cut]
	}
	// Абсолютный адрес в строке запроса встречается у прокси и у ботов.
	if scheme := strings.Index(path, "://"); scheme >= 0 {
		if slash := strings.Index(path[scheme+3:], "/"); slash >= 0 {
			path = path[scheme+3+slash:]
		} else {
			path = "/"
		}
	}
	if path == "" {
		path = "/"
	}

	// Посегментно, а не одной регуляркой: в Go нет заглядывания вперёд, а
	// без него шаблон «число до конца сегмента» не записать. Заодно так
	// понятнее, что именно считается идентификатором.
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		switch {
		case segment == "":
		case isDigits(segment):
			segments[i] = "{n}"
		case isIdentifier(segment):
			segments[i] = "{id}"
		}
	}

	return truncate(strings.Join(segments, "/"), 300)
}

// isIdentifier узнаёт UUID и длинные шестнадцатеричные хвосты: у каждого
// заказа он свой, и без свёртки список групп становится списком запросов.
func isIdentifier(segment string) bool {
	hex := func(value string) bool {
		for _, symbol := range value {
			switch {
			case symbol >= '0' && symbol <= '9':
			case symbol >= 'a' && symbol <= 'f':
			case symbol >= 'A' && symbol <= 'F':
			default:
				return false
			}
		}
		return value != ""
	}

	if len(segment) == 36 {
		parts := strings.Split(segment, "-")
		if len(parts) == 5 && len(parts[0]) == 8 && len(parts[1]) == 4 &&
			len(parts[2]) == 4 && len(parts[3]) == 4 && len(parts[4]) == 12 {
			for _, part := range parts {
				if !hex(part) {
					return false
				}
			}
			return true
		}
	}

	return len(segment) >= 16 && hex(segment)
}

// StripClient убирает из сообщения адрес посетителя.
func StripClient(message string) string {
	message = nginxClient.ReplaceAllString(message, "")
	message = apacheClient.ReplaceAllString(message, "")
	message = inlineIPv4.ReplaceAllString(message, "{ip}")
	message = inlineIPv6.ReplaceAllString(message, "{ip}")
	return strings.TrimSpace(message)
}

// Signature сводит сообщение к подписи группы: без адресов, без чисел и
// короткое. Одна и та же беда обязана попадать в одну строку экрана.
func Signature(message string) string {
	signature := StripClient(message)
	signature = numbers.ReplaceAllString(signature, "{n}")
	signature = strings.Join(strings.Fields(signature), " ")
	return truncate(signature, 200)
}

// truncate — обрезка с многоточием: человек должен видеть, что строку
// укоротили, а не гадать, чем кончалось сообщение.
//
// По символам, а не по байтам: обрезанная посередине кириллица превращает
// сообщение в мусор, который потом ищут глазами в интерфейсе. Общей
// функцией, а не своей копией, — см. `internal/text`.
func truncate(value string, limit int) string {
	short := text.Truncate(value, limit)
	if short == value {
		return value
	}
	return short + "…"
}
