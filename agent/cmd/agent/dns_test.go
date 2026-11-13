package main

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"agent/internal/config"
	"agent/internal/events"
)

// dnsFake — резолвер с изменяемыми ответами.
type dnsFake struct {
	spf string
	ns  []string
}

func (f *dnsFake) LookupTXT(_ context.Context, name string) ([]string, error) {
	if name == "example.com" && f.spf != "" {
		return []string{f.spf}, nil
	}
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func (f *dnsFake) LookupNS(context.Context, string) ([]*net.NS, error) {
	var out []*net.NS
	for _, host := range f.ns {
		out = append(out, &net.NS{Host: host})
	}
	return out, nil
}

func (f *dnsFake) LookupMX(context.Context, string) ([]*net.MX, error) {
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}

func (f *dnsFake) LookupHost(context.Context, string) ([]string, error) {
	return []string{"192.0.2.1"}, nil
}

// Один домен на два сайта (П7); пропавший SPF — одно письмо через 6 часов и
// строка в ленте; смена серверов имён — письмо и строка; адреса IP и
// одиночные имена не спрашиваются.
func TestCheckDNSWritesLettersAndJournal(t *testing.T) {
	clock := moscow(t, 10, 12)
	agent, out := quietAgent(t, nil, "", &clock)
	resolver := &dnsFake{spf: "v=spf1 -all", ns: []string{"a.ns.net"}}
	agent.resolver = resolver
	sites := []config.Site{{Domain: "example.com"}, {Domain: "www.example.com"}, {Domain: "192.0.2.7"}, {Domain: "localhost"}}

	run := func() {
		snapshots, firing, resolved := agent.checkDNS(t.Context(), sites)
		if len(snapshots) != 1 || snapshots[0].Domain != "example.com" || len(snapshots[0].Hosts) != 2 {
			t.Fatalf("один домен на два сайта: %+v", snapshots)
		}
		agent.dispatch(t.Context(), firing, resolved)
	}

	run()
	resolver.spf = ""
	clock = clock.Add(time.Hour)
	run()
	if len(out.sent) != 0 {
		t.Fatalf("через час «нет» — ещё не письмо: %v", out.sent)
	}
	clock = clock.Add(6 * time.Hour)
	run()
	clock = clock.Add(6 * time.Hour)
	run()
	if len(out.sent) != 1 {
		t.Fatalf("пропавший SPF — одно письмо: %v", out.sent)
	}

	resolver.ns = []string{"b.ns.net"}
	clock = clock.Add(time.Hour)
	run()
	if len(out.sent) != 2 {
		t.Fatalf("смена серверов имён — письмо: %v", out.sent)
	}

	list, err := agent.db.Events(t.Context(), time.Unix(0, 0), time.Now().Add(48*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, event := range list {
		if event.Kind == events.KindDNSRecordGone || event.Kind == events.KindDNSChanged {
			got = append(got, event.Kind+":"+event.Detail)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"eventDnsChanged:ns", "eventDnsRecordGone:spf"}) {
		t.Fatalf("лента: %v", got)
	}

	// Сайт убрали — тревоги о его домене уходят вместе со снимком.
	agent.checkDNS(t.Context(), nil)
	state, err := agent.db.AlertState(t.Context(), "spf", "example.com")
	if err != nil || state.Firing {
		t.Fatalf("тревога удалённого домена осталась: %+v %v", state, err)
	}
}
