package api_test

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
)

// bearer is a client that has a token and no cookie.
func (a *acc) bearer(secret, ip string) *client {
	c := a.newClient(ip)
	c.headers["Authorization"] = "Bearer " + secret
	return c
}

// mkToken makes a token as the client and returns its id and secret.
func mkToken(t *testing.T, c *client, access string, days any, spaces ...string) (id, secret string) {
	t.Helper()
	body := map[string]any{"name": "script", "access": access, "spaces": spaces}
	if days != nil {
		body["expiresInDays"] = days
	}
	r := c.do("POST", "/api/account/tokens", body).want(t, 201, "")
	return r.body["token"].(map[string]any)["id"].(string), r.body["secret"].(string)
}

func (a *acc) flush(sid string) {
	a.t.Helper()
	sp, release, err := a.mgr.Acquire(context.Background(), sid)
	if err != nil {
		a.t.Fatal(err)
	}
	defer release()
	if err := sp.Flush(); err != nil {
		a.t.Fatal(err)
	}
}

func spacePath(sid string) string { return "/api/spaces/" + sid }

func newPage(t *testing.T, c *client, sid, title string) map[string]any {
	t.Helper()
	return c.do("POST", spacePath(sid)+"/pages", map[string]any{"meta": map[string]any{"title": title}, "body": "x\n"}).want(t, 201, "").body
}

func TestCreateListAndRevokeTokens(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		mine := tm.owner.personalSpace()
		r := tm.owner.do("POST", "/api/account/tokens", map[string]any{"name": "  Desktop notes ", "access": "write", "spaces": []string{tm.sid, mine}}).want(t, 201, "")
		if cc := r.resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Fatalf("Cache-Control %q", cc)
		}
		secret := r.body["secret"].(string)
		tok := r.body["token"].(map[string]any)
		if !tokens.WellFormed(secret) || tok["prefix"] != secret[:12] || tok["name"] != "Desktop notes" || tok["access"] != "write" ||
			tok["state"] != "active" || tok["lastUsed"] != nil || tok["revoked"] != nil {
			t.Fatalf("%s", r.raw)
		}
		// 90 days by default, counted from now.
		want := a.clock.Now().Add(90 * 24 * time.Hour).Format("2006-01-02T15:04:05Z")
		if tok["expires"] != want {
			t.Fatalf("expires %v, want %s", tok["expires"], want)
		}
		spaces := tok["spaces"].([]any)
		names := map[string]string{}
		for _, s := range spaces {
			m := s.(map[string]any)
			names[m["id"].(string)] = m["name"].(string)
		}
		if len(names) != 2 || names[tm.sid] != "Team" || names[mine] == "" {
			t.Fatalf("%v", names)
		}

		// The list has it, newest first, and never the secret.
		a.clock.Advance(time.Minute)
		id2, secret2 := mkToken(t, tm.owner, "read", 0, tm.sid)
		l := tm.owner.do("GET", "/api/account/tokens", nil).want(t, 200, "")
		if strings.Contains(l.raw, secret) || strings.Contains(l.raw, secret2) {
			t.Fatal("a secret is in the list")
		}
		items := l.body["tokens"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["id"] != id2 || items[0].(map[string]any)["expires"] != nil {
			t.Fatalf("%s", l.raw)
		}
		// Others see none of it.
		if got := tm.editor.do("GET", "/api/account/tokens", nil).want(t, 200, "").body["tokens"].([]any); len(got) != 0 {
			t.Fatal(got)
		}

		// Another person cannot revoke it, and cannot tell it exists.
		tm.editor.do("DELETE", "/api/account/tokens/"+id2, nil).want(t, 404, "not_found")
		tm.owner.do("DELETE", "/api/account/tokens/NOSUCHTOKN", nil).want(t, 404, "not_found")
		b := a.bearer(secret2, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		tm.owner.do("DELETE", "/api/account/tokens/"+id2, nil).want(t, 204, "")
		tm.owner.do("DELETE", "/api/account/tokens/"+id2, nil).want(t, 204, "") // again
		got := b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_revoked")
		if got.resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
			t.Fatalf("%v", got.resp.Header)
		}
		l = tm.owner.do("GET", "/api/account/tokens", nil).want(t, 200, "")
		first := l.body["tokens"].([]any)[0].(map[string]any)
		if first["state"] != "revoked" || first["revoked"] == nil {
			t.Fatalf("%s", l.raw)
		}
	})
}

