package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/store"
)

// Viewing a space (docs/api.md, "Viewing a space"). An administrator who is not
// a member of a space manages it and does not read it. To read it they open a
// view, with a reason: for an hour their session may read the space through the
// ordinary read endpoints, exactly as a viewer would, and nothing else. Every
// view is audited, and so is the first opening of each page or file in it.
//
// A view is a map from (session, space) to its end, kept in memory: it belongs
// to the session that opened it (never to a token, nor to a restart), and it is
// checked only where a request meets a space its caller is not a member of, so a
// member's request pays nothing for it.

const (
	viewTTL      = time.Hour
	maxViewRows  = 100000
	maxViewAudit = 100000
)

type viewKey struct{ session, space string }

type viewTable struct {
	mu    sync.Mutex
	until map[viewKey]time.Time
	// seen is when each (administrator, space, page or path) was last audited as
	// opened: at most one row an hour.
	seen *onceAnHour
}

func newViewTable() *viewTable {
	return &viewTable{until: map[viewKey]time.Time{}, seen: newOnceAnHour(maxViewAudit)}
}

// open starts or extends a view and returns when it ends.
func (v *viewTable) open(session, space string, now time.Time) time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.until) >= maxViewRows {
		for k, t := range v.until {
			if !t.After(now) {
				delete(v.until, k)
			}
		}
	}
	end := now.Add(viewTTL)
	v.until[viewKey{session, space}] = end
	return end
}

func (v *viewTable) end(session, space string) {
	v.mu.Lock()
	delete(v.until, viewKey{session, space})
	v.mu.Unlock()
}

func (v *viewTable) active(session, space string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.until[viewKey{session, space}]
	if ok && !t.After(now) {
		delete(v.until, viewKey{session, space})
		return false
	}
	return ok
}

