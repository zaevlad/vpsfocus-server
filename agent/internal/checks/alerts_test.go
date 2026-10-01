package checks

import (
	"testing"
	"time"

	"agent/internal/config"
	"agent/internal/metrics"
)

func thresholds() config.Alerts {
	return config.Alerts{
		DiskPercent: 85,
		CertDays:    14,
		DomainSteps: []int{30, 14, 7, 1},
	}
}

func find(alerts []Alert, kind, target string) *Alert {
	for i := range alerts {
		if alerts[i].Kind == kind && alerts[i].Target == target {
			return &alerts[i]
		}
	}
	return nil
}

// Диск — единственный показатель сервера, о котором агент беспокоит: память
// и процессор скачут и приходят в норму сами.
func TestDiskAlertFiresAtThreshold(t *testing.T) {
	full := metrics.Sample{DiskTotalMB: 1000, DiskFreeMB: 100} // занято 90%
	firing, resolved := Evaluate(thresholds(), WordsFor("ru"), full, nil, nil)

	if find(firing, KindDisk, "root") == nil {
		t.Fatal("заполненный диск не поднял тревогу")
	}
	if find(resolved, KindDisk, "root") != nil {
		t.Error("заполненный диск не может быть одновременно в норме")
	}

	roomy := metrics.Sample{DiskTotalMB: 1000, DiskFreeMB: 500}
	firing, resolved = Evaluate(thresholds(), WordsFor("ru"), roomy, nil, nil)
	if find(firing, KindDisk, "root") != nil {
		t.Error("свободный диск поднял тревогу")
	}
	if find(resolved, KindDisk, "root") == nil {
		t.Error("свободный диск должен считаться нормой: без этого не сказать, что починилось")
	}
}

// Замер без данных о диске — это «мы не смогли посмотреть», а не «диск
// пуст». Тревожить по нему нельзя.
func TestEmptySampleSaysNothingAboutDisk(t *testing.T) {
	firing, resolved := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, nil, nil)
	if len(firing) != 0 || len(resolved) != 0 {
		t.Errorf("пустой замер породил тревоги: firing=%d resolved=%d", len(firing), len(resolved))
	}
}

func TestCertificateAlerts(t *testing.T) {
	soon := 3
	far := 60

	certs := []TLSStatus{
		{Domain: "soon.example.com", Valid: true, DaysLeft: &soon, ExpiresAt: ptr(time.Now().Add(72 * time.Hour))},
		{Domain: "fine.example.com", Valid: true, DaysLeft: &far, ExpiresAt: ptr(time.Now().Add(1440 * time.Hour))},
		{Domain: "broken.example.com", Error: ErrTLSExpired},
	}

	firing, resolved := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, nil, certs)

	if find(firing, KindCert, "soon.example.com") == nil {
		t.Error("сертификат на исходе не поднял тревогу")
	}
	if find(firing, KindCert, "broken.example.com") == nil {
		t.Error("сломанный сертификат не поднял тревогу")
	}
	if find(resolved, KindCert, "fine.example.com") == nil {
		t.Error("здоровый сертификат должен считаться нормой")
	}
}

// Край собственного стека стоит на своём порту, и в письме он назван вместе
// с портом: на одном сервере могут жить и сайт клиента `stats.example.com`,
// и наш край на том же имени — путать их тревоги нельзя (правка 1б).
func TestStackEdgeIsNamedWithItsPort(t *testing.T) {
	soon := 5
	certs := []TLSStatus{
		{Domain: "stats.example.com", Port: 2096, Valid: true, DaysLeft: &soon,
			ExpiresAt: ptr(time.Now().Add(120 * time.Hour))},
		{Domain: "stats.example.com", Valid: true, DaysLeft: &soon,
			ExpiresAt: ptr(time.Now().Add(120 * time.Hour))},
	}

	firing, _ := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, nil, certs)

	if find(firing, KindCert, "stats.example.com:2096") == nil {
		t.Error("край стека не назван вместе с портом")
	}
	if find(firing, KindCert, "stats.example.com") == nil {
		t.Error("сайт на том же имени потерялся")
	}
}

// Порт 443 в имени цели не появляется: у сайтов клиента он всегда такой, и
// «example.com:443» в письме читалось бы как что-то особенное.
func TestStandardPortStaysOutOfTheName(t *testing.T) {
	for _, status := range []TLSStatus{
		{Domain: "example.com"},
		{Domain: "example.com", Port: 443},
	} {
		if got := status.Target(); got != "example.com" {
			t.Errorf("цель %q, ожидалась example.com", got)
		}
	}
}

// Неполная цепочка — не повод для письма: браузеры её обычно достраивают.
// Показать в интерфейсе стоит, будить человека ночью — нет.
func TestIncompleteChainDoesNotWakeAnyone(t *testing.T) {
	far := 90
	certs := []TLSStatus{{
		Domain:    "chain.example.com",
		Valid:     true,
		DaysLeft:  &far,
		ExpiresAt: ptr(time.Now().Add(2160 * time.Hour)),
		Error:     ErrTLSIncomplete,
	}}

	firing, _ := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, nil, certs)
	if find(firing, KindCert, "chain.example.com") != nil {
		t.Error("неполная цепочка подняла тревогу, хотя сертификат действителен")
	}
}

