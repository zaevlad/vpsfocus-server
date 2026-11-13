package checks

import (
	"fmt"
	"strings"
	"time"

	"agent/internal/dnsrec"
)

// Виды тревог о записях домена (трек AH, спринт 267). Цель — регистрируемый
// домен.
const (
	KindSPF         = "spf"
	KindDMARC       = "dmarc"
	KindNameservers = "nameservers"
)

// DNSAlerts решает, писать ли о записях домена (В9 — Р17).
//
// Письмо — только «запись SPF или DMARC была и пропала» и «сменились серверы
// имён». Нет записи у домена, у которого её не было, — не беда (П8): с
// большинства доменов нашей аудитории письма не уходят. Не знаем — ни
// тревоги, ни отбоя. Обе тревоги ждут утра: если из-за смены серверов имён
// упал сайт, тревога о сайте придёт сама. Одно письмо на беду (ступень 1).
func DNSAlerts(words Words, snapshots []dnsrec.Snapshot, now time.Time) (firing, resolved []Alert) {
	for _, snapshot := range snapshots {
		for _, item := range []struct {
			kind, name string
			record     dnsrec.Record
		}{
			{KindSPF, "SPF", snapshot.SPF},
			{KindDMARC, "DMARC", snapshot.DMARC},
		} {
			switch {
			case item.record.Gone && item.record.State != dnsrec.StateUnknown:
				firing = append(firing, Alert{
					Kind:    item.kind,
					Target:  snapshot.Domain,
					Subject: fmt.Sprintf(words.RecordGoneSubject, snapshot.Domain, item.name),
					Body:    fmt.Sprintf(words.RecordGoneBody, item.name, snapshot.Domain),
					Step:    1,
				})
			case item.record.State == dnsrec.StatePresent:
				resolved = append(resolved, Alert{
					Kind:    item.kind,
					Target:  snapshot.Domain,
					Subject: fmt.Sprintf(words.RecordBackSubject, snapshot.Domain, item.name),
					Body:    fmt.Sprintf(words.RecordBackBody, item.name, snapshot.Domain),
				})
			}
		}

		if snapshot.NSChangedRecently(now) {
			firing = append(firing, Alert{
				Kind:    KindNameservers,
				Target:  snapshot.Domain,
				Subject: fmt.Sprintf(words.NameserversSubject, snapshot.Domain),
				Body: fmt.Sprintf(words.NameserversBody, snapshot.Domain,
					strings.Join(snapshot.NSBefore, ", "), strings.Join(snapshot.NS.List, ", ")),
				Step: 1,
			})
		} else {
			resolved = append(resolved, Alert{Kind: KindNameservers, Target: snapshot.Domain, QuietResolve: true})
		}
	}
	return firing, resolved
}
