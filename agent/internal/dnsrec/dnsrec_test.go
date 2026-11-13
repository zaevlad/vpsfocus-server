package dnsrec

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// fake — подставной резолвер: ответ на имя или ошибка.
type fake struct {
	txt  map[string][]string
	ns   []string
	mx   []string
	host map[string][]string
	err  map[string]error // по имени: «_dmarc.x», «ns», «mx», «host:x», «x»
}

var notFoundErr = &net.DNSError{Err: "no such host", IsNotFound: true}
var servfail = &net.DNSError{Err: "server misbehaving", IsTemporary: true}

func (f *fake) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err := f.err[name]; err != nil {
		return nil, err
	}
	if list, ok := f.txt[name]; ok {
		return list, nil
	}
	return nil, notFoundErr
}

func (f *fake) LookupNS(context.Context, string) ([]*net.NS, error) {
	if err := f.err["ns"]; err != nil {
		return nil, err
	}
	var out []*net.NS
	for _, host := range f.ns {
		out = append(out, &net.NS{Host: host})
	}
	return out, nil
}

func (f *fake) LookupMX(context.Context, string) ([]*net.MX, error) {
	if err := f.err["mx"]; err != nil {
		return nil, err
	}
	var out []*net.MX
	for _, host := range f.mx {
		out = append(out, &net.MX{Host: host})
	}
	return out, nil
}

func (f *fake) LookupHost(_ context.Context, host string) ([]string, error) {
	if err := f.err["host:"+host]; err != nil {
		return nil, err
	}
	return f.host[host], nil
}

func healthy() *fake {
	return &fake{
		txt: map[string][]string{
			"example.com":        {"google-site-verification=x", "v=spf1 include:_spf.google.com ~all"},
			"_dmarc.example.com": {"v=DMARC1; p=quarantine; rua=mailto:d@example.com"},
		},
		ns:   []string{"NS2.Host.net.", "ns1.host.net."},
		mx:   []string{"mx.example.com."},
		host: map[string][]string{"www.example.com": {"192.0.2.1"}},
	}
}

var t0 = time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)

func TestReadsRecords(t *testing.T) {
	snapshot, changes := Read(t.Context(), healthy(), "example.com", []string{"www.example.com"}, nil, t0)
	if snapshot.SPF.State != StatePresent || snapshot.SPF.Value != "v=spf1 include:_spf.google.com ~all" || !snapshot.SPF.Seen {
		t.Errorf("SPF: %+v", snapshot.SPF)
	}
	if snapshot.DMARC.State != StatePresent || snapshot.DMARCPolicy != "quarantine" {
		t.Errorf("DMARC: %+v %q", snapshot.DMARC, snapshot.DMARCPolicy)
	}
	if !snapshot.NS.Known || snapshot.NS.List[0] != "ns1.host.net" || snapshot.NS.List[1] != "ns2.host.net" {
		t.Errorf("серверы имён — в нижнем регистре, без точки, по порядку: %+v", snapshot.NS)
	}
	if len(snapshot.Hosts) != 1 || snapshot.Hosts[0].Addresses.List[0] != "192.0.2.1" {
		t.Errorf("адреса: %+v", snapshot.Hosts)
	}
	if len(changes) != 0 {
		t.Errorf("первый взгляд — не перемена: %+v", changes)
	}
}

// Две записи SPF — для почтовика «записи нет» (Р64); `v=spf10` — не SPF.
func TestTwoSPFIsNoSPF(t *testing.T) {
	resolver := healthy()
	resolver.txt["example.com"] = []string{"v=spf1 -all", "V=SPF1 include:x ~all", "v=spf10 junk"}
	snapshot, _ := Read(t.Context(), resolver, "example.com", nil, nil, t0)
	if snapshot.SPF.State != StateMultiple {
		t.Fatalf("две записи SPF: %+v", snapshot.SPF)
	}
	resolver.txt["example.com"] = []string{"v=spf10 junk"}
	snapshot, _ = Read(t.Context(), resolver, "example.com", nil, nil, t0)
	if snapshot.SPF.State != StateMissing {
		t.Fatalf("v=spf10 — не SPF: %+v", snapshot.SPF)
	}
}

