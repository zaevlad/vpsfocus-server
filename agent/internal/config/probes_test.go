package config

import (
	"os"
	"path/filepath"
	"testing"
)

func profilesFile(t *testing.T, body string) Config {
	t.Helper()

	path := filepath.Join(t.TempDir(), "probes.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{Probes: Probes{ProfilesPath: path}}
}

// Отсутствие файла — не ошибка: ключевые адреса задаёт человек, и пустой
// список означает «пока не задал», а не поломку.
func TestMissingProbesFileIsNotAnError(t *testing.T) {
	cfg := Config{Probes: Probes{ProfilesPath: filepath.Join(t.TempDir(), "нет.json")}}

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatalf("отсутствие файла стало ошибкой: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("профилей %d, ждали ноль", len(profiles))
	}
}

// Профиль без проекта выбрасывается: свод поднимает уровень именно проекту,
// и находка, которой некуда лечь, никого не разбудит.
func TestProfileWithoutAProjectIsDropped(t *testing.T) {
	cfg := profilesFile(t, `[
		{"projectId":"", "url":"https://example.com/"},
		{"projectId":"p1", "url":""},
		{"projectId":"p1", "kind":"page", "url":"https://example.com/"}
	]`)

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 {
		t.Fatalf("профилей %d, ждали один: %+v", len(profiles), profiles)
	}
}

// Схема проверяется: адрес приходит из файла, а не из кода, и `file://`
// превратил бы проверку в чтение чужих файлов чужими глазами.
func TestOnlyHttpSchemesSurvive(t *testing.T) {
	cfg := profilesFile(t, `[
		{"projectId":"p1", "url":"file:///etc/shadow"},
		{"projectId":"p1", "url":"ftp://example.com/"},
		{"projectId":"p1", "url":"https://example.com/"}
	]`)

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 {
		t.Fatalf("профилей %d, ждали один: %+v", len(profiles), profiles)
	}
	if profiles[0].URL != "https://example.com/" {
		t.Fatalf("выжил %q", profiles[0].URL)
	}
}

// Учётные данные в адресе и строка запроса — секрет, а адрес ключевой
// проверки показывается человеку в письме, в журнале сервера и на экране.
// Заголовок профилю разрешён законно (он лежит в файле с правами 640 и
// нигде не показывается), адрес — нет.
func TestProbeURLCarriesNoSecret(t *testing.T) {
	cfg := profilesFile(t, `[
		{"projectId":"p1", "url":"https://user:pass@example.com/"},
		{"projectId":"p1", "url":"https://example.com/api?token=secret"},
		{"projectId":"p1", "url":"https://example.com/api/health"}
	]`)

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 {
		t.Fatalf("профилей %d, ждали один: %+v", len(profiles), profiles)
	}
	if profiles[0].URL != "https://example.com/api/health" {
		t.Fatalf("выжил %q", profiles[0].URL)
	}
}

// Пустой хвостовой `?` адрес не меняет, и отбор у обеих половин один: этот
// адрес принимает приложение, значит обязан принять и агент. Иначе проверка
// молча исчезла бы с экрана, заведённая и сохранённая.
func TestEmptyQueryMarkIsNotAQuery(t *testing.T) {
	cfg := profilesFile(t, `[{"projectId":"p1", "url":"https://example.com/health?"}]`)

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 {
		t.Fatalf("профилей %d, ждали один: %+v", len(profiles), profiles)
	}
}

// Потолок на проект соблюдается и здесь, а не только в приложении: конфиг
// переживает выпуски, а чужой прод один.
func TestCapIsPerProject(t *testing.T) {
	cfg := profilesFile(t, `[
		{"projectId":"p1", "url":"https://a.test/1"},
		{"projectId":"p1", "url":"https://a.test/2"},
		{"projectId":"p1", "url":"https://a.test/3"},
		{"projectId":"p1", "url":"https://a.test/4"},
		{"projectId":"p2", "url":"https://b.test/1"}
	]`)

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != MaxProbesPerProject+1 {
		t.Fatalf("профилей %d, ждали %d", len(profiles), MaxProbesPerProject+1)
	}
	// Лишний отброшен у своего проекта, а не у чужого.
	for _, profile := range profiles {
		if profile.URL == "https://a.test/4" {
			t.Fatal("лишний адрес проекта прошёл потолок")
		}
	}
}

// Имя не задано — берём адрес: строка без имени в отчёте выглядит как
// потерянная, а имя обязательным полем делать незачем.
func TestNamelessProfileGetsItsURL(t *testing.T) {
	cfg := profilesFile(t, `[{"projectId":"p1", "url":"https://example.com/"}]`)

	profiles, err := cfg.ProbeProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if profiles[0].Name != "https://example.com/" {
		t.Fatalf("имя %q", profiles[0].Name)
	}
}
