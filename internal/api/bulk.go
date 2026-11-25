package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Bulk actions (docs/api.md, "Confirmations and bulk actions"). A job runs the
// items of a confirmed action one at a time in the background, each with the
// rules of its single action: an item that fails is listed, the rest go on. At
// most one job per tenant and two on the server run at once; the others wait.
// A job is a row of bulk_jobs, so what was not done when the server stopped is
// read as interrupted.

const maxJobsAtOnce = 2

type jobRunner struct {
	mu      sync.Mutex
	cond    *sync.Cond
	tenants map[string]bool
	running int
}

func newJobRunner() *jobRunner {
	r := &jobRunner{tenants: map[string]bool{}}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// acquire waits for the turn of every tenant the job works on and a place on
// the server, and takes them all at once. It reports false, having taken
// nothing, when ctx ends first (wake makes a waiting acquire look at it).
func (r *jobRunner) acquire(ctx context.Context, tenants ...string) bool {
	r.mu.Lock()
	for r.running >= maxJobsAtOnce || r.anyTaken(tenants) {
		if ctx.Err() != nil {
			r.mu.Unlock()
			return false
		}
		r.cond.Wait()
	}
	if ctx.Err() != nil {
		r.mu.Unlock()
		return false
	}
	for _, t := range tenants {
		r.tenants[t] = true
	}
	r.running++
	r.mu.Unlock()
	return true
}

// wake makes the waiting acquires check their contexts again. It takes the lock
// so that an acquire between its check and its wait cannot miss it.
func (r *jobRunner) wake() {
	r.mu.Lock()
	r.cond.Broadcast()
	r.mu.Unlock()
}

func (r *jobRunner) anyTaken(tenants []string) bool {
	for _, t := range tenants {
		if r.tenants[t] {
			return true
		}
	}
	return false
}

func (r *jobRunner) release(tenants ...string) {
	r.mu.Lock()
	for _, t := range tenants {
		delete(r.tenants, t)
	}
	r.running--
	r.mu.Unlock()
	r.cond.Broadcast()
}

// jobWriteFaults makes the writes of a job's progress fail, by who started the
// job (tests; see SetUpdateBulkJob).
var jobWriteFaults sync.Map

// jobHooks are called at named points of the jobs a user starts ("item" before
// each item, with its id; "end" when the job has ended), by who started the job
// (tests; see SetBulkJobHook).
var jobHooks sync.Map

func jobHook(ctx context.Context, job *store.BulkJob, point, item string) {
	if f, ok := jobHooks.Load(job.MadeBy); ok {
		f.(func(context.Context, string, string))(ctx, point, item)
	}
}

// jobWriteTries is how many times a write of a job's progress is tried.
const jobWriteTries = 3

// writeJob records how far a job is, trying again a few times; an error is
// logged and returned (the caller marks the job failed when it can).
func (s *server) writeJob(job *store.BulkJob, state string, done int, failed []store.BulkFailure) error {
	var err error
	for try := 0; try < jobWriteTries; try++ {
		if try > 0 {
			time.Sleep(time.Duration(try) * 50 * time.Millisecond)
		}
		if f, ok := jobWriteFaults.Load(job.MadeBy); ok {
			if err = f.(func(string, int) error)(state, try); err != nil {
				continue
			}
		}
		if err = store.UpdateBulkJob(context.Background(), s.opt.Store.Q(), job.ID, state, done, failed, s.now()); err == nil {
			return nil
		}
	}
	s.log.Warn("could not record how far a bulk job is", "job", job.ID, "state", state, "error", err)
	return err
}

// jobTenants are the tenants a confirmed action works on: a job waits in the
// queue of each (a site administrator's job on one tenant is that tenant's
// turn, not their own tenant's). With none found, the caller's tenant.
func (s *server) jobTenants(ctx context.Context, req confirmRequest, caller store.User) ([]string, error) {
	set := map[string]bool{}
	add := func(kind string, ids []string) error {
		m, err := store.TenantsOf(ctx, s.opt.Store.Q(), kind, ids)
		for _, t := range m {
			set[t] = true
		}
		return err
	}
	if err := add("spaces", req.Spaces); err != nil {
		return nil, err
	}
	if err := add("users", req.Users); err != nil {
		return nil, err
	}
	if err := add("items", req.Items); err != nil {
		return nil, err
	}
	if len(set) == 0 {
		set[caller.TenantID] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// jobActor is the administrator who started a job as they are now: each item
// is done with their rights of the moment, and one who is no longer an
// administrator (or is disabled or deleted) is not obeyed any more.
func (s *server) jobActor(ctx context.Context, id string) (store.User, error) {
	u, err := store.GetUser(ctx, s.opt.Store.Q(), store.AllTenants(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (u.Role != "admin" || u.Disabled)) {
		return store.User{}, &auth.Error{Kind: auth.Forbidden, Msg: "The administrator who started the job may not do this any more."}
	}
	if err != nil {
		return store.User{}, err
	}
	return *u, nil
}

type jobResp struct {
	ID     string              `json:"id"`
	Action string              `json:"action"`
	Total  int                 `json:"total"`
	Done   int                 `json:"done"`
	Failed []store.BulkFailure `json:"failed"`
	State  string              `json:"state"`
}

func jobOf(j *store.BulkJob) jobResp {
	f := j.Failed
	if f == nil {
		f = []store.BulkFailure{}
	}
	return jobResp{j.ID, j.Action, j.Total, j.Done, f, j.State}
}

// markJobsInterrupted is made once, when the server starts.
func (s *server) markJobsInterrupted() {
	if s.single {
		return
	}
	if n, err := store.InterruptBulkJobs(context.Background(), s.opt.Store.Q(), s.now()); err != nil {
		s.log.Warn("could not mark the bulk jobs a restart interrupted", "error", err)
	} else if n > 0 {
		s.log.Info("bulk jobs were interrupted by a restart", "count", n)
	}
}

func (s *server) adminBulk(c *call) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Confirm == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "A bulk action needs the token of a confirmation: ask POST /api/admin/confirm first.")
		return
	}
	e, ok := s.confirms.takeAny(req.Confirm, c.user().ID, s.now())
	if !ok {
		writeError(c.w, http.StatusNotFound, "not_found", "No such confirmation: it was used, has expired, or is not yours.")
		return
	}
	// The plan is made again from the records now; what it was made from must be
	// what it is.
	plan, ok := s.planFor(c, e.req)
	if !ok {
		return
	}
	if plan.Hash != e.hash {
		writeError(c.w, http.StatusConflict, "confirm_stale", "What the plan was made from has changed: ask again.")
		return
	}
	ids := e.req.idList()
	queue, err := s.jobTenants(c.r.Context(), e.req, c.user())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	// The row is the tenant's it works on; a site administrator's job is the
	// site's (its tenant's administrators do not see it).
	tenant := c.user().TenantID
	if len(queue) == 1 {
		tenant = queue[0]
	}
	job := store.BulkJob{ID: page.NewID(s.now()), TenantID: tenant, MadeBy: c.user().ID, Action: e.req.Action, Total: len(ids), State: store.JobQueued,
		SiteOnly: c.isSiteAdmin(), Created: s.now()}
	if err := store.InsertBulkJob(c.r.Context(), s.opt.Store.Q(), job); err != nil {
		s.fail(c.w, err)
		return
	}
	actor, ip := c.user(), s.auth.ClientAddr(c.r)
	if !s.goBg(func(ctx context.Context) { s.runJob(ctx, job, e.req, actor, ip, queue) }) {
		// The server is stopping: the job never starts, and reads as a restart
		// leaves it.
		s.writeJob(&job, store.JobInterrupted, 0, nil)
		writeError(c.w, http.StatusServiceUnavailable, "unavailable", "The server is stopping: ask again once it has started.")
		return
	}
	s.auditRow(c, "bulk_started", job.TenantID, map[string]any{"job": job.ID, "action": job.Action, "count": job.Total, "failed": 0})
	writeJSON(c.w, http.StatusAccepted, struct {
		Job jobResp `json:"job"`
	}{jobOf(&job)})
}

func (s *server) adminGetJob(c *call) {
	j, err := store.GetBulkJob(c.r.Context(), s.opt.Store.Q(), store.ScopeOf(c.user()), c.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such job.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Job jobResp `json:"job"`
	}{jobOf(j)})
}

