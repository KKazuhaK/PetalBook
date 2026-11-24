package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The states of a bulk job.
const (
	JobQueued      = "queued"
	JobRunning     = "running"
	JobDone        = "done"
	JobInterrupted = "interrupted"
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
	Created, Updated             time.Time
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
	_, err := q.Exec(ctx, `INSERT INTO bulk_jobs(`+bulkCols+`) VALUES (?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)`,
		j.ID, j.TenantID, j.MadeBy, j.Action, j.Total, j.Done, j.State, ms(j.Created), ms(j.Created))
	return err
}

// GetBulkJob returns a job of a tenant in the scope.
func GetBulkJob(ctx context.Context, q Q, sc Scope, id string) (*BulkJob, error) {
	cond, args := sc.cond("tenant_id")
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

// InterruptBulkJobs marks the jobs that were queued or running when the server
// stopped as interrupted: what was not done is not done.
func InterruptBulkJobs(ctx context.Context, q Q, now time.Time) (int64, error) {
	res, err := q.Exec(ctx, `UPDATE bulk_jobs SET state = 'interrupted', updated = ? WHERE state IN ('queued', 'running')`, ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
