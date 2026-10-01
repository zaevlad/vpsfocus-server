package config

import (
	"testing"
	"time"
)

// Адрес базы разбирается целиком, включая пароль.
//
// Строкой он приезжает потому, что её собирает тот же файл компоновки,
// который уже собирает такую же для Umami: две записи одного адреса разошлись
// бы, и разошлась бы та, про которую забыли.
func TestPostgresURLIsParsedWhole(t *testing.T) {
	got := postgres("postgres://umami:Пароль123@postgres:5432/umami")

	if got.Host != "postgres" || got.Port != 5432 {
		t.Errorf("адрес разобран как %s:%d", got.Host, got.Port)
	}
	if got.User != "umami" || got.Database != "umami" {
		t.Errorf("пользователь и база разобраны как %s/%s", got.User, got.Database)
	}
	if got.Password != "Пароль123" {
		t.Errorf("пароль разобран как %q", got.Password)
	}

	// Порт по умолчанию: в файле компоновки его пишут не всегда.
	if short := postgres("postgres://umami:x@postgres/umami"); short.Port != 5432 {
		t.Errorf("порт без указания стал %d", short.Port)
	}

	// Пусто и мусор — это «база не задана», а не половина настроек:
	// круг копии тогда честно скажет «хранилище не настроено».
	for _, raw := range []string{"", "   ", "://"} {
		if got := postgres(raw); got.Host != "" {
			t.Errorf("из %q собрался адрес %+v", raw, got)
		}
	}
}

// Умолчания копии — те же, что обещает экран приложения.
func TestBackupDefaults(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "токен")
	t.Setenv("AGENT_DATABASE", "/data/agent.db")
	t.Setenv("AGENT_SITES", "/config/sites.json")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("настройки: %v", err)
	}

	// Копии выключены, пока человек не завёл хранилище: класть их некуда,
	// и делать вид, что функция работает, нельзя.
	if cfg.Backup.Enabled {
		t.Error("копии включены без единой настройки")
	}
	if cfg.Backup.Configured() {
		t.Error("настроенными считаются копии без бакета")
	}
	if cfg.Backup.Interval != 24*time.Hour {
		t.Errorf("расписание по умолчанию: %v", cfg.Backup.Interval)
	}
	if cfg.Backup.Keep != 7 {
		t.Errorf("копий по умолчанию хранится %d", cfg.Backup.Keep)
	}
	if cfg.Backup.WorkDir != "/data" || cfg.Backup.ConfigDir != "/config" {
		t.Errorf("каталоги выведены неверно: %s и %s", cfg.Backup.WorkDir, cfg.Backup.ConfigDir)
	}
}

// Ноль копий в ротации — законный ответ «не удалять ничего».
//
// У соседних настроек ноль означает бессмыслицу и заменяется умолчанием; тут
// он означает «жизненным циклом бакета распоряжаюсь я сам», и подменять его
// семёркой значило бы стирать чужие копии вопреки прямому указанию.
func TestKeepZeroMeansKeepEverything(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "токен")
	t.Setenv("BACKUP_KEEP", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("настройки: %v", err)
	}
	if cfg.Backup.Keep != 0 {
		t.Fatalf("ноль превратился в %d", cfg.Backup.Keep)
	}

	// А отрицательное число — всё-таки бессмыслица.
	t.Setenv("BACKUP_KEEP", "-3")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("настройки: %v", err)
	}
	if cfg.Backup.Keep != 7 {
		t.Fatalf("минус три копии превратились в %d", cfg.Backup.Keep)
	}
}

// Настроенным хранилище считается только целиком.
func TestBackupNeedsEveryPartOfTheStorage(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "токен")
	t.Setenv("BACKUP_ENABLED", "1")
	t.Setenv("BACKUP_S3_ENDPOINT", "https://s3.example.test")
	t.Setenv("BACKUP_S3_BUCKET", "копии")
	t.Setenv("BACKUP_S3_KEY_ID", "ключ")
	t.Setenv("BACKUP_S3_SECRET", "секрет")
	t.Setenv("BACKUP_S3_PREFIX", "/vpsfocus/srv-1/")
	t.Setenv("AGENT_POSTGRES_URL", "postgres://umami:пароль@postgres:5432/umami")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("настройки: %v", err)
	}
	if !cfg.Backup.Configured() {
		t.Fatal("полные настройки не признаны настройками")
	}
	// Слэши по краям префикса срезаны: ключ собирается склейкой, и
	// `//` в середине превратился бы в пустой сегмент пути.
	if cfg.Backup.Prefix != "vpsfocus/srv-1" {
		t.Fatalf("префикс разобран как %q", cfg.Backup.Prefix)
	}
}
