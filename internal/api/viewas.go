package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Viewing as a person (docs/design/3-6-c.md section 6, docs/api.md "Viewing as
// a person"). An administrator opens, with a reason, an hour in which one
// browser tab sees the app as a given person sees it, read only: the requests
// of that tab carry PetalBook-View-As: <id>, and handle answers them as the
// person, through the routes marked viewable only.
//
// It is a lens over what the administrator could already open with an audited
// view of a space, never a new reach: the person's spaces that are in the
// administrator's scope, never a personal space (of anyone, for anyone), never
// a draft, nothing of the person's account, sessions, tokens, notifications or
// preferences, and no write of any kind. A view belongs to the session that
// opened it, is kept in memory (a restart ends it) and lasts an hour at most.

// ViewAsHeader carries the id of a view as a person on every request of the
// tab that shows it.
const ViewAsHeader = "PetalBook-View-As"

const (
	viewAsTTL = time.Hour
	// maxViewsAs bounds the table: one view per session at most.
	maxViewsAs = 10000
)

// notifyViewedPerson says whether the person is told that an administrator
// viewed the app as them. OWNER DECISION PENDING (docs/design/3-6-c.md
// section 11, question 3; recommended: no). The notification kind
// (store.NotifyViewedAs, notify.ViewedAs) and its texts exist, so telling the
// person is this one line.
const notifyViewedPerson = false

// viewAs is one open view.
type viewAs struct {
	id      string
	session string
	// viewer is the administrator, by id and by name (the banner shows neither;
	// the person's tab says who is looking in GET /api/session).
	viewer, viewerName string
	// target is the person as they were when the view was opened: their
	// spaces are read live, their account is not read again.
	target store.User
	// scope is the tenant the administrator acted in when they opened it ("" for
	// their own scope): their keys are narrowed to it on every request.
	scope string
	// capacity is what the administrator acted as (auditlog.Capacity*).
	capacity string
	until    time.Time
}

type viewAsTable struct {
	mu   sync.Mutex
	byID map[string]*viewAs
	// bySession is the view each session has open: at most one.
	bySession map[string]string
}

func newViewAsTable() *viewAsTable {
	return &viewAsTable{byID: map[string]*viewAs{}, bySession: map[string]string{}}
}

// open stores v under a new id, in place of the view its session had open.
func (t *viewAsTable) open(v viewAs, now time.Time) *viewAs {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("api: no randomness: " + err.Error())
	}
	v.id = "va_" + hex.EncodeToString(b[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.bySession[v.session]; ok {
		delete(t.byID, old)
	}
	if len(t.byID) >= maxViewsAs {
		var oldest *viewAs
		for _, e := range t.byID {
			if !e.until.After(now) {
				t.dropLocked(e)
			} else if oldest == nil || e.until.Before(oldest.until) {
				oldest = e
			}
		}
		if len(t.byID) >= maxViewsAs && oldest != nil {
			t.dropLocked(oldest)
		}
	}
	t.byID[v.id] = &v
	t.bySession[v.session] = v.id
	return &v
}

func (t *viewAsTable) dropLocked(v *viewAs) {
	delete(t.byID, v.id)
	if t.bySession[v.session] == v.id {
		delete(t.bySession, v.session)
	}
}

// get is the view id names if it is open and belongs to session.
func (t *viewAsTable) get(id, session string, now time.Time) (*viewAs, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.byID[id]
	if !ok {
		return nil, false
	}
	if !v.until.After(now) {
		t.dropLocked(v)
		return nil, false
	}
	return v, v.session == session
}

// end ends the view of session, if it is about target; it returns it.
func (t *viewAsTable) end(session, target string) (*viewAs, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.byID[t.bySession[session]]
	if !ok || v.target.ID != target {
		return nil, false
	}
	t.dropLocked(v)
	return v, true
}

func (t *viewAsTable) drop(id string) {
	t.mu.Lock()
	if v, ok := t.byID[id]; ok {
		t.dropLocked(v)
	}
	t.mu.Unlock()
}

// viewAsCall is what a request made in a view carries besides its principal
// (which is the person's).
type viewAsCall struct {
	v *viewAs
	// access is the administrator's, narrowed to the scope the view was
	// opened in: what decides which of the person's spaces are shown.
	access authz.Subject
}

// viewableRoutes are the routes a view as a person reaches: reads of what the
// person sees of their organisation's spaces. Every other route answers 403
// view_as_read_only. TestViewableRoutesAreReadsOutsideThePersonsOwnAreas keeps
// them GETs outside the person's account, notifications, preferences,
// connections, sync and the administration.
var viewableRoutes = map[string]bool{
	"GET /api/meta":                                        true,
	"GET /api/session":                                     true,
	"GET /api/settings":                                    true,
	"GET /api/spaces":                                      true,
	"GET /api/spaces/{space}/pages":                        true,
	"GET /api/spaces/{space}/pages/groups":                 true,
	"GET /api/spaces/{space}/views":                        true,
	"GET /api/spaces/{space}/pages/{id}":                   true,
	"GET /api/spaces/{space}/pages/{id}/backlinks":         true,
	"GET /api/spaces/{space}/pages/{id}/links":             true,
	"GET /api/spaces/{space}/tree":                         true,
	"GET /api/spaces/{space}/tags":                         true,
	"GET /api/spaces/{space}/types":                        true,
	"GET /api/spaces/{space}/appearance":                   true,
	"GET /api/spaces/{space}/files/{path...}":              true,
	"GET /api/spaces/{space}/pages/{id}/history":           true,
	"GET /api/spaces/{space}/pages/{id}/versions/{commit}": true,
}

// viewAsRequested reports whether r names a view as a person.
func viewAsRequested(r *http.Request) bool { return len(r.Header.Values(ViewAsHeader)) > 0 }

func viewAsEnded(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "view_as_ended", "This view as a person has ended.")
}

// enterViewAs turns a request that names a view into the person's: c.p becomes
// the person with no keys and no session (so no administrator's way into a
// space, and no space view, applies), and c.viewAs keeps the administrator's
// narrowed keys. It answers and returns false on a refusal; a refusal never
// falls back to the administrator's own identity. handle has already refused
// a route that is not viewable, and a token.
func (s *server) enterViewAs(c *call) bool {
	vals := c.r.Header.Values(ViewAsHeader)
	if s.single || c.p == nil || c.p.SessionID == "" || len(vals) != 1 {
		viewAsEnded(c.w)
		return false
	}
	v, ok := s.viewsAs.get(vals[0], c.p.SessionID, s.now())
	if !ok {
		viewAsEnded(c.w)
		return false
	}
	access := c.p.Access
	if v.scope != "" {
		_, access = narrowTo(c.p.User, access, v.scope)
	}
	// The person is read again from the user cache (no query until the entry
	// expires or the account changes): disabled, gone, or holding site keys
	// the administrator may not view as, ends the view.
	u, held, err := s.auth.CachedAccess(c.r.Context(), v.target.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.viewsAs.drop(v.id)
		viewAsEnded(c.w)
		return false
	case err != nil:
		s.fail(c.w, err)
		return false
	case u.Disabled || (!held.Site.Empty() && !access.SiteWide(authz.SiteAdminsManage)):
		s.viewsAs.drop(v.id)
		viewAsEnded(c.w)
		return false
	}
	// The administrator still holds the key in the person's tenant (one AND),
	// with the feature on: losing either ends the view.
	viewer := *c.p
	viewer.Access = access
	if err := s.allow(&call{w: c.w, r: c.r, p: &viewer}, authz.TenantUsersViewAs, v.target.TenantID); err != nil {
		var ae *accessError
		if !errors.As(err, &ae) {
			s.fail(c.w, err)
			return false
		}
		s.viewsAs.drop(v.id)
		viewAsEnded(c.w)
		return false
	}
	c.p = &auth.Principal{User: v.target}
	c.viewAs = &viewAsCall{v: v, access: access}
	c.r = c.r.WithContext(auditlog.WithCapacity(c.r.Context(), v.capacity))
	return true
}

// viewAsShows says whether a space of the person is shown in the view: not a
// personal space, in a tenant the administrator holds the key in (their
// scope), and not in a disabled tenant.
func (s *server) viewAsShows(c *call, in *spaces.Info) bool {
	if in.Personal || s.opt.Store.TenantDisabled(in.TenantID) {
		return false
	}
	// The tenant's adminView feature binds an administrator who is not a
	// site-wide holder; a failed read shows nothing.
	feats := &featureRead{s: s}
	d := authz.Decide(c.r.Context(), c.viewAs.access, authz.TenantUsersViewAs, in.TenantID, feats.features())
	return feats.err == nil && d == authz.Allowed
}

// viewAsSpaces is the person's spaces as the view shows them, cut to read,
// and how many are hidden.
func (s *server) viewAsSpaces(c *call, l []spaces.Info) ([]spaces.Info, int) {
	out := make([]spaces.Info, 0, len(l))
	for _, in := range l {
		if !s.viewAsShows(c, &in) {
			continue
		}
		in.Perms &= perm.Of(perm.Read)
		out = append(out, in)
	}
	return out, len(l) - len(out)
}

// openViewAsSpace finishes openSpace for a request in a view: a space the view
// does not show is one that does not exist, and the person's permissions are
// cut to read. Opening a page or a file in it is then audited as a view.
func (s *server) openViewAsSpace(c *call, h *spaces.Handle) bool {
	if !s.viewAsShows(c, &h.Info) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such space.")
		return false
	}
	h.Info.Perms &= perm.Of(perm.Read)
	h.Info.Viewing = true
	return true
}