func TestCreateTokenRules(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		post := func(body map[string]any) result { return tm.owner.do("POST", "/api/account/tokens", body) }
		ok := func() map[string]any { return map[string]any{"name": "n", "spaces": []string{tm.sid}} }
		with := func(k string, v any) map[string]any { m := ok(); m[k] = v; return m }
		many := make([]string, 101)
		for i := range many {
			many[i] = fmt.Sprintf("S%d", i)
		}
		for name, body := range map[string]map[string]any{
			"no name":        with("name", " "),
			"name too long":  with("name", strings.Repeat("x", 81)),
			"bad access":     with("access", "admin"),
			"no spaces":      with("spaces", []string{}),
			"too many":       with("spaces", many),
			"past a year":    with("expiresInDays", 366),
			"negative":       with("expiresInDays", -1),
			"not a number":   with("expiresInDays", "ninety"),
			"unknown fields": with("spaces", "all"),
		} {
			if got := post(body); got.code != 400 || got.body["error"] != "invalid" {
				t.Errorf("%s: %d %s", name, got.code, got.raw)
			}
		}
		// Only spaces one belongs to now, and no "all spaces".
		post(with("spaces", []string{tm.sid, "01JD3K8Q7Z"})).want(t, 404, "not_found")
		tm.outsider.do("POST", "/api/account/tokens", ok()).want(t, 404, "not_found")
		// A viewer may ask for write: what counts is the role when it is used.
		tm.viewer.do("POST", "/api/account/tokens", with("access", "write")).want(t, 201, "")
		// The edges that are fine.
		post(with("expiresInDays", 365)).want(t, 201, "")
		never := post(with("expiresInDays", 0)).want(t, 201, "")
		if never.body["token"].(map[string]any)["expires"] != nil {
			t.Fatal(never.raw)
		}
		// Requests of the wrong kind.
		post(nil).want(t, 400, "invalid")
		tm.owner.do("POST", "/api/account/tokens", "{", "Content-Type", "text/plain").want(t, 415, "invalid")
	})
}

func TestFiftyTokensAndTheLimitMessage(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		var first string
		for i := 0; i < 50; i++ {
			id, _ := mkToken(t, tm.owner, "read", nil, tm.sid)
			if first == "" {
				first = id
			}
			a.clock.Advance(time.Second)
		}
		tm.owner.do("POST", "/api/account/tokens", map[string]any{"name": "n", "spaces": []string{tm.sid}}).want(t, 400, "token_limit")
		tm.owner.do("DELETE", "/api/account/tokens/"+first, nil).want(t, 204, "")
		mkToken(t, tm.owner, "read", nil, tm.sid)
	})
}

