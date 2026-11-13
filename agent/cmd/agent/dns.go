package main

// Записи домена (трек AH, спринт 267): раз в круг сроков агент читает у
// каждого регистрируемого домена SPF, DMARC, серверы имён, почтовые серверы
// и адреса сайтов, пишет перемены в ленту и зовёт человека по Р17.

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"sort"
	"strings"

	"agent/internal/checks"
	"agent/internal/config"
	"agent/internal/dnsrec"
	"agent/internal/domains"
	"agent/internal/events"
)

// Под каким видом снимки лежат в таблице проверок. Своя таблица не нужна:
// `checks` хранит JSON на цель, миграций нет (Р5).
const kindDNS = "dns"

func (a *Agent) dnsResolver() dnsrec.Resolver {
	if a.resolver != nil {
		return a.resolver
	}
	return net.DefaultResolver
}

// mailDomains — регистрируемые домены сайтов и сайты на каждом (П7): SPF и
// DMARC читаются один раз на домен, сколько бы сайтов на нём ни было.
func mailDomains(sites []config.Site) map[string][]string {
	out := map[string][]string{}
	for _, site := range sites {
		host := strings.ToLower(strings.TrimSuffix(site.Domain, "."))
		// Адрес вместо имени и одиночное имя (стенд, localhost) — записей у
		// них нет.
		if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
			continue
		}
		domain := domains.Registrable(host)
		out[domain] = append(out[domain], host)
	}
	for domain := range out {
		sort.Strings(out[domain])
	}
	return out
}

// checkDNS читает записи и отдаёт снимки и тревоги о них.
func (a *Agent) checkDNS(ctx context.Context, sites []config.Site) ([]dnsrec.Snapshot, []checks.Alert, []checks.Alert) {
	previous := map[string]dnsrec.Snapshot{}
	if raw, err := a.db.Checks(ctx, kindDNS); err != nil {
		log.Printf("прошлые записи домена: %v", err)
	} else {
		for target, item := range raw {
			var snapshot dnsrec.Snapshot
			if json.Unmarshal(item, &snapshot) == nil {
				previous[target] = snapshot
			}
		}
	}

	groups := mailDomains(sites)
	names := make([]string, 0, len(groups))
	for domain := range groups {
		names = append(names, domain)
	}
	sort.Strings(names)

	now := a.now()
	alive := map[string]bool{}
	snapshots := []dnsrec.Snapshot{}
	for _, domain := range names {
		if ctx.Err() != nil {
			break
		}
		alive[domain] = true
		var prev *dnsrec.Snapshot
		if item, ok := previous[domain]; ok {
			prev = &item
		}
		snapshot, changes := dnsrec.Read(ctx, a.dnsResolver(), domain, groups[domain], prev, now)
		snapshots = append(snapshots, snapshot)
		if err := a.db.SaveCheck(ctx, kindDNS, domain, snapshot); err != nil {
			log.Printf("сохранение записей домена: %v", err)
		}
		for _, change := range changes {
			kind := events.KindDNSChanged
			switch {
			case change.Gone:
				kind = events.KindDNSRecordGone
			case change.Back:
				kind = events.KindDNSRecordBack
			}
			a.note(kind, change.Target, change.Part)
		}
	}

	// Сайт удалили — его домен, его записи и его тревоги больше не наши.
	// Только после полного круга: оборванный круг не знает, что живо.
	if ctx.Err() == nil {
		if err := a.db.ForgetChecks(ctx, kindDNS, alive); err != nil {
			log.Printf("уборка записей домена: %v", err)
		}
		for _, kind := range []string{checks.KindSPF, checks.KindDMARC, checks.KindNameservers} {
			if err := a.db.ForgetAlerts(ctx, kind, alive); err != nil {
				log.Printf("уборка тревог о записях домена: %v", err)
			}
		}
	}

	firing, resolved := checks.DNSAlerts(a.words(), snapshots, now)
	return snapshots, firing, resolved
}

// dnsSnapshots — что лежит в базе, для ответа `/checks`.
func (a *Agent) dnsSnapshots(ctx context.Context) []dnsrec.Snapshot {
	out := []dnsrec.Snapshot{}
	raw, err := a.db.Checks(ctx, kindDNS)
	if err != nil {
		log.Printf("записи домена: %v", err)
		return out
	}
	for _, item := range raw {
		var snapshot dnsrec.Snapshot
		if json.Unmarshal(item, &snapshot) == nil {
			out = append(out, snapshot)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}
