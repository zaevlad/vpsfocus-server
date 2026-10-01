package checks

import (
	"fmt"

	"agent/internal/config"
	"agent/internal/metrics"
)

// Alert — повод побеспокоить человека.
type Alert struct {
	// Вид и цель вместе образуют ключ, по которому помнится, сообщали ли уже.
	Kind   string
	Target string
	// Как назвать эту цель в журнале сервера. Пусто — сама цель.
	//
	// Обычно они совпадают: «root», домен, адрес — это и ключ, и то, что
	// человек ждёт увидеть в ленте изменений. Расходятся они там, где ключ
	// составной: у ключевой проверки цель — её личность целиком (проект,
	// адрес, вид, имя), потому что тревога у каждой проверки своя, а в
	// ленте нужен адрес, а не склейка четырёх полей.
	Label string
	// Тема и текст письма — на языке `AGENT_LOCALE` (см. Words): сообщение
	// уходит человеку, а не в двуязычный интерфейс.
	Subject string
	Body    string
	// Ступень напоминания: за столько дней предупреждаем. Ноль означает
	// тревогу без ступеней — она повторяется по общему правилу «не чаще
	// раза в сутки», пока беда не кончится.
	//
	// У ступенчатой тревоги повтора нет вовсе: письмо уходит один раз на
	// каждой ступени. Иначе «домен кончается через тридцать дней» пришло
	// бы тридцать раз подряд — а письма, которые приходят тридцать раз,
	// перестают читать вместе со всеми остальными.
	Step int
}

// Shown — как тревога называется в журнале сервера.
func (a Alert) Shown() string {
	if a.Label != "" {
		return a.Label
	}
	return a.Target
}

// Виды поводов.
const (
	KindDisk   = "disk"
	KindCert   = "cert"
	KindDomain = "domain"
	// Всплеск ошибок в логах веб-сервера. Решение о нём принимается не
	// здесь: Evaluate смотрит на замер и проверки, а счёт ошибок живёт в
	// базе агента, и спрашивать её отсюда значило бы тащить сюда хранилище.
	KindLogs = "logs"
	// Ключевой адрес проекта перестал работать так, как должен. Решение о
	// нём принимается там же, где идёт проверка: Evaluate ходить в сеть не
	// умеет и уметь не должен.
	KindProbe = "probe"
	// Копия базы не уехала в хранилище. Решение принимается там же, где
	// идёт круг копии: Evaluate не знает ни про хранилище, ни про то, чем
	// кончилась выгрузка.
	KindBackup = "backup"
)

// Evaluate решает, о чём стоит сообщить, и говорит об этом словами words.
//
// Возвращает и то, что горит, и то, что погасло: сказать «всё снова в
// порядке» так же важно, как сказать «сломалось». Без этого человек не
// узнаёт, помогли ли его действия.
func Evaluate(
	cfg config.Alerts,
	words Words,
	sample metrics.Sample,
	domains []DomainStatus,
	certs []TLSStatus,
) (firing []Alert, resolved []Alert) {
	// ── Диск ──────────────────────────────────────────────────────────────
	// Память и процессор скачут и приходят в норму сами; кончившийся диск
	// сам не рассасывается и роняет сайт клиента.
	if sample.DiskTotalMB > 0 {
		used := sample.DiskUsedPercent()
		alert := Alert{
			Kind:    KindDisk,
			Target:  "root",
			Subject: fmt.Sprintf(words.DiskSubject, used),
			Body:    fmt.Sprintf(words.DiskBody, used, sample.DiskFreeMB, sample.DiskTotalMB),
		}
		if used >= cfg.DiskPercent {
			firing = append(firing, alert)
		} else {
			resolved = append(resolved, Alert{
				Kind:    KindDisk,
				Target:  "root",
				Subject: words.DiskOKSubject,
				Body:    fmt.Sprintf(words.DiskOKBody, used, sample.DiskFreeMB),
			})
		}
	}

	// ── Сертификаты ───────────────────────────────────────────────────────
	for _, cert := range certs {
		// Цель — с портом, когда он не 443: сайт клиента и край нашего
		// стека на том же имени — разные проверки (правка 1б).
		target := cert.Target()
		alert := Alert{Kind: KindCert, Target: target}

		switch {
		case cert.Error != "" && cert.Error != ErrTLSIncomplete:
			alert.Subject = fmt.Sprintf(words.CertProblemSubject, target)
			alert.Body = fmt.Sprintf(words.CertProblemBody, cert.Error)
			firing = append(firing, alert)

		case cert.DaysLeft != nil && *cert.DaysLeft <= cfg.CertDays:
			alert.Subject = fmt.Sprintf(words.CertExpiringSubject, target, *cert.DaysLeft)
			alert.Body = fmt.Sprintf(words.CertExpiringBody, cert.ExpiresAt.Format("2006-01-02"))
			firing = append(firing, alert)

		default:
			alert.Subject = fmt.Sprintf(words.CertOKSubject, target)
			alert.Body = words.CertOKBody
			resolved = append(resolved, alert)
		}
	}

	// ── Домены ────────────────────────────────────────────────────────────
	//
	// Ступенями, а не одним порогом. Продлевает домен человек у
	// регистратора, и до этого дня ничего нашими руками он не сделает:
	// напоминание за тридцать дней, повторённое тридцать раз, — это не
	// забота, а шум за нашей подписью.
	for _, domain := range domains {
		alert := Alert{Kind: KindDomain, Target: domain.Domain}

		// Неизвестный срок — не повод для тревоги: у половины зон нет ни
		// RDAP, ни разбираемого whois. Кричать об этом раз в сутки значит
		// приучить не читать письма.
		if domain.DaysLeft == nil {
			continue
		}

		if step, ok := reachedStep(cfg.DomainSteps, *domain.DaysLeft); ok {
			alert.Step = step
			alert.Subject = fmt.Sprintf(words.DomainExpiringSubject, domain.Domain, *domain.DaysLeft)
			alert.Body = fmt.Sprintf(words.DomainExpiringBody,
				domain.ExpiresAt.Format("2006-01-02"),
				registrarSuffix(domain.Registrar))
			firing = append(firing, alert)
		} else {
			alert.Subject = fmt.Sprintf(words.DomainRenewedSubject, domain.Domain)
			alert.Body = fmt.Sprintf(words.DomainRenewedBody, domain.ExpiresAt.Format("2006-01-02"))
			resolved = append(resolved, alert)
		}
	}

	return firing, resolved
}

// reachedStep — до какой ступени напоминания дожил срок.
//
// Возвращает самую *ближнюю* из пройденных: за пять дней до конца человеку
// надо сказать «через пять», а не «через тридцать», даже если тридцать он
// уже проходил. Отрицательный остаток — тоже ступень: срок прошёл, и
// молчать об этом нельзя.
//
// Ступени приходят по убыванию — их так приводит `envSteps`, и полагаться
// на это можно: другого пути в конфиг у них нет.
func reachedStep(steps []int, daysLeft int) (int, bool) {
	reached := 0
	found := false
	for _, step := range steps {
		if daysLeft <= step {
			reached = step
			found = true
		}
	}
	return reached, found
}

// LargestStep — самая дальняя ступень. Ею свод отвечает на вопрос «пора ли
// беспокоиться»: у него ступеней нет, там повод либо есть, либо нет.
func LargestStep(steps []int) int {
	largest := 0
	for _, step := range steps {
		if step > largest {
			largest = step
		}
	}
	return largest
}

func registrarSuffix(registrar string) string {
	if registrar == "" {
		return ""
	}
	return " (" + registrar + ")"
}
