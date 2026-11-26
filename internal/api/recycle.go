package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// The recycle bin (docs/api.md, "The recycle bin"): every deletion of a space
// goes through it, and nothing in it is deleted until its date or a site
// administrator purges it.

type recycleSpaceRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Personal bool   `json:"personal"`
}

type recycleUserRef struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type recycleItemResp struct {
	ID        string           `json:"id"`
	Kind      string           `json:"kind"`
	Tenant    string           `json:"tenant"`
	Space     *recycleSpaceRef `json:"space"`
	User      *recycleUserRef  `json:"user"`
	With      []recycleItemRef `json:"with"`
	Bytes     int64            `json:"bytes"`
	DeletedAt string           `json:"deletedAt"`
	DeletedBy recycleUserRef   `json:"deletedBy"`
	PurgeAt   string           `json:"purgeAt"`
	Reason    string           `json:"reason"`
	// State is ready, moving (its directories are being moved), restoring or
	// purging: only a ready item can be restored or purged.
	State string `json:"state"`
	// Job is the job that holds the item (null at rest): follow it with
	// GET /api/admin/jobs/{id}.
	Job        *string  `json:"job"`
	Restorable bool     `json:"restorable"`
	Problems   []string `json:"problems"`
}

// recycleItemRef is an item recycled with a person.
type recycleItemRef struct {
	ID    string          `json:"id"`
	Space recycleSpaceRef `json:"space"`
	Bytes int64           `json:"bytes"`
}

// recycleItemOf is the answer's form of an item. days is recycle.days now:
// purgeAt follows it, so shortening it moves every item's date.
func (s *server) recycleItemOf(it store.RecycleItem, days int, with []store.RecycleItem) recycleItemResp {
	out := recycleItemResp{ID: it.ID, Kind: it.Kind, Tenant: it.TenantID, Bytes: it.Bytes, DeletedAt: rfc3339(it.DeletedAt),
		DeletedBy: recycleUserRef{it.DeletedBy, it.DeletedByName}, PurgeAt: rfc3339(it.DeletedAt.Add(time.Duration(days) * 24 * time.Hour)),
		Reason: it.Reason, State: it.State, With: []recycleItemRef{}, Problems: s.spaces.Problems(&it)}
	out.Restorable = len(out.Problems) == 0
	if it.JobID != "" {
		out.Job = &it.JobID
	}
	if it.Kind == store.RecycleUser {
		out.User = &recycleUserRef{it.UserID, it.Name}
	} else {
		out.Space = &recycleSpaceRef{it.SpaceID, it.Name, it.Personal}
	}
	for _, w := range with {
		out.With = append(out.With, recycleItemRef{w.ID, recycleSpaceRef{w.SpaceID, w.Name, w.Personal}, w.Bytes})
	}
	return out
}

// withFor is the items recycled with a person as the caller may see them: a
// tenant administrator never sees a personal space, in the bin or not, so the
// person's own space is left out of what they are shown (it comes back with the
// person all the same).
func withFor(c *call, with []store.RecycleItem) []store.RecycleItem {
	if c.can(authz.SiteSpacesPersonalMeta) {
		return with
	}
	out := with[:0:0]
	for _, w := range with {
		if !w.Personal {
			out = append(out, w)
		}
	}
	return out
}

func (s *server) recycleDays(c *call) int { return s.spaces.RecycleDays(c.r.Context()) }

