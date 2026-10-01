// Package backup делает копию аналитики клиента и кладёт её в хранилище,
// которое клиент оплачивает сам.
//
// Мест, куда её можно класть, три, и два отпадают сами: копия на том же VPS
// умрёт вместе с диском, копия у нас нарушает инвариант — аналитика клиента
// на инфраструктуре vpsFocus не окажется ни при каких обстоятельствах.
// Остаётся S3-совместимое хранилище клиента.
//
// **Секретов в копии нет, и не по обещанию.** `.env` и `agent.env` лежат с
// правами 600 и владельцем root, а агент работает под своим пользователем —
// прочитать их он не может физически. Единственное исключение, `probes.json`
// с правами 640, едет без заголовков: в них живёт токен закрытого раздела, и
// вырезает их сборщик архива. Копия при этом **не шифруется**, и это
// решение: второй невосстановимый ключ рядом с ключом подписи обновлений
// дороже, чем открытая аналитика в бакете, доступ к которому есть только у
// владельца бакета. Сказать об этом человеку обязан экран.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent/internal/probe"
	"agent/internal/s3"
)

// Коды исходов. Наружу уходят они, текст собирает приложение — как и везде.
const (
	// Хранилище не настроено: копии не делаются вовсе.
	CodeNotConfigured = "backupNotConfigured"
	// В образе нет pg_dump. Так выглядит стек, поставленный до того, как
	// автокопии появились: чинится обновлением, и сказать надо именно это.
	CodeToolMissing = "backupToolMissing"
	// Снять дамп не удалось: база не отвечает или пароль не подошёл.
	CodeDumpFailed = "backupDumpFailed"
	// Дамп пуст. Пустой дамп — не копия, а ложное спокойствие.
	CodeDumpEmpty = "backupDumpEmpty"
	// Архив не собрался: чаще всего кончилось место на диске сервера.
	CodeArchiveFailed = "backupArchiveFailed"
	// Собранный архив не открылся обратно. Проверяется тот самый файл,
	// который поедет в бакет, а не поток, из которого он собран.
	CodeArchiveBroken = "backupArchiveBroken"
	// Хранилище не пустило: ключ, секрет или права на бакет.
	CodeAccessDenied = "backupAccessDenied"
	// Бакета с таким именем нет.
	CodeBucketMissing = "backupBucketMissing"
	// До хранилища не достучались: сети нет, адрес неверен, TLS не сошёлся.
	CodeStorageUnreachable = "backupStorageUnreachable"
	// Хранилище отказало по другой причине — код лежит в подробностях.
	CodeUploadFailed = "backupUploadFailed"
	// Выгрузка прошла, но объекта в бакете нет или он другого размера.
	CodeMissingAfterUpload = "backupMissingAfterUpload"
	// Круг не уложился в отведённое время.
	CodeTimeout = "backupTimeout"
	// На диске сервера не хватает места под дамп и архив.
	//
	// Свой код, а не `backupArchiveFailed`: «pg_dump не сработал» и «места
	// нет» чинятся разными действиями, и первый шаг у них разный. И проверка
	// стоит ДО дампа, а не после: мы на чужом проде, а копия — это гигабайты
	// на диск, который нам не принадлежит. Тот же агент меряет свободное
	// место каждый круг и сам шлёт письмо «когда место кончится, встанут и
	// аналитика, и сайт клиента»: доводить до собственного письма своей же
	// ночной задачей нельзя.
	CodeNoSpace = "backupNoSpace"
)

// Формат копии. Число, а не «версия агента»: читает архив приложение, и ему
// важно, как он устроен, а не кто его собрал.
const Format = 1

// Имена внутри архива. Дамп называется так же, как в ручной выгрузке
// восьмого спринта, и это не совпадение: восстановление у них одно на двоих,
// и различать источники ему нечем и незачем.
const (
	FileDump     = "umami.sql"
	FileManifest = "manifest.json"
)

// Минимальный размер дампа, ниже которого это не дамп.
//
// Пустая база Umami — это всё равно десятки килобайт схемы. Ноль байт и
// «-- PostgreSQL database dump\n» — два разных способа получить бэкап, из
// которого ничего не восстановится, и оба обязаны быть отказом.
const minDumpBytes = 1024

