// Package dnsrec читает записи домена, от которых зависит почта и то, куда
// домен указывает (трек AH, предложение 8, спринт 267): SPF, DMARC, серверы
// имён, почтовые серверы и адреса сайтов.
//
// Спрашивает системный резолвер сервера (Р65): в контейнере агента это
// встроенный DNS Docker, он пересылает вышестоящим сервера. Свой резолвер не
// заводим — прочитать записи может сам сервер человека, наш сервер для этого
// не нужен.
//
// **Не прочиталось — «не знаем», никогда «записи нет» (Р63).** «Нет» — только
// удачный ответ `IsNotFound`; SERVFAIL, REFUSED, молчание (через Docker это
// тоже SERVFAIL) и любая другая ошибка — «не знаем». И даже удачное «нет»
// становится «пропала» лишь через 6 часов (П9): подменный резолвер под VPN
// или фильтрующий DNS хостера иначе слали бы ложные письма.
package dnsrec

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"time"
)

// Состояние записи.
const (
	// Запись есть, одна, как положено.
	StatePresent = "present"
	// Записи нет — удачный ответ «нет такой».
	StateMissing = "missing"
	// Записей две и больше: почтовики считают это ошибкой, то есть «записи
	// нет» (RFC 7208 permerror для SPF, RFC 7489 для DMARC) — Р64.
	StateMultiple = "multiple"
	// Не прочиталось.
	StateUnknown = "unknown"
)

// Сколько ждать один запрос (Р63): молчание вышестоящего через Docker стоит 8 с.
const LookupTimeout = 15 * time.Second

// Через сколько удачное «нет» у записи, которая была, становится «пропала»
// (П9). Круг сроков идёт раз в 6 часов, но его запускают и кнопкой —
// поэтому время, а не число кругов.
const GoneAfter = 6 * time.Hour

// Resolver — то, чем читаются записи. Настоящий — `net.DefaultResolver`.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Record — SPF или DMARC.
type Record struct {
	State string `json:"state"`
	// Текст записи, если она одна. У нескольких — все через перевод строки.
	Value string `json:"value,omitempty"`
	// Запись хоть раз была на месте — только тогда её исчезновение — новость
	// (Р17: письмо только на «была и пропала»; П8: «нет SPF» у домена без
	// почты — не беда).
	Seen bool `json:"seen,omitempty"`
	// С какого момента подряд удачно отвечают «нет» (или «несколько»).
	MissingSince *time.Time `json:"missingSince,omitempty"`
	// Была и пропала: «нет» держится дольше GoneAfter.
	Gone bool `json:"gone,omitempty"`
}

// Names — серверы имён, почтовые серверы или адреса: отсортированный список.
type Names struct {
	Known bool     `json:"known"`
	List  []string `json:"list"`
}

// Host — адреса одного сайта.
type Host struct {
	Domain    string `json:"domain"`
	Addresses Names  `json:"addresses"`
}

// Snapshot — что известно о регистрируемом домене. Один на домен, сколько
// бы сайтов на нём ни было (П7): иначе три письма об одной записи.
type Snapshot struct {
	Domain    string    `json:"domain"`
	CheckedAt time.Time `json:"checkedAt"`
	SPF       Record    `json:"spf"`
	DMARC     Record    `json:"dmarc"`
	// DMARC: что делать с чужими письмами — none, quarantine, reject.
	DMARCPolicy string `json:"dmarcPolicy,omitempty"`
	NS          Names  `json:"ns"`
	MX          Names  `json:"mx"`
	// Когда последний раз сменились серверы имён. Тревога держится сутки
	// после смены (П1): событие, а не состояние.
	NSChangedAt *time.Time `json:"nsChangedAt,omitempty"`
	// Прежние серверы имён — для письма и экрана.
	NSBefore []string `json:"nsBefore,omitempty"`
	Hosts    []Host   `json:"hosts"`
}

// Change — что изменилось с прошлого круга: строка в ленте.
type Change struct {
	// Код части: spf, dmarc, ns, mx, address.
	Part string
	// О чём речь: домен (или сайт — у адреса).
	Target string
	// Запись пропала (spf, dmarc) — тогда и письмо.
	Gone bool
	// Запись вернулась после «пропала».
	Back bool
}

// Части записей — коды для ленты и экрана.
const (
	PartSPF     = "spf"
	PartDMARC   = "dmarc"
	PartNS      = "ns"
	PartMX      = "mx"
	PartAddress = "address"
)

