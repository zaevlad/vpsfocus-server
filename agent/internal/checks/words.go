package checks

import "agent/internal/locale"

// Words — тексты тревог на одном языке.
//
// До 2026-09-28 тревоги были только русскими: считалось, что «у агента
// спросить некого». Спросить есть кого — язык уже выбирается для недельного
// отчёта (`AGENT_LOCALE`), и англоязычный человек получал отчёт по-английски,
// а тревогу о кончившемся диске — по-русски. Решение владельца: язык один на
// все сообщения агента.
//
// Структурой с полями, а не картой строк, по той же причине, что у отчёта
// (`digest.phrases`): забытая фраза — ошибка сборки, а не пустое письмо.
// Строки — шаблоны `fmt`; аргументы у обоих языков идут в одном порядке, и
// тест сверяет число подстановок в каждой паре.
type Words struct {
	DiskSubject   string // занято, %
	DiskBody      string // занято %, свободно МБ, всего МБ
	DiskOKSubject string
	DiskOKBody    string // занято %, свободно МБ

	CertProblemSubject  string // цель
	CertProblemBody     string // код ошибки
	CertExpiringSubject string // цель, дней
	CertExpiringBody    string // дата
	CertOKSubject       string // цель
	CertOKBody          string

	DomainExpiringSubject string // домен, дней
	DomainExpiringBody    string // дата, « (регистратор)» или пусто
	DomainRenewedSubject  string // домен
	DomainRenewedBody     string // дата

	LogsSubject   string // ошибок, минут
	LogsBody      string // минут, ошибок, порог
	LogsOKSubject string
	LogsOKBody    string // минут, ошибок, порог

	ProbeSubject   string // имя проверки
	ProbeBody      string // имя, адрес, код
	ProbeOKSubject string // имя проверки
	ProbeOKBody    string // имя, адрес
	// Проверка порта (спринт 149): свои слова — за портом не адрес, а
	// служба, и «ключевой адрес по адресу port:5432» читается как поломка.
	PortSubject   string // имя проверки, порт
	PortBody      string // порт, имя проверки
	PortOKSubject string // имя проверки, порт
	PortOKBody    string // порт, имя проверки

	BackupSubject   string
	BackupBody      string // код
	BackupOKSubject string
	BackupOKBody    string // ключ, байт

	InodesSubject   string // занято, %
	InodesBody      string // занято %, свободно мест
	InodesOKSubject string
	InodesOKBody    string // занято %

	BackupShrunkSubject   string
	BackupShrunkBody      string // было МБ, стало МБ (строки: «0.4», «120»)
	BackupShrunkOKSubject string
	BackupShrunkOKBody    string // МБ (строка)

	// Остановки программ ядром из-за нехватки памяти (трек AH, Р15).
	OomSubject      string
	OomBody         string // остановок, строка про имена (OomNames или OomNamesUnknown)
	OomNames        string // список OomProcess через запятую
	OomNamesUnknown string
	OomProcess      string // имя, МБ

	// Записи домена (трек AH, спринт 267, Р17).
	RecordGoneSubject  string // домен, SPF или DMARC
	RecordGoneBody     string // SPF или DMARC, домен
	RecordBackSubject  string // домен, SPF или DMARC
	RecordBackBody     string // SPF или DMARC, домен
	NameserversSubject string // домен
	NameserversBody    string // домен, было, стало

	TestSubject string
	TestBody    string // имя сервера

	// Оформление письма (трек AC, Р26): слово на полосе по тону, источник в
	// шапке и подвал «кто прислал и где выключить». Только почта — в
	// Telegram и вебхуки уходит текст без них.
	MailBandProblem string
	MailBandWarning string
	MailBandOK      string
	MailSource      string // имя сервера
	MailFooter      string // имя сервера

	// Десятичный разделитель — типографика языка (трек AE): «0,4 МБ» по-
	// русски, «0.4 MB» по-английски. Число с дробью агент собирает сам
	// (размер копии), и точка в русском письме — опечатка.
	Decimal string
}

// words — словари по языкам из перечня `locale.Known`; полноту сверяет
// тест (трек AE).
var words = map[string]Words{
	locale.English:    english,
	locale.Russian:    russian,
	locale.Spanish:    spanish,
	locale.German:     german,
	locale.French:     french,
	locale.Portuguese: portuguese,
}

// WordsFor — тексты на языке `AGENT_LOCALE` по правилу [locale.Parse]:
// пустое — русский, незнакомое — английский.
func WordsFor(code string) Words {
	return words[locale.Parse(code)]
}

