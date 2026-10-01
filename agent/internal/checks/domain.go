// Package checks выясняет сроки доменов и сертификатов.
//
// Обе проверки делает агент, а не приложение: домен и сертификат истекают
// независимо от того, открыт ли десктоп, и узнать об этом надо заранее.
package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"agent/internal/domains"
)

// DomainStatus — что известно о сроке домена.
type DomainStatus struct {
	Domain string `json:"domain"`
	// Каким способом узнали: rdap или whois. Пусто, если не узнали.
	Source string `json:"source,omitempty"`
	// Дата окончания регистрации.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// Сколько дней осталось. Отрицательное — срок уже прошёл.
	DaysLeft *int `json:"daysLeft,omitempty"`
	// Регистратор, если сказали.
	Registrar string `json:"registrar,omitempty"`
	// Почему не получилось. Код, а не текст: интерфейс двуязычный.
	Error string `json:"error,omitempty"`
	// Когда проверяли.
	CheckedAt time.Time `json:"checkedAt"`
}

// Коды ошибок проверки домена. Наружу уезжают именно они: перевод живёт в
// приложении, как и во всём остальном проекте.
const (
	ErrNoExpiry   = "domainExpiryUnknown"
	ErrLookup     = "domainLookupFailed"
	ErrUnregister = "domainNotRegistered"
)

// Реестр RDAP по зонам. Загружается у IANA один раз и держится в памяти:
// зоны появляются раз в месяцы, а агент живёт неделями.
type bootstrap struct {
	fetched time.Time
	byTLD   map[string]string
}

// DomainChecker знает, где спрашивать про какую зону.
//
// Два пути неспроста. RDAP отдаёт разобранный JSON и работает для gTLD, но
// у доброй половины ccTLD его нет вовсе: `.ru`, `.io`, `.de` в реестре IANA
// отсутствуют. Для них остаётся whois — текст, который приходится разбирать
// построчно, зато он есть везде.
type DomainChecker struct {
	http      *http.Client
	dialer    *net.Dialer
	bootstrap *bootstrap
}

func NewDomainChecker() *DomainChecker {
	return &DomainChecker{
		http:   &http.Client{Timeout: 20 * time.Second},
		dialer: &net.Dialer{Timeout: 15 * time.Second},
	}
}

func (d *DomainChecker) Check(ctx context.Context, domain string) DomainStatus {
	status := DomainStatus{Domain: domain, CheckedAt: time.Now().UTC()}

	// Домен сайта может быть поддоменом, а регистрируется зона второго
	// уровня: спрашивать про shop.example.com бессмысленно.
	registrable := domains.Registrable(domain)

	if expires, registrar, err := d.viaRDAP(ctx, registrable); err == nil {
		status.Source = "rdap"
		status.Registrar = registrar
		status.setExpiry(expires)
		return status
	}

	expires, registrar, err := d.viaWhois(ctx, registrable)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.Source = "whois"
	status.Registrar = registrar
	status.setExpiry(expires)
	return status
}

func (s *DomainStatus) setExpiry(expires time.Time) {
	if expires.IsZero() {
		s.Error = ErrNoExpiry
		return
	}
	days := int(time.Until(expires).Hours() / 24)
	s.ExpiresAt = &expires
	s.DaysLeft = &days
}

// ── RDAP ─────────────────────────────────────────────────────────────────

func (d *DomainChecker) viaRDAP(ctx context.Context, domain string) (time.Time, string, error) {
	base, err := d.rdapBase(ctx, tldOf(domain))
	if err != nil {
		return time.Time{}, "", err
	}

	url := strings.TrimSuffix(base, "/") + "/domain/" + domain
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, "", err
	}
	request.Header.Set("Accept", "application/rdap+json")

	response, err := d.http.Do(request)
	if err != nil {
		return time.Time{}, "", err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		return time.Time{}, "", fmt.Errorf(ErrUnregister)
	}
	if response.StatusCode != http.StatusOK {
		return time.Time{}, "", fmt.Errorf("rdap: %s", response.Status)
	}

	var payload struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
		Entities []struct {
			Roles     []string        `json:"roles"`
			VCardArry json.RawMessage `json:"vcardArray"`
		} `json:"entities"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return time.Time{}, "", err
	}

	var expires time.Time
	for _, event := range payload.Events {
		if event.Action == "expiration" {
			if parsed, err := time.Parse(time.RFC3339, event.Date); err == nil {
				expires = parsed.UTC()
			}
		}
	}

	return expires, rdapRegistrar(payload.Entities), nil
}

// rdapRegistrar достаёт имя регистратора из vCard — формата, в котором поля
// лежат вложенными массивами. Не нашли — не беда: это украшение, а не срок.
func rdapRegistrar(entities []struct {
	Roles     []string        `json:"roles"`
	VCardArry json.RawMessage `json:"vcardArray"`
}) string {
	for _, entity := range entities {
		registrar := false
		for _, role := range entity.Roles {
			if role == "registrar" {
				registrar = true
			}
		}
		if !registrar || len(entity.VCardArry) == 0 {
			continue
		}

		var card []any
		if err := json.Unmarshal(entity.VCardArry, &card); err != nil || len(card) < 2 {
			continue
		}
		fields, ok := card[1].([]any)
		if !ok {
			continue
		}
		for _, raw := range fields {
			field, ok := raw.([]any)
			if !ok || len(field) < 4 {
				continue
			}
			if name, ok := field[0].(string); ok && name == "fn" {
				if value, ok := field[3].(string); ok {
					return value
				}
			}
		}
	}
	return ""
}

func (d *DomainChecker) rdapBase(ctx context.Context, tld string) (string, error) {
	if d.bootstrap == nil || time.Since(d.bootstrap.fetched) > 7*24*time.Hour {
		if err := d.loadBootstrap(ctx); err != nil {
			return "", err
		}
	}

	base, ok := d.bootstrap.byTLD[tld]
	if !ok {
		return "", fmt.Errorf("зона %q не поддерживает rdap", tld)
	}
	return base, nil
}

func (d *DomainChecker) loadBootstrap(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://data.iana.org/rdap/dns.json", nil)
	if err != nil {
		return err
	}

	response, err := d.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("реестр rdap: %s", response.Status)
	}

	var payload struct {
		Services [][][]string `json:"services"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return err
	}

	byTLD := make(map[string]string, 1500)
	for _, service := range payload.Services {
		if len(service) < 2 || len(service[1]) == 0 {
			continue
		}
		for _, tld := range service[0] {
			byTLD[strings.ToLower(tld)] = service[1][0]
		}
	}

	d.bootstrap = &bootstrap{fetched: time.Now(), byTLD: byTLD}
	return nil
}