func TestATokenIsNeverMoreThanItsOwner(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, readTok := mkToken(t, tm.editor, "read", nil, tm.sid)
		_, writeTok := mkToken(t, tm.editor, "write", nil, tm.sid)
		_, viewerWrite := mkToken(t, tm.viewer, "write", nil, tm.sid)
		p := newPage(t, tm.editor, tm.sid, "Seed")
		pid := p["id"].(string)
		page := spacePath(tm.sid) + "/pages/" + pid
		save := map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Seed"}, "body": "by script\n"}

		rd, wr, vw := a.bearer(readTok, ""), a.bearer(writeTok, ""), a.bearer(viewerWrite, "")
		rd.do("GET", page, nil).want(t, 200, "")
		// A read token trying to write: the token is the limit.
		got := rd.do("PUT", page, save).want(t, 403, "token_scope")
		if !strings.Contains(got.body["message"].(string), "read only") {
			t.Fatal(got.raw)
		}
		rd.do("POST", spacePath(tm.sid)+"/pages", map[string]any{"meta": map[string]any{"title": "n"}, "body": "x"}).want(t, 403, "token_scope")
		// A write token of someone who is only a viewer: the owner is the limit.
		vw.do("GET", page, nil).want(t, 200, "")
		vw.do("PUT", page, save).want(t, 403, "no_permission")
		// The write token of an editor writes.
		wr.do("PUT", page, save).want(t, 200, "")
		// Never members or manage, whatever the owner is: the owner of the
		// space has a write token and still cannot touch them.
		_, ownerTok := mkToken(t, tm.owner, "write", nil, tm.sid)
		ow := a.bearer(ownerTok, "")
		ow.do("GET", spacePath(tm.sid)+"/members", nil).want(t, 200, "") // read: allowed
		ow.do("POST", spacePath(tm.sid)+"/members", map[string]any{"username": "oscar", "role": "viewer"}).want(t, 403, "token_scope")
		ow.do("PATCH", spacePath(tm.sid), map[string]any{"name": "Mine"}).want(t, 403, "token_scope")
		ow.do("PUT", spacePath(tm.sid)+"/types/note", map[string]any{"fields": []any{}}).want(t, 403, "token_scope")
		ow.do("POST", spacePath(tm.sid)+"/reindex", nil).want(t, 403, "token_scope")
		tm.owner.do("PATCH", spacePath(tm.sid), map[string]any{"name": "Team"}).want(t, 200, "")
	})
}

func TestAReadTokenChangesNothing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, readTok := mkToken(t, tm.editor, "read", nil, tm.sid)
		_, writeTok := mkToken(t, tm.editor, "write", nil, tm.sid)
		p := newPage(t, tm.editor, tm.sid, "Seed")
		page := spacePath(tm.sid) + "/pages/" + p["id"].(string)
		rd, wr := a.bearer(readTok, ""), a.bearer(writeTok, "")
		fav := map[string]any{"page": p["id"]}
		for _, c := range []struct{ m, path string }{
			{"DELETE", page + "/draft"},
			{"POST", spacePath(tm.sid) + "/favorites"},
			{"DELETE", spacePath(tm.sid) + "/favorites"},
			{"PUT", spacePath(tm.sid) + "/favorites/order"},
		} {
			rd.do(c.m, c.path, fav).want(t, 403, "token_scope")
		}
		// A write token is not turned away by the scope on these.
		if got := wr.do("PUT", spacePath(tm.sid)+"/favorites/order", map[string]any{"ids": []string{}}); got.code == 403 && got.body["error"] == "token_scope" {
			t.Fatal(got.raw)
		}
	})
}

func TestNoDraftRouteIsForATokenToUse(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, writeTok := mkToken(t, tm.editor, "write", nil, tm.sid)
		p := newPage(t, tm.editor, tm.sid, "Seed")
		page := spacePath(tm.sid) + "/pages/" + p["id"].(string)
		wr := a.bearer(writeTok, "")
		body := map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Seed"}, "body": "x\n"}
		wr.do("PUT", page+"/draft", body).want(t, 403, "token_scope")
		wr.do("DELETE", page+"/draft", nil).want(t, 403, "token_scope")
		wr.do("POST", page+"/commit", nil).want(t, 403, "token_scope")
		// The same calls with the browser session are the person's.
		tm.editor.do("PUT", page+"/draft", body).want(t, 200, "")
	})
}

func TestAScopeOutsideTheToken(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		mine := tm.owner.personalSpace()
		_, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		b := a.bearer(secret, "")
		// The owner's own space, a space that does not exist, a malformed id:
		// all "not in the token", and none says whether it exists.
		for _, sid := range []string{mine, "01JD3K8Q7Z", "x"} {
			b.do("GET", spacePath(sid)+"/tree", nil).want(t, 403, "token_scope")
			b.do("POST", spacePath(sid)+"/pages", map[string]any{"meta": map[string]any{"title": "n"}, "body": "x"}).want(t, 403, "token_scope")
		}
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// The spaces a token reaches are the only ones it lists.
		l := b.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any)
		if len(l) != 1 || l[0].(map[string]any)["id"] != tm.sid {
			t.Fatal(l)
		}
		meta := b.do("GET", "/api/meta", nil).want(t, 200, "").body["spaces"].([]any)
		if len(meta) != 1 {
			t.Fatal(meta)
		}
	})
}

