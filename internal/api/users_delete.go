package api

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Deleting a person (docs/api.md, "Deleting a user"). The preview is computed
// from records; the deletion is the ordered series of steps that makes a half
// done one harmless: the person is disabled first, then each transfer and each
// membership removed is a transaction of its own, then their spaces are
// recycled one by one, and the last step marks the person deleted. A failure
// leaves a disabled person with fewer spaces, never a space without an owner,
// and the deletion can be asked again.

// heirRequiredError is the refusal of deleting a person who owns shared spaces
// alone when no heir can be chosen.
type heirRequiredError struct{ Spaces []store.PlanSpace }

func (e *heirRequiredError) Error() string {
	return "the person owns shared spaces alone and nobody can be chosen to inherit them"
}

func (s *server) failHeir(w http.ResponseWriter, e *heirRequiredError) {
	type ref struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	list := make([]ref, len(e.Spaces))
	for i, sp := range e.Spaces {
		list[i] = ref{sp.ID, sp.Name}
	}
	writeJSON(w, http.StatusConflict, struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Spaces  []ref  `json:"spaces"`
	}{"heir_required", e.Error() + ": name an heir, or one for each of these spaces", list})
}

type planSpaceResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Personal bool   `json:"personal,omitempty"`
}

type userDeletionResp struct {
	User    recycleUserRef `json:"user"`
	Recycle []struct {
		Space planSpaceResp `json:"space"`
		Bytes int64         `json:"bytes"`
	} `json:"recycle"`
	Transfer []struct {
		Space      planSpaceResp  `json:"space"`
		To         recycleUserRef `json:"to"`
		Why        string         `json:"why"`
		Credential string         `json:"credential"`
	} `json:"transfer"`
	Leave []struct {
		Space planSpaceResp `json:"space"`
		Role  string        `json:"role"`
	} `json:"leave"`
	Revoke struct {
		Tokens     int  `json:"tokens"`
		Grants     int  `json:"grants"`
		Passkeys   int  `json:"passkeys"`
		Identities int  `json:"identities"`
		GitHub     bool `json:"github"`
		Sessions   int  `json:"sessions"`
	} `json:"revoke"`
	Groups    int              `json:"groups"`
	NeedsHeir []planSpaceResp  `json:"needsHeir"`
	Blockers  []map[string]any `json:"blockers"`
	Bytes     int64            `json:"bytes"`
}

// blockersOf says why the caller may not delete the person (the last enabled site
// administrator, the caller themself); a site administrator is refused to a
// tenant administrator outright (forbidden).
func (s *server) blockersOf(ctx context.Context, actor store.User, target *store.User) (blockers []map[string]any, forbidden bool, err error) {
	isSite := actor.SiteAdmin && actor.Role == "admin"
	if target.SiteAdmin && !isSite {
		return nil, true, nil
	}
	if target.ID == actor.ID {
		blockers = append(blockers, map[string]any{"reason": "self", "message": "you cannot delete your own account"})
	}
	if target.SiteAdmin && !target.Disabled && !s.opt.Store.TenantDisabled(target.TenantID) {
		n, err := store.CountEnabledAdmins(ctx, s.opt.Store.Q())
		if err != nil {
			return nil, false, err
		}
		if n <= 1 {
			blockers = append(blockers, map[string]any{"reason": "last_admin", "message": "there must be at least one enabled administrator"})
		}
	}
	return blockers, false, nil
}

func planSpaceOf(sp store.PlanSpace) planSpaceResp { return planSpaceResp{sp.ID, sp.Name, sp.Personal} }