// runJob runs a job's items, in the queue of each of the tenants it works on.
// A panic in an item marks the job failed (with that item) and the server goes
// on; progress that cannot be written is tried again, and a job whose progress
// could not be written ends failed rather than read as running. When ctx ends
// (the handler is closed) the job stops before its next item, or with the item
// it was on cut short as a request whose client went away is, and ends
// interrupted with what was done: as a restart leaves it.
func (s *server) runJob(ctx context.Context, job store.BulkJob, req confirmRequest, actor store.User, ip string, queue []string) {
	defer jobHook(ctx, &job, "end", "")
	if !s.jobs.acquire(ctx, queue...) {
		s.writeJob(&job, store.JobInterrupted, 0, nil)
		return
	}
	defer s.jobs.release(queue...)
	var failed []store.BulkFailure
	done, current, broken := 0, "", false
	write := func(state string) {
		if err := s.writeJob(&job, state, done, failed); err != nil {
			broken = true
		}
	}
	finish := func(state string) {
		if broken && state == store.JobDone {
			state = store.JobFailed
		}
		if err := s.writeJob(&job, state, done, failed); err != nil {
			s.log.Error("a bulk job ended and could not be recorded; the next start reads it as interrupted", "job", job.ID, "error", err)
		}
		// An interrupted job did not finish (as after a restart, nothing says so
		// but its row).
		if s.auth != nil && state != store.JobInterrupted {
			s.auth.Audit(auth.AuditEvent{Kind: "bulk_finished", Actor: actor.ID, Tenant: job.TenantID, IP: ip,
				Detail: map[string]any{"job": job.ID, "action": job.Action, "count": job.Total, "failed": len(failed)}})
		}
	}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("a bulk job broke", "job", job.ID, "item", current, "panic", p, "stack", string(debug.Stack()))
			id := current
			if id == "" {
				id = "*"
			}
			failed = append(failed, store.BulkFailure{ID: id, Error: "an internal error stopped the job", Code: "internal"})
			finish(store.JobFailed)
		}
	}()
	write(store.JobRunning)
	fail := func(id string, err error) {
		code, msg := errorCodeOf(s, err)
		failed = append(failed, store.BulkFailure{ID: id, Error: msg, Code: code})
	}
	if req.Action == "export" {
		// One zip of the whole set, with the rights of the administrator now.
		current = "*"
		jobHook(ctx, &job, "item", "*")
		err := ctx.Err()
		if err == nil {
			var cur store.User
			if cur, err = s.jobActor(ctx, actor.ID); err == nil {
				err = s.bulkExport(ctx, req, cur)
			}
		}
		switch {
		case err == nil:
			done = job.Total
		case ctx.Err() != nil:
			current = ""
			finish(store.JobInterrupted)
			return
		default:
			fail("*", err)
		}
	} else {
		for _, id := range req.idList() {
			current = id
			jobHook(ctx, &job, "item", id)
			if ctx.Err() != nil {
				current = ""
				finish(store.JobInterrupted)
				return
			}
			cur, err := s.jobActor(ctx, actor.ID)
			if err == nil {
				err = s.bulkItem(ctx, req, id, cur, ip, job.ID)
			}
			if err != nil && ctx.Err() != nil {
				// Cut short by the close: not this item's failure.
				current = ""
				finish(store.JobInterrupted)
				return
			}
			if err != nil {
				fail(id, err)
			} else {
				done++
			}
			write(store.JobRunning)
		}
	}
	current = ""
	finish(store.JobDone)
}