// Сколько места круг требует сверх ожидаемого размера копии.
//
// Полгигабайта — не про наш дамп, а про сервер клиента: диск, забитый до
// последних мегабайт, роняет не копию, а Postgres и сайт. Оставить этот
// запас нетронутым дешевле, чем объяснять, почему аналитика встала ночью.
const spaceReserveBytes = 512 << 20

// Во сколько раз место под круг больше самого дампа.
//
// Дамп и архив какое-то время лежат рядом: архив собирается из открытого
// дампа, и удаляется дамп только после проверки. Архив гораздо меньше — это
// gzip текста, — но считать надо по худшему: несжимаемый дамп даёт двойной
// размер.
const spaceFactor = 2

// Config — всё, что нужно кругу копии.
type Config struct {
	Enabled bool
	// Как часто делать копию и сколько их держать в бакете. Ноль в Keep —
	// не удалять ничего: правилами жизненного цикла бакета человек вправе
	// распорядиться сам.
	Interval time.Duration
	Keep     int
	// Потолок времени на весь круг: дамп, сборка, выгрузка.
	Timeout time.Duration

	// Куда складывать временные файлы. Том агента, а не /tmp контейнера:
	// дамп бывает в гигабайты.
	WorkDir string
	// Каталог конфигов агента (только чтение) и каталог стека на хосте.
	ConfigDir string
	StackDir  string

	// Как достучаться до базы. Пароль сюда приезжает из окружения, а в
	// аргументы pg_dump не попадает никогда: `/proc/<pid>/cmdline` читает
	// любой пользователь сервера.
	Postgres Postgres

	// Хранилище и префикс, под которым лежат копии этого сервера.
	Storage s3.Config
	Prefix  string

	// Сколько весил дамп прошлой удачной копии. Ноль — копий ещё не было.
	//
	// Единственная оценка размера, которая у агента есть: спросить у
	// Postgres `pg_database_size` нечем — SQL-драйвера в агенте нет и не
	// будет ради одного числа, а тома docker принадлежат root, и заглянуть
	// в них агент не может. Нуля хватает, чтобы потребовать хотя бы запас:
	// первый круг на диске с двумястами мегабайтами свободного места не
	// начнётся.
	LastDumpBytes int64
}

// Postgres — координаты базы клиента.
type Postgres struct {
	Host     string
	Port     int
	User     string
	Database string
	Password string
}

// Configured — есть ли чему работать. Выключенная функция и функция без
// бакета — одно и то же состояние для человека: копий нет.
func (c Config) Configured() bool {
	return c.Enabled &&
		strings.TrimSpace(c.Storage.Endpoint) != "" &&
		strings.TrimSpace(c.Storage.Bucket) != "" &&
		strings.TrimSpace(c.Storage.KeyID) != "" &&
		strings.TrimSpace(c.Postgres.Host) != ""
}

// Record — чем кончился круг. То же, что лежит в базе агента и уезжает
// экрану.
type Record struct {
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	OK         bool      `json:"ok"`
	// Код беды. Пусто — копия сделана.
	Code string `json:"code,omitempty"`
	// Техническая подробность для мелкого шрифта. Секретов здесь нет:
	// ошибки хранилища — это его коды, а не наши ключи.
	Detail string `json:"detail,omitempty"`
	// Ключ объекта в бакете.
	Key string `json:"key,omitempty"`
	// Размер архива и дампа внутри него.
	Bytes     int64 `json:"bytes,omitempty"`
	DumpBytes int64 `json:"dumpBytes,omitempty"`
	// Сколько старых копий убрала ротация.
	Removed int `json:"removed,omitempty"`
	// Сколько копий видно в бакете и когда сделана самая свежая из них.
	//
	// Спрошено у хранилища, а не взято из нашей памяти: правило жизненного
	// цикла бакета, стирающее объекты через неделю, — обычная настройка, и
	// запомненное «копия сделана вчера» её не заметит никогда. Свежесть в
	// своде считается по этому полю, пока оно есть.
	Stored   int       `json:"stored,omitempty"`
	NewestAt time.Time `json:"newestAt,omitempty"`
}