func TestTokenPermissionsAreTheNarrowerOfOwnerAndToken(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, rd := mkToken(t, tm.owner, "read", nil, tm.sid)
		_, wr := mkToken(t, tm.viewer, "write", nil, tm.sid)
		_, ow := mkToken(t, tm.owner, "write", nil, tm.sid)
		perms := func(secret string) string {
			l := a.bearer(secret, "").do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any)
			return fmt.Sprint(l[0].(map[string]any)["permissions"])
		}
		// The owner holds all four; the token never gives members or manage.
		if got := perms(rd); got != "[read]" {
			t.Fatal(got)
		}
		if got := perms(ow); got != "[read write]" {
			t.Fatal(got)
		}
		if got := perms(wr); got != "[read]" { // a viewer
			t.Fatal(got)
		}
	})
}

func TestOwnerRemovedOrDemotedTakesEffectAtOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.editor, "write", nil, tm.sid)
		b := a.bearer(secret, "")
		p := newPage(t, tm.owner, tm.sid, "Doc")
		page := spacePath(tm.sid) + "/pages/" + p["id"].(string)
		save := func(rev any) result {
			return b.do("PUT", page, map[string]any{"baseRev": rev, "meta": map[string]any{"title": "Doc"}, "body": "edit\n"})
		}
		b.do("GET", page, nil).want(t, 200, "")

		// Demoted to viewer: the very next request cannot write, and still reads.
		tm.owner.do("PATCH", spacePath(tm.sid)+"/members/"+tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		save(p["rev"]).want(t, 403, "no_permission")
		b.do("GET", page, nil).want(t, 200, "")
		// Promoted again: it writes again, the token having stayed the same.
		tm.owner.do("PATCH", spacePath(tm.sid)+"/members/"+tm.editorID, map[string]any{"role": "editor"}).want(t, 200, "")
		save(p["rev"]).want(t, 200, "")

		// Removed: not a member, so the space does not exist for the token.
		tm.owner.do("DELETE", spacePath(tm.sid)+"/members/"+tm.editorID, nil).want(t, 204, "")
		b.do("GET", page, nil).want(t, 404, "not_found")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 404, "not_found")
		if l := b.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any); len(l) != 0 {
			t.Fatalf("%v", l)
		}
		// It stays listed, and nothing more.
		items := tm.editor.do("GET", "/api/account/tokens", nil).want(t, 200, "").body["tokens"].([]any)
		sp := items[0].(map[string]any)["spaces"].([]any)[0].(map[string]any)
		if sp["id"] != tm.sid || sp["name"] != "" {
			t.Fatalf("%v", sp)
		}
		// Back in: it works again.
		tm.owner.do("POST", spacePath(tm.sid)+"/members", map[string]any{"username": "edith", "role": "editor"}).want(t, 201, "")
		b.do("GET", page, nil).want(t, 200, "")
	})
}

func TestAnAdministratorsTokenHasNoWayIntoOtherSpaces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		// The session of an administrator opens every space ...
		admin.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// ... but no token can be made for a space they are not in,
		admin.do("POST", "/api/account/tokens", map[string]any{"name": "n", "spaces": []string{tm.sid}}).want(t, 404, "not_found")
		// and one made while they were a member does not keep the way open.
		tm.owner.do("POST", spacePath(tm.sid)+"/members", map[string]any{"username": "kazuha", "role": "editor"}).want(t, 201, "")
		_, secret := mkToken(t, admin, "write", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// Their role in the space is the editor's, not the administrator's all.
		b.do("POST", spacePath(tm.sid)+"/reindex", nil).want(t, 403, "token_scope")
		admin.do("DELETE", spacePath(tm.sid)+"/members/"+mustUserID(t, a, "kazuha"), nil).want(t, 204, "")
		admin.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "") // the session still can
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 404, "not_found")
		// An administrator's token is no administrator's.
		for _, p := range []string{"/api/admin/status", "/api/users", "/api/admin/access", "/api/admin/tokens", "/api/invites"} {
			b.do("GET", p, nil).want(t, 403, "token_scope")
		}
	})
}

