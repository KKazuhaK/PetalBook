package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

// acquire waits for the tenant's turn and a place on the server.
func (r *jobRunner) acquire(tenant string) {
	r.mu.Lock()
	for r.tenants[tenant] || r.running >= maxJobsAtOnce {
		r.cond.Wait()
	}
	r.tenants[tenant] = true
	r.running++
	r.mu.Unlock()
}

func (r *jobRunner) release(tenant string) {
	r.mu.Lock()
	delete(r.tenants, tenant)
	r.running--
	r.mu.Unlock()
	r.cond.Broadcast()
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
		writeError(c.w, http.StatusBadRequest, "invalid", "a bulk action needs the token of a confirmation: ask POST /api/admin/confirm first")
		return
	}
	e, ok := s.confirms.takeAny(req.Confirm, c.user().ID, s.now())
	if !ok {
		writeError(c.w, http.StatusNotFound, "not_found", "no such confirmation: it was used, has expired, or is not yours")
		return
	}
	// The plan is made again from the records now; what it was made from must be
	// what it is.
	plan, ok := s.planFor(c, e.req)
	if !ok {
		return
	}
	if plan.Hash != e.hash {
		writeError(c.w, http.StatusConflict, "confirm_stale", "what the plan was made from has changed: ask again")
		return
	}
	ids := e.req.idList()
	job := store.BulkJob{ID: page.NewID(s.now()), TenantID: c.user().TenantID, MadeBy: c.user().ID, Action: e.req.Action, Total: len(ids), State: store.JobQueued, Created: s.now()}
	if err := store.InsertBulkJob(c.r.Context(), s.opt.Store.Q(), job); err != nil {
		s.fail(c.w, err)
		return
	}
	actor, ip := c.user(), s.auth.ClientAddr(c.r)
	s.jobWG.Add(1)
	go func() {
		defer s.jobWG.Done()
		s.runJob(job, e.req, actor, ip)
	}()
	s.auditRow(c, "bulk_started", job.TenantID, map[string]any{"job": job.ID, "action": job.Action, "count": job.Total, "failed": 0})
	writeJSON(c.w, http.StatusAccepted, struct {
		Job jobResp `json:"job"`
	}{jobOf(&job)})
}

func (s *server) adminGetJob(c *call) {
	j, err := store.GetBulkJob(c.r.Context(), s.opt.Store.Q(), store.ScopeOf(c.user()), c.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such job")
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

// runJob runs a job's items.
func (s *server) runJob(job store.BulkJob, req confirmRequest, actor store.User, ip string) {
	ctx := context.Background()
	q := s.opt.Store.Q()
	s.jobs.acquire(job.TenantID)
	defer s.jobs.release(job.TenantID)
	job.State = store.JobRunning
	store.UpdateBulkJob(ctx, q, job.ID, job.State, 0, nil, s.now())
	var failed []store.BulkFailure
	done := 0
	fail := func(id string, err error) {
		code, msg := errorCodeOf(s, err)
		failed = append(failed, store.BulkFailure{ID: id, Error: msg, Code: code})
	}
	if req.Action == "export" {
		// One zip of the whole set.
		if err := s.bulkExport(ctx, req, actor); err != nil {
			fail("*", err)
		} else {
			done = job.Total
		}
	} else {
		for _, id := range req.idList() {
			if err := s.bulkItem(ctx, req, id, actor, ip, job.ID); err != nil {
				fail(id, err)
			} else {
				done++
			}
			store.UpdateBulkJob(ctx, q, job.ID, store.JobRunning, done, failed, s.now())
		}
	}
	store.UpdateBulkJob(ctx, q, job.ID, store.JobDone, done, failed, s.now())
	if s.auth != nil {
		s.auth.Audit(auth.AuditEvent{Kind: "bulk_finished", Actor: actor.ID, Tenant: job.TenantID, IP: ip,
			Detail: map[string]any{"job": job.ID, "action": job.Action, "count": job.Total, "failed": len(failed)}})
	}
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
	sc := store.ScopeOf(actor)
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
			return &auth.Error{Kind: auth.Forbidden, Msg: "only an administrator of the whole server may do that"}
		}
		ectx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return s.spaces.Evict(ectx, id, spaces.EvictOptions{AdminRequest: true})
	case "pin", "unpin":
		if !(actor.SiteAdmin && actor.Role == "admin") {
			return &auth.Error{Kind: auth.Forbidden, Msg: "only an administrator of the whole server may do that"}
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
			return &auth.Error{Kind: auth.Forbidden, Msg: "only an administrator of the whole server may purge the recycle bin early"}
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
	sc := store.ScopeOf(actor)
	var sources []store.ExportSource
	tenants := map[string]bool{}
	for _, id := range req.Spaces {
		sp, err := store.GetSpace(ctx, s.opt.Store.Q(), sc, id)
		if err != nil || (sp.Personal && !(actor.SiteAdmin && actor.Role == "admin")) {
			return spaces.ErrNotFound
		}
		sources = append(sources, store.ExportSource{Space: sp.ID, Name: sp.Name})
		tenants[sp.TenantID] = true
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
		sources = append(sources, store.ExportSource{Item: it.ID, Name: it.Name})
		tenants[it.TenantID] = true
	}
	tenant := actor.TenantID
	if len(tenants) == 1 {
		for t := range tenants {
			tenant = t
		}
	}
	_, err = s.spaces.StartExport(ctx, spaces.ExportRequest{TenantID: tenant, By: actor.ID, ByName: actor.Username, Sources: sources})
	return err
}