// Manifest — паспорт копии. Лежит внутри архива первым файлом.
type Manifest struct {
	Format    int    `json:"format"`
	CreatedAt string `json:"createdAt"`
	// Версия стека, который снял копию. Восстановление показывает её рядом
	// с нынешней: разрыв версий закрывает Umami своими миграциями, но знать
	// о нём человек обязан заранее.
	StackVersion string `json:"stackVersion,omitempty"`
	DumpBytes    int64  `json:"dumpBytes"`
	// Секретов в копии нет. Полем, а не молчанием: человек, открывший архив
	// через год, не должен искать `.env`, которого там не будет.
	Secrets bool `json:"secrets"`
	// Заголовки ключевых адресов вырезаны: в них токены закрытых разделов.
	HeadersStripped bool `json:"headersStripped"`
}

// Key собирает имя объекта в бакете.
//
// Двоеточий во времени нет намеренно: они законны в ключе S3, но превращают
// скачанный файл в непригодное имя на Windows — а качает копию человек с
// рабочей машины.
func Key(prefix string, at time.Time) string {
	name := at.UTC().Format("2006-01-02T15-04-05Z") + ".tar.gz"
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

// Run делает копию: дамп, архив, проверка, выгрузка, ротация.
//
// Ошибку не возвращает: круг обязан оставить запись о себе в любом исходе —
// «копия не сделалась и вот почему» это ровно то, ради чего функция и нужна.
func Run(ctx context.Context, cfg Config, now time.Time) Record {
	record := Record{StartedAt: now.UTC()}

	finish := func(code, detail string) Record {
		record.FinishedAt = time.Now().UTC()
		record.Code = code
		record.OK = code == ""
		record.Detail = detail
		return record
	}

	if !cfg.Configured() {
		return finish(CodeNotConfigured, "")
	}

	client, err := s3.New(cfg.Storage)
	if err != nil {
		return finish(CodeNotConfigured, err.Error())
	}

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	// Список копий — первым делом, до дампа. Он стоит один запрос и
	// отвечает сразу на два вопроса: не отвергнут ли ключ (тогда незачем
	// полчаса читать базу, чтобы узнать это в конце) и что хранилище
	// думает о наших прежних копиях.
	existing, err := client.List(ctx, listPrefix(cfg.Prefix), 0)
	if err != nil {
		if ctx.Err() != nil {
			return finish(CodeTimeout, "")
		}
		code, detail := Classify(err)
		return finish(code, detail)
	}
	record.Stored = len(existing)
	if len(existing) > 0 {
		record.NewestAt = existing[0].Modified
	}

	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return finish(CodeArchiveFailed, err.Error())
	}

	// Место проверяется ДО дампа, а не по факту неудачи. Мы на чужом проде:
	// заполнить диск клиента своей ночной задачей — это уронить его сайт, а
	// не потерять копию. Отказ с кодом честнее, чем сломанный сервер.
	if code, detail := checkSpace(cfg); code != "" {
		return finish(code, detail)
	}

	// Каталог убирается целиком и в любом исходе: в нём вся аналитика
	// клиента, и оставлять её на диске, который нам не принадлежит,
	// незачем.
	work, err := os.MkdirTemp(cfg.WorkDir, "backup-")
	if err != nil {
		return finish(CodeArchiveFailed, err.Error())
	}
	defer os.RemoveAll(work)

	dumpPath := filepath.Join(work, FileDump)
	if err := dump(ctx, cfg, dumpPath); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return finish(CodeToolMissing, err.Error())
		}
		if ctx.Err() != nil {
			return finish(CodeTimeout, "")
		}
		return finish(CodeDumpFailed, err.Error())
	}

	info, err := os.Stat(dumpPath)
	if err != nil {
		return finish(CodeDumpFailed, err.Error())
	}
	if info.Size() < minDumpBytes {
		return finish(CodeDumpEmpty, strconv.FormatInt(info.Size(), 10))
	}

	archivePath := filepath.Join(work, "copy.tar.gz")
	if err := pack(cfg, dumpPath, archivePath, info.Size(), now); err != nil {
		return finish(CodeArchiveFailed, err.Error())
	}

	// Проверяется тот самый файл, который поедет в бакет: открывается
	// заново, разжимается, и в нём ищется дамп. Заодно этим же проходом
	// считаются суммы — второй раз читать гигабайт с чужого диска незачем.
	checked, err := verify(archivePath)
	if err != nil {
		return finish(CodeArchiveBroken, err.Error())
	}
	record.Bytes = checked.Size
	record.DumpBytes = checked.DumpBytes

	// Дамп больше не нужен, а место на чужом диске нужно: до выгрузки
	// доживает только архив.
	_ = os.Remove(dumpPath)

	file, err := os.Open(archivePath)
	if err != nil {
		return finish(CodeArchiveBroken, err.Error())
	}
	defer file.Close()

	key := Key(cfg.Prefix, now)
	record.Key = key

	uploadTimeout := cfg.Timeout
	if uploadTimeout <= 0 {
		uploadTimeout = time.Hour
	}

	if err := client.Put(ctx, key, checked.Size, checked.SHA256, checked.MD5, file, uploadTimeout); err != nil {
		if ctx.Err() != nil {
			return finish(CodeTimeout, "")
		}
		code, detail := Classify(err)
		return finish(code, detail)
	}

	// Хранилище сказало «принял» — спрашиваем его же, лежит ли объект.
	// Стоит один запрос, а ловит и молчаливое переполнение квоты, и
	// правило, стирающее объект в момент записи.
	object, err := client.Head(ctx, key)
	if err != nil {
		code, detail := Classify(err)
		if code == CodeUploadFailed || code == CodeBucketMissing {
			code = CodeMissingAfterUpload
		}
		return finish(code, detail)
	}
	if object.Size != checked.Size {
		return finish(CodeMissingAfterUpload,
			fmt.Sprintf("%d != %d", object.Size, checked.Size))
	}

	// Копия легла — значит в бакете стало на одну больше, и самая свежая
	// теперь она. Это тот же вопрос, на который отвечал список выше, и
	// второй раз спрашивать его незачем.
	record.Stored++
	record.NewestAt = now.UTC()

	// Ротация — единственное, что мы удаляем в чужом бакете, и только под
	// своим префиксом. Её неудача копию не отменяет: копия уже лежит.
	if cfg.Keep > 0 {
		removed, err := rotate(ctx, client, cfg.Prefix, cfg.Keep, append([]s3.Object{{
			Key:      key,
			Size:     checked.Size,
			Modified: now.UTC(),
		}}, existing...))
		record.Removed = removed
		record.Stored -= removed
		if err != nil {
			record.Detail = err.Error()
		}
	}

	return finish("", record.Detail)
}

