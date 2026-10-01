package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent/internal/probe"
	"agent/internal/s3"
)

// Заголовки ключевых адресов в копию не едут.
//
// `probes.json` — единственный конфиг агента, который вправе нести токен
// закрытого раздела, а копия лежит в бакете открытой: не шифровать её можно
// ровно потому, что секретов в ней нет.
func TestHeadersNeverReachTheCopy(t *testing.T) {
	source, err := json.Marshal([]probe.Profile{{
		Name:      "закрытый раздел",
		ProjectID: "p1",
		Kind:      probe.KindJSON,
		URL:       "https://example.com/api/health",
		Headers:   map[string]string{"Authorization": "Bearer секрет-клиента"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	cleaned := stripHeaders(source)
	if strings.Contains(string(cleaned), "секрет-клиента") {
		t.Fatalf("токен уехал в копию: %s", cleaned)
	}

	var profiles []probe.Profile
	if err := json.Unmarshal(cleaned, &profiles); err != nil {
		t.Fatalf("вырезанный файл не разобрался: %v", err)
	}
	if len(profiles) != 1 || profiles[0].URL != "https://example.com/api/health" {
		t.Fatalf("вместе с заголовком потерялся сам адрес: %+v", profiles)
	}
	if profiles[0].Headers != nil {
		t.Fatal("заголовки остались")
	}

	// Не разобралось — не кладём вовсе: непонятный файл с возможным
	// секретом в открытой копии хуже отсутствующего.
	if string(stripHeaders([]byte("{это не json"))) != "[]" {
		t.Fatal("неразобранный файл уехал как есть")
	}
}

// Собранный архив открывается обратно, и в нём то, что обещано.
func TestArchiveOpensBackWithEverythingInside(t *testing.T) {
	dir := t.TempDir()
	cfg := stubConfig(t, dir)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	dumpPath := filepath.Join(dir, FileDump)
	body := "-- PostgreSQL database dump\n" + strings.Repeat("insert into website values (1);\n", 200)
	if err := os.WriteFile(dumpPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "copy.tar.gz")
	if err := pack(cfg, dumpPath, archivePath, int64(len(body)), now); err != nil {
		t.Fatalf("сборка архива: %v", err)
	}

	checked, err := verify(archivePath)
	if err != nil {
		t.Fatalf("проверка архива: %v", err)
	}
	if checked.DumpBytes != int64(len(body)) {
		t.Errorf("дамп в архиве %d байт вместо %d", checked.DumpBytes, len(body))
	}
	if checked.SHA256 == "" || checked.MD5 == "" || checked.Size == 0 {
		t.Errorf("суммы не посчитаны: %+v", checked)
	}

	names := entries(t, archivePath)
	for _, want := range []string{
		FileManifest, FileDump,
		"stack-version.json", "agent-config/sites.json", "agent-config/probes.json",
	} {
		if !names[want] {
			t.Errorf("в архиве нет %s", want)
		}
	}
	// Секретов нет не по обещанию: агент их прочитать не может — а если бы
	// мог, они оказались бы в открытой копии.
	for _, secret := range []string{".env", "agent.env"} {
		if names[secret] {
			t.Errorf("в копию уехал %s", secret)
		}
	}

	var manifest Manifest
	if err := json.Unmarshal(entry(t, archivePath, FileManifest), &manifest); err != nil {
		t.Fatalf("паспорт копии не разобрался: %v", err)
	}
	if manifest.Format != Format || manifest.Secrets || !manifest.HeadersStripped {
		t.Errorf("паспорт врёт о содержимом: %+v", manifest)
	}
	if manifest.StackVersion != "0.11.0" {
		t.Errorf("версия стека не записана: %q", manifest.StackVersion)
	}
	if strings.Contains(string(entry(t, archivePath, "agent-config/probes.json")), "секрет") {
		t.Error("токен ключевого адреса уехал в копию")
	}
}

// Пустой дамп — не копия, а ложное спокойствие.
func TestEmptyDumpIsRefused(t *testing.T) {
	dir := t.TempDir()
	cfg := stubConfig(t, dir)
	now := time.Now()

	dumpPath := filepath.Join(dir, FileDump)
	if err := os.WriteFile(dumpPath, []byte("-- PostgreSQL database dump\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dumpPath)
	if err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "copy.tar.gz")
	if err := pack(cfg, dumpPath, archivePath, info.Size(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := verify(archivePath); err == nil {
		t.Fatal("архив с пустым дампом принят за копию")
	}
}

// Дамп, который не дамп, тоже отказ.
//
// Так выглядит pg_dump, упавший после первой строки, и так же выглядит
// перепутанный файл. Проверка «файл на месте и не нулевой» пропустила бы оба.
func TestDumpMustLookLikeADump(t *testing.T) {
	dir := t.TempDir()
	cfg := stubConfig(t, dir)

	dumpPath := filepath.Join(dir, FileDump)
	body := strings.Repeat("это не дамп, это чужой файл\n", 100)
	if err := os.WriteFile(dumpPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "copy.tar.gz")
	if err := pack(cfg, dumpPath, archivePath, int64(len(body)), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := verify(archivePath); err == nil {
		t.Fatal("чужой файл принят за дамп")
	}
}

// Битый архив не выдаёт себя за целый.
//
// Проверяется тот самый файл, который поедет в бакет: ошибка записи на диск
// живёт ровно между «собрали» и «отправили».
func TestBrokenArchiveIsCaught(t *testing.T) {
	dir := t.TempDir()
	cfg := stubConfig(t, dir)

	dumpPath := filepath.Join(dir, FileDump)
	body := "-- PostgreSQL database dump\n" + strings.Repeat("x", 4096)
	if err := os.WriteFile(dumpPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "copy.tar.gz")
	if err := pack(cfg, dumpPath, archivePath, int64(len(body)), time.Now()); err != nil {
		t.Fatal(err)
	}

	whole, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, whole[:len(whole)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := verify(archivePath); err == nil {
		t.Fatal("обрезанный архив прошёл проверку")
	}
}

// Ключ копии начинается с префикса сервера и читается человеком.
func TestKeyIsSortableAndSafe(t *testing.T) {
	at := time.Date(2026, 9, 8, 12, 4, 5, 0, time.UTC)

	key := Key("vpsfocus/srv-1", at)
	if key != "vpsfocus/srv-1/2026-09-08T12-04-05Z.tar.gz" {
		t.Fatalf("ключ собран как %s", key)
	}
	// Двоеточий нет: копию качает человек на рабочую машину, а на Windows
	// такое имя файла не сохранить.
	if strings.Contains(key, ":") {
		t.Fatal("в ключе двоеточие")
	}
	// Ключи сортируются как время: на этом стоит и ротация, и «самая
	// свежая копия».
	earlier := Key("vpsfocus/srv-1", at.Add(-time.Hour))
	if !(earlier < key) {
		t.Fatalf("порядок ключей не совпадает с порядком времени: %s ≥ %s", earlier, key)
	}

	if Key("", at) == "" || strings.HasPrefix(Key("", at), "/") {
		t.Fatal("пустой префикс сломал ключ")
	}
}

// Префикс ротации кончается слэшем, иначе она заберёт чужие копии.
//
// `vpsfocus/srv-1` без слэша захватывает и `vpsfocus/srv-10`: ротация удалила
// бы копии соседнего сервера, оставив «семь самых свежих» на двоих.
func TestRotationPrefixNeverCatchesTheNeighbour(t *testing.T) {
	if got := listPrefix("vpsfocus/srv-1"); got != "vpsfocus/srv-1/" {
		t.Fatalf("префикс списка %q", got)
	}
	if got := listPrefix(""); got != "" {
		t.Fatalf("пустой префикс стал %q — это весь бакет", got)
	}
}

// Отказ хранилища переводится в код, по которому понятен первый шаг.
func TestRefusalsMapToDifferentAnswers(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&s3.Error{Status: 403, Code: "SignatureDoesNotMatch"}, CodeAccessDenied},
		{&s3.Error{Status: 403, Code: "AccessDenied"}, CodeAccessDenied},
		{&s3.Error{Status: 404, Code: "NoSuchBucket"}, CodeBucketMissing},
		{&s3.Error{Status: 500, Code: "InternalError"}, CodeUploadFailed},
		{errors.New("dial tcp: нет маршрута"), CodeStorageUnreachable},
	}

	for _, item := range cases {
		if got, _ := Classify(item.err); got != item.want {
			t.Errorf("%v переведено в %s, ожидалось %s", item.err, got, item.want)
		}
	}
}

// Выключенная функция и функция без бакета — одно состояние: копий нет.
func TestConfiguredNeedsEverything(t *testing.T) {
	full := Config{
		Enabled:  true,
		Storage:  s3.Config{Endpoint: "https://s3.example.test", Bucket: "b", KeyID: "k", Secret: "s"},
		Postgres: Postgres{Host: "postgres", User: "umami", Database: "umami"},
	}
	if !full.Configured() {
		t.Fatal("полные настройки не признаны настройками")
	}

	off := full
	off.Enabled = false
	if off.Configured() {
		t.Error("выключенная функция считается настроенной")
	}

	noBucket := full
	noBucket.Storage.Bucket = ""
	if noBucket.Configured() {
		t.Error("без бакета копию класть некуда, а функция считается живой")
	}

	noDB := full
	noDB.Postgres.Host = ""
	if noDB.Configured() {
		t.Error("без адреса базы снимать нечего")
	}
}

// Круг без настроек не притворяется удачным.
func TestRunWithoutStorageRecordsTheReason(t *testing.T) {
	record := Run(t.Context(), Config{}, time.Now())
	if record.OK || record.Code != CodeNotConfigured {
		t.Fatalf("круг без хранилища кончился как %+v", record)
	}
	if record.FinishedAt.IsZero() {
		t.Fatal("у круга нет времени окончания: запись о нём не с чем сравнить")
	}
}

// ── Помощники ────────────────────────────────────────────────────────────

func stubConfig(t *testing.T, dir string) Config {
	t.Helper()

	stack := filepath.Join(dir, "stack")
	config := filepath.Join(dir, "config")
	for _, path := range []string{stack, config} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(stack, "stack-version.json"), `{"stackVersion":"0.11.0"}`)
	write(filepath.Join(stack, "docker-compose.yml"), "name: vpsfocus\n")
	write(filepath.Join(stack, "Caddyfile"), ":8443 { }\n")
	// Секреты стека агенту нечитаемы по правам: здесь их просто нет, и
	// класть в копию нечего — ровно как на сервере.
	write(filepath.Join(config, "sites.json"), `[{"domain":"example.com"}]`)
	write(filepath.Join(config, "projects.json"), `[]`)
	write(filepath.Join(config, "probes.json"),
		`[{"name":"api","projectId":"p1","kind":"json","url":"https://example.com/api",`+
			`"headers":{"Authorization":"Bearer секрет"}}]`)

	return Config{
		Enabled:   true,
		ConfigDir: config,
		StackDir:  stack,
		WorkDir:   dir,
	}
}

func entries(t *testing.T, path string) map[string]bool {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	zip, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zip.Close()

	names := map[string]bool{}
	archive := tar.NewReader(zip)
	for {
		header, err := archive.Next()
		if err != nil {
			break
		}
		names[header.Name] = true
	}
	return names
}

func entry(t *testing.T, path, name string) []byte {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	zip, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zip.Close()

	archive := tar.NewReader(zip)
	for {
		header, err := archive.Next()
		if err != nil {
			t.Fatalf("в архиве нет %s", name)
		}
		if header.Name != name {
			continue
		}
		body := make([]byte, header.Size)
		if _, err := archive.Read(body); err != nil && header.Size > 0 {
			// io.EOF на последнем чтении — обычное дело для tar.
			if len(body) == 0 {
				t.Fatal(err)
			}
		}
		return body
	}
}
