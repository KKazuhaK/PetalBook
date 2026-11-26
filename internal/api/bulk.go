package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/jobs"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Bulk actions (docs/api.md, "Confirmations and bulk actions"). A job runs the
// items of a confirmed action one at a time in the background, each with the
// rules of its single action: an item that fails is listed, the rest go on. A
// job is a row of jobs, of kind "bulk", run by the server's runner
// (internal/jobs) in the class jobs.Bulk: at most one job per tenant it works
// on and two on the server run at once; the others wait. What was not done
// when the server stopped is read as interrupted.

const bulkKindName = "bulk"

// bulkParams is a bulk job's request, as its row keeps it.
type bulkParams struct {
	confirmRequest
	IP string `json:"ip"`
	// Queue is the tenants whose turn the job takes (jobTenants).
	Queue []string `json:"queue"`
	// Narrowed and ScopeTenant are the acting scope of the request that
	// started it (its capacity is the row's): each item keeps to it.
	Narrowed    bool   `json:"narrowed,omitempty"`
	ScopeTenant string `json:"scopeTenant,omitempty"`
}

func bulkParamsOf(j *store.Job) bulkParams {
	var p bulkParams
	_ = json.Unmarshal(j.Params, &p)
	return p
}

// bulkKind runs the bulk jobs.
type bulkKind struct{ s *server }

func (bulkKind) Class() jobs.Class { return jobs.Bulk }

// Recover: an interrupted bulk job is not taken up again.
func (bulkKind) Recover(context.Context, *store.Job) jobs.Outcome { return jobs.Interrupt }

func (bulkKind) Queues(j *store.Job) []string { return bulkParamsOf(j).Queue }

// jobWriteFaults makes the writes of a job's progress fail, by who started the
// job (tests; see SetUpdateBulkJob).
var jobWriteFaults sync.Map

// jobHooks are called at named points of the jobs a user starts ("item" before
// each item, with its id; "end" when the job has ended), by who started the job
// (tests; see SetBulkJobHook).
var jobHooks sync.Map

func jobHook(ctx context.Context, madeBy, point, item string) {
	if f, ok := jobHooks.Load(madeBy); ok {
		f.(func(context.Context, string, string))(ctx, point, item)
	}
}