var russian = Words{
	DiskSubject: "Диск сервера занят на %d%%",
	DiskBody: "Занято %d%% дискового пространства, свободно %d МБ из %d МБ.\n\n" +
		"Когда место кончится, встанут и аналитика, и сайт.",
	DiskOKSubject: "Место на диске сервера снова в норме",
	DiskOKBody:    "Занято %d%%, свободно %d МБ.",

	CertProblemSubject: "Сертификат %s: проблема",
	CertProblemBody: "Проверка сертификата не прошла: %s.\n\n" +
		"Посетители сайта видят предупреждение браузера.",
	CertExpiringSubject: "Сертификат %s истекает через %d дн.",
	CertExpiringBody: "Сертификат действует до %s.\n\n" +
		"Если его выпустил мониторинг vpsFocus, продление автоматическое — " +
		"проверьте, что домен всё ещё указывает на сервер.",
	CertOKSubject: "Сертификат %s снова в порядке",
	CertOKBody:    "Проверка проходит, срок не близко.",

	DomainExpiringSubject: "Домен %s истекает через %d дн.",
	DomainExpiringBody: "Регистрация домена действует до %s.\n\n" +
		"Продление — на стороне регистратора%s.",
	DomainRenewedSubject: "Домен %s продлён",
	DomainRenewedBody:    "Регистрация действует до %s.",

	LogsSubject: "Сервер отдаёт ошибки: %d за %d мин.",
	LogsBody: "За последние %d минут в логах веб-сервера набралось %d ответов с кодом 5xx " +
		"при пороге %d.\n\nЧто именно ломается, видно на экране «Ошибки» в приложении: " +
		"там ошибки сгруппированы по адресу и коду.",
	LogsOKSubject: "Ошибки сервера прекратились",
	LogsOKBody:    "За последние %d минут в логах веб-сервера %d ответов с кодом 5xx — это ниже порога %d.",

	ProbeSubject: "Ключевой адрес не работает: %s",
	ProbeBody: "Проверка «%s» по адресу %s не прошла: %s.\n\n" +
		"Проверка идёт с самого сервера, поэтому она отвечает на вопрос " +
		"«приложение отвечает», а не «посетитель это видит»: облачный " +
		"файрвол провайдера отсюда не виден.",
	ProbeOKSubject: "Ключевой адрес снова работает: %s",
	ProbeOKBody:    "Проверка «%s» по адресу %s снова проходит.",

	PortSubject: "Порт больше не слушается: %s (%s)",
	PortBody: "Порт %s на сервере больше никто не слушает (проверка «%s»): служба, " +
		"которая на нём работала, остановилась или перешла на другой порт.\n\n" +
		"Проверка смотрит таблицу портов самого сервера: она говорит, занят ли " +
		"порт, но не то, отвечает ли служба правильно. Если служба работает в " +
		"контейнере без опубликованного порта, отсюда её не видно.",
	PortOKSubject: "Порт снова слушается: %s (%s)",
	PortOKBody:    "Порт %s на сервере снова занят службой (проверка «%s»).",

	BackupSubject: "Копия базы не сделалась",
	BackupBody: "Очередная копия аналитики не уехала в хранилище: %s.\n\n" +
		"Пока копий нет, потеря диска на этом сервере означает потерю всей " +
		"статистики посещений. Настройки хранилища — в приложении, на экране " +
		"уведомлений; что именно не заладилось, видно на экране безопасности " +
		"сервера.",
	BackupOKSubject: "Копия базы снова делается",
	BackupOKBody:    "Копия аналитики уехала в хранилище: %s, %d байт.",

	InodesSubject: "На сервере заканчивается лимит файлов: занято %d%%",
	InodesBody: "Лимит файлов на диске (inodes) занят на %d%%, осталось %d.\n\n" +
		"Свободное место на диске при этом может оставаться, но новый файл не " +
		"создастся — сайт перестанет сохранять сессии, загрузки и кеш. Обычно " +
		"его съедает папка с множеством мелких файлов: кеш, сессии, старая " +
		"почта. Подробности — на экране «Здоровье сервера» в приложении.",
	InodesOKSubject: "Лимит файлов на сервере снова в норме",
	InodesOKBody:    "Лимит файлов на диске занят на %d%%.",

	BackupShrunkSubject: "Копия базы стала намного меньше прошлой",
	BackupShrunkBody: "Копия аналитики создана и проверена, но внутри меньше половины " +
		"прошлой: было %s МБ, стало %s МБ.\n\n" +
		"Так бывает после чистки старой статистики — тогда делать ничего не нужно. " +
		"Если вы ничего не удаляли, проверьте, не пропали ли данные: история копий — " +
		"на экране «Резервные копии» в приложении.",
	BackupShrunkOKSubject: "Копия базы снова обычного размера",
	BackupShrunkOKBody:    "Очередная копия аналитики — %s МБ, не меньше половины прошлой.",

	OomSubject: "Серверу не хватило памяти, и система остановила программу",
	OomBody: "За последние сутки система останавливала программы из-за нехватки памяти: " +
		"остановок — %d. Так бывает, когда памяти не хватило всему серверу или пределу, " +
		"заданному самой программе. Сайт в такие минуты может моргнуть или упасть.\n\n%s\n\n" +
		"Что делать: откройте «Здоровье сервера» в приложении — там видно, сколько памяти " +
		"занято и сколько из неё берёт сам мониторинг vpsFocus, и есть задание для " +
		"ИИ-помощника. Обычно помогает добавить памяти на тарифе хостера или включить подкачку.",
	OomNames:        "Больше всего памяти сразу после этого занимали: %s.",
	OomNamesUnknown: "Какие программы занимали память, отсюда не видно.",
	OomProcess:      "%s (%d МБ)",

	RecordGoneSubject: "%s: пропала запись %s",
	RecordGoneBody: "У домена больше нет рабочей записи %s (%s): её удалили, или записей стало две — почтовые службы считают это тем же, что «записи нет».\n\n" +
		"Если с домена уходят письма — регистрация, заказы, рассылки, — они могут начать попадать в спам. Если вы меняли записи у регистратора сами и так задумано, ничего делать не нужно. " +
		"Что сейчас записано у домена, видно на экране «Домены и SSL» в приложении; там же — задание для ИИ-помощника.",
	RecordBackSubject:  "%s: запись %s снова на месте",
	RecordBackBody:     "Запись %s у домена %s снова есть и читается.",
	NameserversSubject: "%s: сменились серверы имён",
	NameserversBody: "Домен %s теперь обслуживают другие серверы имён: было — %s, стало — %s.\n\n" +
		"Так бывает, когда домен переносят к другому регистратору или подключают к Cloudflare. Если вы этого не делали, проверьте доступ к панели регистратора: " +
		"серверы имён решают, куда ведёт домен и куда приходит почта. Резолвер сервера помнит старые записи, поэтому смену видно с задержкой до суток-двух.",

	TestSubject: "Проверка уведомлений",
	TestBody: "Это проверочное сообщение с сервера %s.\n\n" +
		"Если вы его читаете, уведомления о диске, сертификатах и доменах " +
		"дойдут тем же путём.",

	MailBandProblem: "Проблема",
	MailBandWarning: "Предупреждение",
	MailBandOK:      "Снова в порядке",
	MailSource:      "сервер %s",
	MailFooter: "Это письмо прислал мониторинг vpsFocus с сервера %s — через почту, " +
		"указанную в «Каналах уведомлений», а не через нас. Выключить уведомления " +
		"с этого сервера: приложение vpsFocus → «Все серверы» → «Настройки» → " +
		"«Уведомления», блок «Какие серверы присылают уведомления».",

	Decimal: ",",
}