// Сбой резолвера — «не знаем», прежнее не стирается и «пропала» не рождается.
func TestFailureIsUnknownNotMissing(t *testing.T) {
	first, _ := Read(t.Context(), healthy(), "example.com", []string{"www.example.com"}, nil, t0)
	broken := healthy()
	broken.err = map[string]error{
		"example.com": servfail, "_dmarc.example.com": context.DeadlineExceeded,
		"ns": servfail, "mx": errors.New("boom"), "host:www.example.com": servfail,
	}
	later := t0
	snapshot := first
	for range 4 {
		later = later.Add(6 * time.Hour)
		var changes []Change
		snapshot, changes = Read(t.Context(), broken, "example.com", []string{"www.example.com"}, &snapshot, later)
		if len(changes) != 0 {
			t.Fatalf("сбой резолвера — не перемена: %+v", changes)
		}
	}
	if snapshot.SPF.State != StateUnknown || snapshot.SPF.Gone || !snapshot.SPF.Seen ||
		snapshot.SPF.Value == "" || snapshot.NS.Known || len(snapshot.NS.List) != 2 {
		t.Fatalf("не знаем — прежнее на месте, но не «известно»: %+v %+v", snapshot.SPF, snapshot.NS)
	}
}

// «Пропала» — только у записи, которая была, и только через 6 часов «нет»
// подряд (П9). Вернулась — «вернулась».
func TestGoneAfterSixHours(t *testing.T) {
	resolver := healthy()
	snapshot, _ := Read(t.Context(), resolver, "example.com", nil, nil, t0)

	delete(resolver.txt, "example.com")
	snapshot, changes := Read(t.Context(), resolver, "example.com", nil, &snapshot, t0.Add(time.Minute))
	if snapshot.SPF.Gone || len(changes) != 0 {
		t.Fatalf("одно «нет» — ещё не «пропала»: %+v %+v", snapshot.SPF, changes)
	}
	snapshot, changes = Read(t.Context(), resolver, "example.com", nil, &snapshot, t0.Add(5*time.Hour))
	if snapshot.SPF.Gone {
		t.Fatal("через 5 часов — ещё не «пропала» (кнопка гоняет круги чаще)")
	}
	snapshot, changes = Read(t.Context(), resolver, "example.com", nil, &snapshot, t0.Add(6*time.Hour+time.Minute))
	if !snapshot.SPF.Gone || len(changes) != 1 || !changes[0].Gone || changes[0].Part != PartSPF {
		t.Fatalf("через 6 часов — «пропала»: %+v %+v", snapshot.SPF, changes)
	}
	snapshot, changes = Read(t.Context(), resolver, "example.com", nil, &snapshot, t0.Add(12*time.Hour))
	if len(changes) != 0 {
		t.Fatalf("«пропала» — один раз: %+v", changes)
	}

	resolver.txt["example.com"] = []string{"v=spf1 -all"}
	snapshot, changes = Read(t.Context(), resolver, "example.com", nil, &snapshot, t0.Add(13*time.Hour))
	if snapshot.SPF.Gone || len(changes) != 1 || !changes[0].Back {
		t.Fatalf("вернулась: %+v %+v", snapshot.SPF, changes)
	}

	// Не было никогда — и через сутки «нет» не «пропала» (П8).
	never := healthy()
	delete(never.txt, "_dmarc.example.com")
	snapshot, _ = Read(t.Context(), never, "example.com", nil, nil, t0)
	snapshot, _ = Read(t.Context(), never, "example.com", nil, &snapshot, t0.Add(24*time.Hour))
	if snapshot.DMARC.Gone || snapshot.DMARC.State != StateMissing {
		t.Fatalf("не было — не «пропала»: %+v", snapshot.DMARC)
	}
}

