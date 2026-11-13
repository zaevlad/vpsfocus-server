package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent/internal/ignore"
	"agent/internal/seo"
)

// «Не учитывать» (трек AG) и находки обходов SEO по страницам.
//
// Отдельной схемой, а не строками в общей: применяется тем же `if not
// exists` при каждом старте, миграций у агента по-прежнему нет.
const ignoreSchema = `
-- Записи «не учитывать»: находки, которые человек пометил «так задумано».
--
-- Бессрочно (Р2): срок остаётся у приглушения. Ключ — всё, кроме заметки и
-- времени (Р29), уникальным индексом: повтор от второй копии приложения —
-- «уже есть», а не дубль.
create table if not exists ignores (
    id         integer primary key autoincrement,
    kind       text    not null,
    domain     text    not null,
    what       text    not null,
    what_match text    not null default 'exact',
    response   text    not null default '',
    scope      text    not null,
    at         text    not null default '',
    note       text    not null default '',
    created_at integer not null
);

create unique index if not exists idx_ignores_key
    on ignores(kind, domain, what, what_match, response, scope, at);

-- Находки обхода SEO по страницам (Р34): код и путь, без содержимого.
--
-- Страницы целиком (seo_pages) хранятся у двух последних обходов, сводки —
-- у тридцати, и пересчитать историю по сегодняшнему списку «не учитывать»
-- без этой таблицы можно было бы в двух точках из тридцати. Живёт и
-- чистится вместе с seo_scans.
create table if not exists seo_findings (
    scan_id integer not null,
    code    text    not null,
    path    text    not null default ''
);

create index if not exists idx_seo_findings_scan
    on seo_findings(scan_id);

-- У каких обходов находки по страницам записаны. Отдельно от самих
-- находок: обход без единой находки — тоже записанный, и пустота в
-- seo_findings не отличила бы его от обхода агента старше этой таблицы.
create table if not exists seo_finding_sets (
    scan_id integer primary key
);
`

// Ignores отдаёт записи «не учитывать», новые сверху.
func (s *Store) Ignores(ctx context.Context) (ignore.List, error) {
	rows, err := s.db.QueryContext(ctx,
		`select kind, domain, what, what_match, response, scope, at, note, created_at
		 from ignores order by created_at desc, id desc`)
	if err != nil {
		return nil, fmt.Errorf("ignores: %w", err)
	}
	defer rows.Close()

	// Пустой список, а не nil: он уезжает в JSON, где null означал бы
	// «неизвестно».
	out := ignore.List{}
	for rows.Next() {
		var entry ignore.Entry
		var created int64
		if err := rows.Scan(&entry.Kind, &entry.Domain, &entry.What, &entry.Match,
			&entry.Response, &entry.Scope, &entry.At, &entry.Note, &created); err != nil {
			return nil, fmt.Errorf("reading ignore: %w", err)
		}
		entry.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, entry)
	}
	return out, rows.Err()
}

// AddIgnore кладёт запись. Такая уже есть — отдаёт прежнюю и existed = true
// (Р29); сверх предела — ignore.ErrLimit (Р15).
//
// Запись должна быть приведена `ignore.Normalize`: ключ сравнивается как
// есть.
func (s *Store) AddIgnore(ctx context.Context, entry ignore.Entry, now time.Time) (ignore.Entry, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ignore.Entry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		note    string
		created int64
	)
	err = tx.QueryRowContext(ctx,
		`select note, created_at from ignores
		 where kind = ? and domain = ? and what = ? and what_match = ?
		   and response = ? and scope = ? and at = ?`,
		entry.Kind, entry.Domain, entry.What, entry.Match, entry.Response, entry.Scope, entry.At,
	).Scan(&note, &created)
	switch {
	case err == nil:
		entry.Note = note
		entry.CreatedAt = time.Unix(created, 0).UTC()
		return entry, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return ignore.Entry{}, false, fmt.Errorf("ignore lookup: %w", err)
	}

	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from ignores`).Scan(&count); err != nil {
		return ignore.Entry{}, false, fmt.Errorf("ignore count: %w", err)
	}
	if count >= ignore.Limit {
		return ignore.Entry{}, false, ignore.ErrLimit
	}

	entry.CreatedAt = now.UTC().Truncate(time.Second)
	if _, err := tx.ExecContext(ctx,
		`insert into ignores (kind, domain, what, what_match, response, scope, at, note, created_at)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Kind, entry.Domain, entry.What, entry.Match, entry.Response, entry.Scope, entry.At,
		entry.Note, entry.CreatedAt.Unix(),
	); err != nil {
		return ignore.Entry{}, false, fmt.Errorf("adding ignore: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ignore.Entry{}, false, err
	}
	return entry, false, nil
}

// RemoveIgnore снимает запись. removed = false — такой не было: вторая
// копия приложения успела раньше, и это не ошибка.
func (s *Store) RemoveIgnore(ctx context.Context, key ignore.Key) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`delete from ignores
		 where kind = ? and domain = ? and what = ? and what_match = ?
		   and response = ? and scope = ? and at = ?`,
		key.Kind, key.Domain, key.What, key.Match, key.Response, key.Scope, key.At)
	if err != nil {
		return false, fmt.Errorf("removing ignore: %w", err)
	}
	removed, err := result.RowsAffected()
	return removed > 0, err
}