var english = Words{
	DiskSubject: "Server disk is %d%% full",
	DiskBody: "%d%% of the disk is used, %d MB free out of %d MB.\n\n" +
		"When space runs out, both the analytics and the site stop.",
	DiskOKSubject: "Server disk space is back to normal",
	DiskOKBody:    "%d%% used, %d MB free.",

	CertProblemSubject: "Certificate %s: a problem",
	CertProblemBody: "The certificate check failed: %s.\n\n" +
		"Visitors see a browser warning.",
	CertExpiringSubject: "Certificate %s expires in %d days",
	CertExpiringBody: "The certificate is valid until %s.\n\n" +
		"If our monitoring issued it, it renews automatically — " +
		"check that the domain still points at the server.",
	CertOKSubject: "Certificate %s is fine again",
	CertOKBody:    "The check passes, and expiry is not close.",

	DomainExpiringSubject: "Domain %s expires in %d days",
	DomainExpiringBody: "The domain registration is valid until %s.\n\n" +
		"Renewal is done at your registrar%s.",
	DomainRenewedSubject: "Domain %s renewed",
	DomainRenewedBody:    "The registration is valid until %s.",

	LogsSubject: "The server is returning errors: %d in %d min",
	LogsBody: "In the last %d minutes the web server logs collected %d responses with a 5xx code, " +
		"above the threshold of %d.\n\nWhat exactly breaks is on the Errors screen in the app: " +
		"errors are grouped there by address and code.",
	LogsOKSubject: "Server errors have stopped",
	LogsOKBody:    "In the last %d minutes the web server logs have %d responses with a 5xx code — below the threshold of %d.",

	ProbeSubject: "Key address is not working: %s",
	ProbeBody: "The check “%s” at %s failed: %s.\n\n" +
		"The check runs from the server itself, so it answers “does the " +
		"application respond”, not “does a visitor see it”: the hosting " +
		"provider's cloud firewall is not visible from here.",
	ProbeOKSubject: "Key address works again: %s",
	ProbeOKBody:    "The check “%s” at %s passes again.",

	PortSubject: "Port is no longer listening: %s (%s)",
	PortBody: "Nothing on the server listens on port %s any more (check “%s”): the " +
		"service that ran there has stopped or moved to another port.\n\n" +
		"The check reads the server's own table of ports: it tells whether the " +
		"port is taken, not whether the service answers correctly. A service in " +
		"a container without a published port is not visible from here.",
	PortOKSubject: "Port is listening again: %s (%s)",
	PortOKBody:    "A service listens on port %s on the server again (check “%s”).",

	BackupSubject: "Database backup failed",
	BackupBody: "The latest analytics backup did not reach the storage: %s.\n\n" +
		"Until there are backups, losing this server's disk means losing all " +
		"the statistics. Storage settings are in the app, on the Notifications " +
		"screen; what went wrong is shown on the server's Security screen.",
	BackupOKSubject: "Database backups work again",
	BackupOKBody:    "The analytics backup reached the storage: %s, %d bytes.",

	InodesSubject: "The server is running out of its file limit: %d%% used",
	InodesBody: "%d%% of the disk’s file limit (inodes) is used, %d left.\n\n" +
		"There may still be free space on the disk, but no new file can be " +
		"created — the site will stop saving sessions, uploads and cache. Usually " +
		"a folder with a huge number of small files is to blame: cache, sessions, " +
		"old mail. Details are on the Server health screen in the app.",
	InodesOKSubject: "The server’s file limit is back to normal",
	InodesOKBody:    "%d%% of the disk’s file limit is used.",

	BackupShrunkSubject: "The database backup is much smaller than the last one",
	BackupShrunkBody: "The analytics backup was made and checked, but it holds less than half " +
		"of the previous one: it was %s MB, now %s MB.\n\n" +
		"This happens after old statistics are cleaned up — then there is nothing to do. " +
		"If you deleted nothing, make sure no data is missing: the backup history " +
		"is on the Backups screen in the app.",
	BackupShrunkOKSubject: "The database backup is its usual size again",
	BackupShrunkOKBody:    "The latest analytics backup is %s MB, no less than half of the previous one.",

	OomSubject: "The server ran out of memory, and the system stopped a program",
	OomBody: "Over the last day the system stopped programs because memory ran out: " +
		"%d stop(s). This happens when the whole server runs out of memory, or when a program " +
		"hits a memory limit set for it. The website may blink or go down at such moments.\n\n%s\n\n" +
		"What to do: open “Server health” in the app — it shows how much memory is used and how " +
		"much of it the vpsFocus monitoring itself takes, and has a task for an AI assistant. " +
		"Adding memory on your hosting plan or turning on swap usually helps.",
	OomNames:        "Right after that, the programs using the most memory were: %s.",
	OomNamesUnknown: "Which programs were using the memory can’t be seen from here.",
	OomProcess:      "%s (%d MB)",

	RecordGoneSubject: "%s: the %s record is gone",
	RecordGoneBody: "The domain no longer has a working %s record (%s): it was deleted, or there are now two of them — mail services treat that the same as “no record”.\n\n" +
		"If email is sent from this domain — sign-ups, orders, newsletters — it may start landing in spam. If you changed the records at your registrar yourself and meant to, there is nothing to do. " +
		"What the domain has now is shown on the “Domains & SSL” screen in the app, together with a task for an AI assistant.",
	RecordBackSubject:  "%s: the %s record is back",
	RecordBackBody:     "The %s record of %s is there again and can be read.",
	NameserversSubject: "%s: the name servers changed",
	NameserversBody: "The domain %s is now served by other name servers: before — %s, now — %s.\n\n" +
		"This happens when a domain moves to another registrar or is connected to Cloudflare. If you didn’t do this, check access to your registrar’s panel: " +
		"the name servers decide where the domain leads and where its mail goes. The server’s resolver remembers old records, so the change shows up with a delay of up to a day or two.",

	TestSubject: "Notification test",
	TestBody: "This is a test message from server %s.\n\n" +
		"If you are reading it, notifications about the disk, certificates and " +
		"domains will arrive the same way.",

	MailBandProblem: "Problem",
	MailBandWarning: "Heads-up",
	MailBandOK:      "Back to normal",
	MailSource:      "server %s",
	MailFooter: "This email was sent by vpsFocus monitoring on server %s — through the " +
		"mail account set in Notification channels, not through us. To turn off " +
		"notifications from this server: vpsFocus app → All servers → Settings → " +
		"Notifications, the “Which servers send notifications” block.",

	Decimal: ".",
}