// Смена серверов имён, почтовых серверов и адресов — перемены; серверы
// имён помнят, какими были.
func TestChanges(t *testing.T) {
	resolver := healthy()
	snapshot, _ := Read(t.Context(), resolver, "example.com", []string{"www.example.com"}, nil, t0)
	resolver.ns = []string{"amy.ns.cloudflare.com", "bob.ns.cloudflare.com"}
	resolver.mx = []string{"route1.mx.cloudflare.net"}
	resolver.host["www.example.com"] = []string{"203.0.113.9"}
	resolver.txt["example.com"] = []string{"v=spf1 include:_spf.mx.cloudflare.net ~all"}
	later := t0.Add(6 * time.Hour)
	snapshot, changes := Read(t.Context(), resolver, "example.com", []string{"www.example.com"}, &snapshot, later)
	parts := map[string]bool{}
	for _, change := range changes {
		parts[change.Part] = true
	}
	for _, part := range []string{PartNS, PartMX, PartAddress, PartSPF} {
		if !parts[part] {
			t.Errorf("нет перемены %s: %+v", part, changes)
		}
	}
	if !snapshot.NSChangedRecently(later) || snapshot.NSBefore[0] != "ns1.host.net" {
		t.Errorf("смена серверов имён помнится: %+v", snapshot)
	}
	if snapshot.NSChangedRecently(later.Add(25 * time.Hour)) {
		t.Error("через сутки смена уже не свежая")
	}
}

// Смена, пришедшаяся на круг после сбоя резолвера, — тоже смена (аудит
// 265–268): круг «не знаем» хранит прежние значения, и сравнивать надо с
// ними, а не молча принимать новое за норму.
func TestChangeAfterUnknownRoundIsNotLost(t *testing.T) {
	sites := []string{"www.example.com"}
	resolver := healthy()
	snapshot, _ := Read(t.Context(), resolver, "example.com", sites, nil, t0)

	failing := &fake{err: map[string]error{
		"example.com": servfail, "_dmarc.example.com": servfail,
		"ns": servfail, "mx": servfail, "host:www.example.com": servfail,
	}}
	snapshot, changes := Read(t.Context(), failing, "example.com", sites, &snapshot, t0.Add(6*time.Hour))
	if len(changes) != 0 || snapshot.NS.Known || snapshot.SPF.State != StateUnknown {
		t.Fatalf("сбой — не перемена: %+v %+v", changes, snapshot)
	}

	resolver.ns = []string{"amy.ns.cloudflare.com", "bob.ns.cloudflare.com"}
	resolver.mx = []string{"route1.mx.cloudflare.net"}
	resolver.host["www.example.com"] = []string{"203.0.113.9"}
	resolver.txt["example.com"] = []string{"v=spf1 include:_spf.mx.cloudflare.net ~all"}
	resolver.txt["_dmarc.example.com"] = []string{"v=DMARC1; p=reject"}
	later := t0.Add(12 * time.Hour)
	snapshot, changes = Read(t.Context(), resolver, "example.com", sites, &snapshot, later)
	parts := map[string]bool{}
	for _, change := range changes {
		parts[change.Part] = true
	}
	for _, part := range []string{PartNS, PartMX, PartAddress, PartSPF, PartDMARC} {
		if !parts[part] {
			t.Errorf("после круга «не знаем» потеряна перемена %s: %+v", part, changes)
		}
	}
	if !snapshot.NSChangedRecently(later) || len(snapshot.NSBefore) != 2 || snapshot.NSBefore[0] != "ns1.host.net" {
		t.Errorf("смена серверов имён после сбоя помнит прежние: %+v", snapshot)
	}

	// Тот же ответ после сбоя — не перемена.
	snapshot, _ = Read(t.Context(), failing, "example.com", sites, &snapshot, later.Add(time.Hour))
	_, changes = Read(t.Context(), resolver, "example.com", sites, &snapshot, later.Add(2*time.Hour))
	if len(changes) != 0 {
		t.Errorf("тот же ответ после сбоя — не перемена: %+v", changes)
	}

	// «Несколько» до сбоя и одна запись после — не «смена значения»: прежде
	// записи для почтовика не было.
	multi := healthy()
	multi.txt["example.com"] = []string{"v=spf1 -all", "v=spf1 ~all"}
	snapshot, _ = Read(t.Context(), multi, "example.com", nil, nil, t0)
	snapshot, _ = Read(t.Context(), failing, "example.com", nil, &snapshot, t0.Add(time.Hour))
	multi.txt["example.com"] = []string{"v=spf1 -all"}
	_, changes = Read(t.Context(), multi, "example.com", nil, &snapshot, t0.Add(2*time.Hour))
	for _, change := range changes {
		if change.Part == PartSPF {
			t.Errorf("«несколько» → одна — не смена значения: %+v", changes)
		}
	}
}
