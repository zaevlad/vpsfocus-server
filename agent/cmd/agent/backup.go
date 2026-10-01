package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"agent/internal/backup"
	"agent/internal/checks"
	"agent/internal/events"
	"agent/internal/health"
	"agent/internal/s3"
)

// Сколько записей о кругах показываем экрану. Больше человек не прочитает, а
// вопрос у него один: «когда была последняя копия и почему её нет».
const backupRunsShown = 20

// Сколько объектов спрашиваем у хранилища за раз.
//
// Потолок выборки, а не число копий: при `BACKUP_KEEP=0` — законный выбор
// того, кто распоряжается жизненным циклом бакета сам, — копий через двести
// суток станет больше. Поэтому вместе со списком уезжает признак того, что
// он упёрся в потолок: подпись «в хранилище N копий», замершая на двухстах,
// обещает счёт, а показывает предел выборки.
const backupObjectsShown = 200

// Сколько живёт подписанная ссылка на копию.
//
// Пятнадцать минут: в ссылке подпись, дающая доступ к аналитике клиента, и
// живёт она ровно столько, сколько нужно серверу, чтобы скачать файл.
const backupLinkTTL = 15 * time.Minute

// backupLoop раз в сутки кладёт копию базы в хранилище клиента.
func (a *Agent) backupLoop(ctx context.Context) {
	if !a.cfg.Backup.Configured() {
		log.Print("копии базы выключены: хранилище не настроено")
		return
	}

	if !wait(ctx, startDelay(taskBackup)) {
		return
	}
	a.startBackup()

	ticker := time.NewTicker(a.cfg.Backup.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.startBackup()
		}
	}
}

// startBackup запускает круг копии в фоне.
//
// В фоне и с флагом — по той же причине, что и обход сайта: круг читает базу
// целиком и льёт мегабайты в сеть, это минуты. Второй запуск поверх первого
// означал бы два pg_dump на одной базе и вдвое больше трафика клиента.
func (a *Agent) startBackup() bool {
	if !a.backupRunning.CompareAndSwap(false, true) {
		return false
	}

	a.background.Add(1)
	go func() {
		defer a.background.Done()
		defer a.backupRunning.Store(false)

		a.runBackup(a.rootCtx)
	}()
	return true
}

// runBackup делает копию и записывает, чем это кончилось.
//
// Запись обязательна в любом исходе: «копия не сделалась и вот почему» — это
// ровно то, ради чего автокопия и заводится. Круг, который молчит о своей
// неудаче, неотличим от выключенного.
func (a *Agent) runBackup(ctx context.Context) backup.Record {
	cfg := a.cfg.Backup

	// Размер прошлого удачного дампа — единственная оценка, по которой круг
	// решает, хватит ли места на диске клиента. Не прочиталась — идём как
	// прежде: молчащая копия хуже неточной проверки.
	if _, success, _, hasSuccess, err := a.db.LastBackup(ctx); err != nil {
		log.Printf("прошлая копия: %v", err)
	} else if hasSuccess {
		cfg.LastDumpBytes = success.DumpBytes
	}

	record := backup.Run(ctx, cfg, time.Now())

	if err := a.saving(func(ctx context.Context) error {
		return a.db.SaveBackupRun(ctx, record)
	}); err != nil {
		log.Printf("запись итога копии: %v", err)
	}

	if record.OK {
		log.Printf("копия базы: %s, %d байт", record.Key, record.Bytes)
		a.note(events.KindBackupDone, "", "")
	} else {
		log.Printf("копия базы не сделалась: %s %s", record.Code, record.Detail)
		a.note(events.KindBackupFailed, "", record.Code)
	}

	// Письмо уходит на переломе, как и у всех остальных тревог: «копия не
	// сделалась» каждую ночь при сломанном ключе — это письма, которые
	// перестают читать вместе со всеми остальными.
	if err := a.saving(func(ctx context.Context) error {
		a.notifyBackup(ctx, record)
		return nil
	}); err != nil {
		log.Printf("уведомление о копии: %v", err)
	}

	return record
}

// notifyBackup зовёт человека, когда копия не сделалась.
//
// Ступеней здесь нет: беда не приближается со временем, она либо есть, либо
// нет, — а повтор идёт по общему правилу, раз в сутки, пока не починят.
func (a *Agent) notifyBackup(ctx context.Context, record backup.Record) {
	alert := checks.Alert{
		Kind:    checks.KindBackup,
		Target:  "database",
		Subject: a.words().BackupSubject,
		Body:    fmt.Sprintf(a.words().BackupBody, record.Code),
	}

	if !record.OK {
		a.dispatch(ctx, []checks.Alert{alert}, nil)
		return
	}

	a.dispatch(ctx, nil, []checks.Alert{{
		Kind:    checks.KindBackup,
		Target:  "database",
		Subject: a.words().BackupOKSubject,
		Body:    fmt.Sprintf(a.words().BackupOKBody, record.Key, record.Bytes),
	}})
}

// backupState — то, что свод знает о копиях.
//
// Свежесть берётся у хранилища, если оно отвечало на последнем круге:
// правило жизненного цикла бакета, стирающее объекты через неделю, — обычная
// настройка, и наша запись «копия сделана вчера» её не заметит никогда.
func (a *Agent) backupState(ctx context.Context) health.BackupState {
	state := health.BackupState{
		Configured: a.cfg.Backup.Configured(),
		Interval:   a.cfg.Backup.Interval,
	}
	if !state.Configured {
		return state
	}

	last, success, hasLast, hasSuccess, err := a.db.LastBackup(ctx)
	if err != nil {
		log.Printf("свод: копии базы: %v", err)
		return state
	}

	state.Attempted = hasLast
	if hasLast {
		state.LastOK = last.OK
		state.LastCode = last.Code
	}
	if hasSuccess {
		state.Freshest = success.StartedAt
	}
	// Хранилище важнее нашей памяти: оно знает и о копиях, стёртых мимо нас.
	if hasLast && !last.NewestAt.IsZero() {
		state.Freshest = last.NewestAt
	}

	return state
}

