package bots

import (
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// firstAddr — адрес из вшитого снимка (Р33): тест не прописывает чужие
// адреса руками и не устаревает вместе с файлом владельца.
func firstAddr(t *testing.T, name string) netip.Addr {
	t.Helper()
	loaded, err := LoadRanges()
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range loaded.Sets {
		for _, owner := range set.Names {
			if owner == name && len(set.V4) > 0 {
				prefix := netip.MustParsePrefix(set.V4[len(set.V4)-1])
				return lastAddr(prefix)
			}
		}
	}
	t.Fatalf("у %s нет адресов IPv4 в снимке", name)
	return netip.Addr{}
}

func TestVerifyOutcomes(t *testing.T) {
	gpt := firstAddr(t, "GPTBot")
	cases := []struct {
		name, addr      string
		outcome, reason string
	}{
		{"GPTBot", gpt.String(), Verified, ""},
		{"GPTBot", "::ffff:" + gpt.String(), Verified, ""},
		{"GPTBot", "8.8.8.8", Unverified, ""},
		{"GPTBot", "172.18.0.1", Unknown, ReasonPrivate},
		{"GPTBot", "127.0.0.1", Unknown, ReasonPrivate},
		{"GPTBot", "100.64.1.1", Unknown, ReasonPrivate},
		{"GPTBot", "fe80::1", Unknown, ReasonPrivate},
		{"GPTBot", "104.16.1.1", Unknown, ReasonProxy},
		{"GPTBot", "2606:4700::1", Unknown, ReasonProxy},
		{"GPTBot", "", Unknown, ReasonNoAddr},
		// Владелец адресов не публикует (Р53).
		{"ClaudeBot", "8.8.8.8", Unknown, ReasonNoRanges},
		{"YandexBot", "8.8.8.8", Unknown, ReasonNoRanges},
	}
	for _, tc := range cases {
		var addr netip.Addr
		if tc.addr != "" {
			addr = netip.MustParseAddr(tc.addr)
		}
		outcome, reason := Verify(tc.name, addr)
		if outcome != tc.outcome || reason != tc.reason {
			t.Errorf("%s с %q: %s/%s, ждали %s/%s", tc.name, tc.addr, outcome, reason, tc.outcome, tc.reason)
		}
	}
}

// Семейство проверяется по адресам семейства: Classify отдаёт «Googlebot».
func TestFamilyIsCheckable(t *testing.T) {
	if !Checkable("Googlebot") {
		t.Fatal("Googlebot не проверяется")
	}
	if Checkable("Googlebot-Image") {
		t.Fatal("член семейства проверяется отдельно — счётчики пишутся по семейству")
	}
	addr := firstAddr(t, "Googlebot")
	if outcome, _ := Verify(Classify("Googlebot-Image/1.0"), addr); outcome != Verified {
		t.Fatalf("Googlebot-Image с адреса Google: %s", outcome)
	}
}

// Р54: файл старше 12 месяцев не даёт «назвался».
func TestStaleSetIsNotCheckable(t *testing.T) {
	loaded, err := LoadRanges()
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range loaded.Sets {
		for _, name := range set.Names {
			if Checkable(familyOf(name)) == set.Stale {
				t.Errorf("%s (%s, старый: %v): проверяемость %v", name, set.ID, set.Stale, Checkable(familyOf(name)))
			}
		}
	}
}

func TestParseAddrForms(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":            "203.0.113.7",
		"::ffff:203.0.113.7":     "203.0.113.7",
		"203.0.113.7:51234":      "203.0.113.7",
		"[2001:db8::1]:443":      "2001:db8::1",
		"[2001:db8::1]":          "2001:db8::1",
		"2001:db8::1":            "2001:db8::1",
		"fe80::1%eth0":           "fe80::1",
		"-":                      "",
		"":                       "",
		"crawl-66-249-66-1.goog": "",
		"[2001:db8::1":           "",
	}
	for field, want := range cases {
		addr, ok := ParseAddr(field)
		got := ""
		if ok {
			got = addr.String()
		}
		if got != want {
			t.Errorf("%q → %q, ждали %q", field, got, want)
		}
	}
}

func TestSpansMergeAndSearch(t *testing.T) {
	list := toSpans([]string{"10.0.1.0/24", "10.0.0.0/24", "10.0.0.128/25", "2001:db8::/48", "10.0.3.0/24"})
	if len(list) != 3 {
		t.Fatalf("отрезков %d: %+v", len(list), list)
	}
	for addr, want := range map[string]bool{
		"10.0.0.0": true, "10.0.1.255": true, "10.0.2.0": false, "10.0.3.9": true,
		"2001:db8::5": true, "2001:db9::": false, "9.255.255.255": false,
	} {
		if got := list.contains(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

// Вторая копия диапазонов Cloudflare (Р8) сверяется с первой — списком
// `trusted_proxies` в Caddyfile последней версии бандла. Одна копия правила:
// разойдись они, агент называл бы прокси самозванцем.
func TestCloudflareMatchesTheBundle(t *testing.T) {
	versions := filepath.Join("..", "..", "..", "installer-bundle", "versions")
	entries, err := os.ReadDir(versions)
	if err != nil {
		t.Skipf("бандла рядом нет (открытое зеркало агента): %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return versionLess(names[i], names[j]) })
	raw, err := os.ReadFile(filepath.Join(versions, names[len(names)-1], "Caddyfile.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`trusted_proxies static ([^\n]+)`).FindStringSubmatch(string(raw))
	if line == nil {
		t.Fatal("в Caddyfile нет trusted_proxies static")
	}
	bundle := strings.Fields(line[1])
	sort.Strings(bundle)

	loaded, err := LoadRanges()
	if err != nil {
		t.Fatal(err)
	}
	ours := append(append([]string{}, loaded.Cloudflare.V4...), loaded.Cloudflare.V6...)
	sort.Strings(ours)
	if strings.Join(bundle, " ") != strings.Join(ours, " ") {
		t.Fatalf("диапазоны Cloudflare разошлись:\n бандл  %v\n агент  %v", bundle, ours)
	}
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

func BenchmarkVerify(b *testing.B) {
	addr := netip.MustParseAddr("66.249.66.1")
	for i := 0; i < b.N; i++ {
		Verify("Googlebot", addr)
	}
}
