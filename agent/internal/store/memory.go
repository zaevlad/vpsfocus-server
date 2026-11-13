package store

// Память (трек AH, спринт 265): подкачка к замеру, счётчик остановок ядра и
// сами остановки. Своими таблицами: миграций у агента нет, alter table на
// живой базе клиента не выполняется (Р5).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"agent/internal/metrics"
)

const memorySchema = `
-- Подкачка к замеру (Р61) по образцу inode_samples: ключ — время замера,
-- замер без строки здесь — «не знаем».
create table if not exists swap_samples (
    at            integer primary key,
    swap_total_mb integer not null,
    swap_free_mb  integer not null
);

-- Последнее увиденное значение счётчика oom_kill (П2). В базе, а не в
-- памяти: остановленной программой бывает и сам агент, и после его
-- перезапуска сравнивать надо с тем, что было до, а не молчать.
create table if not exists oom_counter (
    id    integer primary key check (id = 1),
    value integer not null,
    at    integer not null
);

-- Замеченные остановки: когда, сколько и кто занимал память сразу после.
-- top — JSON списка имён и мегабайт; пусто — не узнали (hidepid).
create table if not exists oom_kills (
    id        integer primary key autoincrement,
    at        integer not null,
    count     integer not null,
    top       text    not null default '',
    top_known integer not null default 0
);

create index if not exists idx_oom_kills_at on oom_kills(at);
`

// OomCounter — последнее сохранённое значение счётчика. false — ещё ни
// разу не видели (первый запуск агента на сервере).
func (s *Store) OomCounter(ctx context.Context) (uint64, bool, error) {
	var value int64
	err := s.db.QueryRowContext(ctx, `select value from oom_counter where id = 1`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return uint64(value), true, nil
}

func (s *Store) SaveOomCounter(ctx context.Context, value uint64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`insert or replace into oom_counter (id, value, at) values (1, ?, ?)`,
		int64(value), at.UTC().Unix())
	return err
}

func (s *Store) SaveOomKill(ctx context.Context, kill metrics.OomKill) error {
	top, known := "", 0
	if kill.TopKnown {
		list := kill.Top
		if list == nil {
			list = []metrics.Process{}
		}
		encoded, err := json.Marshal(list)
		if err != nil {
			return err
		}
		top, known = string(encoded), 1
	}
	_, err := s.db.ExecContext(ctx,
		`insert into oom_kills (at, count, top, top_known) values (?, ?, ?, ?)`,
		kill.At.UTC().Unix(), kill.Count, top, known)
	return err
}

// OomKills — остановки с указанного момента, от новых к старым.
func (s *Store) OomKills(ctx context.Context, since time.Time) ([]metrics.OomKill, error) {
	rows, err := s.db.QueryContext(ctx,
		`select at, count, top, top_known from oom_kills where at >= ? order by at desc, id desc`,
		since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []metrics.OomKill{}
	for rows.Next() {
		var at int64
		var top string
		var known int
		var kill metrics.OomKill
		if err := rows.Scan(&at, &kill.Count, &top, &known); err != nil {
			return nil, err
		}
		kill.At = time.Unix(at, 0).UTC()
		kill.TopKnown = known == 1
		if kill.TopKnown && top != "" {
			_ = json.Unmarshal([]byte(top), &kill.Top)
		}
		list = append(list, kill)
	}
	return list, rows.Err()
}

// rotateMemory убирает подкачку и остановки старше срока — тем же сроком,
// что замеры.
func (s *Store) rotateMemory(ctx context.Context, cutoff int64) error {
	if _, err := s.db.ExecContext(ctx, `delete from swap_samples where at < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `delete from oom_kills where at < ?`, cutoff)
	return err
}

// withSwap кладёт подкачку из необязательных столбцов в замер: нет строки
// в swap_samples — «не знаем», а не ноль (Р61).
func withSwap(sample *metrics.Sample, total, free sql.NullInt64) {
	if total.Valid && free.Valid {
		t, f := uint64(total.Int64), uint64(free.Int64)
		sample.SwapTotalMB, sample.SwapFreeMB = &t, &f
	}
}