// checkSpace отвечает, хватит ли места на дамп и архив.
//
// Пустой код означает «хватит либо не проверить». Не проверить — это
// нормальный ответ: файловую систему может не удаться опросить, и отказывать
// в копии из-за этого нельзя — свободного места скорее всего вдоволь, а
// молчащая копия хуже неточной проверки.
//
// Оценка берётся у прошлой удачной копии, потому что другой у агента нет:
// SQL-драйвера ради `pg_database_size` в нём не заведётся, а тома docker
// принадлежат root. Прошлого дампа нет — требуем один запас: этого хватает,
// чтобы не начать гигабайтный дамп на диске, где осталось двести мегабайт.
func checkSpace(cfg Config) (string, string) {
	free, err := freeBytes(cfg.WorkDir)
	if err != nil {
		return "", ""
	}

	needed := uint64(spaceReserveBytes)
	if cfg.LastDumpBytes > 0 {
		needed += uint64(cfg.LastDumpBytes) * spaceFactor
	}
	if free >= needed {
		return "", ""
	}

	// Подробность — числами: письмо и экран скажут это словами и на своём
	// языке, а сверять с `df` человек будет мегабайты.
	return CodeNoSpace, fmt.Sprintf("free %d MB, need %d MB",
		free/(1024*1024), needed/(1024*1024))
}

