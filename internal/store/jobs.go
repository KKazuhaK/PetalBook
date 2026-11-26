package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// The states of a job (the jobs table; internal/jobs runs them).
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	// JobInterrupted: the server stopped while it was queued or running, and
	// its kind does not take it up again; what was not done is not done.
	JobInterrupted = "interrupted"
	// JobFailed: the job broke (a panic, an error of its own, or its progress
	// could not be written); failed lists what was being done.
	JobFailed = "failed"
	// JobCancelled: someone stopped it.
	JobCancelled = "cancelled"
)

// JobEnded reports whether a job in this state has ended.
func JobEnded(state string) bool { return state != JobQueued && state != JobRunning }

// JobFailure is one item of a job that did not work, and why.
type JobFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// Job is a row of jobs.
type Job struct {
	ID, TenantID, Kind string
	// Subject is what the job works on (a space, a recycle item), or "" for
	// many.
	Subject string
	// LockKey, when set, allows one queued or running job per key.
	LockKey            string
	MadeBy, MadeByName string
	// Capacity is the acting scope the job was asked in: site_admin,
	// tenant_admin, or "" (the server's own work).
	Capacity string
	// SiteOnly: a site administrator started it; only site administrators see
	// it (as such an export, see Export.SiteOnly).
	SiteOnly bool
	// Params is the request, as its kind reads it (JSON).
	Params                json.RawMessage
	State                 string
	Total, Done           int64
	BytesTotal, BytesDone int64
	Failed                []JobFailure
	Error                 string
	Result                json.RawMessage
	Attempt               int
	Runner                string
	Created, Started      time.Time
	Updated, Expires      time.Time
	LeaseUntil            time.Time
}

const jobCols = `id, tenant_id, kind, subject, lock_key, made_by, made_by_name, capacity, site_only, params, state, total, done, bytes_total, bytes_done,
	failed, error, result, attempt, runner, lease_until, created, started, updated, expires`

