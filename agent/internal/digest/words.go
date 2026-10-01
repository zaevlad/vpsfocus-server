package digest

import (
	"agent/internal/backup"
	"agent/internal/checks"
	"agent/internal/health"
	"agent/internal/probe"
	"agent/internal/seo"
)

// Коды неудачной проверки ссылок.
//
// Строками, а не константами соседнего пакета: собирает их `cmd/agent`, а
// он импортирует нас — обратный импорт невозможен. Расхождение поймает тест
// `TestLinkDetailCodesMatchTheAgent`, который читает их прямо из исходника,
// тем же приёмом, каким приложение сверяет список составных зон.
const (
	linkRobotsUnavailable = "linksRobotsUnavailable"
	linkRobotsForbidden   = "linksRobotsForbidden"
	lycheeUnavailable     = "lycheeUnavailable"
	linkScanTimeout       = "linkScanTimeout"
	linkScanFailed        = "linkScanFailed"
)

// phrases — словарь одного языка.
//
// Структурой с полями, а не картой строк: забытая фраза должна быть ошибкой
// сборки, а не пустотой в письме, которое уже ушло человеку. Тексты поводов
// — карта, и её полнота проверяется тестом по тому же списку кодов, по
// которому проверяется таблица уровней.
type phrases struct {
	subject          string
	intro            string
	summaryOK        string
	summaryAttention string
	summaryProblem   string
	serverTitle      string
	noProjects       string
	nothing          string
	muted            string
	maintenance      string
	footer           string
	markProblem      string
	markAttention    string
	reasons          map[string]string
	// Уточнения: почему проверка не состоялась.
	//
	// Отдельной картой от поводов, потому что список свой: повод приходит
	// от свода, а уточнение — от той проверки, которая не сложилась, и
	// словари у них не пересекаются. Полнота проверяется тестом по обоим
	// языкам сразу: половина переведённой карты — это письмо, в котором
	// половина строк на английском.
	details map[string]string
}

func dictionary(locale string) phrases {
	if locale == "en" {
		return english
	}
	return russian
}