// errorCodeOf is the code and message an error would be answered with.
func errorCodeOf(s *server, err error) (string, string) {
	w := &memWriter{h: http.Header{}}
	s.fail(w, err)
	var b errorBody
	if json.Unmarshal(w.buf.Bytes(), &b) == nil && b.Error != "" {
		return b.Error, b.Message
	}
	return "internal", err.Error()
}

// bulkItem does one item of a job with the rules of the action's own endpoint.
func (s *server) bulkItem(ctx context.Context, req confirmRequest, id string, actor store.User, ip, job string) error {
	sc := store.AdminScopeOf(actor)
	by := func(kind string, tenant string, detail map[string]any) {
		if s.auth == nil {
			return
		}
		detail["bulk"] = job
		s.auth.Audit(auth.AuditEvent{Kind: kind, Actor: actor.ID, Tenant: tenant, Detail: detail, IP: ip})
	}
	switch req.Action {
	case "delete_spaces":
		it, err := s.spaces.RecycleSpace(ctx, sc, id, spaces.RecycleOptions{By: actor.ID, ByName: actor.Username, Reason: "bulk"})
		if err != nil {
			return err
		}
		by("space_deleted", it.TenantID, map[string]any{"item": it.ID, "space": it.SpaceID, "name": it.Name, "bytes": it.Bytes, "reason": it.Reason})
		return nil
	case "disable_spaces", "enable_spaces":
		return s.spaces.SetDisabled(ctx, sc, id, req.Action == "disable_spaces")
	case "evict":
		if !(actor.SiteAdmin && actor.Role == "admin") {
			return &auth.Error{Kind: auth.Forbidden, Msg: "Only an administrator of the whole server may do that."}
		}
		ectx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return s.spaces.Evict(ectx, id, spaces.EvictOptions{AdminRequest: true})
	case "pin", "unpin":
		if !(actor.SiteAdmin && actor.Role == "admin") {
			return &auth.Error{Kind: auth.Forbidden, Msg: "Only an administrator of the whole server may do that."}
		}
		return s.spaces.SetPinned(ctx, id, req.Action == "pin")
	case "delete_users":
		_, err := s.deleteUser(ctx, deleteUserIn{Actor: actor, IP: ip, Heir: req.Heir, Transfers: req.Transfers, Reason: "bulk", Bulk: job}, id)
		return err
	case "disable_users", "enable_users":
		off := req.Action == "disable_users"
		_, err := s.auth.UpdateUser(ctx, actor, ip, id, auth.UserPatch{Disabled: &off})
		return err
	case "purge":
		if !(actor.SiteAdmin && actor.Role == "admin") {
			return &auth.Error{Kind: auth.Forbidden, Msg: "Only an administrator of the whole server may purge the recycle bin early."}
		}
		it, err := s.spaces.PurgeItem(ctx, sc, id)
		if err != nil {
			return err
		}
		if it.Kind == store.RecycleUser {
			by("user_purged", it.TenantID, map[string]any{"item": it.ID, "user": it.UserID, "username": it.Name})
		} else {
			by("space_purged", it.TenantID, map[string]any{"item": it.ID, "space": it.SpaceID, "name": it.Name, "bytes": it.Bytes, "reason": it.Reason})
		}
		return nil
	case "restore":
		it, err := store.GetRecycleItem(ctx, s.opt.Store.Q(), sc, id, false)
		if err != nil {
			return mapNotFound(err)
		}
		if it.Kind == store.RecycleUser {
			res, err := s.spaces.RestoreUser(ctx, sc, id)
			if err != nil {
				return err
			}
			s.forgetUser(it.UserID)
			by("user_restored", it.TenantID, map[string]any{"item": it.ID, "user": it.UserID, "username": it.Name, "transfers": transfersDetail(res.Transfers), "recycled": res.Spaces, "reason": it.Reason})
			return nil
		}
		res, err := s.spaces.RestoreSpace(ctx, sc, id, "")
		if err != nil {
			return err
		}
		by("space_restored", it.TenantID, map[string]any{"item": it.ID, "space": res.SpaceID, "name": res.Name, "bytes": it.Bytes, "reason": it.Reason})
		return nil
	}
	return errors.New("no such action: " + req.Action)
}

func mapNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return spaces.ErrNotFound
	}
	return err
}

// bulkExport makes one export of the spaces and items of a confirmed export.
func (s *server) bulkExport(ctx context.Context, req confirmRequest, actor store.User) error {
	sc := store.AdminScopeOf(actor)
	var sources []store.ExportSource
	tenants := map[string]bool{}
	personal := false
	for _, id := range req.Spaces {
		sp, err := store.GetSpace(ctx, s.opt.Store.Q(), sc, id)
		if err != nil || (sp.Personal && !(actor.SiteAdmin && actor.Role == "admin")) {
			return spaces.ErrNotFound
		}
		sources = append(sources, store.ExportSource{Space: sp.ID, SpaceID: sp.ID, Name: sp.Name})
		tenants[sp.TenantID] = true
		personal = personal || sp.Personal
	}
	items, err := store.GetRecycleItems(ctx, s.opt.Store.Q(), sc, req.Items)
	if err != nil {
		return err
	}
	for _, id := range req.Items {
		it, ok := items[id]
		if !ok || it.Kind != store.RecycleSpace {
			return spaces.ErrNotFound
		}
		sources = append(sources, store.ExportSource{Item: it.ID, SpaceID: it.SpaceID, Name: it.Name})
		tenants[it.TenantID] = true
		personal = personal || it.Personal
	}
	tenant := actor.TenantID
	if len(tenants) == 1 {
		for t := range tenants {
			tenant = t
		}
	}
	_, err = s.spaces.StartExport(ctx, spaces.ExportRequest{TenantID: tenant, By: actor.ID, ByName: actor.Username, Sources: sources,
		SiteOnly: exportSiteOnly(actor, false, personal, len(tenants))})
	return err
}