// viewAsTarget finds the person an administrator may view as: in their scope
// (another tenant's person does not exist for a tenant administrator), never
// themselves, and never an account that holds site keys unless the
// administrator holds site.admins.manage. It answers and returns false
// otherwise.
func (s *server) viewAsTarget(c *call) (*store.User, bool) {
	u, err := store.GetUser(c.r.Context(), s.opt.Store.Q(), c.reach(), c.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such user.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return u, true
}

// adminStartViewAs opens a view as a person.
func (s *server) adminStartViewAs(c *call) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if n := len([]rune(reason)); n < 1 || n > 500 {
		writeError(c.w, http.StatusBadRequest, "invalid", "Reason is from 1 to 500 characters: why the app is being viewed as this person.")
		return
	}
	u, ok := s.viewAsTarget(c)
	if !ok {
		return
	}
	if c.p.SessionID == "" {
		writeError(c.w, http.StatusForbidden, "token_scope", "A view as a person is opened from a browser session.")
		return
	}
	if u.ID == c.user().ID {
		writeError(c.w, http.StatusBadRequest, "invalid", "You see the app as yourself already.")
		return
	}
	if u.Disabled || s.opt.Store.TenantDisabled(u.TenantID) {
		writeError(c.w, http.StatusBadRequest, "invalid", "This person's account, or their tenant, is disabled.")
		return
	}
	if !c.can(authz.SiteAdminsManage) {
		sub, err := s.subjectOf(c.r.Context(), *u)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		if !sub.Site.Empty() {
			writeError(c.w, http.StatusForbidden, "forbidden", "Only a site administrator may view the app as someone who holds site permissions.")
			return
		}
	}
	scope := ""
	if c.scope.narrowed {
		scope = c.scope.tenant
	}
	v := s.viewsAs.open(viewAs{session: c.p.SessionID, viewer: c.user().ID, viewerName: c.user().Name, target: *u,
		scope: scope, capacity: c.scope.capacity, until: s.now().Add(viewAsTTL)}, s.now())
	if err := s.viewAsStarted(c, v, reason); err != nil {
		s.viewsAs.drop(v.id)
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		ID    string `json:"id"`
		Until string `json:"until"`
	}{v.id, rfc3339(v.until)})
}

