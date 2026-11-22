package api

import (
	"errors"
	"net/http"

	"github.com/KazuhaHub/PetalBook/internal/auth"
	"github.com/KazuhaHub/PetalBook/internal/page"
	"github.com/KazuhaHub/PetalBook/internal/spaces"
	"github.com/KazuhaHub/PetalBook/internal/store"
)

// access is who may call a route. There is no zero value on purpose: a route
// added without saying so does not register.
type access int

const (
	// public needs no session.
	public access = iota + 1
	// signedIn needs a session.
	signedIn
	// admin needs a session of an administrator.
	admin
	// member needs a session of a member of the {space} in the path. Anyone
	// else, an administrator included, is told the space does not exist.
	member
	// owner is member, and the caller's role in the space must be owner: a
	// member who is not one is refused with 403, the space being theirs to see.
	owner
)

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

	{"GET", "/api/spaces", signedIn, false, false, false, (*server).listSpaces},
	{"POST", "/api/spaces", signedIn, true, false, false, (*server).createSpace},
	{"PATCH", "/api/spaces/{space}", member, true, false, false, (*server).renameSpace},

	{"GET", "/api/spaces/{space}/pages", member, false, false, false, (*server).listPages},
	{"POST", "/api/spaces/{space}/pages", member, false, false, false, (*server).createPage},
	{"GET", "/api/spaces/{space}/pages/{id}", member, false, false, false, (*server).getPage},
	{"PUT", "/api/spaces/{space}/pages/{id}", member, false, false, false, (*server).savePage},
	{"DELETE", "/api/spaces/{space}/pages/{id}", member, false, false, false, (*server).deletePage},
	{"PUT", "/api/spaces/{space}/pages/{id}/draft", member, false, false, false, (*server).putDraft},
	{"DELETE", "/api/spaces/{space}/pages/{id}/draft", member, false, false, false, (*server).deleteDraft},
	{"POST", "/api/spaces/{space}/pages/{id}/commit", member, false, false, false, (*server).commitDraft},
	{"POST", "/api/spaces/{space}/pages/{id}/move", member, false, false, false, (*server).movePage},
	{"GET", "/api/spaces/{space}/tree", member, false, false, false, (*server).getTree},
	{"POST", "/api/spaces/{space}/folders", member, false, false, false, (*server).createFolder},
	{"GET", "/api/spaces/{space}/tags", member, false, false, false, (*server).getTags},
	{"POST", "/api/spaces/{space}/reindex", member, false, false, false, (*server).reindex},

	{"POST", "/api/spaces/{space}/pages/{id}/assets", member, false, false, true, (*server).putAsset},
	{"GET", "/api/spaces/{space}/files/{path...}", member, false, false, false, (*server).getFile},
	{"POST", "/api/spaces/{space}/import", member, false, false, true, (*server).importMarkdown},

	{"GET", "/api/spaces/{space}/pages/{id}/history", member, false, false, false, (*server).getHistory},
	{"GET", "/api/spaces/{space}/pages/{id}/versions/{commit}", member, false, false, false, (*server).getVersion},

	{"GET", "/api/spaces/{space}/remote", owner, true, false, false, (*server).getRemote},
	{"POST", "/api/spaces/{space}/remote/key", owner, true, false, false, (*server).remoteKey},
	{"PUT", "/api/spaces/{space}/remote", owner, true, false, false, (*server).putRemote},
	{"DELETE", "/api/spaces/{space}/remote", owner, true, false, false, (*server).deleteRemote},
	{"POST", "/api/spaces/{space}/remote/sync", owner, true, false, false, (*server).syncRemote},
	{"POST", "/api/spaces/{space}/remote/accept", owner, true, false, false, (*server).acceptRemote},
}

// RouteInfo describes a route for tests and tooling.
type RouteInfo struct {
	Method, Pattern string
	Public          bool // no session needed
	Admin           bool
	Space           bool // a member of {space} (an owner is one)
	Owner           bool // an owner of {space}
	AccountsOnly    bool
	Upload          bool // the body is a file, sent as application/octet-stream
}

// Routes lists every endpoint with its access.
func Routes() []RouteInfo {
	out := make([]RouteInfo, len(routeTable))
	for i, r := range routeTable {
		out[i] = RouteInfo{r.method, r.pattern, r.access == public, r.access == admin, r.access == member || r.access == owner, r.access == owner, r.accountsOnly, r.upload}
	}
	return out
}

// handle wraps a route with the access decision: who is asking, whether they
// may, and which space. This is the only place any of that is decided.
func (s *server) handle(rt route) http.HandlerFunc {
	if rt.access == 0 {
		panic("api: route " + rt.method + " " + rt.pattern + " has no access level")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if rt.accountsOnly && s.single {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		c := &call{w: w, r: r}
		if rt.access != public || rt.optionalUser {
			p, err := s.principal(r)
			switch {
			case err == nil:
				c.p = p
			case rt.access == public:
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
		if rt.access == admin && c.p.User.Role != "admin" {
			writeError(w, http.StatusForbidden, "forbidden", "only an administrator may do that")
			return
		}
		if rt.access == member || rt.access == owner {
			h, err := s.spaces.Open(r.Context(), c.p.User.ID, r.PathValue("space"))
			if err != nil {
				s.fail(w, err)
				return
			}
			c.sp = h
			defer h.Release()
			if rt.access == owner && h.Info.Role != "owner" {
				writeError(w, http.StatusForbidden, "forbidden", "only an owner of the space may do that")
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
