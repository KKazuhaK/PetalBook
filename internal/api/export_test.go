package api

import "context"

// HeaderPurpose is what a stored header value is bound to, for tests that
// write settings as an earlier version saved them.
var HeaderPurpose = headerPurpose

// SetSyncPagesBytes changes how much of the pages one answer of sync/pages
// holds, and returns what it was.
func SetSyncPagesBytes(n int) int { old := syncPagesBytes; syncPagesBytes = n; return old }

// IndexOnlyRoutes lists the routes an evicted space answers from its index.
func IndexOnlyRoutes() []string {
	out := make([]string, 0, len(indexOnlyRoutes))
	for r := range indexOnlyRoutes {
		out = append(out, r)
	}
	return out
}

// SetUpdateBulkJob makes the writes of the progress of the jobs that the user
// starts go through fail first (state: the state written, try: 0 for the first
// try); fail returning nil writes. It returns what undoes it.
func SetUpdateBulkJob(userID string, fail func(state string, try int) error) func() {
	jobWriteFaults.Store(userID, fail)
	return func() { jobWriteFaults.Delete(userID) }
}

// SetBulkJobHook calls f at named points of the jobs that the user starts:
// "item" before each item (with its id, "*" for an export), and "end" once the
// job has ended and its last state is written. ctx is the job's: it ends when
// the handler is closed. It returns what undoes it.
func SetBulkJobHook(userID string, f func(ctx context.Context, point, item string)) func() {
	jobHooks.Store(userID, f)
	return func() { jobHooks.Delete(userID) }
}
