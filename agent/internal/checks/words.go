package checks

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

	BackupSubject   string
	BackupBody      string // код
	BackupOKSubject string
	BackupOKBody    string // ключ, байт

	TestSubject string
	TestBody    string // имя сервера
}

// WordsFor — тексты на языке `AGENT_LOCALE`; всё, кроме `en`, — русский,
// как и у отчёта (`config.locale`).
func WordsFor(locale string) Words {
	if locale == "en" {
		return english
	}
	return russian
}

var russian = Words{
	DiskSubject: "Диск сервера занят на %d%%",
	DiskBody: "Занято %d%% дискового пространства, свободно %d МБ из %d МБ.\n\n" +
		"Когда место кончится, встанут и аналитика, и сайт клиента.",
	DiskOKSubject: "Место на диске сервера снова в норме",
	DiskOKBody:    "Занято %d%%, свободно %d МБ.",

	CertProblemSubject: "Сертификат %s: проблема",
	CertProblemBody: "Проверка сертификата не прошла: %s.\n\n" +
		"Посетители сайта видят предупреждение браузера.",
	CertExpiringSubject: "Сертификат %s истекает через %d дн.",
	CertExpiringBody: "Сертификат действует до %s.\n\n" +
		"Если он выпущен нашим стеком, продление автоматическое — " +
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

	BackupSubject: "Копия базы не сделалась",
	BackupBody: "Очередная копия аналитики не уехала в хранилище: %s.\n\n" +
		"Пока копий нет, потеря диска на этом сервере означает потерю всей " +
		"статистики клиентов. Настройки хранилища — в приложении, на экране " +
		"уведомлений; что именно не заладилось, видно на экране безопасности " +
		"сервера.",
	BackupOKSubject: "Копия базы снова делается",
	BackupOKBody:    "Копия аналитики уехала в хранилище: %s, %d байт.",

	TestSubject: "Проверка уведомлений",
	TestBody: "Это проверочное сообщение от агента на сервере %s.\n\n" +
		"Если вы его читаете, уведомления о диске, сертификатах и доменах " +
		"дойдут тем же путём.",
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

	BackupSubject: "Database backup failed",
	BackupBody: "The latest analytics backup did not reach the storage: %s.\n\n" +
		"Until there are backups, losing this server's disk means losing all " +
		"the statistics. Storage settings are in the app, on the Notifications " +
		"screen; what went wrong is shown on the server's Security screen.",
	BackupOKSubject: "Database backups work again",
	BackupOKBody:    "The analytics backup reached the storage: %s, %d bytes.",

	TestSubject: "Notification test",
	TestBody: "This is a test message from the agent on server %s.\n\n" +
		"If you are reading it, notifications about the disk, certificates and " +
		"domains will arrive the same way.",
}
