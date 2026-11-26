package api

import (
	"context"
	"net/http"
)

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

// AccessDecision runs the access decision of one route on r, with a handler
// that only answers 299: what the middleware decides, and nothing the handler
// would do. The path values of r are set by the caller (no mux matches here).
func AccessDecision(h *Handler, method, pattern string, w http.ResponseWriter, r *http.Request) {
	for _, rt := range routeTable {
		if rt.method == method && rt.pattern == pattern {
			rt.h = func(_ *server, c *call) { c.w.WriteHeader(299) }
			h.s.handle(rt)(w, r)
			return
		}
	}
	panic("api: no route " + method + " " + pattern)
}

// CheckBoundaries takes in the starts and ends of rule windows up to the
// handler's clock, as the settings' timer does when it fires (a test's clock is
// moved by hand, so the real timer does not).
func (h *Handler) CheckBoundaries() { h.s.settings.CheckBoundaries(context.Background()) }