// recycleItemFound answers an item that is not in the caller's scope as one
// that does not exist.
func (s *server) recycleItemFound(c *call, id string) (*store.RecycleItem, bool) {
	it, err := store.GetRecycleItem(c.r.Context(), s.opt.Store.Q(), c.adminReach(), id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such item in the recycle bin.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return it, true
}

func (s *server) adminListRecycle(c *call) {
	q := c.r.URL.Query()
	limit := defaultAdminLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAdminLimit {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit is a number from 1 to "+strconv.Itoa(maxAdminLimit)+".")
			return
		}
		limit = n
	}
	kind := q.Get("kind")
	if kind != "" && kind != store.RecycleSpace && kind != store.RecycleUser {
		writeError(c.w, http.StatusBadRequest, "invalid", "Kind is space or user.")
		return
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := store.ListRecycle(c.r.Context(), s.opt.Store.Q(), sc, store.RecycleFilter{Kind: kind, Query: q.Get("q"), After: q.Get("cursor"), Limit: limit})
	if errors.Is(err, store.ErrBadCursor) {
		writeError(c.w, http.StatusBadRequest, "invalid", "That cursor is not one this list gave.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	days := s.recycleDays(c)
	items := make([]recycleItemResp, len(rows))
	for i, it := range rows {
		var with []store.RecycleItem
		if it.Kind == store.RecycleUser {
			if with, err = store.RecycleChildren(c.r.Context(), s.opt.Store.Q(), it.ID); err != nil {
				s.fail(c.w, err)
				return
			}
		}
		items[i] = s.recycleItemOf(it, days, withFor(c, with))
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []recycleItemResp `json:"items"`
		Next  string            `json:"next"`
	}{items, next})
}

func (s *server) adminRestoreItem(c *call) {
	it, ok := s.recycleItemFound(c, c.r.PathValue("item"))
	if !ok {
		return
	}
	var req struct {
		Owner string `json:"owner"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if it.Kind != store.RecycleSpace {
		s.restoreUserItem(c, it, req.Owner)
		return
	}
	res, err := s.spaces.RestoreSpace(c.r.Context(), c.adminReach(), it.ID, req.Owner)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.auditRow(c, "space_restored", it.TenantID, map[string]any{"item": it.ID, "space": res.SpaceID, "name": res.Name, "bytes": it.Bytes, "reason": it.Reason, "bulk": ""})
	skipped := res.Skipped
	if skipped == nil {
		skipped = []store.RestoreSkipped{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Restored map[string]any         `json:"restored"`
		Skipped  []store.RestoreSkipped `json:"skipped"`
	}{map[string]any{"item": it.ID, "space": map[string]any{"id": res.SpaceID, "name": res.Name}}, skipped})
}

// restoreUserItem is the restore of a person's item (see users.go).
func (s *server) restoreUserItem(c *call, it *store.RecycleItem, owner string) {
	s.restoreUser(c, it)
}

func (s *server) adminPurgeItem(c *call) {
	it, ok := s.recycleItemFound(c, c.r.PathValue("item"))
	if !ok {
		return
	}
	if !s.useConfirm(c, c.r.URL.Query().Get("confirm"), confirmRequest{Action: "purge", Items: []string{it.ID}}) {
		return
	}
	// A purge is paced and may take long: it is a job on the runner, audited when
	// it is over (purgeEnded). The item is purging meanwhile (a restore of it
	// is busy); one that fails stays purging and the hourly job finishes it.
	started, _, err := s.spaces.StartPurge(c.r.Context(), c.adminReach(), it.ID, spaces.PurgeBy{ID: c.user().ID, Name: c.user().Username, Capacity: c.scope.capacity, IP: s.auth.ClientAddr(c.r)})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	days := s.recycleDays(c)
	writeJSON(c.w, http.StatusAccepted, s.recycleItemOf(*started, days, nil))
}

// purgeEnded audits a purge that a person asked for once its job has ended
// done (the server's own purge of an item past its days is not audited: no
// one asked for it).
func (s *server) purgeEnded(ctx context.Context, j *store.Job) {
	if j.Kind != store.JobRecyclePurge || j.State != store.JobDone || j.MadeBy == "" {
		return
	}
	r := spaces.PurgeResultOf(j)
	s.auditPurged(j.MadeBy, spaces.RecycleParamsOf(j).IP, j.Capacity, &store.RecycleItem{ID: j.Subject, TenantID: r.TenantID, Kind: r.Kind, SpaceID: r.SpaceID, UserID: r.UserID,
		Name: r.Name, Bytes: r.Bytes, Reason: r.Reason}, "")
}

// auditPurged records that an item was purged.
func (s *server) auditPurged(actor, ip, capacity string, it *store.RecycleItem, bulk string) {
	if s.auth == nil {
		return
	}
	ev := auth.AuditEvent{Kind: "space_purged", Actor: actor, Tenant: it.TenantID, IP: ip, Capacity: capacity,
		Detail: map[string]any{"item": it.ID, "space": it.SpaceID, "name": it.Name, "bytes": it.Bytes, "reason": it.Reason, "bulk": bulk}}
	if it.Kind == store.RecycleUser {
		ev.Kind, ev.Detail = "user_purged", map[string]any{"item": it.ID, "user": it.UserID, "username": it.Name, "bulk": bulk}
	}
	s.auth.Audit(ev)
}

// auditRow records what an administrator did, on the row of the tenant it was in.
func (s *server) auditRow(c *call, kind, tenant string, detail map[string]any) {
	s.audit(c, auth.AuditEvent{Kind: kind, Actor: c.user().ID, Tenant: tenant, Detail: detail})
}

// --- deleting a space ------------------------------------------------------------

func (s *server) adminDeleteSpace(c *call) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if len(req.Reason) > 500 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Reason is at most 500 characters.")
		return
	}
	it, err := s.spaces.RecycleSpace(c.r.Context(), c.adminReach(), c.r.PathValue("space"), spaces.RecycleOptions{By: c.user().ID, ByName: c.user().Username, Reason: req.Reason})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.auditRow(c, "space_deleted", it.TenantID, map[string]any{"item": it.ID, "space": it.SpaceID, "name": it.Name, "bytes": it.Bytes, "reason": it.Reason, "bulk": ""})
	writeJSON(c.w, http.StatusOK, s.recycleItemOf(*it, s.recycleDays(c), nil))
}

type deletionResp struct {
	Space       recycleSpaceRefName `json:"space"`
	Pages       int64               `json:"pages"`
	Bytes       int64               `json:"bytes"`
	HeldBytes   int64               `json:"heldBytes"`
	Attachments struct {
		// InSpace is not a stored value (nothing counts a space's attachments
		// without walking it), so it is null.
		InSpace    *int `json:"inSpace"`
		Repository bool `json:"repository"`
	} `json:"attachments"`
	Members      int        `json:"members"`
	Groups       int        `json:"groups"`
	Drafts       int        `json:"drafts"`
	Remote       remoteKept `json:"remote"`
	AssetsRemote remoteKept `json:"assetsRemote"`
	Cache        string     `json:"cache"`
	Exportable   bool       `json:"exportable"`
	RecycleDays  int        `json:"recycleDays"`
}

type recycleSpaceRefName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type remoteKept struct {
	URL  string `json:"url"`
	Kept bool   `json:"kept"`
}

// deletionOf is what deleting a space would do, read from stored values only.
// ok is false (and the answer is given) for a space that is not in the scope.
func (s *server) deletionOf(c *call, sc store.Scope, id string) (deletionResp, bool) {
	ctx := c.r.Context()
	row, err := store.GetAdminSpace(ctx, s.opt.Store.Q(), sc, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
		return deletionResp{}, false
	}
	if err != nil {
		s.fail(c.w, err)
		return deletionResp{}, false
	}
	f, err := store.GetDeletionFacts(ctx, s.opt.Store.Q(), sc, id)
	if err != nil {
		s.fail(c.w, err)
		return deletionResp{}, false
	}
	out := deletionResp{Space: recycleSpaceRefName{row.ID, row.Name}, Pages: f.Pages, Bytes: f.Bytes, HeldBytes: f.HeldBytes,
		Members: f.Members, Groups: f.Groups, Drafts: f.Drafts, RecycleDays: s.recycleDays(c)}
	out.Attachments.Repository = f.AssetsRemote != ""
	// The remote repositories are never touched: the preview says so.
	out.Remote = remoteKept{f.Remote, true}
	out.AssetsRemote = remoteKept{f.AssetsRemote, true}
	out.Cache = s.adminStorageOf(ctx, row).Cache
	// An export needs the files: an evicted space whose remote cannot be reached
	// now cannot be exported (restoring it is what fetches them).
	out.Exportable = out.Cache != "evicted" || f.RemoteState == "ok"
	return out, true
}

func (s *server) adminSpaceDeletion(c *call) {
	out, ok := s.deletionOf(c, c.adminReach(), c.r.PathValue("space"))
	if !ok {
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

// --- the plans of the confirmations ------------------------------------------------

func (s *server) planSpaces(c *call, sc store.Scope, req confirmRequest) (confirmPlan, bool) {
	ctx := c.r.Context()
	fp, err := store.SpaceFingerprints(ctx, s.opt.Store.Q(), sc, req.Spaces)
	if err != nil {
		s.fail(c.w, err)
		return confirmPlan{}, false
	}
	if len(fp) != len(req.Spaces) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
		return confirmPlan{}, false
	}
	var plan confirmPlan
	ids := append([]string(nil), req.Spaces...)
	sort.Strings(ids)
	var parts []string
	for _, id := range ids {
		parts = append(parts, fp[id])
		switch req.Action {
		case "delete_spaces":
			d, ok := s.deletionOf(c, sc, id)
			if !ok {
				return plan, false
			}
			plan.Plan = append(plan.Plan, d)
			plan.Bytes += d.HeldBytes
			if row, err := store.GetSpace(ctx, s.opt.Store.Q(), sc, id); err == nil && row.Personal {
				plan.Blockers = append(plan.Blockers, map[string]any{"space": id, "reason": "personal_space"})
			}
		default:
			row, err := store.GetAdminSpace(ctx, s.opt.Store.Q(), sc, id)
			if err != nil {
				s.fail(c.w, err)
				return plan, false
			}
			line := map[string]any{"space": map[string]any{"id": row.ID, "name": row.Name}, "action": req.Action}
			if row.SizeBytes != nil {
				plan.Bytes += *row.SizeBytes
			}
			plan.Plan = append(plan.Plan, line)
		}
	}
	plan.Hash = hashOf(append([]string{req.Action}, parts...)...)
	return plan, true
}

func (s *server) planItems(c *call, sc store.Scope, req confirmRequest) (confirmPlan, bool) {
	if req.Action == "purge" && !c.can(authz.SiteRecyclePurge) {
		writeError(c.w, http.StatusForbidden, "forbidden", "Only an administrator of the whole server may purge the recycle bin early.")
		return confirmPlan{}, false
	}
	items, err := store.GetRecycleItems(c.r.Context(), s.opt.Store.Q(), sc, req.Items)
	if err != nil {
		s.fail(c.w, err)
		return confirmPlan{}, false
	}
	if len(items) != len(req.Items) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such item in the recycle bin.")
		return confirmPlan{}, false
	}
	ids := append([]string(nil), req.Items...)
	sort.Strings(ids)
	var plan confirmPlan
	var parts []string
	for _, id := range ids {
		it := items[id]
		parts = append(parts, strings.Join([]string{it.ID, it.State, strconv.FormatInt(it.DeletedAt.UnixMilli(), 10)}, "|"))
		plan.Bytes += it.Bytes
		plan.Plan = append(plan.Plan, map[string]any{"item": it.ID, "kind": it.Kind, "name": it.Name, "bytes": it.Bytes, "action": req.Action, "deletedAt": rfc3339(it.DeletedAt)})
	}
	plan.Hash = hashOf(append([]string{req.Action}, parts...)...)
	return plan, true
}