// userDeletionOf is the preview of deleting a person, for the caller's scope. ok
// is false when it has answered (a person who is not in the scope is one that
// does not exist).
func (s *server) userDeletionOf(c *call, id, heir string, transfers map[string]string) (userDeletionResp, bool) {
	ctx := c.r.Context()
	sc := store.ScopeOf(c.user())
	target, err := store.GetUser(ctx, s.opt.Store.Q(), sc, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "user_not_found", "there is no such user")
		return userDeletionResp{}, false
	}
	if err != nil {
		s.fail(c.w, err)
		return userDeletionResp{}, false
	}
	blockers, forbidden, err := s.blockersOf(ctx, c.user(), target)
	if err != nil {
		s.fail(c.w, err)
		return userDeletionResp{}, false
	}
	if forbidden {
		writeError(c.w, http.StatusForbidden, "forbidden", "only a site administrator may delete a site administrator")
		return userDeletionResp{}, false
	}
	plan, err := store.PlanUserDeletion(ctx, s.opt.Store.Q(), sc, id, heir, transfers)
	if err != nil {
		s.fail(c.w, err)
		return userDeletionResp{}, false
	}
	out := userDeletionResp{User: recycleUserRef{target.ID, target.Username}, Groups: plan.Groups, Blockers: blockers}
	if out.Blockers == nil {
		out.Blockers = []map[string]any{}
	}
	for _, r := range plan.Recycle {
		out.Recycle = append(out.Recycle, struct {
			Space planSpaceResp `json:"space"`
			Bytes int64         `json:"bytes"`
		}{planSpaceOf(r), r.Bytes})
		out.Bytes += r.Bytes
	}
	for _, t := range plan.Transfer {
		out.Transfer = append(out.Transfer, struct {
			Space      planSpaceResp  `json:"space"`
			To         recycleUserRef `json:"to"`
			Why        string         `json:"why"`
			Credential string         `json:"credential"`
		}{planSpaceOf(t.Space), recycleUserRef{t.To, t.ToUsername}, t.Why, t.Credential})
	}
	for _, l := range plan.Leave {
		out.Leave = append(out.Leave, struct {
			Space planSpaceResp `json:"space"`
			Role  string        `json:"role"`
		}{planSpaceOf(l), l.Role})
	}
	out.NeedsHeir = []planSpaceResp{}
	for _, n := range plan.NeedsHeir {
		out.NeedsHeir = append(out.NeedsHeir, planSpaceOf(n))
	}
	out.Revoke.Tokens, out.Revoke.Grants, out.Revoke.Passkeys, out.Revoke.Identities, out.Revoke.GitHub, out.Revoke.Sessions =
		plan.Tokens, plan.Grants, plan.Passkeys, plan.Identity, plan.GitHub, plan.Sessions
	if out.Recycle == nil {
		out.Recycle = []struct {
			Space planSpaceResp `json:"space"`
			Bytes int64         `json:"bytes"`
		}{}
	}
	if out.Transfer == nil {
		out.Transfer = []struct {
			Space      planSpaceResp  `json:"space"`
			To         recycleUserRef `json:"to"`
			Why        string         `json:"why"`
			Credential string         `json:"credential"`
		}{}
	}
	if out.Leave == nil {
		out.Leave = []struct {
			Space planSpaceResp `json:"space"`
			Role  string        `json:"role"`
		}{}
	}
	return out, true
}

func (s *server) adminUserDeletion(c *call) {
	out, ok := s.userDeletionOf(c, c.r.PathValue("user"), c.r.URL.Query().Get("heir"), nil)
	if !ok {
		return
	}
	writeJSON(c.w, http.StatusOK, out)
}

// deleteUserIn is a request to delete a person.
type deleteUserIn struct {
	Actor     store.User
	IP        string
	Heir      string
	Transfers map[string]string
	Reason    string
	Bulk      string
}

// deletedUser is what deleting a person did.
type deletedUser struct {
	Item      *store.RecycleItem
	Transfers []store.UserTransfer
	Recycled  []string
}

