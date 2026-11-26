package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Permission keys (docs/api.md, "Permission keys"). Every route outside spaces
// names the key it needs in the route table, and handle asks allow; nothing
// else on the way decides who may call a route.

// accessError is a refusal of allow.
type accessError struct {
	status    int
	code, msg string
	key       string
	feature   string
}

func (e *accessError) Error() string { return e.msg }

// allow is the check of a permission key in a tenant, in this order:
//  1. the tenant's feature the key needs (a holder of the key across the
//     site, a site administrator, is not bound by it): 403 feature_off;
//  2. the key, among what the caller holds there (one AND on the subject the
//     session cache built): 403 forbidden;
//  3. verify, where 3.7 checks the caller's standing in that organization.
//
// tenant is always explicit: the caller's own for the route table.
func (s *server) allow(c *call, k authz.Key, tenant string) error {
	feats := &featureRead{s: s}
	d := authz.Decide(c.r.Context(), c.p.Access, k, tenant, feats.features())
	if feats.err != nil {
		// Not knowing is not "on": whatever Decide made of it, nothing is granted.
		return feats.err
	}
	switch d {
	case authz.FeatureOff:
		return featureOffError(k.Feature(), k.Name())
	case authz.Denied:
		msg := "Only an administrator may do that."
		if k.Scope() == authz.ScopeSite {
			msg = "Only an administrator of the whole server may do that."
		}
		return &accessError{status: http.StatusForbidden, code: "forbidden", key: k.Name(), msg: msg}
	}
	return s.verify(c, k, tenant)
}

// featureRead is the features of one decision, which keeps the failure of a
// read (authz.Features cannot return one) for allow to refuse on.
type featureRead struct {
	s   *server
	err error
}

func (f *featureRead) features() authz.Features {
	if f.s.features() == nil {
		return nil
	}
	return f
}

func (f *featureRead) On(ctx context.Context, tenant, feature string) bool {
	if f.err != nil {
		return true
	}
	on, err := f.s.featureOn(ctx, tenant, feature)
	f.err = err
	return on
}

// features is the features of the tenants, which the settings answer; nil in
// single-user mode, where every feature is on.
func (s *server) features() authz.Features {
	if s.single {
		return nil
	}
	return s.settings
}

// subjectOf is what an account holds when it acts outside a request (a bulk
// job's actor, as they are now): built-in and custom roles.
func (s *server) subjectOf(ctx context.Context, u store.User) (authz.Subject, error) {
	if s.auth == nil {
		return auth.AccessOf(u), nil
	}
	return s.auth.SubjectOf(ctx, u)
}

// verify is the last step of allow. It allows everything until 3.7, where a
// person's membership of the tenant is checked here.
func (s *server) verify(*call, authz.Key, string) error { return nil }

// refuse answers a refusal of allow (any other error as fail does).
func (s *server) refuse(w http.ResponseWriter, err error) {
	var ae *accessError
	if !errors.As(err, &ae) {
		s.fail(w, err)
		return
	}
	writeJSON(w, ae.status, errorBody{Error: ae.code, Message: ae.msg, Permission: ae.key, Feature: ae.feature})
}

// permissionsOf is what GET /api/session says the caller holds: the names of
// the keys they hold in their own tenant, site keys included. A request with a
// token holds none of them (no route outside spaces takes a token).
func permissionsOf(c *call) []string {
	if c.p.Grant != nil {
		return []string{}
	}
	return c.p.Access.In(c.tenant()).Names()
}

type permissionResp struct {
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	Feature     string `json:"feature"`
	Description string `json:"description"`
}

// adminPermissions lists the registry of keys.
func (s *server) adminPermissions(c *call) {
	all := authz.All()
	out := make([]permissionResp, len(all))
	for i, in := range all {
		out[i] = permissionResp{in.Name, in.Scope.String(), in.Feature, in.Description}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Permissions []permissionResp `json:"permissions"`
	}{out})
}

// adminTakeOverSpace makes the caller the owner of a space they manage without
// being its member (docs/api.md, "Taking over a space"): the one way an
// administrator gets an owner's say, its remote included. It needs a reason,
// is audited, and every owner it replaces is told.
func (s *server) adminTakeOverSpace(c *call) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if n := len([]rune(reason)); n < 1 || n > 500 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Reason is from 1 to 500 characters: why the space is being taken over.")
		return
	}
	s.transferSpace(c, "", reason, "space_taken_over")
}

// transferSpace makes userID (the caller, for "": a takeover) the owner of the
// {space} and the other owners its editors, and records it: an audit row of
// kind, and a notice to each owner it replaced.
func (s *server) transferSpace(c *call, userID, reason, kind string) {
	id := c.r.PathValue("space")
	var t *spaces.Transferred
	var err error
	if userID == "" {
		if c.inOtherTenant() {
			// A takeover in another tenant makes a member across tenants, which
			// only a site administrator's key allows: in the site's scope.
			writeError(c.w, http.StatusForbidden, "forbidden", "A space of another tenant is taken over in the whole site's scope, not in the tenant's.")
			return
		}
		userID = c.user().ID
		t, err = s.spaces.TakeOver(c.r.Context(), c.adminReach(), id, c.user(), c.p.Access)
	} else {
		t, err = s.spaces.Transfer(c.r.Context(), c.adminReach(), id, userID)
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	ev := notify.SpaceTransferred
	if kind == "space_taken_over" {
		ev = notify.SpaceTakenOver
	}
	ip := ""
	if s.auth != nil {
		ip = s.auth.ClientAddr(c.r)
	}
	// A failure is only logged: the transfer is done.
	err = notify.WithAudit(c.r.Context(), s.opt.Store.Q(), ev, notify.Entry{
		Actor: c.user().ID, IP: ip, Tenant: t.TenantID,
		Detail:  map[string]any{"space": id, "to": userID, "previous": t.Previous, "ownerGroups": t.OwnerGroups, "reason": reason},
		To:      append(append([]string(nil), t.Previous...), t.GroupOwners...),
		Ref:     id,
		Payload: map[string]string{"space": t.Name, "by": c.user().Name, "reason": reason},
		Now:     s.now(),
	})
	if err != nil {
		s.log.Warn("could not write the audit row and the notifications", "error", err)
	}
	c.w.WriteHeader(http.StatusNoContent)
}
