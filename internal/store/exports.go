package store

// Exports (milestone 3.5, docs/api.md "Exports"): a zip of what some spaces
// hold, made on request and kept for exports.days.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// The states of an export.
const (
	ExportQueued  = "queued"
	ExportRunning = "running"
	ExportReady   = "ready"
	ExportFailed  = "failed"
)

// Export is a row of exports.
type Export struct {
	ID, TenantID, MadeBy, MadeByName string
	// Spaces are the ids of the spaces and the recycle items it is made of, as
	// the request named them (see ExportSource).
	Spaces  []ExportSource
	History bool
	// SiteOnly: a site administrator made it. Only site administrators see it,
	// whatever its tenant (it may hold a personal space, a history bundle, or
	// spaces of several tenants).
	SiteOnly bool
	State    string
	Error    string
	Bytes    int64
	// Path is the file, relative to the data directory.
	Path    string
	Created time.Time
	Expires time.Time
}

// ExportSource is one thing an export holds: a space (Space), or the space of a
// recycle item (Item).
type ExportSource struct {
	Space string `json:"space,omitempty"`
	Item  string `json:"item,omitempty"`
	Name  string `json:"name,omitempty"`
}

const exportCols = `id, tenant_id, made_by, made_by_name, spaces, history, site_only, state, error, bytes, path, created, expires`

func scanExport(sc interface{ Scan(...any) error }) (*Export, error) {
	var e Export
	var spaces string
	var history, siteOnly, created, expires int64
	if err := sc.Scan(&e.ID, &e.TenantID, &e.MadeBy, &e.MadeByName, &spaces, &history, &siteOnly, &e.State, &e.Error, &e.Bytes, &e.Path, &created, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.History, e.SiteOnly, e.Created, e.Expires = history != 0, siteOnly != 0, fromMS(created), fromMS(expires)
	_ = json.Unmarshal([]byte(spaces), &e.Spaces)
	return &e, nil
}

// InsertExport adds an export.
func InsertExport(ctx context.Context, q Q, e Export) error {
	src, _ := json.Marshal(e.Spaces)
	_, err := q.Exec(ctx, `INSERT INTO exports(`+exportCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TenantID, e.MadeBy, e.MadeByName, string(src), b2i(e.History), b2i(e.SiteOnly), e.State, e.Error, e.Bytes, e.Path, ms(e.Created), ms(e.Expires))
	return err
}

// exportCond is which exports the scope sees: a tenant's are those of the
// tenant that are not the site's (SiteOnly).
func exportCond(sc Scope) (string, []any) {
	cond, args := sc.cond("tenant_id")
	if !sc.all {
		cond += " AND site_only = 0"
	}
	return cond, args
}

// GetExport returns an export the scope sees (see exportCond).
func GetExport(ctx context.Context, q Q, sc Scope, id string) (*Export, error) {
	cond, args := exportCond(sc)
	return scanExport(q.QueryRow(ctx, `SELECT `+exportCols+` FROM exports WHERE id = ? AND `+cond, append([]any{id}, args...)...))
}

// ListExports returns a page of the exports of the scope, newest first (the
// cursor is "<created ms>:<id>").
func ListExports(ctx context.Context, q Q, sc Scope, after string, limit int) (out []Export, next string, err error) {
	if limit <= 0 {
		limit = 50
	}
	cond, args := exportCond(sc)
	where := []string{cond}
	if after != "" {
		at, id, ok := strings.Cut(after, ":")
		n, perr := strconv.ParseInt(at, 10, 64)
		if !ok || perr != nil {
			return nil, "", ErrBadCursor
		}
		where = append(where, `(created < ? OR (created = ? AND id < ?))`)
		args = append(args, n, n, id)
	}
	args = append(args, limit+1)
	rows, err := q.Query(ctx, `SELECT `+exportCols+` FROM exports WHERE `+strings.Join(where, " AND ")+` ORDER BY created DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = strconv.FormatInt(ms(last.Created), 10) + ":" + last.ID
	}
	return out, next, nil
}

// SetExportState records where an export is: its state, error, size and file.
func SetExportState(ctx context.Context, q Q, id, state, errText string, bytes int64, path string) error {
	res, err := q.Exec(ctx, `UPDATE exports SET state = ?, error = ?, bytes = ?, path = ? WHERE id = ?`, state, errText, bytes, path, id)
	return oneRow(res, err)
}

// DeleteExport removes an export's row.
func DeleteExport(ctx context.Context, q Q, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM exports WHERE id = ?`, id)
	return oneRow(res, err)
}

// ExpiredExports lists the exports whose time is up (the oldest first, at most
// limit), and the ones a restart found half made are listed by ExportsInFlight.
func ExpiredExports(ctx context.Context, q Q, now time.Time, limit int) ([]Export, error) {
	rows, err := q.Query(ctx, `SELECT `+exportCols+` FROM exports WHERE expires < ? AND state IN ('ready', 'failed') ORDER BY expires LIMIT ?`, ms(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Export
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ExportsInFlight lists the exports that are queued or running: after a
// restart they are not (nothing resumes them).
func ExportsInFlight(ctx context.Context, q Q) ([]Export, error) {
	rows, err := q.Query(ctx, `SELECT `+exportCols+` FROM exports WHERE state IN ('queued', 'running')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Export
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ExportBytes is the size of every export on the server that is made or being
// made: what counts against exports.maxBytes. Running exports count what they
// have written so far.
func ExportBytes(ctx context.Context, q Q) (n int64, err error) {
	err = q.QueryRow(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM exports WHERE state IN ('ready', 'running')`).Scan(&n)
	return
}