// deleteUser deletes a person, in the order that makes a half done deletion
// harmless (see the top of the file).
func (s *server) deleteUser(ctx context.Context, in deleteUserIn, id string) (*deletedUser, error) {
	sc := store.ScopeOf(in.Actor)
	target, err := store.GetUser(ctx, s.opt.Store.Q(), sc, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, spaces.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	blockers, forbidden, err := s.blockersOf(ctx, in.Actor, target)
	if err != nil {
		return nil, err
	}
	if forbidden {
		return nil, &auth.Error{Kind: auth.Forbidden, Msg: "only a site administrator may delete a site administrator"}
	}
	if len(blockers) > 0 {
		return nil, &auth.Error{Kind: auth.Conflict, Msg: blockers[0]["message"].(string)}
	}
	plan, err := store.PlanUserDeletion(ctx, s.opt.Store.Q(), sc, id, in.Heir, in.Transfers)
	if err != nil {
		return nil, err
	}
	if len(plan.NeedsHeir) > 0 {
		return nil, &heirRequiredError{Spaces: plan.NeedsHeir}
	}
	// What the person was before the first attempt (a deletion asked again after
	// a failure finds them disabled by it) is recorded before anything is done.
	pending, began, err := store.BeginUserDeletion(ctx, s.opt.Store.Q(), id, target.Disabled)
	if err != nil {
		return nil, err
	}
	// 1. disabled at once: sessions end, tokens and grants answer 401.
	disabled := true
	if _, err := s.auth.UpdateUser(ctx, in.Actor, in.IP, id, auth.UserPatch{Disabled: &disabled}); err != nil {
		if began {
			// Nothing was done: no deletion is under way.
			if ferr := store.ForgetUserDeletion(context.WithoutCancel(ctx), s.opt.Store.Q(), id); ferr != nil {
				s.log.Warn("could not drop the record of a deletion that was refused", "user", id, "error", ferr)
			}
		}
		return nil, err
	}
	if err := s.spaces.StepHook("delete-user:disabled"); err != nil {
		return nil, err
	}
	// 2. transfers, then memberships removed, each a transaction of its own.
	out := &deletedUser{}
	var left []store.UserLeft
	for _, t := range plan.Transfer {
		if err := s.spaces.Transfer(ctx, sc, t.Space.ID, t.To); err != nil {
			return nil, err
		}
		tr := store.UserTransfer{Space: t.Space.ID, Name: t.Space.Name, To: t.To, ToUsername: t.ToUsername}
		out.Transfers = append(out.Transfers, tr)
		if err := store.NoteUserDeletion(ctx, s.opt.Store.Q(), id, &tr, nil); err != nil {
			return nil, err
		}
		if err := s.spaces.StepHook("delete-user:transferred"); err != nil {
			return nil, err
		}
	}
	// A person who handed a space on is an editor of it now: they leave it too.
	for _, t := range plan.Transfer {
		if err := s.spaces.LeaveSpace(ctx, t.Space.ID, id); err != nil {
			return nil, leaveError(err)
		}
		lv := store.UserLeft{Space: t.Space.ID, Name: t.Space.Name, Role: "owner"}
		left = append(left, lv)
		if err := store.NoteUserDeletion(ctx, s.opt.Store.Q(), id, nil, &lv); err != nil {
			return nil, err
		}
	}
	for _, l := range plan.Leave {
		if err := s.spaces.LeaveSpace(ctx, l.ID, id); err != nil {
			return nil, leaveError(err)
		}
		lv := store.UserLeft{Space: l.ID, Name: l.Name, Role: l.Role}
		left = append(left, lv)
		if err := store.NoteUserDeletion(ctx, s.opt.Store.Q(), id, nil, &lv); err != nil {
			return nil, err
		}
	}
	if err := s.spaces.StepHook("delete-user:left"); err != nil {
		return nil, err
	}
	// 3. their own spaces and those they owned alone go into the recycle bin, under
	// the person's item once that is made.
	for _, r := range plan.Recycle {
		if _, err := s.spaces.RecycleSpace(ctx, sc, r.ID, spaces.RecycleOptions{By: in.Actor.ID, ByName: in.Actor.Username, Reason: "deleted with " + target.Username,
			Parent: store.UserPendingMarker(id), WithPerson: true}); err != nil {
			return nil, err
		}
		out.Recycled = append(out.Recycled, r.ID)
		if err := s.spaces.StepHook("delete-user:recycled"); err != nil {
			return nil, err
		}
	}
	// 4. what the person held to open the server is revoked (not restored later),
	// and last the row is marked, in one transaction with the item.
	s.revokeUserAccess(ctx, id)
	item, err := s.spaces.FinishUserDeletion(ctx, sc, store.FinishUserDeletionIn{ItemID: page.NewID(s.now()), UserID: id, By: in.Actor.ID, ByName: in.Actor.Username,
		Reason: in.Reason, WasDisabled: target.Disabled, Transfers: out.Transfers, Left: left, Recycled: out.Recycled})
	if err != nil {
		return nil, leaveError(err)
	}
	out.Item = item
	out.Transfers = store.MergeTransfers(pending.Transfers, out.Transfers)
	s.forgetUser(id)
	if s.auth != nil {
		ids := out.Recycled
		if ids == nil {
			ids = []string{}
		}
		s.auth.Audit(auth.AuditEvent{Kind: "user_deleted", Actor: in.Actor.ID, User: id, Username: target.Username, Tenant: target.TenantID, IP: in.IP,
			Detail: map[string]any{"item": item.ID, "user": id, "username": target.Username, "transfers": transfersDetail(out.Transfers), "recycled": ids, "reason": in.Reason, "bulk": in.Bulk}})
	}
	return out, nil
}

func transfersDetail(t []store.UserTransfer) []map[string]string {
	out := make([]map[string]string, len(t))
	for i, x := range t {
		out[i] = map[string]string{"space": x.Space, "to": x.To}
	}
	return out
}

func leaveError(err error) error {
	if errors.Is(err, store.ErrLastOwnerOfSpace) {
		return &auth.Error{Kind: auth.Conflict, Msg: "a space the person owned changed while they were being deleted: try again"}
	}
	return err
}

// revokeUserAccess ends the API tokens and OAuth grants of a person, and
// disconnects their account at the providers (best effort: a platform that
// cannot be reached does not stop the deletion; their authorisation row goes
// either way, with the rest of their data, when they are purged).
func (s *server) revokeUserAccess(ctx context.Context, id string) {
	if s.tokens != nil {
		if rows, err := s.tokens.List(ctx, store.AllTenants(), store.TokenFilter{UserID: id, Limit: 1000}); err == nil {
			for _, t := range rows {
				if t.Revoked.IsZero() {
					if _, err := s.tokens.Revoke(ctx, t.ID); err != nil {
						s.log.Warn("could not revoke a token of a deleted person", "token", t.ID, "error", err)
					}
				}
			}
		}
	}
	if rows, err := store.ListOAuthGrants(ctx, s.opt.Store.Q(), store.AllTenants(), store.GrantFilter{UserID: id, Limit: 1000}); err == nil {
		for _, g := range rows {
			if s.oauth != nil {
				s.oauth.Revoke(ctx, g.ID, "the person was deleted")
			} else {
				store.DeleteOAuthGrant(ctx, s.opt.Store.Q(), g.ID)
			}
		}
	}
	for _, p := range s.opt.Connections.List() {
		if p.Descriptor().ID == "git" {
			continue
		}
		// A person who was never connected is no failure; any other error is logged.
		if err := p.Disconnect(ctx, id); err != nil {
			s.log.Debug("a deleted person's account was not disconnected", "provider", p.Descriptor().ID, "error", err)
		}
	}
}

func (s *server) adminDeleteUser(c *call) {
	var req struct {
		Heir      string            `json:"heir"`
		Transfers map[string]string `json:"transfers"`
		Reason    string            `json:"reason"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if len(req.Reason) > 500 {
		writeError(c.w, http.StatusBadRequest, "invalid", "reason is at most 500 characters")
		return
	}
	res, err := s.deleteUser(c.r.Context(), deleteUserIn{Actor: c.user(), IP: s.auth.ClientAddr(c.r), Heir: req.Heir, Transfers: req.Transfers, Reason: req.Reason}, c.r.PathValue("id"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.recycleItemOf(*res.Item, s.recycleDays(c), withFor(c, s.userChildren(c, res.Item))))
}

// forgetUser drops the copies of a person the sign-in service holds.
func (s *server) forgetUser(id string) {
	if s.auth != nil {
		s.auth.Forget(id)
	}
}

func (s *server) userChildren(c *call, it *store.RecycleItem) []store.RecycleItem {
	l, _ := store.RecycleChildren(c.r.Context(), s.opt.Store.Q(), it.ID)
	return l
}

// restoreUser restores a person's item, with the spaces recycled with them.
func (s *server) restoreUser(c *call, it *store.RecycleItem) {
	res, err := s.spaces.RestoreUser(c.r.Context(), store.AdminScopeOf(c.user()), it.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.forgetUser(it.UserID)
	s.auditRow(c, "user_restored", it.TenantID, map[string]any{"item": it.ID, "user": it.UserID, "username": it.Name, "transfers": transfersDetail(res.Transfers), "recycled": res.Spaces, "reason": it.Reason, "bulk": ""})
	list := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	skipped := res.Skipped
	if skipped == nil {
		skipped = []store.RestoreSkipped{}
	}
	if res.Transfers == nil {
		res.Transfers = []store.UserTransfer{}
	}
	if res.Left == nil {
		res.Left = []store.UserLeft{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Restored  map[string]any         `json:"restored"`
		Skipped   []store.RestoreSkipped `json:"skipped"`
		Transfers []store.UserTransfer   `json:"transfers"`
		Left      []store.UserLeft       `json:"left"`
	}{map[string]any{"item": it.ID, "user": s.toUser(res.User), "spaces": list(res.Spaces)}, skipped, res.Transfers, res.Left})
}

// deletionDecisions is what the preview of deleting a person decides, as one
// string for the hash of a confirmation: the spaces recycled, each space handed
// on and to whom, the spaces left and with which role, the spaces that need an
// heir, and the blockers. A member added to a space of the person, or another
// heir, changes it; a measurement does not.
func deletionDecisions(d userDeletionResp) string {
	var b []string
	for _, r := range d.Recycle {
		b = append(b, "r:"+r.Space.ID)
	}
	for _, t := range d.Transfer {
		b = append(b, "t:"+t.Space.ID+">"+t.To.ID+":"+t.Credential)
	}
	for _, l := range d.Leave {
		b = append(b, "l:"+l.Space.ID+":"+l.Role)
	}
	for _, n := range d.NeedsHeir {
		b = append(b, "h:"+n.ID)
	}
	for _, x := range d.Blockers {
		if r, ok := x["reason"].(string); ok {
			b = append(b, "b:"+r)
		}
	}
	sort.Strings(b)
	return hashOf(b...)
}

// planUsers is the plan of delete_users, disable_users and enable_users over
// people: for a deletion, the previews of each; blockers and spaces without an
// heir are listed for the whole set.
func (s *server) planUsers(c *call, sc store.Scope, req confirmRequest) (confirmPlan, bool) {
	ctx := c.r.Context()
	fp, err := store.UserFingerprints(ctx, s.opt.Store.Q(), sc, req.Users)
	if err != nil {
		s.fail(c.w, err)
		return confirmPlan{}, false
	}
	if len(fp) != len(req.Users) {
		writeError(c.w, http.StatusNotFound, "user_not_found", "there is no such user")
		return confirmPlan{}, false
	}
	ids := append([]string(nil), req.Users...)
	sort.Strings(ids)
	var plan confirmPlan
	parts := []string{req.Action, req.Heir}
	for _, id := range ids {
		parts = append(parts, fp[id])
		if req.Action == "delete_users" {
			d, ok := s.userDeletionOf(c, id, req.Heir, req.Transfers)
			if !ok {
				return plan, false
			}
			plan.Plan = append(plan.Plan, d)
			parts = append(parts, deletionDecisions(d))
			plan.Bytes += d.Bytes
			if len(d.NeedsHeir) > 0 {
				plan.NeedsHeir = append(plan.NeedsHeir, map[string]any{"user": id, "spaces": d.NeedsHeir})
			}
			for _, b := range d.Blockers {
				plan.Blockers = append(plan.Blockers, map[string]any{"user": id, "reason": b["reason"]})
			}
			continue
		}
		u, err := store.GetUser(ctx, s.opt.Store.Q(), sc, id)
		if err != nil {
			s.fail(c.w, err)
			return plan, false
		}
		plan.Plan = append(plan.Plan, map[string]any{"user": map[string]any{"id": u.ID, "username": u.Username}, "action": req.Action})
	}
	if req.Action == "delete_users" {
		keys := make([]string, 0, len(req.Transfers))
		for k, v := range req.Transfers {
			keys = append(keys, k+">"+v)
		}
		sort.Strings(keys)
		parts = append(parts, keys...)
	}
	plan.Hash = hashOf(parts...)
	return plan, true
}