// Read читает записи домена и сводит их с прошлым снимком.
//
// `sites` — сайты на этом домене (их адреса). `previous` — прошлый снимок
// или nil. Что не прочиталось, остаётся прежним: «не знаем» не стирает
// известного и не рождает перемен.
func Read(ctx context.Context, resolver Resolver, domain string, sites []string, previous *Snapshot, now time.Time) (Snapshot, []Change) {
	var prev Snapshot
	if previous != nil {
		prev = *previous
	}
	out := Snapshot{Domain: domain, CheckedAt: now.UTC(), NSChangedAt: prev.NSChangedAt, NSBefore: prev.NSBefore}
	var changes []Change

	txt, txtErr := lookupTXT(ctx, resolver, domain)
	out.SPF = record(prev.SPF, pick(txt, txtErr, "v=spf1"), now)
	changes = append(changes, recordChange(PartSPF, domain, prev.SPF, out.SPF, previous != nil)...)

	dmarc, dmarcErr := lookupTXT(ctx, resolver, "_dmarc."+domain)
	dmarcValue := pick(dmarc, dmarcErr, "v=dmarc1")
	out.DMARC = record(prev.DMARC, dmarcValue, now)
	if out.DMARC.State == StatePresent {
		out.DMARCPolicy = policy(out.DMARC.Value)
	} else if out.DMARC.State == StateUnknown {
		out.DMARCPolicy = prev.DMARCPolicy
	}
	changes = append(changes, recordChange(PartDMARC, domain, prev.DMARC, out.DMARC, previous != nil)...)

	// Сравнивается с последним известным списком, даже если прошлый круг
	// не прочитался: иначе смена, пришедшаяся на круг после сбоя
	// резолвера, молча стала бы нормой (аудит 265–268).
	out.NS = names(prev.NS, lookupNS(ctx, resolver, domain))
	if previous != nil && len(prev.NS.List) > 0 && out.NS.Known &&
		len(out.NS.List) > 0 && !equal(prev.NS.List, out.NS.List) {
		at := now.UTC()
		out.NSChangedAt = &at
		out.NSBefore = prev.NS.List
		changes = append(changes, Change{Part: PartNS, Target: domain})
	}

	out.MX = names(prev.MX, lookupMX(ctx, resolver, domain))
	if previous != nil && lastKnown(prev.MX) && out.MX.Known && !equal(prev.MX.List, out.MX.List) {
		changes = append(changes, Change{Part: PartMX, Target: domain})
	}

	before := map[string]Names{}
	for _, host := range prev.Hosts {
		before[host.Domain] = host.Addresses
	}
	for _, site := range sites {
		old, had := before[site]
		addresses := names(old, lookupHost(ctx, resolver, site))
		if had && lastKnown(old) && addresses.Known && !equal(old.List, addresses.List) {
			changes = append(changes, Change{Part: PartAddress, Target: site})
		}
		out.Hosts = append(out.Hosts, Host{Domain: site, Addresses: addresses})
	}
	if out.Hosts == nil {
		out.Hosts = []Host{}
	}
	return out, changes
}

// answer — то, что вернул резолвер для одной записи.
type answer struct {
	state  string
	values []string
}

// pick выбирает из TXT записи с нужным началом (`v=spf1`, `v=DMARC1`), без
// учёта регистра; дальше — пробел, точка с запятой или конец.
func pick(txt []string, err error, prefix string) answer {
	switch {
	case err != nil && notFound(err):
		return answer{state: StateMissing}
	case err != nil:
		return answer{state: StateUnknown}
	}
	var found []string
	for _, item := range txt {
		value := strings.TrimSpace(item)
		lower := strings.ToLower(value)
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := lower[len(prefix):]
		if rest == "" || rest[0] == ' ' || rest[0] == ';' {
			found = append(found, value)
		}
	}
	switch len(found) {
	case 0:
		return answer{state: StateMissing}
	case 1:
		return answer{state: StatePresent, values: found}
	default:
		sort.Strings(found)
		return answer{state: StateMultiple, values: found}
	}
}

// record сводит ответ с прошлым состоянием записи.
func record(prev Record, got answer, now time.Time) Record {
	if got.state == StateUnknown {
		// Не знаем — прежнее остаётся, но состоянием «не знаем» на экране.
		prev.State = StateUnknown
		return prev
	}
	out := Record{State: got.state, Value: strings.Join(got.values, "\n"), Seen: prev.Seen}
	if got.state == StatePresent {
		out.Seen = true
		return out
	}
	// Нет (или несколько — для почтовика то же «нет»).
	since := now.UTC()
	if prev.MissingSince != nil {
		since = *prev.MissingSince
	}
	out.MissingSince = &since
	out.Gone = out.Seen && now.Sub(since) >= GoneAfter
	return out
}