// ── whois ────────────────────────────────────────────────────────────────

// Поля, в которых разные реестры пишут срок окончания. Порядок важен:
// первое совпадение выигрывает.
var expiryFields = []string{
	"registry expiry date",
	"registrar registration expiration date",
	"expiration date",
	"expiration time",
	"expires on",
	"expiry date",
	"expire date",
	// `.ru` и `.su`: срок называется «оплачено до».
	"paid-till",
	"renewal date",
}

var registrarFields = []string{"registrar", "sponsoring registrar"}

// Форматы, в которых те же реестры эту дату записывают. Единого нет.
var expiryLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"02-Jan-2006",
	"2006.01.02",
	"02.01.2006",
	"20060102",
}

func (d *DomainChecker) viaWhois(ctx context.Context, domain string) (time.Time, string, error) {
	server, err := d.whoisServer(ctx, tldOf(domain))
	if err != nil {
		return time.Time{}, "", fmt.Errorf(ErrLookup)
	}

	body, err := d.ask(ctx, server, domain)
	if err != nil {
		return time.Time{}, "", fmt.Errorf(ErrLookup)
	}

	// Тонкие реестры отвечают ссылкой на реестр регистратора, где и лежит
	// настоящий срок.
	if referral := findField(body, []string{"registrar whois server"}); referral != "" && referral != server {
		if deeper, err := d.ask(ctx, referral, domain); err == nil {
			if expires := parseExpiry(findField(deeper, expiryFields)); !expires.IsZero() {
				return expires, findField(deeper, registrarFields), nil
			}
		}
	}

	expires := parseExpiry(findField(body, expiryFields))
	return expires, findField(body, registrarFields), nil
}

// whoisServer спрашивает у IANA, кто отвечает за зону. Свой список зашивать
// нельзя: он устареет молча.
func (d *DomainChecker) whoisServer(ctx context.Context, tld string) (string, error) {
	body, err := d.ask(ctx, "whois.iana.org", tld)
	if err != nil {
		return "", err
	}
	server := findField(body, []string{"whois"})
	if server == "" {
		return "", fmt.Errorf("для зоны %q нет whois-сервера", tld)
	}
	return server, nil
}

func (d *DomainChecker) ask(ctx context.Context, server, query string) (string, error) {
	conn, err := d.dialer.DialContext(ctx, "tcp", net.JoinHostPort(server, "43"))
	if err != nil {
		return "", err
	}
	defer conn.Close()

	deadline := time.Now().Add(15 * time.Second)
	if from, ok := ctx.Deadline(); ok && from.Before(deadline) {
		deadline = from
	}
	_ = conn.SetDeadline(deadline)

	if _, err := fmt.Fprintf(conn, "%s\r\n", query); err != nil {
		return "", err
	}

	// Ответ whois — текст без обещанного размера. Ограничиваем: сервер может
	// быть и недружелюбным.
	body, err := io.ReadAll(io.LimitReader(conn, 1<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// findField ищет первое непустое значение одного из полей `имя: значение`.
func findField(body string, names []string) string {
	for _, line := range strings.Split(body, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		for _, name := range names {
			if key == name {
				return value
			}
		}
	}
	return ""
}

func parseExpiry(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	// Часть реестров дописывает пояснения после даты.
	raw = strings.TrimSpace(strings.Fields(raw + " ")[0])

	for _, layout := range expiryLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// ── Имена ────────────────────────────────────────────────────────────────

func tldOf(domain string) string {
	parts := strings.Split(strings.Trim(domain, "."), ".")
	return strings.ToLower(parts[len(parts)-1])
}