func scanJob(sc interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var params, failed, result string
	var site int64
	var lease, created, started, updated, expires int64
	if err := sc.Scan(&j.ID, &j.TenantID, &j.Kind, &j.Subject, &j.LockKey, &j.MadeBy, &j.MadeByName, &j.Capacity, &site, &params, &j.State,
		&j.Total, &j.Done, &j.BytesTotal, &j.BytesDone, &failed, &j.Error, &result, &j.Attempt, &j.Runner, &lease, &created, &started, &updated, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	j.SiteOnly = site != 0
	j.Params, j.Result = json.RawMessage(params), json.RawMessage(result)
	_ = json.Unmarshal([]byte(failed), &j.Failed)
	j.LeaseUntil, j.Created, j.Started, j.Updated, j.Expires = msOrZero(lease), fromMS(created), msOrZero(started), fromMS(updated), msOrZero(expires)
	return &j, nil
}

func jsonOr(b json.RawMessage, empty string) string {
	if len(b) == 0 {
		return empty
	}
	return string(b)
}

func failedJSON(f []JobFailure) string {
	if len(f) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func msOrZeroOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

// ErrJobLocked is returned by InsertJob when a queued or running job holds the
// lock key already.
var ErrJobLocked = errors.New("store: another job holds the lock")

// InsertJob adds a job, queued unless State says otherwise. A job whose lock
// key a queued or running job holds is refused with ErrJobLocked (jobs_lock:
// two inserts at once, one wins).
func InsertJob(ctx context.Context, q Q, j Job) error {
	if j.State == "" {
		j.State = JobQueued
	}
	_, err := q.Exec(ctx, `INSERT INTO jobs(`+jobCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.TenantID, j.Kind, j.Subject, j.LockKey, j.MadeBy, j.MadeByName, j.Capacity, b2i(j.SiteOnly), jsonOr(j.Params, "{}"), j.State,
		j.Total, j.Done, j.BytesTotal, j.BytesDone, failedJSON(j.Failed), j.Error, jsonOr(j.Result, "{}"), j.Attempt, j.Runner, msOrZeroOf(j.LeaseUntil),
		ms(j.Created), msOrZeroOf(j.Started), ms(j.Created), msOrZeroOf(j.Expires))
	if err != nil && j.LockKey != "" && IsUnique(err) {
		return ErrJobLocked
	}
	return err
}

// GetJob returns a job of a tenant in the scope; a tenant's scope does not see
// the site's jobs (SiteOnly).
func GetJob(ctx context.Context, q Q, sc Scope, id string) (*Job, error) {
	cond, args := sc.cond("tenant_id")
	if !sc.all {
		cond += " AND site_only = 0"
	}
	return scanJob(q.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ? AND `+cond, append([]any{id}, args...)...))
}

// Sees reports whether the scope sees a job of this tenant (as GetJob does).
func (sc Scope) Sees(j *Job) bool {
	if sc.all {
		return true
	}
	return sc.tenant != "" && j.TenantID == sc.tenant && !j.SiteOnly
}

// ActiveJobs returns the queued and running jobs, oldest first. It reads
// jobs_active, which holds only them: a handful of rows.
func ActiveJobs(ctx context.Context, q Q) ([]*Job, error) {
	rows, err := q.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE state IN ('queued', 'running') ORDER BY state, created, id`)
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

// ClaimJob takes a queued job for the runner: one statement on the primary key,
// which only one of two runners asking at once wins (false for the other, and
// for a job that is not queued any more).
func ClaimJob(ctx context.Context, q Q, id, runner string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE jobs SET state = 'running', runner = ?, attempt = attempt + 1, started = ?, updated = ? WHERE id = ? AND state = 'queued'`,
		runner, ms(now), ms(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// JobProgress records how far a running job is. It changes no indexed column
// (not even updated), so on PostgreSQL it is a HOT update.
func JobProgress(ctx context.Context, q Q, id string, done, bytesDone int64, failed []JobFailure) error {
	res, err := q.Exec(ctx, `UPDATE jobs SET done = ?, bytes_done = ?, failed = ? WHERE id = ? AND state = 'running'`, done, bytesDone, failedJSON(failed), id)
	return oneRow(res, err)
}

// EndJob records how a job ended, from the state it was in (from): a job that
// another hand ended first (cancelled while queued, say) is left as it is, and
// ErrNotFound says so.
func EndJob(ctx context.Context, q Q, j Job, from string, now time.Time) error {
	res, err := q.Exec(ctx, `UPDATE jobs SET state = ?, done = ?, bytes_done = ?, total = ?, bytes_total = ?, failed = ?, error = ?, result = ?, runner = '', updated = ?
		WHERE id = ? AND state = ?`,
		j.State, j.Done, j.BytesDone, j.Total, j.BytesTotal, failedJSON(j.Failed), j.Error, jsonOr(j.Result, "{}"), ms(now), j.ID, from)
	return oneRow(res, err)
}

// RequeueJob puts a job that was running back in the queue (its kind takes it
// up again after a restart).
func RequeueJob(ctx context.Context, q Q, id string, now time.Time) error {
	res, err := q.Exec(ctx, `UPDATE jobs SET state = 'queued', runner = '', updated = ? WHERE id = ? AND state = 'running'`, ms(now), id)
	return oneRow(res, err)
}

// SetJobState moves a job from one state to another (a restart's outcome, a
// cancel of a queued job); ErrNotFound when it was not in from.
func SetJobState(ctx context.Context, q Q, id, from, to string, now time.Time) error {
	res, err := q.Exec(ctx, `UPDATE jobs SET state = ?, runner = '', updated = ? WHERE id = ? AND state = ?`, to, ms(now), id, from)
	return oneRow(res, err)
}

// JobKeep is how long a job that ended is kept after its last change: GET
// /api/admin/jobs/{id} answers it until then.
const JobKeep = 30 * 24 * time.Hour

// PruneJobs deletes the jobs that ended before the given time (the daily pass;
// a queued or running job is never deleted, nor an export, whose file its days
// decide (ExpiredExports), nor a job that holds a recycle item). It reads
// jobs_updated.
func PruneJobs(ctx context.Context, q Q, before time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM jobs WHERE updated < ? AND state NOT IN ('queued', 'running') AND expires = 0
		AND id NOT IN (SELECT job_id FROM recycle_items WHERE job_id <> '')`, ms(before))
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

// DeleteJob removes the row of a job that has ended (an export deleted or
// expired).
func DeleteJob(ctx context.Context, q Q, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM jobs WHERE id = ? AND state NOT IN ('queued', 'running')`, id)
	return oneRow(res, err)
}

// jobCond is which jobs the scope sees (as GetJob has it).
func jobCond(sc Scope) (string, []any) {
	cond, args := sc.cond("tenant_id")
	if !sc.all {
		cond += " AND site_only = 0"
	}
	return cond, args
}

// JobFilter picks a page of ListJobs.
type JobFilter struct {
	// Kinds, when set, are the kinds listed ("export", "bulk", ...).
	Kinds []string
	// State, when set, is the state listed.
	State string
	// After is the cursor of the page ("<created ms>:<id>"; "" for the first).
	After string
	Limit int
}

// ListJobs returns a page of the jobs the scope sees, newest first; next is the
// cursor of the following page. It reads jobs_tenant for a tenant; the table
// holds the jobs of the last month and the active ones, so the whole server's
// list is a scan of a few rows.
func ListJobs(ctx context.Context, q Q, sc Scope, f JobFilter) (out []*Job, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	cond, args := jobCond(sc)
	where := []string{cond}
	if len(f.Kinds) > 0 {
		where = append(where, `kind IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(f.Kinds)), ", ")+`)`)
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.State != "" {
		where, args = append(where, `state = ?`), append(args, f.State)
	}
	if f.After != "" {
		at, id, ok := strings.Cut(f.After, ":")
		n, perr := strconv.ParseInt(at, 10, 64)
		if !ok || perr != nil {
			return nil, "", ErrBadCursor
		}
		where = append(where, `(created < ? OR (created = ? AND id < ?))`)
		args = append(args, n, n, id)
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE `+strings.Join(where, " AND ")+` ORDER BY created DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = strconv.FormatInt(ms(last.Created), 10) + ":" + last.ID
	}
	return out, next, nil
}
