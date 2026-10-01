package checks

import (
	"testing"
	"time"
)

// Реестры пишут срок разными словами и в разных форматах. Разбор обязан
// понимать все, что встречались вживую: ответы сняты с whois.tcinet.ru,
// whois.nic.io и verisign.
func TestParseExpiryAcceptsRealFormats(t *testing.T) {
	cases := map[string]string{
		"2026-09-30T21:00:00Z":   "2026-09-30",
		"2027-03-08T19:12:48Z":   "2027-03-08",
		"2027-08-13T04:00:00Z":   "2027-08-13",
		"2026.09.30":             "2026-09-30",
		"30.09.2026":             "2026-09-30",
		"08-Mar-2027":            "2027-03-08",
		"2027-03-08 19:12:48":    "2027-03-08",
		"2027-03-08 (пояснение)": "2027-03-08",
	}

	for raw, want := range cases {
		got := parseExpiry(raw)
		if got.IsZero() {
			t.Errorf("%q: дата не разобралась", raw)
			continue
		}
		if got.Format("2006-01-02") != want {
			t.Errorf("%q: получили %s, ожидали %s", raw, got.Format("2006-01-02"), want)
		}
	}
}

// Мусор не должен превращаться в дату: лучше «срок неизвестен», чем
// уверенное враньё про завтрашнее истечение.
func TestParseExpiryRejectsNonsense(t *testing.T) {
	for _, raw := range []string{"", "неизвестно", "redacted for privacy", "0000-00-00"} {
		if got := parseExpiry(raw); !got.IsZero() {
			t.Errorf("%q разобралось как %s, а не должно было", raw, got)
		}
	}
}

// Ответ whois разбирается по именам полей, а не по позициям: у каждого
// реестра свой порядок и свои отступы.
func TestFindFieldReadsRealAnswers(t *testing.T) {
	ru := `
% TCI Whois Service
domain:        YANDEX.RU
nserver:       ns1.yandex.ru.
state:         REGISTERED, DELEGATED, VERIFIED
registrar:     RU-CENTER-RU
created:       1997-09-23T09:45:07Z
paid-till:     2026-09-30T21:00:00Z
free-date:     2026-11-01
`
	if got := findField(ru, expiryFields); got != "2026-09-30T21:00:00Z" {
		t.Errorf("paid-till не найден, получили %q", got)
	}
	if got := findField(ru, registrarFields); got != "RU-CENTER-RU" {
		t.Errorf("регистратор не найден, получили %q", got)
	}

	gtld := `
   Domain Name: EXAMPLE.COM
   Registrar: Example Registrar, LLC
   Registry Expiry Date: 2027-08-13T04:00:00Z
   Registrar WHOIS Server: whois.example-registrar.com
`
	if got := findField(gtld, expiryFields); got != "2027-08-13T04:00:00Z" {
		t.Errorf("Registry Expiry Date не найден, получили %q", got)
	}
}

// Поле без значения — это не значение. Реестры так прячут данные, и пустая
// строка не должна выигрывать у настоящего поля ниже.
func TestFindFieldSkipsEmptyValues(t *testing.T) {
	body := "expiry date:\nregistry expiry date: 2027-01-01T00:00:00Z\n"
	if got := findField(body, expiryFields); got != "2027-01-01T00:00:00Z" {
		t.Errorf("пустое поле перебило заполненное: %q", got)
	}
}

// Срок в днях считается от «сейчас», и знак важнее числа: просроченный
// домен должен выглядеть просроченным.
func TestSetExpiryComputesDaysLeft(t *testing.T) {
	// Не круглое число часов намеренно: на Windows у часов грубое
	// разрешение, и ровно 72 часа превращались то в два дня, то в три.
	var status DomainStatus
	status.setExpiry(time.Now().Add(72*time.Hour + 30*time.Minute))
	if status.DaysLeft == nil || *status.DaysLeft != 3 {
		t.Errorf("через трое с половиной суток ожидали 3 полных дня, получили %v", deref(status.DaysLeft))
	}

	var expired DomainStatus
	expired.setExpiry(time.Now().Add(-48 * time.Hour))
	if expired.DaysLeft == nil || *expired.DaysLeft >= 0 {
		t.Errorf("просроченный домен должен давать отрицательный остаток, получили %v", deref(expired.DaysLeft))
	}

	var unknown DomainStatus
	unknown.setExpiry(time.Time{})
	if unknown.Error != ErrNoExpiry {
		t.Errorf("нулевая дата должна давать код %s, получили %q", ErrNoExpiry, unknown.Error)
	}
}

func deref(value *int) any {
	if value == nil {
		return "nil"
	}
	return *value
}
