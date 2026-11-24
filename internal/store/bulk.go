package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The states of a bulk job.
const (
	JobQueued      = "queued"
	JobRunning     = "running"
	JobDone        = "done"
	JobInterrupted = "interrupted"
	// JobFailed: the job broke (a panic, or its progress could not be written);
	// failed lists what was being done.
	JobFailed = "failed"
)

// BulkFailure is one item of a bulk job that did not work, and why.
type BulkFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// BulkJob is a row of bulk_jobs.
type BulkJob struct {
	ID, TenantID, MadeBy, Action string
	Total, Done                  int
	Failed                       []BulkFailure
	State                        string
	// SiteOnly: a site administrator started it; only site administrators see
	// it (as such an export, see Export.SiteOnly).
	SiteOnly         bool
	Created, Updated time.Time
}

const bulkCols = `id, tenant_id, made_by, action, total, done, failed, state, created, updated`

func scanJob(sc interface{ Scan(...any) error }) (*BulkJob, error) {
	var j BulkJob
	var failed string
	var created, updated int64
	if err := sc.Scan(&j.ID, &j.TenantID, &j.MadeBy, &j.Action, &j.Total, &j.Done, &failed, &j.State, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.Created, j.Updated = fromMS(created), fromMS(updated)
	_ = json.Unmarshal([]byte(failed), &j.Failed)
	return &j, nil
}

// InsertBulkJob adds a job.
func InsertBulkJob(ctx context.Context, q Q, j BulkJob) error {
	_, err := q.Exec(ctx, `INSERT INTO bulk_jobs(`+bulkCols+`, site_only) VALUES (?, ?, ?, ?, ?, ?, '[]', ?, ?, ?, ?)`,
		j.ID, j.TenantID, j.MadeBy, j.Action, j.Total, j.Done, j.State, ms(j.Created), ms(j.Created), b2i(j.SiteOnly))
	return err
}

// GetBulkJob returns a job of a tenant in the scope; a tenant's scope does not
// see the site's jobs (SiteOnly).
func GetBulkJob(ctx context.Context, q Q, sc Scope, id string) (*BulkJob, error) {
	cond, args := sc.cond("tenant_id")
	if !sc.all {
		cond += " AND site_only = 0"
	}
	return scanJob(q.QueryRow(ctx, `SELECT `+bulkCols+` FROM bulk_jobs WHERE id = ? AND `+cond, append([]any{id}, args...)...))
}

// UpdateBulkJob records how far a job is.
func UpdateBulkJob(ctx context.Context, q Q, id, state string, done int, failed []BulkFailure, now time.Time) error {
	b, _ := json.Marshal(failed)
	if failed == nil {
		b = []byte("[]")
	}
	res, err := q.Exec(ctx, `UPDATE bulk_jobs SET state = ?, done = ?, failed = ?, updated = ? WHERE id = ?`, state, done, string(b), ms(now), id)
	return oneRow(res, err)
}

// BulkJobKeep is how long a job that ended (done, failed, interrupted) is kept
// after its last change: GET /api/admin/jobs/{id} answers it until then.
const BulkJobKeep = 30 * 24 * time.Hour

// PruneBulkJobs deletes the jobs that ended before the given time (the daily
// pass; a queued or running job is never deleted). It reads bulk_jobs_updated.
func PruneBulkJobs(ctx context.Context, q Q, before time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM bulk_jobs WHERE updated < ? AND state NOT IN ('queued', 'running')`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InterruptBulkJobs marks the jobs that were queued or running when the server
// stopped as interrupted: what was not done is not done.
func InterruptBulkJobs(ctx context.Context, q Q, now time.Time) (int64, error) {
	res, err := q.Exec(ctx, `UPDATE bulk_jobs SET state = 'interrupted', updated = ? WHERE state IN ('queued', 'running')`, ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TenantsOf returns the tenant of each of the spaces, people or recycle items
// (kind "spaces", "users", "items") with these ids, whoever they belong to: what
// a bulk job works on, for its queue. Ids that are not there are left out.
func TenantsOf(ctx context.Context, q Q, kind string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var query string
	switch kind {
	case "spaces":
		query = `SELECT id, tenant_id FROM spaces WHERE id IN (` + ph + `)`
	case "users":
		query = `SELECT id, tenant_id FROM users WHERE deleted_at IS NULL AND id IN (` + ph + `)`
	case "items":
		query = `SELECT id, tenant_id FROM recycle_items WHERE id IN (` + ph + `)`
	default:
		return out, nil
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}