func recordChange(part, domain string, prev, now Record, hadPrevious bool) []Change {
	if !hadPrevious || now.State == StateUnknown {
		return nil
	}
	switch {
	case now.Gone && !prev.Gone:
		return []Change{{Part: part, Target: domain, Gone: true}}
	case now.State == StatePresent && prev.Gone:
		return []Change{{Part: part, Target: domain, Back: true}}
	case now.State == StatePresent && wasPresent(prev) && now.Value != prev.Value:
		return []Change{{Part: part, Target: domain}}
	}
	return nil
}

// wasPresent — была ли запись на месте в последний раз, когда её удалось
// прочитать. Круг «не знаем» хранит прежнее значение (`record`), и смена
// после него — тоже смена. У «несколько» отсчёт `MissingSince` идёт всегда,
// у одной записи его нет — так их не спутать.
func wasPresent(prev Record) bool {
	return prev.State == StatePresent ||
		(prev.State == StateUnknown && prev.Value != "" && prev.MissingSince == nil)
}

// lastKnown — есть ли с чем сравнивать список: он прочитан сейчас или
// остался от удачного круга (сбой резолвера список не стирает).
func lastKnown(prev Names) bool {
	return prev.Known || len(prev.List) > 0
}

// policy — значение тега p= записи DMARC.
func policy(value string) string {
	for _, tag := range strings.Split(value, ";") {
		key, val, found := strings.Cut(strings.TrimSpace(tag), "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "p") {
			return strings.ToLower(strings.TrimSpace(val))
		}
	}
	return ""
}

// lookup — список имён или «не знаем».
type lookup struct {
	known bool
	list  []string
}

func names(prev Names, got lookup) Names {
	if !got.known {
		// Не знаем — прежний список остаётся, «известно» снимается.
		return Names{Known: false, List: nonNil(prev.List)}
	}
	return Names{Known: true, List: nonNil(got.list)}
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func lookupTXT(ctx context.Context, resolver Resolver, name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, LookupTimeout)
	defer cancel()
	return resolver.LookupTXT(ctx, name)
}

func lookupNS(ctx context.Context, resolver Resolver, name string) lookup {
	ctx, cancel := context.WithTimeout(ctx, LookupTimeout)
	defer cancel()
	list, err := resolver.LookupNS(ctx, name)
	if err != nil {
		// У регистрируемого домена NODATA не бывает (Р63): «нет» здесь —
		// это тоже «не знаем», пустой список серверов имён — не ответ.
		return lookup{}
	}
	var out []string
	for _, item := range list {
		out = append(out, clean(item.Host))
	}
	return lookup{known: len(out) > 0, list: sorted(out)}
}

func lookupMX(ctx context.Context, resolver Resolver, name string) lookup {
	ctx, cancel := context.WithTimeout(ctx, LookupTimeout)
	defer cancel()
	list, err := resolver.LookupMX(ctx, name)
	if err != nil {
		if notFound(err) {
			return lookup{known: true, list: []string{}}
		}
		return lookup{}
	}
	var out []string
	for _, item := range list {
		out = append(out, clean(item.Host))
	}
	return lookup{known: true, list: sorted(out)}
}

func lookupHost(ctx context.Context, resolver Resolver, host string) lookup {
	ctx, cancel := context.WithTimeout(ctx, LookupTimeout)
	defer cancel()
	list, err := resolver.LookupHost(ctx, host)
	if err != nil {
		if notFound(err) {
			return lookup{known: true, list: []string{}}
		}
		return lookup{}
	}
	return lookup{known: true, list: sorted(list)}
}

func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func clean(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func sorted(list []string) []string {
	out := append([]string{}, list...)
	sort.Strings(out)
	// Без повторов: два адреса одного сервера — одна строка.
	unique := out[:0]
	for i, item := range out {
		if i == 0 || item != out[i-1] {
			unique = append(unique, item)
		}
	}
	return unique
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NSChangedRecently — сменились ли серверы имён за последние сутки. Тревога
// о смене — событие, и держится она сутки (П1), как остановки по памяти.
func (s Snapshot) NSChangedRecently(now time.Time) bool {
	return s.NSChangedAt != nil && now.Sub(*s.NSChangedAt) < 24*time.Hour
}