// viewAsStarted writes the row of a view that opened, and tells the person
// when notifyViewedPerson says so (one transaction for both).
func (s *server) viewAsStarted(c *call, v *viewAs, reason string) error {
	if s.auth == nil {
		return nil
	}
	var to []string
	if notifyViewedPerson {
		to = []string{v.target.ID}
	}
	return notify.WithAudit(c.r.Context(), s.opt.Store.Q(), notify.ViewedAs, notify.Entry{
		Actor: v.viewer, User: v.target.ID, IP: s.auth.ClientAddr(c.r), Tenant: v.target.TenantID,
		Detail:  map[string]any{"viewAs": v.id, "reason": reason, "until": rfc3339(v.until)},
		To:      to,
		Ref:     v.id,
		Payload: map[string]string{"by": v.viewerName, "reason": reason, "until": rfc3339(v.until)},
		Now:     s.now(),
	})
}

// adminEndViewAs ends the session's view of the person.
func (s *server) adminEndViewAs(c *call) {
	u, ok := s.viewAsTarget(c)
	if !ok {
		return
	}
	if v, ok := s.viewsAs.end(c.p.SessionID, u.ID); ok && c.p.SessionID != "" {
		s.audit(c, auth.AuditEvent{Kind: "view_as_ended", Actor: v.viewer, User: v.target.ID, Tenant: v.target.TenantID,
			Capacity: v.capacity, Detail: map[string]any{"viewAs": v.id}})
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// viewAsResp is what GET /api/session says of a view.
type viewAsResp struct {
	ID     string `json:"id"`
	Viewer struct {
		Name string `json:"name"`
	} `json:"viewer"`
	Until  string `json:"until"`
	Hidden int    `json:"hidden"`
}

// viewAsSession is the session of a tab in a view: the person's public
// profile (no address), no keys, and the view.
func (s *server) viewAsSession(c *call) (any, error) {
	l, err := s.spaces.List(c.r.Context(), c.user().ID)
	if err != nil {
		return nil, err
	}
	_, hidden := s.viewAsSpaces(c, l)
	u := c.user()
	type person struct {
		ID          string   `json:"id"`
		Username    string   `json:"username"`
		Name        string   `json:"name"`
		Email       string   `json:"email"`
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
	}
	va := viewAsResp{ID: c.viewAs.v.id, Until: rfc3339(c.viewAs.v.until), Hidden: hidden}
	va.Viewer.Name = c.viewAs.v.viewerName
	return struct {
		Mode   string     `json:"mode"`
		User   person     `json:"user"`
		Setup  bool       `json:"setup"`
		Hold   *string    `json:"hold"`
		Token  *struct{}  `json:"token"`
		ViewAs viewAsResp `json:"viewAs"`
	}{Mode: "accounts", User: person{u.ID, u.Username, u.Name, "", u.Role, []string{}}, ViewAs: va}, nil
}
