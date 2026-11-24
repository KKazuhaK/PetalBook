package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// CompareAndSetSiteSetting replaces a server-wide setting only if it still has
// the value that was read (old), and says whether it did. A writer that read
// the setting some time ago uses it so as never to overwrite a newer value.
func CompareAndSetSiteSetting(ctx context.Context, q Q, key, old, value string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE site_settings SET value = ?, updated = ? WHERE key = ? AND value = ?`, value, ms(now), key, old)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DailyLine is one line of text of the daily text's own list: the text and
// where it is from, in a language ("" for any).
type DailyLine struct {
	Lang   string
	Body   string
	Author string
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
		if _, err := q.Exec(ctx, `INSERT INTO daily_lines(position, lang, body, author, source) VALUES (?, ?, ?, ?, ?)`, last+int64(i)+1, lang, l.Body, l.Author, l.Source); err != nil {
			return 0, err
		}
	}
	return n + len(lines), nil
}

// DailyLines are the lines of one language, in the order they were added.
func DailyLines(ctx context.Context, q Q, lang string) ([]DailyLine, error) {
	rows, err := q.Query(ctx, `SELECT body, author, source FROM daily_lines WHERE lang = ? ORDER BY position`, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DailyLine{}
	for rows.Next() {
		l := DailyLine{Lang: lang}
		if err := rows.Scan(&l.Body, &l.Author, &l.Source); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
	err := q.QueryRow(ctx, `SELECT body, author, source FROM daily_lines WHERE lang = ? ORDER BY position LIMIT 1 OFFSET ?`, lang, n).Scan(&l.Body, &l.Author, &l.Source)
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

// SpaceChoice is the external interfaces a space uses, in order, and the
// options chosen for each (interface id, option key, values). Stored in
// SpaceDaily.APIs as the JSON list of the ids when there are no options (the
// first form), else as {"apis": [...], "options": {...}}.
type SpaceChoice struct {
	APIs    []string
	Options map[string]map[string][]string
}

// ParseSpaceChoice reads what SpaceDaily.APIs holds; what cannot be read is no choice.
func ParseSpaceChoice(raw string) SpaceChoice {
	var c SpaceChoice
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) == nil {
		c.APIs = ids
		return c
	}
	var obj struct {
		APIs    []string                       `json:"apis"`
		Options map[string]map[string][]string `json:"options"`
	}
	if json.Unmarshal([]byte(raw), &obj) == nil {
		c.APIs, c.Options = obj.APIs, obj.Options
	}
	return c
}

// JSON is the choice as SpaceDaily.APIs keeps it.
func (c SpaceChoice) JSON() string {
	ids := c.APIs
	if ids == nil {
		ids = []string{}
	}
	if len(c.Options) == 0 {
		b, _ := json.Marshal(ids)
		return string(b)
	}
	b, _ := json.Marshal(struct {
		APIs    []string                       `json:"apis"`
		Options map[string]map[string][]string `json:"options"`
	}{ids, c.Options})
	return string(b)
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

// RenameSpaceDailyAPI makes the spaces that chose the interface from choose
// the interface to instead, in the place it had. It reads the rows that
// mention it (a LIKE on the JSON, then an exact check) and writes those only.
func RenameSpaceDailyAPI(ctx context.Context, q Q, from, to string) error {
	rows, err := q.Query(ctx, `SELECT space_id, apis FROM space_daily WHERE apis LIKE ? ESCAPE '\'`, "%"+likeEscape(from)+"%")
	if err != nil {
		return err
	}
	type change struct{ id, apis string }
	var changes []change
	for rows.Next() {
		var id, apis string
		if err := rows.Scan(&id, &apis); err != nil {
			rows.Close()
			return err
		}
		c := ParseSpaceChoice(apis)
		out, changed, seen := make([]string, 0, len(c.APIs)), false, map[string]bool{}
		for _, v := range c.APIs {
			if v == from {
				v, changed = to, true
			}
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
		if o, ok := c.Options[from]; ok {
			opts := map[string]map[string][]string{}
			for k, v := range c.Options {
				opts[k] = v
			}
			delete(opts, from)
			if _, taken := opts[to]; !taken {
				opts[to] = o
			}
			c.Options, changed = opts, true
		}
		if changed {
			c.APIs = out
			changes = append(changes, change{id, c.JSON()})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, c := range changes {
		if _, err := q.Exec(ctx, `UPDATE space_daily SET apis = ? WHERE space_id = ?`, c.apis, c.id); err != nil {
			return err
		}
	}
	return nil
}

// DailyCached is one thing an external interface said.
type DailyCached struct {
	Body    string // JSON
	Fetched time.Time
}

// GetDailyCache returns what was kept for an interface, language, period and skip.
func GetDailyCache(ctx context.Context, q Q, apiKey, lang, period string, skip int) (DailyCached, bool, error) {
	var c DailyCached
	var at int64
	err := q.QueryRow(ctx, `SELECT body, fetched FROM daily_cache WHERE api_key = ? AND lang = ? AND period = ? AND skip = ?`, apiKey, lang, period, skip).Scan(&c.Body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	c.Fetched = fromMS(at)
	return c, err == nil, err
}

// PutDailyCache keeps what an interface said.
func PutDailyCache(ctx context.Context, q Q, apiKey, lang, period string, skip int, c DailyCached) error {
	_, err := q.Exec(ctx, `INSERT INTO daily_cache(api_key, lang, period, skip, body, fetched) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (api_key, lang, period, skip) DO UPDATE SET body = excluded.body, fetched = excluded.fetched`, apiKey, lang, period, skip, c.Body, ms(c.Fetched))
	return err
}

// LatestDailyCache returns the newest thing kept for an interface and language.
func LatestDailyCache(ctx context.Context, q Q, apiKey, lang string) (DailyCached, bool, error) {
	var c DailyCached
	var at int64
	err := q.QueryRow(ctx, `SELECT body, fetched FROM daily_cache WHERE api_key = ? AND lang = ? ORDER BY fetched DESC LIMIT 1`, apiKey, lang).Scan(&c.Body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	c.Fetched = fromMS(at)
	return c, err == nil, err
}

// PruneDailyCache forgets what was kept before the time.
func PruneDailyCache(ctx context.Context, q Q, before time.Time) error {
	_, err := q.Exec(ctx, `DELETE FROM daily_cache WHERE fetched < ?`, ms(before))
	return err
}

// TrimDailyCache keeps the newest keep answers of an interface (and those
// fetched at the same time as the last of them) and forgets the rest. Cost:
// keep entries of an index.
func TrimDailyCache(ctx context.Context, q Q, apiKey string, keep int) error {
	_, err := q.Exec(ctx, `DELETE FROM daily_cache WHERE api_key = ? AND fetched < (
		SELECT fetched FROM daily_cache WHERE api_key = ? ORDER BY fetched DESC LIMIT 1 OFFSET ?)`, apiKey, apiKey, keep-1)
	return err
}

// LockSiteSetting holds a server-wide setting until the end of the
// transaction q is in, so that one change at a time reads and writes it; a
// setting never set is made, with the value "{}".
func LockSiteSetting(ctx context.Context, q Q, key string, now time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO site_settings(key, value, updated) VALUES (?, '{}', ?) ON CONFLICT (key) DO NOTHING`, key, ms(now)); err != nil {
		return err
	}
	res, err := q.Exec(ctx, `UPDATE site_settings SET key = key WHERE key = ?`, key)
	return oneRow(res, err)
}