// dump снимает дамп базы.
//
// Пароль уезжает окружением дочернего процесса, а не аргументом: строка
// `pg_dump -d postgres://umami:пароль@…` видна в `/proc/<pid>/cmdline`
// любому пользователю сервера, а окружение процесса читает только владелец.
func dump(ctx context.Context, cfg Config, path string) error {
	tool, err := exec.LookPath("pg_dump")
	if err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	port := cfg.Postgres.Port
	if port == 0 {
		port = 5432
	}

	command := exec.CommandContext(ctx, tool,
		"--host", cfg.Postgres.Host,
		"--port", strconv.Itoa(port),
		"--username", cfg.Postgres.User,
		"--dbname", cfg.Postgres.Database,
		// Владельцев и права не переносим: восстановление идёт в свежий
		// стек, где роли называются так же, но принадлежность объектов
		// чужой базе — лишний повод для отказа на ровном месте.
		"--no-owner", "--no-privileges",
		"--format", "plain",
	)
	command.Env = append(os.Environ(), "PGPASSWORD="+cfg.Postgres.Password)
	command.Stdout = file

	var complaints bytes.Buffer
	command.Stderr = &complaints

	if err := command.Run(); err != nil {
		// Текст ошибки pg_dump пароля не содержит — он его не знает.
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(truncate(complaints.String(), 400)))
	}

	return file.Sync()
}

// pack собирает архив: дамп, паспорт и конфиги, которые агенту читаемы.
func pack(cfg Config, dumpPath, archivePath string, dumpBytes int64, now time.Time) error {
	out, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	zip := gzip.NewWriter(out)
	archive := tar.NewWriter(zip)

	manifest := Manifest{
		Format:          Format,
		CreatedAt:       now.UTC().Format(time.RFC3339),
		StackVersion:    stackVersion(cfg.StackDir),
		DumpBytes:       dumpBytes,
		Secrets:         false,
		HeadersStripped: true,
	}
	passport, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeEntry(archive, FileManifest, passport, now); err != nil {
		return err
	}

	if err := copyEntry(archive, FileDump, dumpPath, dumpBytes, now); err != nil {
		return err
	}

	// Конфиги стека: не секреты (их читает кто угодно на сервере), но без
	// них человек, восстанавливающий копию руками, остаётся ни с чем.
	for _, name := range []string{"stack-version.json", "docker-compose.yml", "Caddyfile"} {
		if err := addOptional(archive, name, filepath.Join(cfg.StackDir, name), now, nil); err != nil {
			return err
		}
	}

	// Конфиги агента. `probes.json` едет без заголовков: в них токен
	// закрытого раздела, а копия лежит в бакете открытой.
	for _, name := range []string{"sites.json", "projects.json", "vitals.json", "logs.json"} {
		path := filepath.Join(cfg.ConfigDir, name)
		if err := addOptional(archive, "agent-config/"+name, path, now, nil); err != nil {
			return err
		}
	}
	if err := addOptional(archive, "agent-config/probes.json",
		filepath.Join(cfg.ConfigDir, "probes.json"), now, stripHeaders); err != nil {
		return err
	}

	if err := archive.Close(); err != nil {
		return err
	}
	if err := zip.Close(); err != nil {
		return err
	}
	return out.Sync()
}

// addOptional кладёт файл, если он читается. Нечитаемый — не беда: секреты
// стека агенту и не должны быть доступны, а конфига может не быть вовсе.
func addOptional(
	archive *tar.Writer,
	name, path string,
	now time.Time,
	transform func([]byte) []byte,
) error {
	// Потолок на всякий случай: конфиги агента — это килобайты, и класть в
	// копию файл, который вырос до гигабайта, мы не станем.
	body, err := readLimited(path, 4<<20)
	if err != nil {
		return nil
	}
	if transform != nil {
		body = transform(body)
	}
	return writeEntry(archive, name, body, now)
}

