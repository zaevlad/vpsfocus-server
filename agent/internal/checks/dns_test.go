package checks

import (
	"strings"
	"testing"
	"time"

	"agent/internal/dnsrec"
)

// Письмо — только «была и пропала» и смена серверов имён (Р17); «нет» у
// домена без записи и «не знаем» молчат.
func TestDNSAlerts(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	changed := now.Add(-time.Hour)
	words := WordsFor("en")
	snapshots := []dnsrec.Snapshot{
		{
			Domain: "gone.com",
			SPF:    dnsrec.Record{State: dnsrec.StateMissing, Seen: true, Gone: true},
			DMARC:  dnsrec.Record{State: dnsrec.StatePresent, Seen: true},
			NS:     dnsrec.Names{Known: true, List: []string{"b.ns"}}, NSBefore: []string{"a.ns"}, NSChangedAt: &changed,
		},
		{
			Domain: "quiet.com",
			SPF:    dnsrec.Record{State: dnsrec.StateMissing},
			DMARC:  dnsrec.Record{State: dnsrec.StateUnknown, Seen: true, Gone: true},
		},
	}
	firing, resolved := DNSAlerts(words, snapshots, now)
	kinds := []string{}
	for _, alert := range firing {
		kinds = append(kinds, alert.Kind+":"+alert.Target)
		if alert.Urgent || alert.Step != 1 {
			t.Errorf("не срочная, одно письмо на беду: %+v", alert)
		}
	}
	if strings.Join(kinds, ",") != "spf:gone.com,nameservers:gone.com" {
		t.Fatalf("тревоги: %v", kinds)
	}
	if !strings.Contains(firing[1].Body, "a.ns") || !strings.Contains(firing[1].Body, "b.ns") {
		t.Errorf("в письме — было и стало: %q", firing[1].Body)
	}
	quiet := 0
	for _, alert := range resolved {
		if alert.Target == "quiet.com" && alert.Kind != KindNameservers {
			t.Errorf("«нет» без записи и «не знаем» — без отбоя: %+v", alert)
		}
		if alert.QuietResolve {
			quiet++
		}
	}
	if quiet != 1 {
		t.Errorf("отбой смены серверов имён — без письма, у quiet.com: %d", quiet)
	}
}
