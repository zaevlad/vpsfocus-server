package probe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Таблицы — как на живом тестовом сервере (2026-10-12): стек на петле
// (47821, 47822), 443 и 22 на всех адресах, плюс строки, которых в боевой
// таблице не было: соединение (не LISTEN) и база на конкретном адресе.
const tcpTable = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:BACD 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 37393872 1 0000000000000000 100 0 0 10 0
   1: 0100007F:BACE 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 37493102 1 0000000000000000 100 0 0 10 0
   2: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 37493414 1 0000000000000000 100 0 0 10 0
   3: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 178218 1 0000000000000000 100 0 0 10 0
   4: 4F7A0B59:0016 0A01A8C0:D431 01 00000000:00000000 02:000A0B3C 00000000     0        0 39000001 4 0000000000000000 20 4 30 10 -1
   5: 0A01A8C0:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 39000002 1 0000000000000000 100 0 0 10 0
`

const tcp6Table = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 37493415 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:18EB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 39000003 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:0CEA 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 39000004 1 0000000000000000 100 0 0 10 0
`

// fakeProc раскладывает таблицы так, как их видит агент: <proc>/1/net/tcp.
func fakeProc(t *testing.T, tcp, tcp6 string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "1", "net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if tcp != "" {
		if err := os.WriteFile(filepath.Join(dir, "tcp"), []byte(tcp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if tcp6 != "" {
		if err := os.WriteFile(filepath.Join(dir, "tcp6"), []byte(tcp6), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runOnPort(t *testing.T, root, address string) Result {
	t.Helper()
	previous := hostProc
	SetHostProc(root)
	t.Cleanup(func() { hostProc = previous })
	return runPort(Profile{Name: "база", Kind: KindPort, URL: address},
		Result{Kind: KindPort, URL: address}, time.Now())
}

func TestPortOfAcceptsOnlyAPortNumber(t *testing.T) {
	for address, want := range map[string]int{"port:1": 1, "port:5432": 5432, "port:65535": 65535, " port:22 ": 22} {
		if got, ok := PortOf(address); !ok || got != want {
			t.Errorf("%q: %d %v", address, got, ok)
		}
	}
	for _, address := range []string{"", "port:", "port:0", "port:022", "port:65536", "port:5432/tcp", "tcp:5432", "5432", "port:-1", "http://x:5432"} {
		if _, ok := PortOf(address); ok {
			t.Errorf("%q принят", address)
		}
	}
}

// Где слушает порт: петля — «только этот сервер», любой другой адрес — «из
// сети», и сеть побеждает петлю.
func TestPortListeningPlaces(t *testing.T) {
	root := fakeProc(t, tcpTable, tcp6Table)

	cases := map[string]string{
		"port:47821": ListenLocal,   // 127.0.0.1
		"port:443":   ListenNetwork, // 0.0.0.0 и ::
		"port:22":    ListenNetwork, // 0.0.0.0; соединение на 22 не в счёт
		"port:5432":  ListenNetwork, // 192.168.1.10
		"port:6379":  ListenLocal,   // ::1
		"port:3306":  ListenLocal,   // ::ffff:127.0.0.1
	}
	for address, want := range cases {
		result := runOnPort(t, root, address)
		if !result.OK || result.Listen != want || result.Code != "" {
			t.Errorf("%s: ok=%v listen=%q code=%q, ждали %q", address, result.OK, result.Listen, result.Code, want)
		}
	}
}

// Никто не слушает — «закрыт»; соединение на порту (не LISTEN) — тоже закрыт.
func TestPortNobodyListens(t *testing.T) {
	root := fakeProc(t, tcpTable, tcp6Table)
	for _, address := range []string{"port:8080", "port:54321"} {
		result := runOnPort(t, root, address)
		if result.OK || result.Code != CodePortClosed || result.Listen != "" {
			t.Errorf("%s: %+v", address, result)
		}
	}
}

// Нет tcp6 — сервер без IPv6, а не «не знаем».
func TestPortWithoutIPv6Table(t *testing.T) {
	root := fakeProc(t, tcpTable, "")
	if result := runOnPort(t, root, "port:22"); !result.OK || result.Listen != ListenNetwork {
		t.Errorf("без tcp6: %+v", result)
	}
}

// Таблицы не прочитались — «не знаем», серым, а не «закрыт».
func TestPortUnreadableTableIsUnknown(t *testing.T) {
	root := t.TempDir()
	result := runOnPort(t, root, "port:22")
	if result.OK || result.Code != CodePortUnknown {
		t.Errorf("пустой /proc: %+v", result)
	}
}

// Негодный адрес — «проверять нечего», и до таблиц дело не доходит.
func TestPortMisconfigured(t *testing.T) {
	root := fakeProc(t, tcpTable, tcp6Table)
	if result := runOnPort(t, root, "port:0"); result.Code != CodeMisconfigured {
		t.Errorf("port:0: %+v", result)
	}
}
