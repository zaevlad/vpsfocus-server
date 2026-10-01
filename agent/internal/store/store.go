// Package store хранит замеры и состояние оповещений в SQLite.
//
// SQLite, а не файл с числами: запросы по диапазонам и ротация здесь — три
// строки SQL, а таблиц с тех пор стало вдвое больше — ссылки, замеры
// производительности, счётчики из логов, снимки страниц. Драйвер чистый на Go
// (`modernc.org/sqlite`), потому что агент собирается статически и
// мультиархитектурно — cgo это ломает.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"agent/internal/backup"
	"agent/internal/checks"
	"agent/internal/events"
	"agent/internal/links"
	"agent/internal/logs"
	"agent/internal/metrics"
	"agent/internal/probe"
	"agent/internal/seo"
	"agent/internal/vitals"
)

type Store struct {
	db *sql.DB
}

// Верхняя граница «без верхней границы».
//
// Год выбран заведомо недостижимый, но не `math.MaxInt64`: время в базе
// лежит секундами эпохи, и переполнение при арифметике над ним — та ошибка,
// которую ищут потом полдня.
var forever = time.Date(2999, time.January, 1, 0, 0, 0, 0, time.UTC)

// Схема применяется при каждом старте: миграций у агента нет, а `if not
// exists` дешевле их механизма, пока таблиц три.
const schema = `
create table if not exists samples (
    at              integer primary key,
    cpu_percent     real    not null,
    mem_total_mb    integer not null,
    mem_available_mb integer not null,
    disk_total_mb   integer not null,
    disk_free_mb    integer not null,
    load1           real    not null,
    uptime_seconds  integer not null
);

create table if not exists checks (
    kind       text not null,
    target     text not null,
    payload    text not null,
    checked_at integer not null,
    primary key (kind, target)
);

create table if not exists alerts (
    kind        text    not null,
    target      text    not null,
    firing      integer not null,
    last_sent   integer not null,
    primary key (kind, target)
);

-- Докуда досчитано в ступенчатых напоминаниях.
--
-- Отдельной таблицей, а не колонкой в alerts: миграций у агента нет по
-- решению, и "create table if not exists" обновляет базу, которая уже
-- работает на сервере клиента, а "alter table" — нет. Заводить механизм
-- миграций ради одного числа дороже, чем завести таблицу.
--
-- (Обратных кавычек в этих комментариях быть не может: схема лежит в
-- сыром строковом литерале Go, и первая же из них его оборвёт.)
--
-- Хранится ближайшая пройденная ступень: «о тридцати днях уже написали».
-- Строка исчезает, когда домен продлили, — и следующий год начинается с
-- чистого счёта, а не с молчания до самой последней ступени.
create table if not exists alert_steps (
    kind   text    not null,
    target text    not null,
    step   integer not null,
    primary key (kind, target)
);

create table if not exists link_reports (
    id           integer primary key autoincrement,
    domain       text    not null,
    started_at   integer not null,
    duration_s   integer not null,
    pages        integer not null,
    total_links  integer not null,
    broken       integer not null,
    run_error    text    not null default ''
);

create index if not exists idx_link_reports_domain
    on link_reports(domain, started_at);

create table if not exists link_failures (
    report_id integer not null,
    url       text    not null,
    kind      text    not null,
    code      integer,
    detail    text    not null default '',
    source    text    not null
);

create index if not exists idx_link_failures_report
    on link_failures(report_id);

create table if not exists vitals_reports (
    id          integer primary key autoincrement,
    domain      text    not null,
    path        text    not null,
    strategy    text    not null,
    started_at  integer not null,
    payload     text    not null
);

create index if not exists idx_vitals_reports_page
    on vitals_reports(domain, path, strategy, started_at);

create table if not exists vitals_quota (
    day  text    primary key,
    used integer not null
);

create table if not exists log_positions (
    path      text primary key,
    inode     integer not null,
    size      integer not null,
    offset_at integer not null,
    read_at   integer not null,
    run_error text    not null default ''
);

create table if not exists log_events (
    bucket    integer not null,
    source    text    not null,
    kind      text    not null,
    status    integer not null,
    level     text    not null default '',
    host      text    not null default '',
    method    text    not null default '',
    path      text    not null default '',
    signature text    not null default '',
    sample    text    not null default '',
    count     integer not null,
    first_at  integer not null,
    last_at   integer not null,
    primary key (bucket, source, kind, status, level, host, method, path, signature)
);

create index if not exists idx_log_events_bucket on log_events(bucket);

-- Роботы по логам (трек V, экран «Агенты и ИИ»): сутки, сайт, имя из
-- закрытого списка — и число. Ни строки User-agent, ни адреса посетителя.
create table if not exists robot_visits (
    day     integer not null,
    host    text    not null default '',
    name    text    not null,
    count   integer not null,
    last_at integer not null,
    primary key (day, host, name)
);

create table if not exists seo_scans (
    id          integer primary key autoincrement,
    domain      text    not null,
    started_at  integer not null,
    duration_s  integer not null,
    stats       text    not null,
    run_error   text    not null default ''
);

create index if not exists idx_seo_scans_domain
    on seo_scans(domain, started_at);

create table if not exists seo_pages (
    scan_id integer not null,
    url     text    not null,
    status  integer not null,
    depth   integer not null,
    hash    text    not null default '',
    worst   text    not null default '',
    codes   text    not null default '',
    payload text    not null
);

create index if not exists idx_seo_pages_scan
    on seo_pages(scan_id, depth, url);

create index if not exists idx_seo_pages_worst
    on seo_pages(scan_id, worst);

-- Приглушённые находки: «знаю, так и задумано».
--
-- Без них свод превращается в спам за нашей подписью: проект с находкой,
-- которую чинить не собираются, светится вечно, и еженедельный отчёт
-- присылает её пятьдесят раз в год. Та же беда, из-за которой письмо о
-- чёрных списках уходит только на переходе состояния.
--
-- Ключ — повод и цель: приглушают «этот сертификат», а не «сертификаты
-- вообще». Пустая цель означает повод целиком, без привязки к домену.
create table if not exists suppressions (
    code       text    not null,
    target     text    not null,
    -- До какого времени приглушено. Ноль — бессрочно.
    until      integer not null default 0,
    note       text    not null default '',
    created_at integer not null,
    primary key (code, target)
);

-- Ключевые адреса проектов: чем кончилась последняя проверка.
--
-- Хранится только последний исход, а не история: круг ходит каждые
-- несколько минут, и история превратилась бы в триста строк в сутки на
-- адрес — на диске, который нам не принадлежит. То, ради чего нужна
-- история, лежит в журнале: переход «перестал отвечать» и «вернулся»
-- записывается туда отметкой, и лента показывает его вместе с остальным.
--
-- Ключ набором полей, а не склейкой строк: разделитель в ключе — это лишний
-- вопрос «а если он встретится в адресе», на который не хочется отвечать.
--
-- В ключе четыре поля, потому что столько же их в личности проверки. Пока
-- ключом были проект и адрес, две проверки одного адреса — «главная отвечает
-- 200» и «на главной есть слово Каталог», осмысленная пара — затирали исход
-- друг друга каждый круг: сравнение свежего результата с чужим прошлым
-- объявляло перелом на ровном месте, то есть слало письмо о беде, которой
-- нет, и молчало о настоящей. Источник правды (probes.json) разрешает
-- несколько профилей на адрес, и хранилище обязано уметь их представить.
--
-- Имя в ключе намеренно: им человек различает две проверки одного вида на
-- одном адресе («есть слово Каталог» и «есть слово Корзина»), его же
-- показывает экран и называет письмо. Цена — переименование начинает
-- историю проверки заново, и первый круг после него о переломе молчит; это
-- дешевле, чем два исхода под одним ключом.
--
-- Новой таблицей, а не переделкой старой: миграций у агента нет по решению,
-- и "create table if not exists" обновляет живую базу на сервере клиента, а
-- "alter table" — нет. Та же линия, что у alert_steps. Прежняя таблица
-- убирается: в ней лежал только последний исход, и он набирается заново за
-- один круг.
drop table if exists probe_results;

create table if not exists probe_outcomes (
    project_id text    not null,
    url        text    not null,
    kind       text    not null,
    name       text    not null,
    payload    text    not null,
    checked_at integer not null,
    primary key (project_id, url, kind, name)
);

-- Журнал сервера: что здесь менялось.
--
-- Половина ответа на вопрос «почему сайт лежал» — это «что при этом
-- происходило». Замеры показывают, как было плохо, но не говорят, что за
-- минуту до этого перезапустился агент или перезагрузился сам сервер.
--
-- Строк здесь мало и они редкие: в журнал попадают перемены, а не пульс.
-- Проход по логам раз в минуту сюда не пишется намеренно — лента, в
-- которой полторы тысячи строк в сутки, отвечает на вопрос «что менялось»
-- ничем.
create table if not exists events (
    id     integer primary key autoincrement,
    at     integer not null,
    kind   text    not null,
    target text    not null default '',
    detail text    not null default ''
);

create index if not exists idx_events_at on events(at);

-- Окно обслуживания: человек сам сказал, что сейчас чинит.
--
-- Строка одна: обслуживают сервер целиком, а не отдельный проект на нём —
-- обновление стека касается всех. Отсюда и единственный ключ.
-- Копии базы: чем кончился каждый круг.
--
-- История, а не последний исход: «когда была последняя удачная копия» и
-- «сколько раз подряд не получилось» — разные вопросы, и второй задают
-- ровно тогда, когда первый ответил плохо. Строк здесь одна в сутки,
-- ротация общая со всем остальным.
create table if not exists backup_runs (
    id          integer primary key autoincrement,
    started_at  integer not null,
    finished_at integer not null,
    ok          integer not null,
    code        text    not null default '',
    detail      text    not null default '',
    key         text    not null default '',
    bytes       integer not null default 0,
    dump_bytes  integer not null default 0,
    removed     integer not null default 0,
    -- Что о наших копиях думает само хранилище: сколько их там лежит и
    -- когда сделана самая свежая. Свежесть в своде считается по этому
    -- полю, а не по нашей записи об успехе.
    stored      integer not null default 0,
    newest_at   integer not null default 0
);

create index if not exists idx_backup_runs_started on backup_runs(started_at);

create table if not exists maintenance (
    id      integer primary key check (id = 1),
    until   integer not null,
    note    text    not null default '',
    started integer not null
);
`

