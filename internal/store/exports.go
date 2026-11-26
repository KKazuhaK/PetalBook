package store

// Exports (milestone 3.5, docs/api.md "Exports"): a zip of what some spaces
// hold, made on request and kept for exports.days. An export is a job of kind
// "export" (internal/jobs): its params are an ExportParams, its result an
// ExportResult, its progress the sources done and the bytes written, and its
// expires the end of its days.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// JobExport is the kind of the jobs that make an export.
const JobExport = "export"

// ExportParams is what an export job's params hold.
type ExportParams struct {
	Spaces  []ExportSource `json:"spaces"`
	History bool           `json:"history,omitempty"`
}

// ExportResult is what an export job's result holds once it is done.
type ExportResult struct {
	// Path is the file, relative to the data directory.
	Path string `json:"path"`
	// Warnings is what the export could not include, a line each.
	Warnings string `json:"warnings,omitempty"`
}

// ExportParamsOf reads the params of an export job.
func ExportParamsOf(j *Job) (p ExportParams) {
	_ = json.Unmarshal(j.Params, &p)
	return p
}

// ExportResultOf reads the result of an export job.
func ExportResultOf(j *Job) (r ExportResult) {
	_ = json.Unmarshal(j.Result, &r)
	return r
}

// ExportSource is one thing an export holds: a space (Space), or the space of a
// recycle item (Item).
type ExportSource struct {
	Space string `json:"space,omitempty"`
	Item  string `json:"item,omitempty"`
	Name  string `json:"name,omitempty"`
	// SpaceID is the space either way (for an item, the space it held), so that
	// a list of exports can link back to it while it exists.
	SpaceID string `json:"spaceId,omitempty"`
}

// OpenableSpaces says which of the spaces exist and are in the scope (a personal
// one never for a tenant administrator's scope, see AdminScopeOf): the ones an
// administrator with the scope may open. Spaces in the recycle bin do not exist.
// One query for each 500 ids.
func OpenableSpaces(ctx context.Context, q Q, sc Scope, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	const chunk = 500
	for len(ids) > 0 {
		n := min(chunk, len(ids))
		part := ids[:n]
		ids = ids[n:]
		cond, args := sc.spaceCond("tenant_id", "personal_of IS NULL")
		for _, id := range part {
			args = append(args, id)
		}
		rows, err := q.Query(ctx, `SELECT id FROM spaces WHERE `+cond+` AND id IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(part)), ", ")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ExportBytes is the size of every export on the server that is made or being
// made: what counts against exports.maxBytes. Running exports count what they
// have written so far (bytes_done).
func ExportBytes(ctx context.Context, q Q) (n int64, err error) {
	err = q.QueryRow(ctx, `SELECT COALESCE(SUM(bytes_done), 0) FROM jobs WHERE kind = 'export' AND state IN ('done', 'running')`).Scan(&n)
	return
}

// ExpiredExports lists the export jobs whose days are over and that have ended
// (the oldest first, at most limit): the daily pass deletes their files and
// rows. It reads jobs_expires.
func ExpiredExports(ctx context.Context, q Q, now time.Time, limit int) ([]*Job, error) {
	rows, err := q.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE kind = 'export' AND expires > 0 AND expires < ? AND state NOT IN ('queued', 'running') ORDER BY expires LIMIT ?`, ms(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
