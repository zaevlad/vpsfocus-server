package bots

import (
	"net/netip"
	"sort"
	"strings"
)

// Проверка имени робота по адресу (трек AI, спринт 273, Р6–Р8, Р53–Р54).
//
// Владельцы многих роботов публикуют, с каких адресов те ходят; файлы
// вшиты в агента (`data/ranges.json`). Адрес из строки лога сверяется с
// ними в памяти и забывается: наружу выходит только исход. Агент для этого
// никуда не ходит — ни в DNS, ни в интернет.

// Исходы проверки. На экране — «настоящий», «назвался так с чужого адреса»
// и «не проверить» (Р43).
const (
	Verified   = "verified"
	Unverified = "unverified"
	Unknown    = "unknown"
)

// Почему «не проверить» (Р8).
const (
	// Владелец адресов не публикует, или файл старше 12 месяцев (Р54).
	ReasonNoRanges = "noRanges"
	// В строке лога нет адреса или вместо него имя хоста.
	ReasonNoAddr = "noAddr"
	// Адрес частный или петля: веб-сервер в Docker видит шлюз, а не
	// посетителя.
	ReasonPrivate = "private"
	// Адрес Cloudflare: сайт за прокси, а настоящий адрес посетителя
	// веб-сервер в лог не пишет.
	ReasonProxy = "proxy"
)

// span — непрерывный отрезок адресов; отрезки одного списка не
// пересекаются и идут по порядку — поиск двоичный (Р28).
type span struct{ from, to netip.Addr }

type spans []span

func (s spans) contains(addr netip.Addr) bool {
	i := sort.Search(len(s), func(i int) bool { return s[i].to.Compare(addr) >= 0 })
	return i < len(s) && s[i].from.Compare(addr) <= 0
}

// lastAddr — последний адрес префикса.
func lastAddr(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Masked().Addr().AsSlice()
	for bit := prefix.Bits(); bit < len(bytes)*8; bit++ {
		bytes[bit/8] |= 0x80 >> (bit % 8)
	}
	addr, _ := netip.AddrFromSlice(bytes)
	return addr
}

func toSpans(prefixes []string) spans {
	out := spans{}
	for _, item := range prefixes {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			// Вшитые данные проверены тестами пакета.
			panic(err)
		}
		out = append(out, span{prefix.Masked().Addr(), lastAddr(prefix)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from.Compare(out[j].from) < 0 })
	merged := spans{}
	for _, item := range out {
		if n := len(merged); n > 0 && merged[n-1].from.Is4() == item.from.Is4() {
			last := &merged[n-1]
			// Пересекается или примыкает: один отрезок. Next невалиден —
			// последний отрезок дошёл до конца адресов.
			next := last.to.Next()
			if !next.IsValid() || item.from.Compare(next) <= 0 {
				if item.to.Compare(last.to) > 0 {
					last.to = item.to
				}
				continue
			}
		}
		merged = append(merged, item)
	}
	return merged
}

type rangeIndex struct {
	byName     map[string]spans
	cloudflare spans
}

var ranges = mustRanges()

func mustRanges() *rangeIndex {
	loaded, err := LoadRanges()
	if err != nil {
		panic(err)
	}
	idx := &rangeIndex{byName: map[string]spans{}}
	for _, set := range loaded.Sets {
		// Старый файл не даёт «назвался» (Р54): неправда о самозванце хуже
		// «не проверить».
		if set.Stale {
			continue
		}
		list := toSpans(append(append([]string{}, set.V4...), set.V6...))
		for _, name := range set.Names {
			idx.byName[familyOf(name)] = list
		}
	}
	idx.cloudflare = toSpans(append(append([]string{}, loaded.Cloudflare.V4...), loaded.Cloudflare.V6...))
	return idx
}

// familyOf — под каким именем робот пишется в счётчики: у семейства — его
// имя (Googlebot-Image проверяется по адресам Googlebot, у них один файл).
func familyOf(name string) string {
	if i, ok := catalog.byName[name]; ok && catalog.robots[i].Family != "" {
		return catalog.robots[i].Family
	}
	return name
}

// Checkable — публикует ли владелец адреса робота с этим именем (так, как
// его возвращает [Classify]). Адрес из строки лога разбирается только у
// таких (Р28).
func Checkable(name string) bool {
	_, ok := ranges.byName[name]
	return ok
}

// sharedSpace — 100.64.0.0/10, адреса операторской NAT: настоящего
// посетителя за ними не видно, как и за частными.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// Verify — исход проверки робота name, пришедшего с адреса addr.
// Невалидный addr — адреса в строке не было.
func Verify(name string, addr netip.Addr) (string, string) {
	list, ok := ranges.byName[name]
	if !ok {
		return Unknown, ReasonNoRanges
	}
	if !addr.IsValid() {
		return Unknown, ReasonNoAddr
	}
	addr = addr.Unmap().WithZone("")
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || sharedSpace.Contains(addr) {
		return Unknown, ReasonPrivate
	}
	if list.contains(addr) {
		return Verified, ""
	}
	if ranges.cloudflare.contains(addr) {
		return Unknown, ReasonProxy
	}
	return Unverified, ""
}

// ParseAddr — адрес посетителя из поля строки лога, во всех формах, что
// пишут веб-серверы (Р27): `1.2.3.4`, `::ffff:1.2.3.4`, `1.2.3.4:5678`,
// `[2001:db8::1]:443`, `[2001:db8::1]`, `2001:db8::1`. `-` и имя хоста —
// не адрес.
func ParseAddr(field string) (netip.Addr, bool) {
	field = strings.TrimSpace(field)
	if field == "" || field == "-" {
		return netip.Addr{}, false
	}
	if strings.HasPrefix(field, "[") {
		end := strings.IndexByte(field, ']')
		if end < 0 {
			return netip.Addr{}, false
		}
		field = field[1:end]
	} else if strings.Count(field, ":") == 1 {
		// IPv4 с портом; у IPv6 двоеточий больше одного.
		field = field[:strings.IndexByte(field, ':')]
	}
	addr, err := netip.ParseAddr(field)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// CheckableNames — имена, у которых проверка по адресу есть, по порядку.
func CheckableNames() []string {
	out := make([]string, 0, len(ranges.byName))
	for name := range ranges.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