// stripHeaders вырезает заголовки ключевых адресов.
//
// Разбором, а не поиском строки: профиль вправе нести любой заголовок, и
// «вырезать всё, что похоже на токен» — это способ однажды не вырезать
// ничего. Не разобралось — не кладём вовсе: непонятный файл с возможным
// секретом в открытой копии хуже отсутствующего.
func stripHeaders(raw []byte) []byte {
	var profiles []probe.Profile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return []byte("[]")
	}
	for i := range profiles {
		profiles[i].Headers = nil
	}
	cleaned, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return []byte("[]")
	}
	return cleaned
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

func writeEntry(archive *tar.Writer, name string, body []byte, now time.Time) error {
	header := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(body)),
		ModTime: now.UTC(),
		Format:  tar.FormatPAX,
	}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	_, err := archive.Write(body)
	return err
}

// copyEntry кладёт файл потоком: дамп в память не помещается и не должен.
func copyEntry(archive *tar.Writer, name, path string, size int64, now time.Time) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	header := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    size,
		ModTime: now.UTC(),
		Format:  tar.FormatPAX,
	}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}

	written, err := io.Copy(archive, file)
	if err != nil {
		return err
	}
	// Размер в заголовке tar обязан совпасть с числом записанных байт:
	// разойдись они, архив открылся бы, а дамп внутри оказался бы обрезан.
	if written != size {
		return fmt.Errorf("дамп изменился при записи: %d вместо %d", written, size)
	}
	return nil
}

// Checked — что мы узнали, открыв собранный архив обратно.
type Checked struct {
	Size      int64
	DumpBytes int64
	SHA256    string
	MD5       string
}

// verify открывает архив заново и проверяет, что в нём то, что обещано.
//
// «Не пустая и распаковывается» проверяется на том самом файле, который
// поедет в бакет, а не на потоке, из которого он собран: ошибка записи на
// диск живёт ровно между этими двумя.
func verify(path string) (Checked, error) {
	file, err := os.Open(path)
	if err != nil {
		return Checked{}, err
	}
	defer file.Close()

	sum := sha256.New()
	sumMD5 := md5.New()
	counter := &counting{}

	// Один проход: подпись, сумма для хранилища, размер и разбор
	// содержимого — всё с одного чтения.
	reader := io.TeeReader(file, io.MultiWriter(sum, sumMD5, counter))

	zip, err := gzip.NewReader(reader)
	if err != nil {
		return Checked{}, fmt.Errorf("архив не разжимается: %w", err)
	}
	defer zip.Close()

	archive := tar.NewReader(zip)

	var dumpBytes int64
	found := false
	headerSeen := false

	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Checked{}, fmt.Errorf("архив не читается: %w", err)
		}
		if entry.Name != FileDump {
			// Остальные записи дочитываем, чтобы дойти до конца потока: без
			// этого gzip не досчитает свою сумму, и битый хвост остался бы
			// незамеченным.
			if _, err := io.Copy(io.Discard, archive); err != nil {
				return Checked{}, err
			}
			continue
		}

		found = true
		start := make([]byte, 64)
		read, err := io.ReadFull(archive, start)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return Checked{}, err
		}
		// Заголовок дампа. Пустой файл и обрезанный на первой строке — два
		// разных способа получить копию, из которой ничего не поднимется.
		headerSeen = bytes.Contains(start[:read], []byte("PostgreSQL database dump"))

		rest, err := io.Copy(io.Discard, archive)
		if err != nil {
			return Checked{}, err
		}
		dumpBytes = int64(read) + rest
	}

	// Хвост после последней записи tar (нули выравнивания) тоже участвует в
	// сумме: считаем её по всему файлу до самого конца.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return Checked{}, err
	}

	if !found {
		return Checked{}, errors.New("в архиве нет дампа")
	}
	if !headerSeen {
		return Checked{}, errors.New("дамп не похож на дамп PostgreSQL")
	}
	if dumpBytes < minDumpBytes {
		return Checked{}, fmt.Errorf("дамп в архиве пуст: %d байт", dumpBytes)
	}

	return Checked{
		Size:      counter.bytes,
		DumpBytes: dumpBytes,
		SHA256:    hex.EncodeToString(sum.Sum(nil)),
		MD5:       base64.StdEncoding.EncodeToString(sumMD5.Sum(nil)),
	}, nil
}