func TestWhichRoutesATokenMayUse(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		b := a.bearer(secret, "")
		outside := map[string]bool{"GET /api/meta": true, "GET /api/session": true, "GET /api/spaces": true}
		var allowed, refused int
		for _, rt := range api.Routes() {
			path := routePath(rt, tm.sid)
			got := b.do(rt.Method, path, "{}", typeFor(rt)...)
			scope := got.code == 403 && got.body["error"] == "token_scope"
			tokenRoute := false
			switch {
			case rt.Space && !rt.OrSelf && (rt.Perm == perm.Read || rt.Perm == perm.Write):
				tokenRoute = !strings.HasSuffix(rt.Pattern, "/draft") && !strings.HasSuffix(rt.Pattern, "/commit")
			case outside[rt.Method+" "+rt.Pattern]:
				tokenRoute = true
			}
			if tokenRoute == scope {
				t.Errorf("%s %s: token_scope=%v but the contract says a token may use it: %v (%d %s)", rt.Method, rt.Pattern, scope, tokenRoute, got.code, got.raw)
			}
			if tokenRoute {
				allowed++
			} else {
				refused++
			}
		}
		if allowed < 30 || refused < 25 {
			t.Fatalf("allowed %d, refused %d", allowed, refused)
		}
		// A token cannot make a token, nor sign in.
		b.do("POST", "/api/account/tokens", map[string]any{"name": "n", "spaces": []string{tm.sid}}).want(t, 403, "token_scope")
		b.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 403, "token_scope")
		b.do("POST", "/api/logout", nil).want(t, 403, "token_scope")
	})
}

// The GitHub endpoints act for the person (their authorisation, their owners
// and repositories) or for the administrators: none is for a token, even one
// of an administrator, and even with the server's GitHub service on.
func TestNoGitHubRouteIsForAToken(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{github: true}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Mine"}).want(t, 201, "").body["id"].(string)
		_, secret := mkToken(t, admin, "write", nil, sid)
		b := a.bearer(secret, "")
		var n int
		for _, rt := range api.Routes() {
			if !strings.HasPrefix(rt.Pattern, "/api/connections") && !strings.HasPrefix(rt.Pattern, "/api/admin/connections") {
				continue
			}
			n++
			b.do(rt.Method, routePath(rt, sid), "{}", typeFor(rt)...).want(t, 403, "token_scope")
		}
		if n < 11 {
			t.Fatalf("%d GitHub routes, want at least 11", n)
		}
	})
}

func TestTheHeaderAloneDecides(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		// A good cookie does not rescue a bad token.
		got := tm.owner.do("GET", "/api/spaces", nil, "Authorization", "Bearer pbk_"+strings.Repeat("a", 43)).want(t, 401, "token_invalid")
		if got.resp.Header.Get("WWW-Authenticate") == "" {
			t.Fatal("no WWW-Authenticate")
		}
		tm.owner.do("GET", "/api/session", nil, "Authorization", "Bearer nonsense").want(t, 401, "token_invalid")
		tm.owner.do("GET", "/api/session", nil, "Authorization", "Bearer").want(t, 401, "token_invalid")
		// A good token is the token, whatever cookie comes with it.
		_, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		r := tm.editor.do("GET", "/api/session", nil, "Authorization", "Bearer "+secret).want(t, 200, "")
		if r.user()["username"] != "olive" || r.body["token"] == nil {
			t.Fatalf("%s", r.raw)
		}
		// Not a bearer: another scheme is not a token, and does not stop a cookie.
		tm.owner.do("GET", "/api/spaces", nil, "Authorization", "Basic dXNlcjpwdw==").want(t, 200, "")
		// Case of the scheme does not matter.
		a.newClient("").do("GET", "/api/spaces", nil, "Authorization", "bearer "+secret).want(t, 200, "")
		// No token in the query or a cookie.
		a.newClient("").do("GET", "/api/spaces?access_token="+secret, nil).want(t, 401, "unauthenticated")
	})
}