var russian = phrases{
	subject:          "Отчёт за неделю",
	intro:            "Еженедельный отчёт о сервере %s и проектах на нём.",
	summaryOK:        "Делать нечего: всё в порядке.",
	summaryAttention: "Стоит посмотреть: %d.",
	summaryProblem:   "Требует вмешательства: %d.",
	serverTitle:      "Сервер",
	noProjects:       "Проектов на сервере пока нет.",
	nothing:          "всё в порядке",
	muted:            "Приглушено находок: %d. Их можно вернуть в отчёт на экране проектов.",
	maintenance:      "Сейчас идёт обслуживание: тревоги приглушены до его конца.",
	footer:           "Отчёт отправлен агентом vpsFocus с этого сервера. День и час меняются в настройках уведомлений.",
	markProblem:      "[!]",
	markAttention:    "[·]",
	reasons: map[string]string{
		health.CodeDiskFilling:    "диск занят на {count}%",
		health.CodeDiskFull:       "диск занят на {count}% — сервер начнёт ломаться сам",
		health.CodeMemoryTight:    "память занята на {count}%",
		health.CodeMetricsMissing: "замеров нет вовсе, возможно, агент не запустился",
		health.CodeMetricsStale:   "последнему замеру {count} минут — похоже, агент встал",
		health.CodeNoChannels:     "не настроен ни один канал уведомлений",
		health.CodeDomainExpiring: "регистрация домена кончается через {days} дн.",
		health.CodeDomainExpired:  "регистрация домена кончилась",
		health.CodeDomainUnknown:  "срок регистрации не удалось узнать ({detail})",
		health.CodeCertExpiring:   "сертификат истекает через {days} дн.",
		health.CodeCertExpired:    "сертификат истёк",
		health.CodeCertInvalid:    "сертификат не принят или не подходит домену ({detail})",
		health.CodeLinksBroken:    "битых ссылок: {count}",
		health.CodeLinksFailed:    "проверка ссылок не состоялась ({detail})",
		health.CodeSeoCritical:    "критичных находок SEO: {count}",
		health.CodeSeoFailed:      "обход сайта не состоялся ({detail})",
		health.CodeSeoCanonical:   "вход на сайт настроен неверно: {detail}",
		health.CodeSeoBlocked:     "robots.txt закрывает сайт от поисковиков: {detail}",
		health.CodeVitalsPoor:     "посетители видят плохие Core Web Vitals, замеров с оценкой «плохо»: {count}",
		health.CodeProbeFailing:   "ключевой адрес не работает как должен ({detail})",
		health.CodeErrorSpike:     "веб-сервер отдал ошибок: {count} за {minutes} мин.",
		health.CodeNotFoundSpike:  "ответов «страницы нет»: {count} за {minutes} мин.",
		health.CodeServiceDown:    "сервис стека не отвечает ({detail})",
		health.CodeTrackerMissing: "скрипт трекинга не отдаётся — аналитика не собирается ({detail})",
		health.CodeBackupFailed:   "последняя копия базы не сделалась ({detail})",
		// Ноль дней здесь означает «копий нет вовсе»: подставлять «0 дн.»
		// было бы неправдой в самую тревожную сторону.
		health.CodeBackupStale:   "свежей копии базы нет: последняя {count} дн. назад ({detail})",
		health.CodeBackupMissing: "копий базы нет ни одной ({detail})",
	},
	details: map[string]string{
		checks.ErrNoExpiry:   "реестр не назвал срок",
		checks.ErrLookup:     "реестр не ответил",
		checks.ErrUnregister: "домен не зарегистрирован",

		checks.ErrTLSUnreachable:  "порт не отвечает",
		checks.ErrTLSExpired:      "срок сертификата вышел",
		checks.ErrTLSWrongHost:    "сертификат выдан другому домену",
		checks.ErrTLSUntrusted:    "сертификат не принят системными корнями",
		checks.ErrTLSIncomplete:   "цепочка сертификата неполная",
		checks.ErrTLSHandshakeBad: "соединение TLS не установилось",

		// Коды проверки ссылок собираются в `cmd/agent`, а не константами
		// в пакете: импортировать оттуда нельзя — это он импортирует нас.
		linkRobotsUnavailable: "robots.txt не отдался",
		linkRobotsForbidden:   "robots.txt закрыл сайт целиком",
		lycheeUnavailable:     "проверяльщик ссылок недоступен",
		linkScanTimeout:       "круг не уложился в отведённое время",
		linkScanFailed:        "круг оборвался",

		seo.ErrRobotsUnavailable: "robots.txt не отдался",
		seo.ErrRobotsForbidden:   "robots.txt закрыл сайт целиком",
		seo.ErrStartUnreachable:  "главная страница не открылась",
		seo.ErrTimeout:           "обход не уложился в отведённое время",
		seo.ErrNoPages:           "обход не принёс ни одной страницы",
		seo.ErrScanFailed:        "обход оборвался",

		seo.IssueCanonicalNoHTTPS:   "по http сайт отдаётся вместо перехода на https",
		seo.IssueCanonicalHostSplit: "www и адрес без www — два разных сайта",
		seo.IssueCanonicalLoop:      "перенаправления входа зациклены",

		probe.CodeUnreachable:   "до адреса не достучались",
		probe.CodeTimeout:       "ответа не дождались",
		probe.CodeTLSFailed:     "сертификат не принят",
		probe.CodeStatus:        "не тот код ответа",
		probe.CodeNotHTML:       "ответ не похож на страницу",
		probe.CodeNotJSON:       "ответ не разобрался как JSON",
		probe.CodeNotScript:     "ответ не похож на скрипт",
		probe.CodeNoRedirect:    "перенаправления не было",
		probe.CodeWrongTarget:   "перенаправляет не туда",
		probe.CodeTextMissing:   "нужного текста на странице нет",
		probe.CodeMisconfigured: "проверять нечего: профиль пуст",

		backup.CodeNotConfigured:      "хранилище не настроено",
		backup.CodeToolMissing:        "в образе агента нет pg_dump — обновите стек",
		backup.CodeDumpFailed:         "дамп базы не снялся",
		backup.CodeDumpEmpty:          "дамп оказался пустым",
		backup.CodeArchiveFailed:      "архив не собрался",
		backup.CodeArchiveBroken:      "собранный архив не открылся обратно",
		backup.CodeAccessDenied:       "хранилище не приняло ключ доступа",
		backup.CodeBucketMissing:      "бакета с таким именем нет",
		backup.CodeStorageUnreachable: "до хранилища не достучались",
		backup.CodeUploadFailed:       "хранилище отказало",
		backup.CodeMissingAfterUpload: "копии в хранилище не оказалось",
		backup.CodeTimeout:            "круг не уложился в отведённое время",
	},
}