// counting считает прочитанные байты: размер файла берём тем же проходом,
// что и суммы, а не отдельным Stat — файл между ними мог бы измениться.
type counting struct{ bytes int64 }

func (c *counting) Write(chunk []byte) (int, error) {
	c.bytes += int64(len(chunk))
	return len(chunk), nil
}

// rotate убирает старые копии, оставляя `keep` самых свежих.
//
// Список приходит готовым: его уже спрашивали до дампа, и второй раз
// спрашивать то же самое незачем — а главное, два списка расходятся ровно
// тогда, когда по бакету кто-то ходит руками.
func rotate(ctx context.Context, client *s3.Client, prefix string, keep int, objects []s3.Object) (int, error) {
	// Ключи начинаются со времени, поэтому обратный порядок по имени —
	// это порядок от новых к старым. Сортировка по дате изменения врала бы
	// после ручной перезаливки объекта.
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key > objects[j].Key })

	removed := 0
	for index, object := range objects {
		if index < keep {
			continue
		}
		if err := client.Delete(ctx, object.Key); err != nil {
			return removed, err
		}
		removed++
	}

	return removed, nil
}

// listPrefix — префикс со слэшем на конце.
//
// Слэш не украшение: без него префикс `vpsfocus/srv-1` захватил бы и
// `vpsfocus/srv-10`, а ротация удалила бы копии чужого сервера.
func listPrefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

// Objects — что лежит в бакете под префиксом.
//
// Спрашивается у хранилища, а не у нашей памяти: правило жизненного цикла
// бакета, стирающее объекты через неделю, — обычная настройка, и запомненное
// «копия сделана вчера» её не заметит никогда.
func Objects(ctx context.Context, cfg Config, prefix string, limit int) ([]s3.Object, error) {
	client, err := s3.New(cfg.Storage)
	if err != nil {
		return nil, err
	}
	return client.List(ctx, listPrefix(prefix), limit)
}

// Link выдаёт подписанную ссылку на копию.
//
// Ею живёт восстановление: приложение с хранилищем не разговаривает вовсе —
// второй клиент S3, на Rust, был бы второй реализацией одного протокола.
// Сервер клиента качает копию сам, обычным curl.
func Link(cfg Config, key string, ttl time.Duration) (string, error) {
	client, err := s3.New(cfg.Storage)
	if err != nil {
		return "", err
	}
	return client.Presign(key, ttl)
}

// Classify переводит отказ хранилища в наш код.
//
// «Ключ не подошёл» и «бакета нет» чинятся разными действиями, и первый шаг
// человека зависит от того, что мы ему скажем.
//
// Наружу открыта затем, что спрашивают хранилище двое: круг копии и список
// копий на экране. Два перевода одного отказа однажды разошлись бы, и
// разошёлся бы тот, про который забыли.
func Classify(err error) (string, string) {
	var failure *s3.Error
	if !errors.As(err, &failure) {
		return CodeStorageUnreachable, truncate(err.Error(), 300)
	}

	detail := failure.Code
	if detail == "" {
		detail = strconv.Itoa(failure.Status)
	}

	switch {
	case failure.Status == 403,
		failure.Code == "AccessDenied",
		failure.Code == "SignatureDoesNotMatch",
		failure.Code == "InvalidAccessKeyId":
		return CodeAccessDenied, detail
	case failure.Code == "NoSuchBucket",
		failure.Status == 404 && failure.Code == "":
		return CodeBucketMissing, detail
	default:
		return CodeUploadFailed, detail
	}
}

// stackVersion читает версию стека рядом с копией.
func stackVersion(dir string) string {
	raw, err := readLimited(filepath.Join(dir, "stack-version.json"), 64<<10)
	if err != nil {
		return ""
	}
	var manifest struct {
		StackVersion string `json:"stackVersion"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ""
	}
	return manifest.StackVersion
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