// Зона без RDAP и без разбираемого whois оставляет срок неизвестным. Писать
// об этом каждые сутки — верный способ приучить не читать письма.
func TestUnknownDomainExpiryIsSilent(t *testing.T) {
	domains := []DomainStatus{{Domain: "example.su", Error: ErrNoExpiry}}

	firing, resolved := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, domains, nil)
	if len(firing) != 0 || len(resolved) != 0 {
		t.Errorf("неизвестный срок породил тревоги: firing=%d resolved=%d", len(firing), len(resolved))
	}
}

func TestDomainExpiryAlerts(t *testing.T) {
	soon := 10
	far := 200

	domains := []DomainStatus{
		{Domain: "soon.ru", DaysLeft: &soon, ExpiresAt: ptr(time.Now().Add(240 * time.Hour)), Registrar: "RU-CENTER"},
		{Domain: "fine.ru", DaysLeft: &far, ExpiresAt: ptr(time.Now().Add(4800 * time.Hour))},
	}

	firing, resolved := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, domains, nil)

	alert := find(firing, KindDomain, "soon.ru")
	if alert == nil {
		t.Fatal("истекающий домен не поднял тревогу")
	}
	// Регистратор в письме экономит человеку минуту поиска, к кому идти.
	if !contains(alert.Body, "RU-CENTER") {
		t.Errorf("в письме нет регистратора: %q", alert.Body)
	}
	if find(resolved, KindDomain, "fine.ru") == nil {
		t.Error("продлённый домен должен считаться нормой")
	}
}

// Напоминание называет ступень, до которой дожил срок, и называет
// ближайшую: за пять дней до конца человеку надо сказать «через пять», а не
// «через тридцать», хотя тридцать он тоже проходил.
//
// Ступень — это то, по чему отправка помнится: без неё письмо про домен,
// который кончается через тридцать дней, уходило бы каждые сутки все
// тридцать дней подряд.
func TestDomainRemindersComeInSteps(t *testing.T) {
	cases := []struct {
		daysLeft int
		step     int
	}{
		{daysLeft: 200, step: 0},
		{daysLeft: 31, step: 0},
		{daysLeft: 30, step: 30},
		{daysLeft: 20, step: 30},
		{daysLeft: 14, step: 14},
		{daysLeft: 5, step: 7},
		{daysLeft: 1, step: 1},
		{daysLeft: -3, step: 1},
	}

	for _, item := range cases {
		days := item.daysLeft
		domains := []DomainStatus{
			{Domain: "шаг.ru", DaysLeft: &days, ExpiresAt: ptr(time.Now())},
		}
		firing, _ := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, domains, nil)

		alert := find(firing, KindDomain, "шаг.ru")
		if item.step == 0 {
			if alert != nil {
				t.Errorf("%d дн.: тревога поднялась раньше первой ступени", item.daysLeft)
			}
			continue
		}
		if alert == nil {
			t.Errorf("%d дн.: ступень %d не сработала", item.daysLeft, item.step)
			continue
		}
		if alert.Step != item.step {
			t.Errorf("%d дн.: ступень %d, ждали %d", item.daysLeft, alert.Step, item.step)
		}
	}
}

// Одно число в настройке — это тот же список из одной ступени: конфиг,
// собранный руками до тридцатого спринта, обязан продолжать работать.
func TestSingleThresholdStillWorks(t *testing.T) {
	days := 10
	domains := []DomainStatus{{Domain: "одна.ru", DaysLeft: &days, ExpiresAt: ptr(time.Now())}}

	cfg := thresholds()
	cfg.DomainSteps = []int{30}

	firing, _ := Evaluate(cfg, WordsFor("ru"), metrics.Sample{}, domains, nil)
	alert := find(firing, KindDomain, "одна.ru")
	if alert == nil || alert.Step != 30 {
		t.Fatalf("одна ступень не сработала: %+v", alert)
	}
}

// У сертификата ступеней нет намеренно: его продлевает Caddy сам за
// тридцать дней до срока, и тревога за четырнадцать означает, что
// автопродление уже не сработало. Там повтор — не шум, а незакрытая беда.
func TestCertificateAlertsHaveNoSteps(t *testing.T) {
	days := 3
	certs := []TLSStatus{
		{Domain: "cert.example.com", Valid: true, DaysLeft: &days, ExpiresAt: ptr(time.Now())},
	}

	firing, _ := Evaluate(thresholds(), WordsFor("ru"), metrics.Sample{}, nil, certs)
	alert := find(firing, KindCert, "cert.example.com")
	if alert == nil {
		t.Fatal("истекающий сертификат не поднял тревогу")
	}
	if alert.Step != 0 {
		t.Errorf("у сертификата появилась ступень %d", alert.Step)
	}
}

func ptr(value time.Time) *time.Time { return &value }

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