// openIn is the end of each view the session has open among the spaces, one
// lock for all of them.
func (v *viewTable) openIn(session string, ids []string, now time.Time) map[string]time.Time {
	if session == "" || len(ids) == 0 {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var out map[string]time.Time
	for _, id := range ids {
		if t, ok := v.until[viewKey{session, id}]; ok && t.After(now) {
			if out == nil {
				out = map[string]time.Time{}
			}
			out[id] = t
		}
	}
	return out
}

// adminViewResp is a view the caller's session has open: when it started (the
// last time it was opened or extended) and when it ends.
type adminViewResp struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

// firstOpen says whether (who, space, what) has not been audited in the last
// hour, and notes that it is now.
func (v *viewTable) firstOpen(who, space, what string, now time.Time) bool {
	return v.seen.first(who+"\x00"+space+"\x00"+what, now)
}

// onceAnHour is a bounded table of when each key was last noted: what is
// audited at most once an hour. At its cap the keys older than an hour are
// dropped, and if none are, all of them (a burst of new keys then gets a row
// each again, which is the safe side for an audit).
type onceAnHour struct {
	mu   sync.Mutex
	max  int
	seen map[string]time.Time
}

func newOnceAnHour(limit int) *onceAnHour {
	return &onceAnHour{max: limit, seen: map[string]time.Time{}}
}

// first says whether key has not been noted in the last hour, and notes it.
func (o *onceAnHour) first(key string, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.seen[key]; ok && now.Sub(t) < time.Hour {
		return false
	}
	if len(o.seen) >= o.max {
		for k, t := range o.seen {
			if now.Sub(t) >= time.Hour {
				delete(o.seen, k)
			}
		}
		if len(o.seen) >= o.max {
			o.seen = map[string]time.Time{}
		}
	}
	o.seen[key] = now
	return true
}

func (o *onceAnHour) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

// viewing says whether the caller's session has a view of the space open.
func (s *server) viewing(c *call, space string) bool {
	if c.p == nil || c.p.Grant != nil || c.p.SessionID == "" {
		return false
	}
	return s.views.active(c.p.SessionID, space, s.now())
}

// viewedWhat records that an administrator opened a page or file in a view (at
// most one row an hour for each administrator and page).
func (s *server) viewedWhat(c *call, what string, detail map[string]any) {
	if c.sp == nil || !c.sp.Info.Viewing {
		return
	}
	if !s.views.firstOpen(c.user().ID, c.sp.Info.ID, what, s.now()) {
		return
	}
	detail["space"] = c.sp.Info.ID
	// A view is administration even on a space route: the row says as what.
	s.audit(c, auth.AuditEvent{Kind: "page_viewed", Actor: c.user().ID, Tenant: s.spaceTenant(c), Detail: detail, Capacity: capacityFor(c.p.Access, authz.TenantDataView)})
}

func (s *server) viewedPage(c *call, id string) {
	s.viewedWhat(c, "page:"+id, map[string]any{"page": id})
}
func (s *server) viewedPath(c *call, p string) { s.viewedWhat(c, "path:"+p, map[string]any{"path": p}) }

// spaceTenant is the tenant of the space the call opened (the audit rows carry
// the tenant of the space).
func (s *server) spaceTenant(c *call) string {
	if sp, err := store.GetSpace(c.r.Context(), s.opt.Store.Q(), store.AllTenants(), c.sp.Info.ID); err == nil {
		return sp.TenantID
	}
	return c.tenant()
}

// adminMeta are the routes an administrator who is not a member and has no view
// may use to read: the names of a space's people, which managing them needs.
var adminMetaRoutes = map[string]bool{
	"GET /api/spaces/{space}/members":           true,
	"GET /api/spaces/{space}/members/effective": true,
}

// adminSpaceFor finds a space an administrator may view or export: in their
// scope, and not a personal space unless they are a site administrator (a tenant
// administrator never sees a person's own space). It answers 404 otherwise.
func (s *server) adminSpaceFor(c *call, id string) (*store.SpaceInfo, bool) {
	sp, err := store.GetSpace(c.r.Context(), s.opt.Store.Q(), c.adminReach(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sp.Personal && !c.can(authz.SiteSpacesPersonalContent)) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return sp, true
}

// adminOpenView starts, or extends, a view of a space.
func (s *server) adminOpenView(c *call) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if n := len([]rune(reason)); n < 1 || n > 500 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Reason is from 1 to 500 characters: why the space is being read.")
		return
	}
	id := c.r.PathValue("space")
	sp, ok := s.adminSpaceFor(c, id)
	if !ok {
		return
	}
	if s.spaces.Single() || c.p.SessionID == "" {
		writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
		return
	}
	if s.opt.Store.TenantDisabled(sp.TenantID) {
		writeError(c.w, http.StatusForbidden, "space_disabled", "The tenant of this space is disabled.")
		return
	}
	until := s.views.open(c.p.SessionID, sp.ID, s.now())
	s.auditRow(c, "space_viewed", sp.TenantID, map[string]any{"space": sp.ID, "reason": reason, "until": rfc3339(until)})
	writeJSON(c.w, http.StatusOK, struct {
		Space string `json:"space"`
		Until string `json:"until"`
	}{sp.ID, rfc3339(until)})
}

func (s *server) adminEndView(c *call) {
	id := c.r.PathValue("space")
	if _, ok := s.adminSpaceFor(c, id); !ok {
		return
	}
	if c.p.SessionID != "" {
		s.views.end(c.p.SessionID, id)
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// withView adds what the caller may do in a space they are not a member of: read
// it while they have a view of it open, and the names of its people always.
func (s *server) withView(c *call, rt route, perms perm.Set, id string) (perm.Set, bool) {
	viewing := s.viewing(c, id)
	if viewing {
		perms |= perm.Of(perm.Read)
	} else if adminMetaRoutes[rt.method+" "+rt.pattern] {
		perms |= perm.Of(perm.Read)
	}
	return perms, viewing
}