// jobWriteFault is the runner's WriteFault: the fault a test set for whoever
// started the job.
func jobWriteFault(j *store.Job, state string, try int) error {
	if f, ok := jobWriteFaults.Load(j.MadeBy); ok {
		return f.(func(string, int) error)(state, try)
	}
	return nil
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
// administrator (or is disabled or deleted) is not obeyed any more. A job
// started in a tenant's scope keeps to it (narrowTo), as its request did.
func (s *server) jobActor(ctx context.Context, id string, sc actingScope) (store.User, authz.Subject, error) {
	gone := &auth.Error{Kind: auth.Forbidden, Msg: "The administrator who started the job may not do this any more."}
	u, err := store.GetUser(ctx, s.opt.Store.Q(), store.AllTenants(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		return store.User{}, authz.Subject{}, gone
	}
	if err != nil {
		return store.User{}, authz.Subject{}, err
	}
	access, err := s.subjectOf(ctx, *u)
	if err != nil {
		return store.User{}, authz.Subject{}, err
	}
	actor := *u
	if sc.narrowed {
		actor, access = narrowTo(actor, access, sc.tenant)
	}
	if !access.Can(authz.TenantBulkRun, actor.TenantID) {
		return store.User{}, authz.Subject{}, gone
	}
	return actor, access, nil
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
	tenant := c.tenant()
	if len(queue) == 1 {
		tenant = queue[0]
	}
	params, err := json.Marshal(bulkParams{confirmRequest: e.req, IP: s.auth.ClientAddr(c.r), Queue: queue, Narrowed: c.scope.narrowed, ScopeTenant: c.scope.tenant})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	job, err := s.jobs.Enqueue(c.r.Context(), store.Job{ID: page.NewID(s.now()), TenantID: tenant, Kind: bulkKindName, MadeBy: c.user().ID, MadeByName: c.user().Username,
		Capacity: c.scope.capacity, SiteOnly: c.siteWide(authz.TenantBulkRun), Params: params, Total: int64(len(ids))})
	if errors.Is(err, jobs.ErrClosed) {
		// The server is stopping: the job is not added.
		writeError(c.w, http.StatusServiceUnavailable, "unavailable", "The server is stopping: ask again once it has started.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.auditRow(c, "bulk_started", job.TenantID, map[string]any{"job": job.ID, "action": e.req.Action, "count": job.Total, "failed": 0})
	writeJSON(c.w, http.StatusAccepted, struct {
		Job jobResp `json:"job"`
	}{jobOf(job)})
}

// Run runs a bulk job's items. A panic in an item fails the job (with that
// item) and the server goes on; progress that cannot be written is tried again,
// and a job whose progress could not be written ends failed rather than read
// as running (the runner sees to both). When ctx ends (the handler is closed)
// the job stops before its next item, or with the item it was on cut short as a
// request whose client went away is, and ends interrupted with what was done:
// as a restart leaves it.
func (k bulkKind) Run(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	s := k.s
	bp := bulkParamsOf(j)
	// Its rows record the capacity of the request that started it.
	ctx = auditlog.WithCapacity(ctx, j.Capacity)
	sc := actingScope{tenant: bp.ScopeTenant, narrowed: bp.Narrowed, capacity: j.Capacity}
	fail := func(id string, err error) {
		code, msg := errorCodeOf(s, err)
		p.Fail(store.JobFailure{ID: id, Error: msg, Code: code})
	}
	if bp.Action == "export" {
		// One zip of the whole set, with the rights of the administrator now.
		p.Item("*")
		jobHook(ctx, j.MadeBy, "item", "*")
		err := ctx.Err()
		if err == nil {
			var cur store.User
			var access authz.Subject
			if cur, access, err = s.jobActor(ctx, j.MadeBy, sc); err == nil {
				err = s.bulkExport(ctx, bp.confirmRequest, cur, access)
			}
		}
		switch {
		case err == nil:
			p.Step(j.Total, 0)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			fail("*", err)
		}
		return nil
	}
	for _, id := range bp.idList() {
		p.Item(id)
		jobHook(ctx, j.MadeBy, "item", id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cur, access, err := s.jobActor(ctx, j.MadeBy, sc)
		if err == nil {
			err = s.bulkItem(ctx, bp.confirmRequest, id, cur, access, bp.IP, j.ID)
		}
		if err != nil && ctx.Err() != nil {
			// Cut short by the close: not this item's failure.
			return ctx.Err()
		}
		if err != nil {
			fail(id, err)
		} else {
			p.Step(1, 0)
		}
	}
	return nil
}

// Ended records the end of a job that finished (an interrupted one did not: as
// after a restart, nothing says so but its row).
func (k bulkKind) Ended(ctx context.Context, j *store.Job) {
	defer jobHook(ctx, j.MadeBy, "end", "")
	if k.s.auth == nil || j.State == store.JobInterrupted {
		return
	}
	bp := bulkParamsOf(j)
	k.s.auth.AuditIn(auditlog.WithCapacity(ctx, j.Capacity), auth.AuditEvent{Kind: "bulk_finished", Actor: j.MadeBy, Tenant: j.TenantID, IP: bp.IP,
		Detail: map[string]any{"job": j.ID, "action": bp.Action, "count": j.Total, "failed": len(j.Failed)}})
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
func (s *server) bulkItem(ctx context.Context, req confirmRequest, id string, actor store.User, access authz.Subject, ip, job string) error {
	can := func(k authz.Key) bool { return access.Can(k, actor.TenantID) }
	sc := store.AdminScopeOf(actor)
	by := func(kind string, tenant string, detail map[string]any) {
		if s.auth == nil {
			return
		}
		detail["bulk"] = job
		s.auth.AuditIn(ctx, auth.AuditEvent{Kind: kind, Actor: actor.ID, Tenant: tenant, Detail: detail, IP: ip})
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
		if !can(authz.SiteStorageManage) {
			return &auth.Error{Kind: auth.Forbidden, Msg: "Only an administrator of the whole server may do that."}
		}
		ectx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return s.spaces.Evict(ectx, id, spaces.EvictOptions{AdminRequest: true})
	case "pin", "unpin":
		if !can(authz.SiteStorageManage) {
			return &auth.Error{Kind: auth.Forbidden, Msg: "Only an administrator of the whole server may do that."}
		}
		return s.spaces.SetPinned(ctx, id, req.Action == "pin")
	case "delete_users":
		_, err := s.deleteUser(ctx, deleteUserIn{Actor: actor, Access: access, IP: ip, Heir: req.Heir, Transfers: req.Transfers, Reason: "bulk", Bulk: job}, id)
		return err
	case "disable_users", "enable_users":
		off := req.Action == "disable_users"
		_, err := s.auth.UpdateUser(ctx, actor, access, ip, id, auth.UserPatch{Disabled: &off})
		return err
	case "purge":
		if !can(authz.SiteRecyclePurge) {
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
func (s *server) bulkExport(ctx context.Context, req confirmRequest, actor store.User, access authz.Subject) error {
	sc := store.AdminScopeOf(actor)
	var sources []store.ExportSource
	tenants := map[string]bool{}
	personal := false
	for _, id := range req.Spaces {
		sp, err := store.GetSpace(ctx, s.opt.Store.Q(), sc, id)
		if err != nil || (sp.Personal && !access.Can(authz.SiteSpacesPersonalContent, "")) {
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
		SiteOnly: exportSiteOnly(access.SiteWide(authz.TenantDataExport), false, personal, len(tenants))})
	return err
}