// ── То, что видит приложение ─────────────────────────────────────────────

// StoredCopy — копия, лежащая в хранилище.
type StoredCopy struct {
	Key      string `json:"key"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified"`
}

// BackupsState — экран копий целиком.
type BackupsState struct {
	// Настроено ли хранилище. Выключенная функция и функция без бакета —
	// одно состояние для человека: копий нет.
	Configured    bool `json:"configured"`
	Enabled       bool `json:"enabled"`
	IntervalHours int  `json:"intervalHours"`
	Keep          int  `json:"keep"`
	// Куда уезжают копии. Секретов здесь нет: адрес, бакет и префикс — это
	// первое, что спрашивают при разборе «а где мои копии».
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	// Копия едет в бакет открытым каналом (`http://`). Не отказ: MinIO в
	// своей сети законен. Но копия не шифруется, и допустимо это ровно
	// потому, что канал защищён, — значит сказать об этом надо вслух.
	Insecure bool `json:"insecure,omitempty"`
	// Идёт ли круг прямо сейчас.
	Running bool `json:"running"`
	// История кругов, от новых к старым.
	Runs []backup.Record `json:"runs"`
	// Что лежит в хранилище сейчас. Спрошено у него, а не собрано из
	// истории: копии переживают нашу базу, а наша база — копии.
	Objects []StoredCopy `json:"objects"`
	// Код беды, если спросить не удалось. Пустой список объектов при
	// неудаче — это молчаливый ноль, а он хуже отказа.
	ObjectsError string `json:"objectsError,omitempty"`
	// Список упёрся в потолок выборки: копий в бакете **не меньше** этого,
	// а сколько именно — мы не спрашивали. Экран говорит то же самое.
	ObjectsTruncated bool `json:"objectsTruncated,omitempty"`
}

// Backups отдаёт состояние копий и то, что лежит в бакете.
func (a *Agent) Backups(prefix string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	cfg := a.cfg.Backup
	state := BackupsState{
		Configured:    cfg.Configured(),
		Enabled:       cfg.Enabled,
		IntervalHours: int(cfg.Interval / time.Hour),
		Keep:          cfg.Keep,
		Bucket:        cfg.Storage.Bucket,
		Prefix:        cfg.Prefix,
		Running:       a.backupRunning.Load(),
		Runs:          []backup.Record{},
		Objects:       []StoredCopy{},
	}

	runs, err := a.db.BackupRuns(ctx, backupRunsShown)
	if err != nil {
		return nil, err
	}
	state.Runs = runs

	if !cfg.Configured() {
		return state, nil
	}

	if client, err := s3.New(cfg.Storage); err == nil {
		state.Endpoint = client.Endpoint()
		state.Insecure = client.Insecure()
	}

	// Префикс чужого сервера — законный запрос: восстановление на новый VPS
	// начинается с того, что человек ищет копии старого. Бакет один на
	// агентство, ключи в нём наши, и прятать их друг от друга незачем.
	if strings.TrimSpace(prefix) == "" {
		prefix = cfg.Prefix
	}

	objects, err := backup.Objects(ctx, cfg, prefix, backupObjectsShown)
	if err != nil {
		state.ObjectsError, _ = backup.Classify(err)
		return state, nil
	}
	state.ObjectsTruncated = len(objects) >= backupObjectsShown

	for _, object := range objects {
		state.Objects = append(state.Objects, StoredCopy{
			Key:      object.Key,
			Bytes:    object.Size,
			Modified: object.Modified.UTC().Format(time.RFC3339),
		})
	}

	return state, nil
}

// RunBackup делает копию по кнопке и отвечает сразу.
//
// Сразу — потому что круг идёт минутами: дамп базы, сборка архива, выгрузка
// по каналу VPS. Ответ «начали» и опрос состояния — тот же приём, что у
// обхода сайта и замеров.
func (a *Agent) RunBackup() (any, error) {
	if !a.cfg.Backup.Configured() {
		return nil, errors.New(backup.CodeNotConfigured)
	}
	return map[string]bool{"started": a.startBackup()}, nil
}

// BackupLink выдаёт подписанную ссылку на копию.
//
// Ею живёт восстановление: приложение с хранилищем не разговаривает вовсе, а
// сервер клиента качает копию сам. Ключ проверяется на вид — подписывать
// произвольный объект чужого бакета мы не станем.
func (a *Agent) BackupLink(key string) (any, error) {
	key = strings.TrimSpace(key)
	if key == "" || !strings.HasSuffix(key, ".tar.gz") || strings.Contains(key, "..") {
		return nil, errors.New("ключ копии не похож на ключ копии")
	}
	if !a.cfg.Backup.Configured() {
		return nil, errors.New(backup.CodeNotConfigured)
	}

	link, err := backup.Link(a.cfg.Backup, key, backupLinkTTL)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"url": link,
		// Секунды, а не время: часы сервера уезжают, а «сколько ещё
		// действует» одинаково читается по обе стороны туннеля.
		"expiresIn": int(backupLinkTTL.Seconds()),
	}, nil
}
