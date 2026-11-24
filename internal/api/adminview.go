package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/store"
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
	seen map[string]time.Time
}

func newViewTable() *viewTable {
	return &viewTable{until: map[viewKey]time.Time{}, seen: map[string]time.Time{}}
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
	key := who + "\x00" + space + "\x00" + what
	v.mu.Lock()
	defer v.mu.Unlock()
	if t, ok := v.seen[key]; ok && now.Sub(t) < time.Hour {
		return false
	}
	if len(v.seen) >= maxViewAudit {
		for k, t := range v.seen {
			if now.Sub(t) >= time.Hour {
				delete(v.seen, k)
			}
		}
		if len(v.seen) >= maxViewAudit {
			v.seen = map[string]time.Time{}
		}
	}
	v.seen[key] = now
	return true
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
	s.auditRow(c, "page_viewed", s.spaceTenant(c), detail)
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
	return c.user().TenantID
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
	sp, err := store.GetSpace(c.r.Context(), s.opt.Store.Q(), store.AdminScopeOf(c.user()), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sp.Personal && !c.isSiteAdmin()) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such space")
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
		writeError(c.w, http.StatusBadRequest, "invalid", "reason is from 1 to 500 characters: why the space is being read")
		return
	}
	id := c.r.PathValue("space")
	sp, ok := s.adminSpaceFor(c, id)
	if !ok {
		return
	}
	if s.spaces.Single() || c.p.SessionID == "" {
		writeError(c.w, http.StatusNotFound, "not_found", "no such space")
		return
	}
	if s.opt.Store.TenantDisabled(sp.TenantID) {
		writeError(c.w, http.StatusForbidden, "space_disabled", "the tenant of this space is disabled")
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