func TestSessionOfAToken(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		id, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		r := a.bearer(secret, "").do("GET", "/api/session", nil).want(t, 200, "")
		tok := r.body["token"].(map[string]any)
		if r.user()["username"] != "olive" || tok["id"] != id || tok["name"] != "script" || tok["access"] != "write" || fmt.Sprint(tok["spaces"]) != "["+tm.sid+"]" {
			t.Fatalf("%s", r.raw)
		}
		// A session has token: null.
		r = tm.owner.do("GET", "/api/session", nil).want(t, 200, "")
		if v, present := r.body["token"]; !present || v != nil {
			t.Fatalf("%s", r.raw)
		}
		// Signed out, there is no token either.
		if v, present := a.newClient("").do("GET", "/api/session", nil).want(t, 200, "").body["token"]; !present || v != nil {
			t.Fatal(v)
		}
	})
}

func TestExpiredTokens(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.owner, "read", 7, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		a.clock.Advance(7*24*time.Hour - time.Minute)
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		a.clock.Advance(2 * time.Minute)
		got := b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_expired")
		if got.resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
			t.Fatal(got.resp.Header)
		}
		l := tm.owner.do("GET", "/api/account/tokens", nil).want(t, 200, "").body["tokens"].([]any)
		if l[0].(map[string]any)["state"] != "expired" {
			t.Fatalf("%v", l)
		}
		// Kept for 90 days, then gone: the answer changes from expired to invalid.
		a.clock.Advance(91 * 24 * time.Hour)
		a.tokens.Sweep(context.Background())
		a.bearer(secret, "198.51.100.77").do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_invalid")
	})
}

func TestDisabledOwner(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.editor, "write", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": true}).want(t, 200, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_owner_disabled")
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": false}).want(t, 200, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
	})
}

func TestFailedTokensAreRateLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		guess := "pbk_" + strings.Repeat("g", 43)
		attacker := a.bearer(guess, "203.0.113.50")
		for i := 0; i < 10; i++ {
			attacker.do("GET", "/api/spaces", nil).want(t, 401, "token_invalid")
		}
		got := attacker.do("GET", "/api/spaces", nil).want(t, 429, "rate_limited")
		if got.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		// The right token from that address waits too; other addresses and
		// signed-in people do not.
		a.bearer(secret, "203.0.113.50").do("GET", "/api/spaces", nil).want(t, 429, "rate_limited")
		a.bearer(secret, "203.0.113.51").do("GET", "/api/spaces", nil).want(t, 200, "")
		tm.owner.do("GET", "/api/spaces", nil).want(t, 200, "")
		a.clock.Advance(61 * time.Second)
		a.bearer(secret, "203.0.113.50").do("GET", "/api/spaces", nil).want(t, 200, "")
	})
}

func TestRevokedByAnotherProcessIsRefusedWithin30Seconds(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		id, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		if _, err := store.RevokeAPIToken(context.Background(), a.st.Q(), id, a.clock.Now()); err != nil {
			t.Fatal(err)
		}
		a.clock.Advance(10 * time.Second)
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		a.clock.Advance(21 * time.Second)
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_revoked")
	})
}

func TestDisabledSpace(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/disable", nil).want(t, 204, "")
		a.bearer(secret, "").do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 403, "space_disabled")
	})
}

