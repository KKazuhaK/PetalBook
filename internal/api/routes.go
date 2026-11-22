package api

import (
	"errors"
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// access is who may call a route. There is no zero value on purpose: a route
// added without saying so does not register.
type access struct {
	kind kind
	// need is what a space route asks of the caller's permissions in the space.
	need perm.Perm
	// orSelf waives need for a caller acting on themselves: when the {user} of
	// the path is the caller, being a member is enough (anyone may leave).
	orSelf bool
}

type kind int

const (
	kindPublic kind = iota + 1
	kindSignedIn
	kindAdmin
	kindSpace
)

var (
	// public needs no session.
	public = access{kind: kindPublic}
	// signedIn needs a session.
	signedIn = access{kind: kindSignedIn}
	// admin needs a session of an administrator.
	admin = access{kind: kindAdmin}
)

// inSpaceOrSelf is inSpace, except that the caller may do it to themselves.
func inSpaceOrSelf(need perm.Perm) access {
	a := inSpace(need)
	a.orSelf = true
	return a
}

// inSpace is a route on the {space} of the path: the caller must hold need in it.
// Someone who may not even see the space (not a member, and not an
// administrator) is told it does not exist; a member without the permission
// is refused with 403, the space being theirs to see.
func inSpace(need perm.Perm) access {
	if need == 0 {
		panic("api: a space route must ask for a permission")
	}
	return access{kind: kindSpace, need: need}
}

// call is a request after the middleware has decided it may proceed. Handlers
// read who is asking and which space from here, and never decide either.
type call struct {
	w http.ResponseWriter
	r *http.Request
	// p is the signed-in user; nil only on public routes, where it is also nil
	// for a visitor who is not signed in.
	p *auth.Principal
	// sp is the space of {space} on member routes.
	sp *spaces.Handle
}

func (c *call) user() store.User { return c.p.User }

// author is who a change by the caller is credited to in the space's history
// (see spaces.Manager.AuthorOf).
func (s *server) author(c *call) space.Author { return s.spaces.AuthorOf(c.user()) }

type route struct {
	method, pattern string
	access          access
	// accountsOnly routes answer 404 in single-user mode.
	accountsOnly bool
	// optionalUser makes a public route look for a session without requiring one.
	optionalUser bool
	// upload marks the one kind of route whose body is a file, not JSON: it
	// must be sent as application/octet-stream (see wrap).
	upload bool
	h      func(*server, *call)
}

// routeTable is every endpoint. The access of each is stated here and enforced
// in handle, in one place.
var routeTable = []route{
	{"GET", "/api/meta", public, false, true, false, (*server).getMeta},
	{"GET", "/api/session", public, false, true, false, (*server).getSession},

	{"POST", "/api/setup", public, true, false, false, (*server).setup},
	{"POST", "/api/login", public, true, false, false, (*server).login},
	{"POST", "/api/logout", public, true, false, false, (*server).logout},
	{"POST", "/api/auth/oidc/start", public, true, true, false, (*server).oidcStart},
	{"GET", oidcCallbackPath, public, true, false, false, (*server).oidcCallback},
	{"POST", "/api/invites/check", public, true, false, false, (*server).checkInvite},
	{"POST", "/api/invites/accept", public, true, false, false, (*server).acceptInvite},

	{"GET", "/api/account", signedIn, true, false, false, (*server).getAccount},
	{"PATCH", "/api/account", signedIn, true, false, false, (*server).patchAccount},
	{"POST", "/api/account/password", signedIn, true, false, false, (*server).changePassword},
	{"DELETE", "/api/account/identities/{id}", signedIn, true, false, false, (*server).unlinkIdentity},

	{"GET", "/api/users", admin, true, false, false, (*server).listUsers},
	{"PATCH", "/api/users/{id}", admin, true, false, false, (*server).patchUser},
	{"POST", "/api/invites", admin, true, false, false, (*server).createInvite},
	{"GET", "/api/invites", admin, true, false, false, (*server).listInvites},
	{"DELETE", "/api/invites/{id}", admin, true, false, false, (*server).deleteInvite},

	{"GET", "/api/admin/spaces", admin, true, false, false, (*server).adminListSpaces},
	{"POST", "/api/admin/spaces/{space}/size", admin, true, false, false, (*server).adminResizeSpace},
	{"POST", "/api/admin/spaces/{space}/transfer", admin, true, false, false, (*server).adminTransferSpace},
	{"POST", "/api/admin/spaces/{space}/disable", admin, true, false, false, (*server).adminDisableSpace},
	{"POST", "/api/admin/spaces/{space}/enable", admin, true, false, false, (*server).adminEnableSpace},
	{"DELETE", "/api/admin/spaces/{space}", admin, true, false, false, (*server).adminDeleteSpace},
	{"GET", "/api/admin/status", admin, true, false, false, (*server).adminStatus},

	{"GET", "/api/spaces", signedIn, false, false, false, (*server).listSpaces},
	{"POST", "/api/spaces", signedIn, true, false, false, (*server).createSpace},
	{"PATCH", "/api/spaces/{space}", inSpace(perm.Manage), true, false, false, (*server).renameSpace},

	{"GET", "/api/spaces/{space}/pages", inSpace(perm.Read), false, false, false, (*server).listPages},
	{"POST", "/api/spaces/{space}/pages", inSpace(perm.Write), false, false, false, (*server).createPage},
	{"GET", "/api/spaces/{space}/pages/{id}", inSpace(perm.Read), false, false, false, (*server).getPage},
	{"PUT", "/api/spaces/{space}/pages/{id}", inSpace(perm.Write), false, false, false, (*server).savePage},
	{"DELETE", "/api/spaces/{space}/pages/{id}", inSpace(perm.Write), false, false, false, (*server).deletePage},
	{"PUT", "/api/spaces/{space}/pages/{id}/draft", inSpace(perm.Write), false, false, false, (*server).putDraft},
	{"DELETE", "/api/spaces/{space}/pages/{id}/draft", inSpace(perm.Read), false, false, false, (*server).deleteDraft},
	{"POST", "/api/spaces/{space}/pages/{id}/commit", inSpace(perm.Write), false, false, false, (*server).commitDraft},
	{"POST", "/api/spaces/{space}/pages/{id}/move", inSpace(perm.Write), false, false, false, (*server).movePage},
	{"GET", "/api/spaces/{space}/tree", inSpace(perm.Read), false, false, false, (*server).getTree},
	{"POST", "/api/spaces/{space}/folders", inSpace(perm.Write), false, false, false, (*server).createFolder},
	{"POST", "/api/spaces/{space}/folders/move", inSpace(perm.Write), false, false, false, (*server).moveFolder},
	{"DELETE", "/api/spaces/{space}/folders", inSpace(perm.Write), false, false, false, (*server).deleteFolder},
	{"POST", "/api/spaces/{space}/pages/batch", inSpace(perm.Write), false, false, false, (*server).batchPages},
	{"GET", "/api/spaces/{space}/tags", inSpace(perm.Read), false, false, false, (*server).getTags},
	{"PUT", "/api/spaces/{space}/tags/{tag}", inSpace(perm.Write), false, false, false, (*server).putTag},
	{"POST", "/api/spaces/{space}/reindex", inSpace(perm.Manage), false, false, false, (*server).reindex},

	{"POST", "/api/spaces/{space}/pages/{id}/assets", inSpace(perm.Write), false, false, true, (*server).putAsset},
	{"GET", "/api/spaces/{space}/files/{path...}", inSpace(perm.Read), false, false, false, (*server).getFile},
	{"POST", "/api/spaces/{space}/import", inSpace(perm.Write), false, false, true, (*server).importMarkdown},

	{"GET", "/api/spaces/{space}/pages/{id}/history", inSpace(perm.Read), false, false, false, (*server).getHistory},
	{"GET", "/api/spaces/{space}/pages/{id}/versions/{commit}", inSpace(perm.Read), false, false, false, (*server).getVersion},

	{"GET", "/api/spaces/{space}/members", inSpace(perm.Read), true, false, false, (*server).listMembers},
	{"POST", "/api/spaces/{space}/members", inSpace(perm.Members), true, false, false, (*server).addMember},
	{"PATCH", "/api/spaces/{space}/members/{user}", inSpace(perm.Members), true, false, false, (*server).patchMember},
	{"DELETE", "/api/spaces/{space}/members/{user}", inSpaceOrSelf(perm.Members), true, false, false, (*server).removeMember},

	{"GET", "/api/spaces/{space}/remote", inSpace(perm.Manage), true, false, false, (*server).getRemote},
	{"POST", "/api/spaces/{space}/remote/key", inSpace(perm.Manage), true, false, false, (*server).remoteKey},
	{"PUT", "/api/spaces/{space}/remote", inSpace(perm.Manage), true, false, false, (*server).putRemote},
	{"DELETE", "/api/spaces/{space}/remote", inSpace(perm.Manage), true, false, false, (*server).deleteRemote},
	{"POST", "/api/spaces/{space}/remote/sync", inSpace(perm.Manage), true, false, false, (*server).syncRemote},
	{"POST", "/api/spaces/{space}/remote/accept", inSpace(perm.Manage), true, false, false, (*server).acceptRemote},
}

// RouteInfo describes a route for tests and tooling.
type RouteInfo struct {
	Method, Pattern string
	Public          bool // no session needed
	Admin           bool
	Space           bool      // a route on {space}
	Perm            perm.Perm // what a space route asks of the caller in the space
	OrSelf          bool      // the permission is waived for the caller's own {user}
	AccountsOnly    bool
	Upload          bool // the body is a file, sent as application/octet-stream
}

// Routes lists every endpoint with its access.
func Routes() []RouteInfo {
	out := make([]RouteInfo, len(routeTable))
	for i, r := range routeTable {
		a := r.access
		out[i] = RouteInfo{Method: r.method, Pattern: r.pattern, Public: a.kind == kindPublic, Admin: a.kind == kindAdmin,
			Space: a.kind == kindSpace, Perm: a.need, OrSelf: a.orSelf, AccountsOnly: r.accountsOnly, Upload: r.upload}
	}
	return out
}

// handle wraps a route with the access decision: who is asking, whether they
// may, and which space. This is the only place any of that is decided.
func (s *server) handle(rt route) http.HandlerFunc {
	if rt.access.kind == 0 {
		panic("api: route " + rt.method + " " + rt.pattern + " has no access level")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if rt.accountsOnly && s.single {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		c := &call{w: w, r: r}
		if rt.access.kind != kindPublic || rt.optionalUser {
			p, err := s.principal(r)
			switch {
			case err == nil:
				c.p = p
			case rt.access.kind == kindPublic:
				// A visitor without a session is fine here; a broken database is not.
				var ae *auth.Error
				if !errors.As(err, &ae) {
					s.fail(w, err)
					return
				}
			default:
				s.fail(w, err)
				return
			}
		}
		if rt.access.kind == kindAdmin && c.p.User.Role != "admin" {
			writeError(w, http.StatusForbidden, "forbidden", "only an administrator may do that")
			return
		}
		if rt.access.kind == kindSpace {
			self := rt.access.orSelf && r.PathValue("user") == c.p.User.ID
			var h *spaces.Handle
			var err error
			if self {
				// Acting on one's own membership (leaving) needs no open space.
				h, err = s.spaces.OpenMembership(r.Context(), c.p.User, r.PathValue("space"))
			}
			if !self || errors.Is(err, spaces.ErrNotFound) {
				h, err = s.spaces.Open(r.Context(), c.p.User, r.PathValue("space"))
			}
			if err != nil {
				s.fail(w, err)
				return
			}
			c.sp = h
			defer h.Release()
			need := rt.access.need
			if self {
				need = perm.Read
			}
			if !h.Info.Perms.Has(need) {
				writeError(w, http.StatusForbidden, "forbidden", "your role in this space does not allow that; it needs the "+need.String()+" permission")
				return
			}
		}
		rt.h(s, c)
	}
}

// principal is who a request is from: the built-in user in single-user mode,
// else the owner of the session cookie.
func (s *server) principal(r *http.Request) (*auth.Principal, error) {
	if s.single {
		return &auth.Principal{User: s.local}, nil
	}
	ck, err := r.Cookie(s.auth.SessionCookieName())
	if err != nil {
		return nil, &auth.Error{Kind: auth.Unauthenticated, Msg: "sign in to continue"}
	}
	return s.auth.Authenticate(r.Context(), ck.Value)
}

// pageID returns the {id} of the request, answering 404 for anything that
// cannot be a page id.
func (s *server) pageID(c *call) (string, bool) {
	id := c.r.PathValue("id")
	if !page.ValidID(id) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such page")
		return "", false
	}
	return id, true
}