var english = phrases{
	subject:          "Weekly report",
	intro:            "Weekly report on server %s and the projects it runs.",
	summaryOK:        "Nothing to do: everything is fine.",
	summaryAttention: "Worth a look: %d.",
	summaryProblem:   "Needs attention: %d.",
	serverTitle:      "Server",
	noProjects:       "No projects on this server yet.",
	nothing:          "all fine",
	muted:            "Muted findings: %d. They can be brought back on the projects screen.",
	maintenance:      "Maintenance is running: alerts are muted until it ends.",
	footer:           "Sent by the vpsFocus agent on this server. The day and hour are set in notification settings.",
	markProblem:      "[!]",
	markAttention:    "[·]",
	reasons: map[string]string{
		health.CodeDiskFilling:    "disk is {count}% full",
		health.CodeDiskFull:       "disk is {count}% full — the server will start failing on its own",
		health.CodeMemoryTight:    "memory is {count}% used",
		health.CodeMetricsMissing: "no measurements at all, the agent may not have started",
		health.CodeMetricsStale:   "the last measurement is {count} minutes old — the agent seems to have stopped",
		health.CodeNoChannels:     "no notification channel is configured",
		health.CodeDomainExpiring: "the domain registration ends in {days} days",
		health.CodeDomainExpired:  "the domain registration has run out",
		health.CodeDomainUnknown:  "the registration date could not be read ({detail})",
		health.CodeCertExpiring:   "the certificate expires in {days} days",
		health.CodeCertExpired:    "the certificate has expired",
		health.CodeCertInvalid:    "the certificate is not trusted or does not match the domain ({detail})",
		health.CodeLinksBroken:    "{count} broken links",
		health.CodeLinksFailed:    "the link check did not go through ({detail})",
		health.CodeSeoCritical:    "{count} critical SEO findings",
		health.CodeSeoFailed:      "the crawl did not go through ({detail})",
		health.CodeSeoCanonical:   "the site entry is misconfigured: {detail}",
		health.CodeSeoBlocked:     "robots.txt blocks the site from search engines: {detail}",
		health.CodeVitalsPoor:     "visitors see poor Core Web Vitals, {count} measurements rated poor",
		health.CodeProbeFailing:   "a key URL is not working as it should ({detail})",
		health.CodeErrorSpike:     "the web server returned {count} errors in {minutes} min",
		health.CodeNotFoundSpike:  "{count} not-found responses in {minutes} min",
		health.CodeServiceDown:    "a stack service is not answering ({detail})",
		health.CodeTrackerMissing: "the tracking script is not served — analytics is not being collected ({detail})",
		health.CodeBackupFailed:   "the last database copy failed ({detail})",
		health.CodeBackupStale:    "no fresh database copy: the last one is {count} days old ({detail})",
		health.CodeBackupMissing:  "there is no database copy at all ({detail})",
	},
	details: map[string]string{
		checks.ErrNoExpiry:   "the registry did not give a date",
		checks.ErrLookup:     "the registry did not answer",
		checks.ErrUnregister: "the domain is not registered",

		checks.ErrTLSUnreachable:  "the port does not answer",
		checks.ErrTLSExpired:      "the certificate has run out",
		checks.ErrTLSWrongHost:    "the certificate was issued for another domain",
		checks.ErrTLSUntrusted:    "the certificate is not trusted by the system roots",
		checks.ErrTLSIncomplete:   "the certificate chain is incomplete",
		checks.ErrTLSHandshakeBad: "the TLS connection was not established",

		// Коды проверки ссылок собираются в `cmd/agent`, а не константами
		// в пакете: импортировать оттуда нельзя — это он импортирует нас.
		linkRobotsUnavailable: "robots.txt was not served",
		linkRobotsForbidden:   "robots.txt closed the whole site",
		lycheeUnavailable:     "the link checker is unavailable",
		linkScanTimeout:       "the run did not fit in its time",
		linkScanFailed:        "the run broke off",

		seo.ErrRobotsUnavailable: "robots.txt was not served",
		seo.ErrRobotsForbidden:   "robots.txt closed the whole site",
		seo.ErrStartUnreachable:  "the home page did not open",
		seo.ErrTimeout:           "the crawl did not fit in its time",
		seo.ErrNoPages:           "the crawl brought back no pages",
		seo.ErrScanFailed:        "the crawl broke off",

		seo.IssueCanonicalNoHTTPS:   "http serves the site instead of redirecting to https",
		seo.IssueCanonicalHostSplit: "www and the bare domain are two separate sites",
		seo.IssueCanonicalLoop:      "the entry redirects loop",

		probe.CodeUnreachable:   "the address could not be reached",
		probe.CodeTimeout:       "no answer in time",
		probe.CodeTLSFailed:     "the certificate was not accepted",
		probe.CodeStatus:        "wrong status code",
		probe.CodeNotHTML:       "the answer is not a page",
		probe.CodeNotJSON:       "the answer is not JSON",
		probe.CodeNotScript:     "the answer does not look like a script",
		probe.CodeNoRedirect:    "there was no redirect",
		probe.CodeWrongTarget:   "it redirects somewhere else",
		probe.CodeTextMissing:   "the text is not on the page",
		probe.CodeMisconfigured: "there is nothing to check: the profile is empty",

		backup.CodeNotConfigured:      "storage is not configured",
		backup.CodeToolMissing:        "the agent image has no pg_dump — update the stack",
		backup.CodeDumpFailed:         "the database dump failed",
		backup.CodeDumpEmpty:          "the dump came out empty",
		backup.CodeArchiveFailed:      "the archive was not built",
		backup.CodeArchiveBroken:      "the built archive did not open back",
		backup.CodeAccessDenied:       "the storage rejected the access key",
		backup.CodeBucketMissing:      "there is no bucket with that name",
		backup.CodeStorageUnreachable: "the storage could not be reached",
		backup.CodeUploadFailed:       "the storage refused the upload",
		backup.CodeMissingAfterUpload: "the copy was not in the storage afterwards",
		backup.CodeTimeout:            "the round did not fit in its time",
	},
}