// ForgetIgnores убирает записи сайтов, которых больше нет в списке (Р30).
//
// Пустой список — не повод стирать (Р37): так выглядит и не прочитанный
// файл, и записанный наполовину, и одна неудачная запись файла иначе молча
// стёрла бы все отметки сервера, а вернуть их нечем. Решать, что список
// годный, — вызывающему; здесь — последний сторож.
func (s *Store) ForgetIgnores(ctx context.Context, alive map[string]bool) error {
	if len(alive) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `select distinct domain from ignores`)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			rows.Close()
			return err
		}
		if !alive[domain] {
			gone = append(gone, domain)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, domain := range gone {
		if _, err := s.db.ExecContext(ctx, `delete from ignores where domain = ?`, domain); err != nil {
			return err
		}
	}
	return nil
}

// saveSeoFindings пишет находки обхода по страницам внутри его транзакции.
func saveSeoFindings(ctx context.Context, tx *sql.Tx, scanID int64, pages []seo.Page) error {
	if len(pages) == 0 {
		// Неудачная попытка: страниц нет, записанной её не считаем.
		return nil
	}
	for _, page := range pages {
		path := ignore.PagePath(page.URL)
		for _, issue := range page.Issues {
			if _, err := tx.ExecContext(ctx,
				`insert into seo_findings (scan_id, code, path) values (?, ?, ?)`,
				scanID, issue.Code, path); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `insert or ignore into seo_finding_sets (scan_id) values (?)`, scanID)
	return err
}

// forgetOrphanSeoFindings чистит находки обходов, которых больше нет.
func (s *Store) forgetOrphanSeoFindings(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`delete from seo_findings where scan_id not in (select id from seo_scans)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`delete from seo_finding_sets where scan_id not in (select id from seo_scans)`)
	return err
}

// seoScanFindings — коды находок обхода по страницам: из снимков страниц,
// если они ещё хранятся, иначе из seo_findings. ok = false — не сохранилось
// ни того, ни другого (обход агента старше Р34).
//
// Ключ — адрес страницы или её путь: записи «не учитывать» приводят оба к
// одному виду сами (`ignore.PagePath`).
func (s *Store) seoScanFindings(ctx context.Context, scanID int64) (map[string][]string, bool, error) {
	pages, err := s.codesFrom(ctx, `select url, codes from seo_pages where scan_id = ?`, scanID, true)
	if err != nil || len(pages) > 0 {
		return pages, err == nil, err
	}

	var recorded int
	if err := s.db.QueryRowContext(ctx,
		`select count(*) from seo_finding_sets where scan_id = ?`, scanID).Scan(&recorded); err != nil {
		return nil, false, err
	}
	if recorded == 0 {
		return nil, false, nil
	}
	pages, err = s.codesFrom(ctx, `select path, code from seo_findings where scan_id = ?`, scanID, false)
	if err != nil {
		return nil, false, err
	}
	if pages == nil {
		pages = map[string][]string{}
	}
	return pages, true, nil
}

// codesFrom читает пары «страница — коды»: либо строкой кодов на страницу
// (`spaced`), либо строкой на находку.
func (s *Store) codesFrom(ctx context.Context, query string, scanID int64, spaced bool) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, query, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out map[string][]string
	for rows.Next() {
		var page, codes string
		if err := rows.Scan(&page, &codes); err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string][]string{}
		}
		if spaced {
			out[page] = append(out[page], strings.Fields(codes)...)
		} else {
			out[page] = append(out[page], codes)
		}
	}
	return out, rows.Err()
}

// seoPagesSkipping — SeoPages с неучтёнными находками: отбор и окно по
// учтённым находкам каждой страницы.
func (s *Store) seoPagesSkipping(
	ctx context.Context,
	scanID int64,
	severity, code string,
	limit, offset int,
	skip seo.Skip,
) ([]seo.Page, int, error) {
	// Находку SQL отбирает по сырым кодам — это надмножество, дальше
	// уточняет Go.
	filter, arguments := "", []any{scanID}
	if code = seo.KnownCode(code); code != "" {
		filter = " and codes like ?"
		arguments = append(arguments, "% "+code+" %")
	}

	rows, err := s.db.QueryContext(ctx,
		`select payload from seo_pages where scan_id = ?`+filter+` order by depth, url`,
		arguments...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	levels := map[string]bool{}
	for _, level := range seo.AtLeast(severity) {
		levels[level] = true
	}

	pages := []seo.Page{}
	total := 0
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, 0, err
		}
		var page seo.Page
		if err := json.Unmarshal([]byte(payload), &page); err != nil {
			return nil, 0, err
		}
		page = page.Split(skip)

		if len(levels) > 0 && !levels[seo.Worst(page.Issues)] {
			continue
		}
		if code != "" && !hasCode(page.Issues, code) {
			continue
		}
		if total >= offset && len(pages) < limit {
			pages = append(pages, page)
		}
		total++
	}
	return pages, total, rows.Err()
}

func hasCode(issues []seo.Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