func TestWritesWithATokenSayTheyWereMadeWithOne(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.editor, "write", nil, tm.sid)
		b := a.bearer(secret, "")
		// Typed by the editor, then written by the script, then typed again:
		// the same person, three commits, each said how it was made.
		typed := newPage(t, tm.editor, tm.sid, "Doc")
		id := typed["id"].(string)
		page := spacePath(tm.sid) + "/pages/" + id
		a.flush(tm.sid)
		put := func(c *client, rev any, text string) map[string]any {
			return c.do("PUT", page, map[string]any{"baseRev": rev, "meta": map[string]any{"title": "Doc"}, "body": text}).want(t, 200, "").body
		}
		scripted := put(b, typed["rev"], "by script\n")
		a.flush(tm.sid)
		put(tm.editor, scripted["rev"], "by hand\n")
		a.flush(tm.sid)
		// Properties and creation also.
		made := b.do("POST", spacePath(tm.sid)+"/pages", map[string]any{"meta": map[string]any{"title": "Made by script"}, "body": "x\n"}).want(t, 201, "").body
		a.flush(tm.sid)

		items := tm.owner.do("GET", page+"/history", nil).want(t, 200, "").body["items"].([]any)
		var vias []string
		for _, it := range items {
			v, _ := it.(map[string]any)["via"].(string)
			vias = append(vias, v)
			if au := fmt.Sprint(it.(map[string]any)["author"]); !strings.Contains(au, "edith") {
				t.Errorf("author %v", au)
			}
		}
		if fmt.Sprint(vias) != "[ token ]" {
			t.Fatalf("via of the versions, newest first: %q", vias)
		}
		hist := tm.owner.do("GET", spacePath(tm.sid)+"/pages/"+made["id"].(string)+"/history", nil).want(t, 200, "").body["items"].([]any)
		if len(hist) != 1 || hist[0].(map[string]any)["via"] != "token" {
			t.Fatalf("%v", hist)
		}
		// The trailer is in the commit, and does not name the token.
		sp, release, err := a.mgr.Acquire(context.Background(), tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		out := gitLog(t, sp.Root())
		if strings.Count(out, "Changed-via: API token") != 2 || strings.Contains(out, "Changed-via: script") {
			t.Fatalf("%s", out)
		}
	})
}

func TestAccessSwitchesAndTheTokenList(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		// Both on by default; oauth is only read.
		r := admin.do("GET", "/api/admin/access", nil).want(t, 200, "")
		if r.body["tokens"] != true || r.body["mcp"] != true || r.body["oauth"] != false {
			t.Fatal(r.raw)
		}
		_, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// Off: every token says so, nothing is deleted, the app still works.
		r = admin.do("PUT", "/api/admin/access", map[string]any{"tokens": false}).want(t, 200, "")
		if r.body["tokens"] != false || r.body["mcp"] != true {
			t.Fatal(r.raw)
		}
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 403, "tokens_off")
		a.bearer("rubbish", "").do("GET", "/api/spaces", nil).want(t, 403, "tokens_off")
		tm.owner.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		if got := tm.owner.do("GET", "/api/account/tokens", nil).want(t, 200, "").body["tokens"].([]any); len(got) != 1 {
			t.Fatal(got)
		}
		admin.do("PUT", "/api/admin/access", map[string]any{"tokens": true, "mcp": false}).want(t, 200, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		if r = admin.do("GET", "/api/admin/access", nil).want(t, 200, ""); r.body["mcp"] != false || r.body["tokens"] != true {
			t.Fatal(r.raw)
		}
		// Only administrators.
		tm.owner.do("GET", "/api/admin/access", nil).want(t, 403, "forbidden")
		tm.owner.do("PUT", "/api/admin/access", map[string]any{"tokens": false}).want(t, 403, "forbidden")
		tm.owner.do("GET", "/api/admin/tokens", nil).want(t, 403, "forbidden")
		admin.do("PUT", "/api/admin/access", "{").want(t, 400, "invalid")
	})
}