func Open(path string) (*Store, error) {
	// Журнал в WAL: агент пишет замер раз в минуту и одновременно отвечает
	// на запросы приложения, и без WAL они бы ждали друг друга.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("база агента: %w", err)
	}

	// Одно соединение: SQLite всё равно сериализует запись, а пул только
	// плодит блокировки.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("схема базы агента: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ── Замеры ───────────────────────────────────────────────────────────────

func (s *Store) SaveSample(ctx context.Context, sample metrics.Sample) error {
	_, err := s.db.ExecContext(ctx,
		`insert or replace into samples
		 (at, cpu_percent, mem_total_mb, mem_available_mb, disk_total_mb, disk_free_mb, load1, uptime_seconds)
		 values (?, ?, ?, ?, ?, ?, ?, ?)`,
		sample.At.Unix(), sample.CPUPercent, sample.MemTotalMB, sample.MemAvailableMB,
		sample.DiskTotalMB, sample.DiskFreeMB, sample.Load1, sample.UptimeSeconds,
	)
	return err
}

// Samples отдаёт замеры с указанного момента и до сейчас, от старых к новым.
func (s *Store) Samples(ctx context.Context, since time.Time) ([]metrics.Sample, error) {
	return s.SamplesBetween(ctx, since, forever)
}

// SamplesBetween отдаёт замеры окна, от старых к новым.
//
// Отдельным именем, а не вторым запросом: разбор аварии спрашивает окно
// вокруг часа, экран здоровья — хвост от «столько-то часов назад». SQL при
// этом один: два похожих запроса однажды разойдутся границей включения, и
// разойдётся тот, про который забыли.
func (s *Store) SamplesBetween(ctx context.Context, from, to time.Time) ([]metrics.Sample, error) {
	rows, err := s.db.QueryContext(ctx,
		`select at, cpu_percent, mem_total_mb, mem_available_mb,
		        disk_total_mb, disk_free_mb, load1, uptime_seconds
		 from samples where at >= ? and at <= ? order by at`,
		from.Unix(), to.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []metrics.Sample
	for rows.Next() {
		var sample metrics.Sample
		var at int64
		if err := rows.Scan(&at, &sample.CPUPercent, &sample.MemTotalMB, &sample.MemAvailableMB,
			&sample.DiskTotalMB, &sample.DiskFreeMB, &sample.Load1, &sample.UptimeSeconds); err != nil {
			return nil, err
		}
		sample.At = time.Unix(at, 0).UTC()
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

func (s *Store) LatestSample(ctx context.Context) (metrics.Sample, bool, error) {
	samples, err := s.Samples(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || len(samples) == 0 {
		return metrics.Sample{}, false, err
	}
	return samples[len(samples)-1], true, nil
}

// SampleBefore — последний замер строго до указанного времени.
//
// То самое «последнее, что было перед падением». Во время аварии замеров
// нет и быть не может: лежал сервер — лежал и агент. Показывать при этом
// нечего — кроме того, что было за минуту до, и это как раз самое ценное.
func (s *Store) SampleBefore(ctx context.Context, at time.Time) (metrics.Sample, bool, error) {
	var sample metrics.Sample
	var unix int64
	err := s.db.QueryRowContext(ctx,
		`select at, cpu_percent, mem_total_mb, mem_available_mb,
		        disk_total_mb, disk_free_mb, load1, uptime_seconds
		 from samples where at < ? order by at desc limit 1`,
		at.Unix(),
	).Scan(&unix, &sample.CPUPercent, &sample.MemTotalMB, &sample.MemAvailableMB,
		&sample.DiskTotalMB, &sample.DiskFreeMB, &sample.Load1, &sample.UptimeSeconds)

	if errors.Is(err, sql.ErrNoRows) {
		return metrics.Sample{}, false, nil
	}
	if err != nil {
		return metrics.Sample{}, false, err
	}
	sample.At = time.Unix(unix, 0).UTC()
	return sample, true, nil
}

// Rotate убирает старые замеры.
//
// Без неё база растёт вечно на сервере, который нам не принадлежит: замер в
// минуту это полмиллиона строк в год. Место клиента — не наше место.
func (s *Store) Rotate(ctx context.Context, keep time.Duration) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`delete from samples where at < ?`, time.Now().Add(-keep).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ── Проверки ─────────────────────────────────────────────────────────────
//
// Результаты проверок лежат как есть, одним JSON на цель. Раскладывать их по
// колонкам незачем: приложение показывает их целиком, а запрашивать по
// отдельным полям не приходится.

func (s *Store) SaveCheck(ctx context.Context, kind, target string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`insert or replace into checks (kind, target, payload, checked_at) values (?, ?, ?, ?)`,
		kind, target, string(encoded), time.Now().Unix(),
	)
	return err
}

// Checks отдаёт сохранённые результаты одного вида проверки.
func (s *Store) Checks(ctx context.Context, kind string) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		`select target, payload from checks where kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := make(map[string]json.RawMessage)
	for rows.Next() {
		var target, payload string
		if err := rows.Scan(&target, &payload); err != nil {
			return nil, err
		}
		found[target] = json.RawMessage(payload)
	}
	return found, rows.Err()
}

// ForgetChecks убирает проверки целей, которых больше нет в списке сайтов.
func (s *Store) ForgetChecks(ctx context.Context, kind string, keep map[string]bool) error {
	targets, err := s.Checks(ctx, kind)
	if err != nil {
		return err
	}
	for target := range targets {
		if keep[target] {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`delete from checks where kind = ? and target = ?`, kind, target); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx,
			`delete from alerts where kind = ? and target = ?`, kind, target); err != nil {
			return err
		}
		// Ступени напоминаний уходят вместе с тревогой: сайт удалили —
		// его сроки больше не наши. Оставленная строка встретила бы
		// вернувшийся домен молчанием.
		if _, err := s.db.ExecContext(ctx,
			`delete from alert_steps where kind = ? and target = ?`, kind, target); err != nil {
			return err
		}
	}
	return nil
}

// ── Приглушённые находки ─────────────────────────────────────────────────

// Suppression — приглушённая находка.
type Suppression struct {
	Code   string `json:"code"`
	Target string `json:"target"`
	// До какого времени. Нулевое время — бессрочно.
	Until     time.Time `json:"until"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
}

// Suppressions отдаёт действующие приглушения, попутно убирая истёкшие.
//
// Уборка здесь, а не отдельным кругом: истёкшее приглушение — это находка,
// которая снова должна попасть в свод, и откладывать её возвращение до
// следующего круга ротации значило бы молчать о ней лишние сутки.
func (s *Store) Suppressions(ctx context.Context, now time.Time) ([]Suppression, error) {
	if _, err := s.db.ExecContext(ctx,
		`delete from suppressions where until != 0 and until <= ?`, now.Unix()); err != nil {
		return nil, fmt.Errorf("уборка приглушений: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`select code, target, until, note, created_at from suppressions
		 order by created_at desc`)
	if err != nil {
		return nil, fmt.Errorf("приглушения: %w", err)
	}
	defer rows.Close()

	// Пустой срез, а не nil: он уезжает в JSON, где null означал бы
	// «неизвестно», а мы точно знаем — приглушений нет.
	out := []Suppression{}
	for rows.Next() {
		var item Suppression
		var until, created int64
		if err := rows.Scan(&item.Code, &item.Target, &until, &item.Note, &created); err != nil {
			return nil, fmt.Errorf("чтение приглушения: %w", err)
		}
		if until != 0 {
			item.Until = time.Unix(until, 0).UTC()
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, item)
	}
	return out, rows.Err()
}

// Suppress приглушает находку. Нулевое `until` — бессрочно.
func (s *Store) Suppress(ctx context.Context, code, target string, until time.Time, note string) error {
	var deadline int64
	if !until.IsZero() {
		deadline = until.Unix()
	}
	_, err := s.db.ExecContext(ctx,
		`insert or replace into suppressions (code, target, until, note, created_at)
		 values (?, ?, ?, ?, ?)`,
		code, target, deadline, note, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("приглушение находки: %w", err)
	}
	return nil
}

// Unsuppress возвращает находку в свод.
func (s *Store) Unsuppress(ctx context.Context, code, target string) error {
	_, err := s.db.ExecContext(ctx,
		`delete from suppressions where code = ? and target = ?`, code, target)
	if err != nil {
		return fmt.Errorf("снятие приглушения: %w", err)
	}
	return nil
}

// ── Ключевые адреса проектов ─────────────────────────────────────────────

// ProbeKey — личность проверки: то, чем одна отличается от другой.
//
// Одна на хранилище, круг и свод: разойдись эти три, исходы снова начали бы
// затирать друг друга — просто в другом месте.
type ProbeKey = [4]string

// KeyOfProbe — личность по исходу проверки.
//
// Считается по probe.Result, а не по профилю, намеренно: круг сравнивает
// свежий исход с прошлым, и взять личность ему больше неоткуда.
func KeyOfProbe(result probe.Result) ProbeKey {
	return ProbeKey{result.ProjectID, result.URL, string(result.Kind), result.Name}
}

// ProbeAlertTarget — цель тревоги ключевой проверки.
//
// Тревога адресуется той же личностью, что и исход, а не одним адресом.
// Пока целью был адрес, две проверки на нём делили одну строку `alerts`:
// сломалась первая — ушло письмо; сломалась через час вторая — dispatch
// видел уже горящую тревогу и молчал про неё сутки (`ALERT_REPEAT_AFTER`),
// а отбой по первой гасил состояние, принадлежавшее второй. То есть беда,
// ради которой спринт 38 разводил исходы по четырём полям, оставалась на
// почтовом пути: письмо о беде, которой нет, и молчание о настоящей.
//
// Склейка, а не отдельные колонки: таблица `alerts` общая на все виды
// тревог, ключ у неё `(kind, target)`, и заводить ей пятую колонку ради
// одного вида значило бы переделывать таблицу, которой пользуются диск,
// домены, сертификаты, логи и копии. Разделитель — `\x1f` (unit
// separator): в адресе, имени проекта и имени проверки его не бывает.
//
// Человеку эта строка не показывается: в журнале сервера тревога называется
// адресом (`checks.Alert.Label`), а в письме — именем и адресом.
// Разделитель полей в цели тревоги.
const probeKeySeparator = "\x1f"

func ProbeAlertTarget(key ProbeKey) string {
	return strings.Join(key[:], probeKeySeparator)
}

// KeyOfProfile — та же личность по профилю из конфига.
func KeyOfProfile(profile probe.Profile) ProbeKey {
	name := profile.Name
	if name == "" {
		// Так же поступает разбор конфига: безымянная проверка называется
		// своим адресом. Считай мы иначе, живой профиль не совпал бы с
		// собственным исходом, и уборка стирала бы его каждый круг.
		name = profile.URL
	}
	return ProbeKey{profile.ProjectID, profile.URL, string(profile.Kind), name}
}

// SaveProbeResult запоминает исход проверки одного адреса.
func (s *Store) SaveProbeResult(ctx context.Context, result probe.Result) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	key := KeyOfProbe(result)
	_, err = s.db.ExecContext(ctx,
		`insert or replace into probe_outcomes (project_id, url, kind, name, payload, checked_at)
		 values (?, ?, ?, ?, ?, ?)`,
		key[0], key[1], key[2], key[3], string(payload), result.CheckedAt.UTC().Unix())
	return err
}

// ProbeResults отдаёт исходы по живым адресам.
//
// Живым — тем, что есть в конфиге сейчас: адрес убрали, и показывать
// вчерашнюю его беду больше незачем.
func (s *Store) ProbeResults(ctx context.Context, alive map[ProbeKey]bool) ([]probe.Result, error) {
	rows, err := s.db.QueryContext(ctx,
		`select project_id, url, kind, name, payload from probe_outcomes
		 order by project_id, url, kind, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []probe.Result{}
	for rows.Next() {
		var key ProbeKey
		var payload string
		if err := rows.Scan(&key[0], &key[1], &key[2], &key[3], &payload); err != nil {
			return nil, err
		}
		if !alive[key] {
			continue
		}
		var result probe.Result
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// ForgetProbeResults убирает проверки, которых больше нет в конфиге.
//
// Вместе с последним исходом проверки уходит и её тревога — ровно как у
// ForgetChecks, где это записано так: «сайт удалили — его сроки больше не
// наши». Оставленная строка alerts с firing = 1 означала бы, что
// вернувшаяся проверка, снова сломавшись, промолчит: dispatch увидит уже
// горящую тревогу и сочтёт беду известной.
//
// Тревоги чистятся не по списку снятых, а по списку живых: всё, что не
// принадлежит ни одной живой проверке, — мусор. Так уходят и строки,
// оставшиеся от выпусков, где целью тревоги был один адрес: перебирать их
// отдельно значило бы помнить прежний формат вечно.
func (s *Store) ForgetProbeResults(ctx context.Context, alive map[ProbeKey]bool) error {
	rows, err := s.db.QueryContext(ctx,
		`select project_id, url, kind, name from probe_outcomes`)
	if err != nil {
		return err
	}

	var stale []ProbeKey
	for rows.Next() {
		var key ProbeKey
		if err := rows.Scan(&key[0], &key[1], &key[2], &key[3]); err != nil {
			rows.Close()
			return err
		}
		if !alive[key] {
			stale = append(stale, key)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, key := range stale {
		if _, err := s.db.ExecContext(ctx,
			`delete from probe_outcomes
			 where project_id = ? and url = ? and kind = ? and name = ?`,
			key[0], key[1], key[2], key[3]); err != nil {
			return err
		}
	}

	return s.forgetProbeAlerts(ctx, alive)
}

// forgetProbeAlerts гасит тревоги проверок, которых больше нет.
//
// Ступеней у ключевой проверки не бывает — письмо о ней идёт по общему
// правилу «не чаще раза в сутки», — поэтому alert_steps здесь не при чём.
func (s *Store) forgetProbeAlerts(ctx context.Context, alive map[ProbeKey]bool) error {
	live := make(map[string]bool, len(alive))
	for key := range alive {
		live[ProbeAlertTarget(key)] = true
	}

	rows, err := s.db.QueryContext(ctx,
		`select target from alerts where kind = ?`, checks.KindProbe)
	if err != nil {
		return err
	}

	var stale []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			rows.Close()
			return err
		}
		if !live[target] {
			stale = append(stale, target)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, target := range stale {
		if _, err := s.db.ExecContext(ctx,
			`delete from alerts where kind = ? and target = ?`,
			checks.KindProbe, target); err != nil {
			return err
		}
	}
	return nil
}

// ── Копии базы ───────────────────────────────────────────────────────────

// SaveBackupRun записывает итог круга копии — удачный и неудачный одинаково.
//
// Неудачный важнее: «копия не сделалась и вот почему» — это то, ради чего
// автокопия и заводится, а молчащий круг неотличим от выключенного.
func (s *Store) SaveBackupRun(ctx context.Context, run backup.Record) error {
	ok := 0
	if run.OK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx,
		`insert into backup_runs
		 (started_at, finished_at, ok, code, detail, key, bytes, dump_bytes,
		  removed, stored, newest_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.StartedAt.UTC().Unix(), run.FinishedAt.UTC().Unix(), ok,
		run.Code, run.Detail, run.Key, run.Bytes, run.DumpBytes,
		run.Removed, run.Stored, newestUnix(run.NewestAt))
	return err
}

// BackupRuns отдаёт последние круги, от новых к старым.
func (s *Store) BackupRuns(ctx context.Context, limit int) ([]backup.Record, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`select started_at, finished_at, ok, code, detail, key, bytes, dump_bytes,
		        removed, stored, newest_at
		 from backup_runs order by started_at desc, id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []backup.Record{}
	for rows.Next() {
		var run backup.Record
		var started, finished, newest int64
		var ok int
		if err := rows.Scan(&started, &finished, &ok, &run.Code, &run.Detail,
			&run.Key, &run.Bytes, &run.DumpBytes, &run.Removed,
			&run.Stored, &newest); err != nil {
			return nil, err
		}
		run.StartedAt = time.Unix(started, 0).UTC()
		run.FinishedAt = time.Unix(finished, 0).UTC()
		run.OK = ok == 1
		if newest > 0 {
			run.NewestAt = time.Unix(newest, 0).UTC()
		}
		list = append(list, run)
	}
	return list, rows.Err()
}

// LastBackup — последний круг вообще и последний удачный.
//
// Оба сразу, потому что вопросов у свода два: «когда мы последний раз
// сохранили данные» и «что случилось на последней попытке». Ответ только на
// первый прятал бы неделю неудач за удачной копией недельной давности,
// ответ только на второй — терял бы «а данные-то у нас есть».
func (s *Store) LastBackup(ctx context.Context) (last, success backup.Record, hasLast, hasSuccess bool, err error) {
	read := func(where string) (backup.Record, bool, error) {
		var run backup.Record
		var started, finished, newest int64
		var ok int
		row := s.db.QueryRowContext(ctx,
			`select started_at, finished_at, ok, code, detail, key, bytes, dump_bytes,
			        removed, stored, newest_at
			 from backup_runs `+where+` order by started_at desc, id desc limit 1`)
		switch err := row.Scan(&started, &finished, &ok, &run.Code, &run.Detail,
			&run.Key, &run.Bytes, &run.DumpBytes, &run.Removed,
			&run.Stored, &newest); {
		case err == sql.ErrNoRows:
			return backup.Record{}, false, nil
		case err != nil:
			return backup.Record{}, false, err
		}
		run.StartedAt = time.Unix(started, 0).UTC()
		run.FinishedAt = time.Unix(finished, 0).UTC()
		run.OK = ok == 1
		if newest > 0 {
			run.NewestAt = time.Unix(newest, 0).UTC()
		}
		return run, true, nil
	}

	if last, hasLast, err = read(""); err != nil {
		return
	}
	success, hasSuccess, err = read("where ok = 1")
	return
}

// newestUnix — ноль вместо отрицательного числа у нулевого времени.
//
// `time.Time{}.Unix()` — это минус шестьдесят два миллиарда, и записанное в
// базу такое число прочиталось бы как «самая свежая копия сделана в первом
// веке»: свод объявил бы копии протухшими на сервере, где хранилище просто
// не спросили.
func newestUnix(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UTC().Unix()
}

// RotateBackupRuns убирает старые записи о кругах.
//
// Срок тот же, что у замеров и журнала: история копий, уходящая дальше
// графиков, отвечает на вопросы, которые рядом не с чем сверить.
func (s *Store) RotateBackupRuns(ctx context.Context, keep time.Duration) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`delete from backup_runs where started_at < ?`, time.Now().Add(-keep).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ── Журнал сервера ───────────────────────────────────────────────────────

// SaveEvent записывает отметку в журнал.
//
// Ошибка записи не останавливает того, кто её позвал: журнал — это
// объяснение задним числом, и терять из-за него замер или уведомление
// нельзя. Вызывающие пишут неудачу в лог и идут дальше.
func (s *Store) SaveEvent(ctx context.Context, event events.Event) error {
	at := event.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`insert into events (at, kind, target, detail) values (?, ?, ?, ?)`,
		at.UTC().Unix(), event.Kind, event.Target, event.Detail)
	return err
}

// Events отдаёт журнал за окно, от новых к старым.
//
// От новых к старым, потому что лента читается сверху: «что случилось
// последним» — первый вопрос, с которым в неё приходят.
func (s *Store) Events(ctx context.Context, from, to time.Time, limit int) ([]events.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`select at, kind, target, detail from events
		 where at >= ? and at <= ?
		 order by at desc, id desc limit ?`,
		from.Unix(), to.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []events.Event{}
	for rows.Next() {
		var event events.Event
		var at int64
		if err := rows.Scan(&at, &event.Kind, &event.Target, &event.Detail); err != nil {
			return nil, err
		}
		event.At = time.Unix(at, 0).UTC()
		list = append(list, event)
	}
	return list, rows.Err()
}

// RotateEvents убирает отметки старше срока хранения.
//
// Тот же принцип, что у замеров и счётчиков логов: место на диске клиента
// не наше место. Срок держится общим с замерами — лента, уходящая дальше
// графиков, показывала бы события, к которым нечего приложить.
func (s *Store) RotateEvents(ctx context.Context, keep time.Duration) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`delete from events where at < ?`, time.Now().Add(-keep).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ── Окно обслуживания ────────────────────────────────────────────────────

// Maintenance — идущее обслуживание.
type Maintenance struct {
	Until   time.Time `json:"until"`
	Note    string    `json:"note,omitempty"`
	Started time.Time `json:"started"`
}

// MaintenanceWindow отдаёт действующее окно или false, если его нет.
//
// Истёкшее окно удаляется тут же: «обслуживание кончилось» должно означать
// «письма снова ходят», а не «ходят со следующего круга уборки».
func (s *Store) MaintenanceWindow(ctx context.Context, now time.Time) (Maintenance, bool, error) {
	var until, started int64
	var note string

	err := s.db.QueryRowContext(ctx,
		`select until, note, started from maintenance where id = 1`).Scan(&until, &note, &started)
	if err == sql.ErrNoRows {
		return Maintenance{}, false, nil
	}
	if err != nil {
		return Maintenance{}, false, fmt.Errorf("окно обслуживания: %w", err)
	}

	if until <= now.Unix() {
		if _, err := s.db.ExecContext(ctx, `delete from maintenance where id = 1`); err != nil {
			return Maintenance{}, false, fmt.Errorf("уборка окна обслуживания: %w", err)
		}
		return Maintenance{}, false, nil
	}

	return Maintenance{
		Until:   time.Unix(until, 0).UTC(),
		Note:    note,
		Started: time.Unix(started, 0).UTC(),
	}, true, nil
}

// StartMaintenance открывает окно до указанного времени.
func (s *Store) StartMaintenance(ctx context.Context, until time.Time, note string) error {
	_, err := s.db.ExecContext(ctx,
		`insert or replace into maintenance (id, until, note, started) values (1, ?, ?, ?)`,
		until.Unix(), note, time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("начало обслуживания: %w", err)
	}
	return nil
}

// EndMaintenance закрывает окно досрочно.
func (s *Store) EndMaintenance(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `delete from maintenance where id = 1`)
	if err != nil {
		return fmt.Errorf("конец обслуживания: %w", err)
	}
	return nil
}

// ── Оповещения ───────────────────────────────────────────────────────────

// AlertState — что мы уже сообщали про эту цель.
type AlertState struct {
	Firing   bool
	LastSent time.Time
}

func (s *Store) AlertState(ctx context.Context, kind, target string) (AlertState, error) {
	var firing int
	var lastSent int64

	err := s.db.QueryRowContext(ctx,
		`select firing, last_sent from alerts where kind = ? and target = ?`,
		kind, target,
	).Scan(&firing, &lastSent)

	if err == sql.ErrNoRows {
		return AlertState{}, nil
	}
	if err != nil {
		return AlertState{}, err
	}

	return AlertState{Firing: firing == 1, LastSent: time.Unix(lastSent, 0).UTC()}, nil
}

// AlertStep — о какой ступени уже написали. Ноль означает «ни о какой».
func (s *Store) AlertStep(ctx context.Context, kind, target string) (int, error) {
	var step int
	err := s.db.QueryRowContext(ctx,
		`select step from alert_steps where kind = ? and target = ?`,
		kind, target).Scan(&step)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return step, nil
}

// SaveAlertStep запоминает ступень, о которой написали.
func (s *Store) SaveAlertStep(ctx context.Context, kind, target string, step int) error {
	_, err := s.db.ExecContext(ctx,
		`insert or replace into alert_steps (kind, target, step) values (?, ?, ?)`,
		kind, target, step)
	return err
}

// ForgetAlertStep забывает ступени цели: беда кончилась.
//
// Без этого продлённый домен на следующий год не напомнил бы о себе ни
// разу: запись «о тридцати днях уже писали» пережила бы продление, и
// молчание длилось бы до самой ближней ступени.
func (s *Store) ForgetAlertStep(ctx context.Context, kind, target string) error {
	_, err := s.db.ExecContext(ctx,
		`delete from alert_steps where kind = ? and target = ?`, kind, target)
	return err
}

func (s *Store) SaveAlertState(ctx context.Context, kind, target string, state AlertState) error {
	firing := 0
	if state.Firing {
		firing = 1
	}
	_, err := s.db.ExecContext(ctx,
		`insert or replace into alerts (kind, target, firing, last_sent) values (?, ?, ?, ?)`,
		kind, target, firing, state.LastSent.Unix(),
	)
	return err
}

// ── Битые ссылки ─────────────────────────────────────────────────────────
//
// Отчётов у сайта много (история), битых ссылок в каждом тоже. Хранится всё
// построчно: отчёт одной строкой, каждая пара «ссылка — страница-источник»
// своей. Экрану нужен обратный разрез — одна ссылка со всеми источниками, —
// и он собирается при чтении.

// Сколько кругов истории хранить на сайт. Круги редкие (раз в сутки), но
// без потолка база росла бы вечно на сервере, который нам не принадлежит.
const keepLinkReports = 30

func (s *Store) SaveLinkReport(ctx context.Context, report links.Report) error {
	startedAt, err := time.Parse(time.RFC3339, report.CheckedAt)
	if err != nil {
		return fmt.Errorf("отчёт %s: %w", report.Domain, err)
	}

	result, err := s.db.ExecContext(ctx,
		`insert into link_reports (domain, started_at, duration_s, pages, total_links, broken, run_error)
		 values (?, ?, ?, ?, ?, ?, ?)`,
		report.Domain, startedAt.Unix(), report.DurationSeconds,
		report.Pages, report.TotalLinks, report.Broken, report.Error,
	)
	if err != nil {
		return err
	}

	reportID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	for _, failure := range report.Failures {
		var code any
		if failure.Code != 0 {
			code = failure.Code
		}
		for _, source := range failure.Sources {
			if _, err := s.db.ExecContext(ctx,
				`insert into link_failures (report_id, url, kind, code, detail, source)
				 values (?, ?, ?, ?, ?, ?)`,
				reportID, failure.URL, failure.Kind, code, failure.Detail, source,
			); err != nil {
				return err
			}
		}
	}

	return s.rotateLinkReports(ctx, report.Domain)
}

// rotateLinkReports оставляет сайту последние keepLinkReports кругов и
// подчищает осиротевшие строки деталей.
func (s *Store) rotateLinkReports(ctx context.Context, domain string) error {
	if _, err := s.db.ExecContext(ctx,
		`delete from link_reports where id in (
		     select id from (
		         select id from link_reports
		         where domain = ?
		         order by started_at desc
		         limit -1 offset ?
		     )
		 )`, domain, keepLinkReports); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`delete from link_failures where report_id not in (select id from link_reports)`)
	return err
}

// LinkReports отдаёт последний отчёт по каждому живому сайту с деталями
// и короткой историей прошлых кругов.
func (s *Store) LinkReports(ctx context.Context, alive map[string]bool) ([]links.Report, error) {
	rows, err := s.db.QueryContext(ctx,
		`select r.id, r.domain, r.started_at, r.duration_s, r.pages,
		        r.total_links, r.broken, r.run_error
		 from link_reports r
		 where r.id in (select max(id) from link_reports group by domain)
		 order by r.domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type latest struct {
		id    int64
		index int
		row   links.Report
	}
	reports := []latest{}
	for rows.Next() {
		var row links.Report
		var id, startedAt int64
		if err := rows.Scan(&id, &row.Domain, &startedAt, &row.DurationSeconds,
			&row.Pages, &row.TotalLinks, &row.Broken, &row.Error); err != nil {
			return nil, err
		}
		row.CheckedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		row.Failures = []links.Failure{}
		reports = append(reports, latest{id: id, index: len(reports), row: row})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Удалённый сайт не показывается, хотя его история ещё в базе: она
	// умрёт при ротации сама.
	kept := reports[:0]
	for _, report := range reports {
		if alive[report.row.Domain] {
			kept = append(kept, report)
		}
	}

	for i := range kept {
		failures, err := s.linkFailures(ctx, kept[i].id)
		if err != nil {
			return nil, err
		}
		kept[i].row.Failures = failures

		history, err := s.linkHistory(ctx, kept[i].row.Domain, keepLinkReports-1)
		if err != nil {
			return nil, err
		}
		kept[i].row.History = history
	}

	result := make([]links.Report, 0, len(kept))
	for _, report := range kept {
		result = append(result, report.row)
	}
	return result, nil
}

// linkFailures собирает детали одного отчёта: строки по парам
// «ссылка — источник» складываются обратно в список ссылок с источниками.
func (s *Store) linkFailures(ctx context.Context, reportID int64) ([]links.Failure, error) {
	rows, err := s.db.QueryContext(ctx,
		`select url, kind, code, detail, source from link_failures
		 where report_id = ? order by url`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byURL := map[string]*links.Failure{}
	var order []string
	for rows.Next() {
		var urlValue, kind, detail, source string
		var code *int
		if err := rows.Scan(&urlValue, &kind, &code, &detail, &source); err != nil {
			return nil, err
		}

		failure, ok := byURL[urlValue]
		if !ok {
			codeValue := 0
			if code != nil {
				codeValue = *code
			}
			failure = &links.Failure{
				URL:     urlValue,
				Kind:    kind,
				Code:    codeValue,
				Detail:  detail,
				Sources: []string{},
			}
			byURL[urlValue] = failure
			order = append(order, urlValue)
		}
		failure.Sources = append(failure.Sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]links.Failure, 0, len(order))
	for _, urlValue := range order {
		result = append(result, *byURL[urlValue])
	}
	return result, nil
}

// linkHistory — сводки прошлых кругов сайта, от старых к новым, кроме
// последнего: он и так показан целиком.
func (s *Store) linkHistory(ctx context.Context, domain string, limit int) ([]links.HistoryPoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`select started_at, broken, total_links from link_reports
		 where domain = ? order by started_at desc, id desc limit ?`, domain, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []links.HistoryPoint
	for rows.Next() {
		var startedAt int64
		var point links.HistoryPoint
		if err := rows.Scan(&startedAt, &point.Broken, &point.TotalLinks); err != nil {
			return nil, err
		}
		point.CheckedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Строки пришли от новых к старым; самая свежая в начале списка —
	// это текущий отчёт, экран показывает его целиком, без сводки.
	if len(points) > 0 {
		points = points[1:]
	}
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	return points, nil
}

// ForgetLinkReports стирает историю сайтов, которых больше нет в списке.
func (s *Store) ForgetLinkReports(ctx context.Context, keep map[string]bool) error {
	domains, err := s.db.QueryContext(ctx, `select distinct domain from link_reports`)
	if err != nil {
		return err
	}
	defer domains.Close()

	var gone []string
	for domains.Next() {
		var domain string
		if err := domains.Scan(&domain); err != nil {
			return err
		}
		if !keep[domain] {
			gone = append(gone, domain)
		}
	}
	if err := domains.Err(); err != nil {
		return err
	}

	for _, domain := range gone {
		if _, err := s.db.ExecContext(ctx,
			`delete from link_reports where domain = ?`, domain); err != nil {
			return err
		}
	}
	return s.rotateLinkReportsAll(ctx)
}

// rotateLinkReportsAll чистит детали отчётов, чьи владельцы уже удалены.
// Отдельно от ForgetLinkReports: после удаления домена осиротевшие строки
// могут остаться и от других причин.
func (s *Store) rotateLinkReportsAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`delete from link_failures where report_id not in (select id from link_reports)`)
	return err
}

// ── Core Web Vitals ──────────────────────────────────────────────────────
//
// Замер хранится целиком, одним JSON: в нём полтора десятка чисел, и
// раскладывать их по колонкам значило бы менять схему всякий раз, когда
// Google заведёт новую метрику. Разрезов, кроме «последний замер этой
// страницы» и «её история», не бывает — для них хватает ключа и времени.

// Сколько замеров хранить на страницу и тип устройства. Столько же, сколько
// кругов проверки ссылок: замер редкий, но база живёт на чужом сервере.
const keepVitalsReports = 30

// Сутки, которыми считается квота, — UTC: по ним Google сбрасывает свою, а
// местная полночь сервера клиента к его квоте отношения не имеет.
const vitalsDayLayout = "2006-01-02"

// Today — сутки, на которые записывается расход квоты.
func Today() string { return time.Now().UTC().Format(vitalsDayLayout) }

// VitalsKey — ключ страницы в истории замеров.
func VitalsKey(domain, path, strategy string) string {
	return domain + " " + path + " " + strategy
}

// SaveVitalsReport кладёт замер и подчищает историю этой страницы.
func (s *Store) SaveVitalsReport(ctx context.Context, report vitals.Report) error {
	startedAt, err := time.Parse(time.RFC3339, report.CheckedAt)
	if err != nil {
		return fmt.Errorf("замер %s: %w", report.URL, err)
	}

	// История в самой записи не хранится: она собирается при чтении из
	// соседних строк, и записанная копия разъехалась бы с ними после первой
	// же ротации.
	report.History = nil

	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}

	if _, err := s.db.ExecContext(ctx,
		`insert into vitals_reports (domain, path, strategy, started_at, payload)
		 values (?, ?, ?, ?, ?)`,
		report.Domain, report.Path, report.Strategy, startedAt.Unix(), string(encoded),
	); err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx,
		`delete from vitals_reports where id in (
		     select id from (
		         select id from vitals_reports
		         where domain = ? and path = ? and strategy = ?
		         order by started_at desc, id desc
		         limit -1 offset ?
		     )
		 )`, report.Domain, report.Path, report.Strategy, keepVitalsReports)
	return err
}

// VitalsReports отдаёт последний замер каждой живой страницы с её историей.
//
// Живое — тройка «домен, путь, устройство»: страницу убрали из списка или
// выключили настольные замеры, и показывать вчерашние цифры больше незачем.
func (s *Store) VitalsReports(ctx context.Context, alive map[string]bool) ([]vitals.Report, error) {
	rows, err := s.db.QueryContext(ctx,
		`select domain, path, strategy, payload from vitals_reports
		 where id in (
		     select max(id) from vitals_reports group by domain, path, strategy
		 )
		 order by domain, path, strategy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type page struct{ domain, path, strategy string }
	var order []page
	latest := map[page]vitals.Report{}

	for rows.Next() {
		var id page
		var payload string
		if err := rows.Scan(&id.domain, &id.path, &id.strategy, &payload); err != nil {
			return nil, err
		}
		if !alive[VitalsKey(id.domain, id.path, id.strategy)] {
			continue
		}

		var report vitals.Report
		if err := json.Unmarshal([]byte(payload), &report); err != nil {
			continue
		}
		latest[id] = report
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]vitals.Report, 0, len(order))
	for _, id := range order {
		report := latest[id]
		history, err := s.vitalsHistory(ctx, id.domain, id.path, id.strategy, keepVitalsReports-1)
		if err != nil {
			return nil, err
		}
		report.History = history
		result = append(result, report)
	}
	return result, nil
}

// vitalsHistory — сводки прошлых замеров, от старых к новым, кроме
// последнего: он и так показан целиком.
func (s *Store) vitalsHistory(
	ctx context.Context,
	domain, path, strategy string,
	limit int,
) ([]vitals.HistoryPoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`select payload from vitals_reports
		 where domain = ? and path = ? and strategy = ?
		 order by started_at desc, id desc limit ?`,
		domain, path, strategy, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []vitals.HistoryPoint
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var report vitals.Report
		if err := json.Unmarshal([]byte(payload), &report); err != nil {
			continue
		}
		points = append(points, report.Point())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Первым идёт самый свежий замер — он же текущий отчёт, показанный
	// целиком. В истории он лишний.
	if len(points) > 0 {
		points = points[1:]
	}
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	return points, nil
}

// ForgetVitalsReports стирает историю страниц, которых больше нет в списке.
func (s *Store) ForgetVitalsReports(ctx context.Context, alive map[string]bool) error {
	rows, err := s.db.QueryContext(ctx,
		`select distinct domain, path, strategy from vitals_reports`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type page struct{ domain, path, strategy string }
	var gone []page
	for rows.Next() {
		var id page
		if err := rows.Scan(&id.domain, &id.path, &id.strategy); err != nil {
			return err
		}
		if !alive[VitalsKey(id.domain, id.path, id.strategy)] {
			gone = append(gone, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range gone {
		if _, err := s.db.ExecContext(ctx,
			`delete from vitals_reports where domain = ? and path = ? and strategy = ?`,
			id.domain, id.path, id.strategy); err != nil {
			return err
		}
	}
	return nil
}

// LatestVitalsAt — когда мерили в прошлый раз.
//
// Нужно на старте: замер стоит квоты и минут, и перезапуск агента после
// правки настроек не повод мерить всё заново.
func (s *Store) LatestVitalsAt(ctx context.Context) (time.Time, bool, error) {
	var startedAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`select max(started_at) from vitals_reports`).Scan(&startedAt); err != nil {
		return time.Time{}, false, err
	}
	if !startedAt.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(startedAt.Int64, 0).UTC(), true, nil
}

// VitalsSpent — сколько обращений к API Google потрачено за эти сутки.
func (s *Store) VitalsSpent(ctx context.Context, day string) (int, error) {
	var used int
	err := s.db.QueryRowContext(ctx,
		`select used from vitals_quota where day = ?`, day).Scan(&used)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return used, nil
}

// AddVitalsSpent прибавляет потраченное и убирает счёт прошлой недели.
func (s *Store) AddVitalsSpent(ctx context.Context, day string, requests int) error {
	if requests <= 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`insert into vitals_quota (day, used) values (?, ?)
		 on conflict(day) do update set used = used + excluded.used`,
		day, requests); err != nil {
		return err
	}

	// Дни старше недели никому не интересны, а таблица иначе растёт вечно.
	_, err := s.db.ExecContext(ctx,
		`delete from vitals_quota where day < ?`,
		time.Now().UTC().AddDate(0, 0, -7).Format(vitalsDayLayout))
	return err
}

// ── Логи веб-сервера ─────────────────────────────────────────────────────
//
// Хранятся счётчики, а не строки. Лог за сутки — это гигабайты на диске,
// который нам не принадлежит, а в каждой строке access-лога стоит адрес
// посетителя: агрегату он не нужен, и держать его у себя мы не станем.

// LogPosition — где мы остановились в файле и чем кончилось чтение.
type LogPosition struct {
	logs.Position
	Error  string
	ReadAt time.Time
}

// LogPositions отдаёт позиции по всем известным файлам.
func (s *Store) LogPositions(ctx context.Context) (map[string]LogPosition, error) {
	rows, err := s.db.QueryContext(ctx,
		`select path, inode, size, offset_at, read_at, run_error from log_positions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := map[string]LogPosition{}
	for rows.Next() {
		var path string
		var readAt int64
		var position LogPosition
		if err := rows.Scan(&path, &position.Inode, &position.Size,
			&position.Offset, &readAt, &position.Error); err != nil {
			return nil, err
		}
		position.ReadAt = time.Unix(readAt, 0).UTC()
		found[path] = position
	}
	return found, rows.Err()
}

// SaveLogPosition запоминает, до какого места файл прочитан.
func (s *Store) SaveLogPosition(ctx context.Context, path string, position logs.Position, failure string) error {
	_, err := s.db.ExecContext(ctx,
		`insert or replace into log_positions (path, inode, size, offset_at, read_at, run_error)
		 values (?, ?, ?, ?, ?, ?)`,
		path, position.Inode, position.Size, position.Offset, time.Now().Unix(), failure)
	return err
}

// ForgetLogPositions убирает файлы, которых больше нет в списке.
func (s *Store) ForgetLogPositions(ctx context.Context, alive map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `select path from log_positions`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var gone []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if !alive[path] {
			gone = append(gone, path)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, path := range gone {
		if _, err := s.db.ExecContext(ctx,
			`delete from log_positions where path = ?`, path); err != nil {
			return err
		}
	}
	return nil
}

// AddLogGroups прибавляет счётчики прохода к тому, что уже накоплено.
//
// Прибавляет, а не заменяет: тот же ключ приходит и из прошлого прохода, и
// из этого — «insert or replace» потерял бы прошлый счёт, и график ошибок
// показывал бы только последнюю минуту.
func (s *Store) AddLogGroups(ctx context.Context, source string, groups []logs.Counted) error {
	for _, group := range groups {
		if _, err := s.db.ExecContext(ctx,
			`insert into log_events
			 (bucket, source, kind, status, level, host, method, path, signature,
			  sample, count, first_at, last_at)
			 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 on conflict do update set
			     count = count + excluded.count,
			     first_at = min(first_at, excluded.first_at),
			     last_at = max(last_at, excluded.last_at),
			     sample = case when sample = '' then excluded.sample else sample end`,
			group.Bucket(), source, group.Kind, group.Status, group.Level, group.Host,
			group.Method, group.Path, group.Signature, group.Sample, group.Count,
			group.At.Unix(), group.LastAt.Unix(),
		); err != nil {
			return err
		}
	}
	return nil
}

// AddRobotCounts прибавляет счётчики роботов прохода к накопленным.
func (s *Store) AddRobotCounts(ctx context.Context, counts []logs.RobotCount) error {
	for _, count := range counts {
		if _, err := s.db.ExecContext(ctx,
			`insert into robot_visits (day, host, name, count, last_at)
			 values (?, ?, ?, ?, ?)
			 on conflict do update set
			     count = count + excluded.count,
			     last_at = max(last_at, excluded.last_at)`,
			count.Day, count.Host, count.Name, count.Count, count.LastAt.Unix(),
		); err != nil {
			return err
		}
	}
	return nil
}

// RobotSummary — счётчики роботов за период, сложенные по сайту и имени.
func (s *Store) RobotSummary(ctx context.Context, since time.Time) ([]logs.RobotCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`select host, name, sum(count), max(last_at)
		 from robot_visits where day >= ?
		 group by host, name
		 order by host, sum(count) desc`,
		since.Unix()-since.Unix()%86_400)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := []logs.RobotCount{}
	for rows.Next() {
		var count logs.RobotCount
		var lastAt int64
		if err := rows.Scan(&count.Host, &count.Name, &count.Count, &lastAt); err != nil {
			return nil, err
		}
		count.LastAt = time.Unix(lastAt, 0).UTC()
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

// LogGroups отдаёт самые частые группы за период.
//
// Потолок обязателен: у сайта под ботами уникальных путей тысячи, и список
// без предела превращает экран в ленту, которую никто не читает.
func (s *Store) LogGroups(ctx context.Context, since time.Time, limit int) ([]logs.Group, error) {
	return s.LogGroupsBetween(ctx, since, forever, limit)
}

// LogGroupsBetween — те же группы, но за окно.
//
// Нужно разбору аварии: «что сыпалось в тот час», а не «что сыпалось с тех
// пор». Запрос один на оба случая по той же причине, что и у замеров.
func (s *Store) LogGroupsBetween(
	ctx context.Context,
	from, to time.Time,
	limit int,
) ([]logs.Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`select source, kind, status, level, host, method, path, signature,
		        max(sample), sum(count), min(first_at), max(last_at)
		 from log_events
		 where bucket >= ? and bucket <= ?
		 group by source, kind, status, level, host, method, path, signature
		 order by sum(count) desc, max(last_at) desc
		 limit ?`,
		bucketOf(from), bucketOf(to), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := []logs.Group{}
	for rows.Next() {
		var group logs.Group
		var firstAt, lastAt int64
		if err := rows.Scan(&group.Source, &group.Kind, &group.Status, &group.Level,
			&group.Host, &group.Method, &group.Path, &group.Signature,
			&group.Sample, &group.Count, &firstAt, &lastAt); err != nil {
			return nil, err
		}
		group.FirstAt = time.Unix(firstAt, 0).UTC().Format(time.RFC3339)
		group.LastAt = time.Unix(lastAt, 0).UTC().Format(time.RFC3339)
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// LogSeries отдаёт ряд для графика: по окну на точку.
func (s *Store) LogSeries(ctx context.Context, since time.Time) ([]logs.Point, error) {
	return s.LogSeriesBetween(ctx, since, forever)
}

// LogSeriesBetween — тот же ряд за окно.
func (s *Store) LogSeriesBetween(ctx context.Context, from, to time.Time) ([]logs.Point, error) {
	rows, err := s.db.QueryContext(ctx,
		`select bucket,
		        sum(case when kind = ? then count else 0 end),
		        sum(case when kind = ? then count else 0 end)
		 from log_events where bucket >= ? and bucket <= ?
		 group by bucket order by bucket`,
		logs.KindAccess, logs.KindError,
		bucketOf(from), bucketOf(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := []logs.Point{}
	for rows.Next() {
		var bucket int64
		var point logs.Point
		if err := rows.Scan(&bucket, &point.Access, &point.Errors); err != nil {
			return nil, err
		}
		point.At = time.Unix(bucket, 0).UTC().Format(time.RFC3339)
		points = append(points, point)
	}
	return points, rows.Err()
}

// bucketOf округляет время вниз до начала окна счётчиков.
//
// Счётчики лежат по окнам, и сравнивать с ними голую секунду нельзя:
// событие в середине окна попало бы за границу выборки вместе со всем
// окном. Одной функцией на все три запроса — арифметика повторялась
// четвёртый раз.
func bucketOf(at time.Time) int64 {
	unix := at.Unix()
	return unix - unix%logs.BucketSeconds
}

// LogCountsBySource — сколько событий пришло из каждого файла за период.
func (s *Store) LogCountsBySource(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`select source, sum(count) from log_events where bucket >= ? group by source`,
		bucketOf(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			return nil, err
		}
		counts[source] = count
	}
	return counts, rows.Err()
}

// LogSpikes — сколько ошибок пришлось на окно тревоги, в разрезе по домену.
//
// Считаются только строки access-лога: записи error-лога сопровождают ту же
// беду и удвоили бы счёт, а порог человек задаёт в понятных ему пятисотках.
//
// Разрез по домену, потому что всплеск — это повод в своде, а свод у
// проекта свой. Домен в строке лога есть не всегда: формат `combined` у
// nginx его не пишет, и такие ошибки приезжают с пустым `Host` — они про
// сервер целиком, и в своде оказываются у сервера. Считать их «ничьими» и
// молчать о них нельзя: это тот же молчаливый ноль.
//
// Разделение на 5xx и «страницы нет» живёт в запросе, а не в разборе
// ответа: строк в окне бывают тысячи, и тащить их наружу ради двух сумм
// значит гонять чужой диск впустую.
//
// Граница окна — по бакетам (`bucket >= ?`), а не по времени последнего
// события в нём. В строке лежит счётчик за весь пятиминутный бакет, и взять
// из него «только последние минуты» нечем: до спринта 36 отбор шёл по
// `last_at`, и бакет, начавшийся до окна, попадал в счёт целиком — счёт
// раздувался на треть, а письмо называло его точным числом за точные
// пятнадцать минут. Начало окна выравнивает `logs.WindowStart`, а сколько
// минут сосчитано на самом деле, говорит `logs.WindowMinutes`.
func (s *Store) LogSpikes(ctx context.Context, since time.Time) ([]logs.Spike, error) {
	rows, err := s.db.QueryContext(ctx,
		`select host,
		        coalesce(sum(case when status >= 500 then count else 0 end), 0),
		        coalesce(sum(case when status = ? then count else 0 end), 0)
		 from log_events
		 where kind = ? and bucket >= ?
		 group by host`,
		logs.StatusNotFound, logs.KindAccess, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	spikes := []logs.Spike{}
	for rows.Next() {
		var spike logs.Spike
		if err := rows.Scan(&spike.Host, &spike.ServerErrors, &spike.NotFound); err != nil {
			return nil, err
		}
		if spike.ServerErrors == 0 && spike.NotFound == 0 {
			continue
		}
		spikes = append(spikes, spike)
	}
	return spikes, rows.Err()
}

// RotateLogEvents убирает счётчики старше срока хранения.
//
// Без неё база растёт вечно на сервере, который нам не принадлежит: место
// клиента — не наше место.
func (s *Store) RotateLogEvents(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		days = 14
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Unix()
	result, err := s.db.ExecContext(ctx, `delete from log_events where bucket < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	// Счётчики роботов живут столько же, сколько ошибки: это та же история
	// логов, и место на диске клиента — не наше место.
	if _, err := s.db.ExecContext(ctx, `delete from robot_visits where day < ?`, cutoff); err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ── Обход сайта: снимки страниц ──────────────────────────────────────────
//
// Хранится не HTML, а извлечённая структура — по строке на страницу, одним
// JSON. Раскладывать её по колонкам незачем: разрезов, кроме «страницы
// последнего обхода этого сайта», не бывает, а состав полей меняется каждый
// раз, когда в аудит добавляют новую проверку.

// Что считается состоявшимся обходом.
//
// Тот, который сохранил хотя бы одну страницу, — а не тот, у которого пуст
// `run_error`. Разница существенная в обе стороны: обход, упёршийся в
// отведённое время, приносит триста страниц из пятисот и вполне годится в
// работу, а обход, которому robots.txt ответил перенаправлением, не приносит
// ничего и годиться не может.
//
// Раньше «последним» считался просто последний по id, и следствий было три:
// экран говорил «обход не сохранил ни одной страницы», хотя вчерашний
// снимок на пятьсот страниц лежал в той же базе; сравнение объявляло
// пропавшими все страницы разом; а ротация, оставлявшая снимки двум
// последним обходам безотносительно успеха, после двух неудач подряд стирала
// последний удачный насовсем. Условие одно на все четыре места намеренно:
// разойдись они — снимок всё равно однажды удалится, только теперь незаметно.
//
// Функция, а не константа, ровно затем, чтобы «одно на четыре места»
// осталось правдой: в одном из запросов таблица обходов идёт под
// псевдонимом, и константа с жёстким `seo_scans.id` там не подставлялась —
// условие пришлось написать текстом рядом. Из четырёх мест, защищённых
// одним условием, одно стало правиться отдельно, и правка условия до него
// бы не доехала.
func seoScanHasPages(scans string) string {
	return `exists (select 1 from seo_pages p where p.scan_id = ` + scans + `.id)`
}

// Сколько обходов хранить целиком, со страницами.
//
// Два, а не тридцать: страниц у обхода сотни, и каждая — килобайт структуры.
// Двух хватает на «что появилось и что исправилось с прошлого раза»; на
// третий разрез никто не смотрит, а место на чужом диске он занимает.
const keepSeoPageSets = 2

// Сколько сводок обходов хранить. Сводка — три числа, её можно держать
// долго: график «страниц было — страниц стало» без истории не построить.
const keepSeoScans = 30

// SaveSeoScan кладёт обход вместе со страницами и подчищает старое.
//
// Обход и его страницы пишутся одной транзакцией: половина снимка — это не
// «часть данных», а испорченные данные. Следующее сравнение прочтёт
// недописанное как массовое «исправилось» и «страницы пропали», причём
// молча. Раньше пятьсот страниц уходили отдельными вставками, и оборвать их
// могла остановка агента или ошибка на одной странице — остальные при этом
// просто не писались.
//
// Побочная выгода — на чужом диске: один коммит вместо пятисот.
func (s *Store) SaveSeoScan(ctx context.Context, scan seo.Scan, pages []seo.Page) error {
	startedAt, err := time.Parse(time.RFC3339, scan.CheckedAt)
	if err != nil {
		return fmt.Errorf("обход %s: %w", scan.Domain, err)
	}

	stats, err := json.Marshal(scan.Stats)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Откат после успешного коммита — не ошибка, а пустое действие: так
	// защита от раннего возврата не требует флага «уже закоммитили».
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx,
		`insert into seo_scans (domain, started_at, duration_s, stats, run_error)
		 values (?, ?, ?, ?, ?)`,
		scan.Domain, startedAt.Unix(), scan.DurationSeconds, string(stats), scan.Error,
	)
	if err != nil {
		return err
	}

	scanID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	for _, page := range pages {
		payload, err := json.Marshal(page)
		if err != nil {
			return err
		}
		// Худший уровень находки — отдельной колонкой, хотя сами находки
		// лежат в payload. Экран спрашивает «покажи только страницы с
		// критичным», и разбирать ради этого JSON у каждой из пятисот
		// страниц значит читать весь обход целиком на каждый вопрос.
		if _, err := tx.ExecContext(ctx,
			`insert into seo_pages (scan_id, url, status, depth, hash, worst, codes, payload)
			 values (?, ?, ?, ?, ?, ?, ?, ?)`,
			scanID, page.URL, page.Status, page.Depth, page.Hash,
			seo.Worst(page.Issues), codesColumn(page.Issues), string(payload),
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Уборка — отдельной транзакцией и после коммита: она смотрит на то,
	// у каких обходов есть страницы, а внутри незавершённой транзакции
	// новый обход этому условию ещё не отвечает.
	return s.rotateSeoScans(ctx, scan.Domain)
}

// codesColumn складывает коды находок в строку для отбора.
//
// С пробелами по краям и между: отбор идёт как `codes like '% код %'`, и без
// краевых пробелов «titleLong» нашёлся бы внутри несуществующего
// «titleLonger». Отдельная таблица связей была бы правильнее в теории, но у
// страницы находок пять, а у обхода их пятьсот строк — вторая таблица здесь
// стоит дороже, чем даёт.
func codesColumn(issues []seo.Issue) string {
	codes := seo.Codes(issues)
	if len(codes) == 0 {
		return ""
	}
	return " " + strings.Join(codes, " ") + " "
}

// rotateSeoScans оставляет сайту keepSeoScans сводок и страницы только у
// последних keepSeoPageSets обходов.
func (s *Store) rotateSeoScans(ctx context.Context, domain string) error {
	if _, err := s.db.ExecContext(ctx,
		`delete from seo_scans where id in (
		     select id from (
		         select id from seo_scans
		         where domain = ?
		         order by started_at desc
		         limit -1 offset ?
		     )
		 )`, domain, keepSeoScans); err != nil {
		return err
	}

	// Страницы более старых обходов: сводка остаётся, снимки уходят.
	//
	// Считаются только состоявшиеся: неудачная попытка не приносит страниц,
	// и записывать её в число тех, у кого снимки хранятся, значит вытеснять
	// снимок вместо неё. Двух неудач подряд хватало, чтобы удалить
	// последний удачный обход насовсем.
	if _, err := s.db.ExecContext(ctx,
		`delete from seo_pages where scan_id in (
		     select id from (
		         select id from seo_scans
		         where domain = ? and `+seoScanHasPages("seo_scans")+`
		         order by started_at desc
		         limit -1 offset ?
		     )
		 )`, domain, keepSeoPageSets); err != nil {
		return err
	}

	_, err := s.db.ExecContext(ctx,
		`delete from seo_pages where scan_id not in (select id from seo_scans)`)
	return err
}

// SeoScans отдаёт последний обход по каждому живому сайту с историей.
func (s *Store) SeoScans(ctx context.Context, alive map[string]bool) ([]seo.Scan, error) {
	rows, err := s.db.QueryContext(ctx,
		`select id, domain, started_at, duration_s, stats, run_error
		 from seo_scans
		 where id in (
		     -- Последний состоявшийся обход по каждому сайту; если таких
		     -- нет вовсе, показываем последнюю попытку с её причиной —
		     -- сказать больше о сайте нам нечего.
		     select coalesce(
		         (select max(id) from seo_scans inner_scan
		          where inner_scan.domain = seo_scans.domain
		            and `+seoScanHasPages("inner_scan")+`),
		         max(id))
		     from seo_scans group by domain
		 )
		 order by domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		scans []seo.Scan
		// Идентификатор показанного обхода: по нему история вырезает
		// именно его, а не последнюю строку по домену.
		shown []int64
	)
	for rows.Next() {
		var (
			scan      seo.Scan
			scanID    int64
			startedAt int64
			stats     string
		)
		if err := rows.Scan(&scanID, &scan.Domain, &startedAt, &scan.DurationSeconds, &stats, &scan.Error); err != nil {
			return nil, err
		}
		// Сайт удалили — показывать его вчерашний обход больше незачем.
		if !alive[scan.Domain] {
			continue
		}
		if err := json.Unmarshal([]byte(stats), &scan.Stats); err != nil {
			return nil, err
		}
		// Обходу, сохранённому до 0.18.0, перечень проверок достраивается
		// из его же сводки.
		scan.Stats.EnsureChecks()
		scan.CheckedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		scans = append(scans, scan)
		shown = append(shown, scanID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for index := range scans {
		history, previous, err := s.seoHistory(ctx, scans[index].Domain, shown[index], keepSeoScans)
		if err != nil {
			return nil, err
		}
		scans[index].History = history
		if previous != nil {
			scans[index].Progress = seo.CompareChecks(scans[index].Stats.Checks, previous.checks, previous.at)
		}

		failedAt, failedError, err := s.lastFailedAttempt(ctx, scans[index])
		if err != nil {
			return nil, err
		}
		scans[index].FailedAt, scans[index].FailedError = failedAt, failedError
	}
	return scans, nil
}

// lastFailedAttempt — последняя попытка обхода после показанного, если она
// не удалась.
//
// Нужна затем, чтобы данные не выдавались за сегодняшние: показан
// предыдущий обход, а сегодняшняя попытка провалилась — человек обязан
// узнать об этом, иначе он смотрит на снимок недельной давности и считает
// его свежим.
func (s *Store) lastFailedAttempt(ctx context.Context, scan seo.Scan) (string, string, error) {
	shown, err := time.Parse(time.RFC3339, scan.CheckedAt)
	if err != nil {
		return "", "", nil
	}

	var (
		startedAt int64
		runError  string
	)
	err = s.db.QueryRowContext(ctx,
		`select started_at, run_error from seo_scans
		 where domain = ? and started_at > ? and run_error != ''
		 order by started_at desc
		 limit 1`, scan.Domain, shown.Unix()).Scan(&startedAt, &runError)

	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return time.Unix(startedAt, 0).UTC().Format(time.RFC3339), runError, nil
}

// seoHistory — сводки прошлых обходов, от старых к новым, кроме показанного:
// он и так виден целиком.
//
// Вырезается именно показанный обход, а не последний по id. Показывается
// последний **состоявшийся**, и когда последней в базе лежит неудачная
// попытка, «последний по id» — это она: показанный обход оставался в
// истории и дублировал сам себя, а комментарий рядом обещал обратное.
func (s *Store) seoHistory(
	ctx context.Context,
	domain string,
	shownID int64,
	limit int,
) ([]seo.HistoryPoint, *seoPrevious, error) {
	rows, err := s.db.QueryContext(ctx,
		`select id, started_at, stats from seo_scans
		 where domain = ? and id != ?
		 order by started_at desc
		 limit ?`, domain, shownID, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		points   []seo.HistoryPoint
		previous *seoPrevious
	)
	for rows.Next() {
		var (
			scanID    int64
			startedAt int64
			raw       string
		)
		if err := rows.Scan(&scanID, &startedAt, &raw); err != nil {
			return nil, nil, err
		}
		var stats seo.Stats
		if err := json.Unmarshal([]byte(raw), &stats); err != nil {
			return nil, nil, err
		}
		stats.EnsureChecks()

		point := seo.HistoryPoint{
			CheckedAt: time.Unix(startedAt, 0).UTC().Format(time.RFC3339),
			Pages:     stats.Pages,
			Fetched:   stats.Fetched,
			Failed:    stats.Failed,
			Critical:  stats.Issues.Critical,
			Warning:   stats.Issues.Warning,
			Notice:    stats.Issues.Notice,
		}
		if stats.Checks != nil {
			tally := stats.Checks.Tally
			point.Checks = &tally
			// Сравнивать — с ближайшим обходом, который был раньше
			// показанного. Попытка после него (неудачная: иначе показана
			// была бы она) прошлым обходом не является, даже если успела
			// собрать страницы до обрыва.
			if previous == nil && scanID < shownID {
				previous = &seoPrevious{at: point.CheckedAt, checks: stats.Checks}
			}
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Читали от новых к старым, показываем наоборот.
	for left, right := 0, len(points)-1; left < right; left, right = left+1, right-1 {
		points[left], points[right] = points[right], points[left]
	}
	return points, previous, nil
}

// seoPrevious — перечень проверок прошлого обхода и когда тот был.
type seoPrevious struct {
	at     string
	checks *seo.ChecksReport
}

// SeoPages отдаёт страницы последнего обхода сайта: окно и общее число.
//
// Окном, а не целиком: страниц у обхода сотни, и тащить их все через
// SSH-туннель ради одного экрана незачем. Пустые severity и code означают
// «все страницы»; заданные отбирают — по уровню «этот и тяжелее» и по
// конкретной находке. Total считается по тому же условию: «показано 20 из
// 500», когда критичных двадцать, — это неверный ответ.
func (s *Store) SeoPages(
	ctx context.Context,
	domain string,
	severity, code string,
	limit, offset int,
) ([]seo.Page, int, error) {
	var scanID sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`select max(id) from seo_scans where domain = ? and `+seoScanHasPages("seo_scans"),
		domain).Scan(&scanID)
	if err != nil {
		// Ошибка базы — это ошибка, а не «обхода не было». Пока они шли
		// одной веткой, не ответившая база выглядела на экране как чистый
		// сайт без единой находки: разные новости с одинаковым видом.
		return nil, 0, fmt.Errorf("последний обход %s: %w", domain, err)
	}
	if !scanID.Valid {
		// Состоявшегося обхода не было: пустой список, а не ошибка.
		return []seo.Page{}, 0, nil
	}

	// Уровни перечисляются явно, а не сравниваются строками: «critical
	// хуже warning» — знание о смысле кодов, и в SQL ему делать нечего.
	levels := seo.AtLeast(severity)
	filter, arguments := "", []any{scanID.Int64}
	if len(levels) > 0 {
		filter = " and worst in (?" + strings.Repeat(", ?", len(levels)-1) + ")"
		for _, level := range levels {
			arguments = append(arguments, level)
		}
	}
	if code = seo.KnownCode(code); code != "" {
		filter += " and codes like ?"
		arguments = append(arguments, "% "+code+" %")
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		`select count(*) from seo_pages where scan_id = ?`+filter,
		arguments...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx,
		`select payload from seo_pages
		 where scan_id = ?`+filter+`
		 order by depth, url
		 limit ? offset ?`, append(arguments, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	pages := []seo.Page{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, 0, err
		}
		var page seo.Page
		if err := json.Unmarshal([]byte(payload), &page); err != nil {
			return nil, 0, err
		}
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return pages, total, nil
}

// SeoScanCodes отдаёт находки двух последних обходов сайта: по адресу
// страницы — коды, что на ней нашлись.
//
// Двух, потому что снимки страниц и хранятся у двух последних обходов. Это
// ровно то, что нужно для ответа «что появилось и что исправилось»; на
// третий разрез смотреть некому, и хранить его ради этого мы не станем.
//
// Читаются только адрес и коды, без payload: сравнивать структуру страниц не
// требуется, а разбирать полтысячи JSON ради списка кодов — работа впустую.
func (s *Store) SeoScanCodes(ctx context.Context, domain string) (current, previous seo.ScanCodes, err error) {
	rows, err := s.db.QueryContext(ctx,
		`select id, started_at from seo_scans
		 where domain = ? and `+seoScanHasPages("seo_scans")+`
		 order by id desc
		 limit 2`, domain)
	if err != nil {
		return seo.ScanCodes{}, seo.ScanCodes{}, err
	}

	// Курсор закрывается явно, а не через defer: соединение к базе одно, и
	// открыть второй запрос, пока первый не дочитан, значит встать намертво
	// самим на себе.
	type scanRow struct {
		id        int64
		startedAt int64
	}
	var found []scanRow
	for rows.Next() {
		var row scanRow
		if err := rows.Scan(&row.id, &row.startedAt); err != nil {
			rows.Close()
			return seo.ScanCodes{}, seo.ScanCodes{}, err
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return seo.ScanCodes{}, seo.ScanCodes{}, err
	}
	rows.Close()

	load := func(row scanRow) (seo.ScanCodes, error) {
		result := seo.ScanCodes{
			CheckedAt: time.Unix(row.startedAt, 0).UTC().Format(time.RFC3339),
			Pages:     map[string][]string{},
		}

		pageRows, err := s.db.QueryContext(ctx,
			`select url, codes from seo_pages where scan_id = ?`, row.id)
		if err != nil {
			return seo.ScanCodes{}, err
		}
		defer pageRows.Close()

		for pageRows.Next() {
			var url, codes string
			if err := pageRows.Scan(&url, &codes); err != nil {
				return seo.ScanCodes{}, err
			}
			result.Pages[url] = strings.Fields(codes)
		}
		return result, pageRows.Err()
	}

	if len(found) > 0 {
		if current, err = load(found[0]); err != nil {
			return seo.ScanCodes{}, seo.ScanCodes{}, err
		}
	}
	if len(found) > 1 {
		if previous, err = load(found[1]); err != nil {
			return seo.ScanCodes{}, seo.ScanCodes{}, err
		}
	}
	return current, previous, nil
}

// ForgetSeoScans стирает историю сайтов, которых больше нет в списке.
func (s *Store) ForgetSeoScans(ctx context.Context, alive map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `select distinct domain from seo_scans`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return err
		}
		if !alive[domain] {
			stale = append(stale, domain)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, domain := range stale {
		if _, err := s.db.ExecContext(ctx, `delete from seo_scans where domain = ?`, domain); err != nil {
			return err
		}
	}

	// Осиротевшие снимки страниц: их владельцев уже нет.
	_, err = s.db.ExecContext(ctx,
		`delete from seo_pages where scan_id not in (select id from seo_scans)`)
	return err
}

// LatestSeoAt — когда обходили в прошлый раз.
//
// Нужно на старте: обход стоит сотен запросов к чужому сайту, и перезапуск
// агента после правки настроек не повод начинать его заново.
func (s *Store) LatestSeoAt(ctx context.Context) (time.Time, bool, error) {
	var startedAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`select max(started_at) from seo_scans`).Scan(&startedAt); err != nil {
		return time.Time{}, false, err
	}
	if !startedAt.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(startedAt.Int64, 0).UTC(), true, nil
}
