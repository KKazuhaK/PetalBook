package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Limits of the lines an administrator gives as a source of the daily text.
const (
	MaxDailyLines     = 5000
	MaxDailyLineBytes = 8000
)

// ErrTooManyLines is the refusal of lines beyond MaxDailyLines.
var ErrTooManyLines = errors.New("too many lines")

// GetSiteSetting returns a server-wide setting (JSON text); ok is false when
// it was never set.
func GetSiteSetting(ctx context.Context, q Q, key string) (value string, ok bool, err error) {
	err = q.QueryRow(ctx, `SELECT value FROM site_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSiteSetting stores a server-wide setting.
func SetSiteSetting(ctx context.Context, q Q, key, value string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO site_settings(key, value, updated) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated = excluded.updated`, key, value, ms(now))
	return err
}

// DailyLine is one line of text of the daily text's own list: the text and
// where it is from, in a language ("" for any).
type DailyLine struct {
	Lang   string
	Body   string
	Source string
}

// DailyLineCounts is how many lines there are in each language.
func DailyLineCounts(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT lang, COUNT(*) FROM daily_lines GROUP BY lang`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			return nil, err
		}
		out[lang] = n
	}
	return out, rows.Err()
}

// ReplaceDailyLines replaces the lines of one language (all the lines of the
// language, "" for those with none). Call it in a transaction. total is how
// many lines there are afterwards; more than MaxDailyLines is refused.
func ReplaceDailyLines(ctx context.Context, q Q, lang string, lines []DailyLine) (total int, err error) {
	if _, err := q.Exec(ctx, `DELETE FROM daily_lines WHERE lang = ?`, lang); err != nil {
		return 0, err
	}
	var n int
	var last int64
	if err := q.QueryRow(ctx, `SELECT COUNT(*), COALESCE(MAX(position), 0) FROM daily_lines`).Scan(&n, &last); err != nil {
		return 0, err
	}
	if n+len(lines) > MaxDailyLines {
		return 0, ErrTooManyLines
	}
	for i, l := range lines {
		if _, err := q.Exec(ctx, `INSERT INTO daily_lines(position, lang, body, source) VALUES (?, ?, ?, ?)`, last+int64(i)+1, lang, l.Body, l.Source); err != nil {
			return 0, err
		}
	}
	return n + len(lines), nil
}

// DeleteDailyLines removes the lines of one language.
func DeleteDailyLines(ctx context.Context, q Q, lang string) error {
	_, err := q.Exec(ctx, `DELETE FROM daily_lines WHERE lang = ?`, lang)
	return err
}

// NthDailyLine returns the n-th line (from 0, in the order they were added)
// of a language. One read on the index of the language.
func NthDailyLine(ctx context.Context, q Q, lang string, n int) (DailyLine, error) {
	var l DailyLine
	l.Lang = lang
	err := q.QueryRow(ctx, `SELECT body, source FROM daily_lines WHERE lang = ? ORDER BY position LIMIT 1 OFFSET ?`, lang, n).Scan(&l.Body, &l.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// SpaceDaily is what the owner of a space chose of the server's daily text.
type SpaceDaily struct {
	// Inherit: with no source of its own the space shows the server's default.
	Inherit bool
	// APIs are the ids of the external interfaces the space uses, as JSON.
	APIs string
}

// GetSpaceDaily returns the choice of a space; the default (inherit, no
// interface) for a space that never chose.
func GetSpaceDaily(ctx context.Context, q Q, spaceID string) (SpaceDaily, error) {
	var inherit int64
	var apis string
	err := q.QueryRow(ctx, `SELECT inherit, apis FROM space_daily WHERE space_id = ?`, spaceID).Scan(&inherit, &apis)
	if errors.Is(err, sql.ErrNoRows) {
		return SpaceDaily{Inherit: true, APIs: "[]"}, nil
	}
	return SpaceDaily{Inherit: inherit != 0, APIs: apis}, err
}

// SetSpaceDaily stores the choice of a space.
func SetSpaceDaily(ctx context.Context, q Q, spaceID string, d SpaceDaily) error {
	_, err := q.Exec(ctx, `INSERT INTO space_daily(space_id, inherit, apis) VALUES (?, ?, ?)
		ON CONFLICT (space_id) DO UPDATE SET inherit = excluded.inherit, apis = excluded.apis`, spaceID, b2i(d.Inherit), d.APIs)
	return err
}