func TestAdministratorsListAndRevokeEveryonesTokens(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		var ids []string
		for i, c := range []*client{tm.owner, tm.editor, tm.owner, tm.viewer, tm.editor} {
			id, _ := mkToken(t, c, "read", nil, tm.sid)
			ids = append(ids, id)
			a.clock.Advance(time.Duration(i+1) * time.Second)
		}
		all := admin.do("GET", "/api/admin/tokens", nil).want(t, 200, "")
		items := all.body["items"].([]any)
		if len(items) != 5 || all.body["next"] != "" || items[0].(map[string]any)["id"] != ids[4] {
			t.Fatalf("%s", all.raw)
		}
		owner := items[0].(map[string]any)["owner"].(map[string]any)
		if owner["username"] != "edith" || owner["id"] != tm.editorID || owner["name"] == nil {
			t.Fatalf("%v", owner)
		}
		// Pages of two.
		var seen []string
		cursor := ""
		for page := 0; page < 5; page++ {
			q := "/api/admin/tokens?limit=2"
			if cursor != "" {
				q += "&cursor=" + cursor
			}
			r := admin.do("GET", q, nil).want(t, 200, "")
			for _, it := range r.body["items"].([]any) {
				seen = append(seen, it.(map[string]any)["id"].(string))
			}
			cursor, _ = r.body["next"].(string)
			if cursor == "" {
				break
			}
		}
		want := []string{ids[4], ids[3], ids[2], ids[1], ids[0]}
		if fmt.Sprint(seen) != fmt.Sprint(want) {
			t.Fatalf("%v, want %v", seen, want)
		}
		one := admin.do("GET", "/api/admin/tokens?user="+tm.editorID, nil).want(t, 200, "").body["items"].([]any)
		if len(one) != 2 {
			t.Fatalf("%v", one)
		}
		for _, bad := range []string{"limit=0", "limit=201", "limit=x", "cursor=!!"} {
			admin.do("GET", "/api/admin/tokens?"+bad, nil).want(t, 400, "invalid")
		}
		// Revoke someone else's, at once.
		_, secret := mkToken(t, tm.editor, "read", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		var theirs string
		for _, it := range admin.do("GET", "/api/admin/tokens?user="+tm.editorID, nil).want(t, 200, "").body["items"].([]any) {
			if it.(map[string]any)["prefix"] == secret[:12] {
				theirs = it.(map[string]any)["id"].(string)
			}
		}
		admin.do("DELETE", "/api/admin/tokens/"+theirs, nil).want(t, 204, "")
		admin.do("DELETE", "/api/admin/tokens/"+theirs, nil).want(t, 204, "")
		admin.do("DELETE", "/api/admin/tokens/NOSUCHTOKN", nil).want(t, 404, "not_found")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_revoked")
		tm.owner.do("DELETE", "/api/admin/tokens/"+ids[1], nil).want(t, 403, "forbidden")
	})
}

func TestACachedTokenAsksNoQuestionOfTheDatabase(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// The row is gone behind the server's back: a request that read the
		// database would notice.
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM api_tokens`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, http.StatusOK, "")
		}
	})
}

func TestTokensInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := api.NewTokens(tokens.Config{Store: f.opts.Store}, nil)
	ctx := context.Background()
	tok, secret, err := svc.Create(ctx, tokens.CreateParams{UserID: store.LocalUserID, Name: "cli", Access: "read", Spaces: []string{"default"}, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f.h = api.New(api.Options{Store: f.opts.Store, Spaces: f.opts.Spaces, Drafts: f.opts.Drafts, Tokens: svc, Version: "test-1"})
	auth := []string{"Authorization", "Bearer " + secret}
	// With no token the server works as it did.
	if code, _, raw := f.do("GET", base+"/tree", nil); code != 200 {
		t.Fatal(raw)
	}
	code, r, raw := f.do("GET", "/api/session", nil, auth...)
	if code != 200 || r["token"].(map[string]any)["id"] != tok.ID || r["user"].(map[string]any)["id"] != "local" {
		t.Fatal(raw)
	}
	if code, _, raw := f.do("GET", base+"/tree", nil, auth...); code != 200 {
		t.Fatal(raw)
	}
	// Read only.
	if code, r, raw := f.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "n"}, "body": "x"}, auth...); code != 403 || r["error"] != "token_scope" {
		t.Fatal(raw)
	}
	// A token the server does not know is refused, not ignored.
	bad := []string{"Authorization", "Bearer pbk_" + strings.Repeat("a", 43)}
	if code, r, raw := f.do("GET", base+"/tree", nil, bad...); code != 401 || r["error"] != "token_invalid" {
		t.Fatal(raw)
	}
	// The pages of accounts are not there, token or not.
	if code, r, raw := f.do("POST", "/api/account/tokens", map[string]any{"name": "n"}, auth...); code != 404 || r["error"] != "not_found" {
		t.Fatal(raw)
	}
	if code, r, raw := f.do("GET", "/api/account/tokens", nil); code != 404 || r["error"] != "not_found" {
		t.Fatal(raw)
	}
	// Revoked in the service, it stops at once.
	if _, err := svc.Revoke(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if code, r, raw := f.do("GET", base+"/tree", nil, auth...); code != 401 || r["error"] != "token_revoked" {
		t.Fatal(raw)
	}
}

// gitLog is the messages of the commits of a repository, newest first.
func gitLog(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--format=%an|%B@@").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	return string(out)
}
