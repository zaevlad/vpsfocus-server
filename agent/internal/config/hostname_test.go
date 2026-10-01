package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Имя сервера — из его /etc/hostname, а не имя контейнера агента.
func TestHostNameComesFromTheServer(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "hostname"), []byte("shop-vps-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := HostName(root); got != "shop-vps-1" {
		t.Fatalf("имя сервера: %q", got)
	}

	// Файла нет — имя из системы, и не пустое.
	if got := HostName(t.TempDir()); got == "" {
		t.Fatal("пустое имя сервера")
	}
}
