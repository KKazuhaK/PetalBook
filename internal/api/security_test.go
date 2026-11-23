package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func TestUnknownUserCostsAsMuchAsAWrongPassword(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		ops := func() int64 { return auth.HashOps(a.auth) }
		before := ops()
		a.newClient("203.0.113.1").do("POST", "/api/login", map[string]any{"username": "kazuha", "password": "wrong password"}).want(t, 401, "")
		wrong := ops() - before
		before = ops()
		a.newClient("203.0.113.2").do("POST", "/api/login", map[string]any{"username": "nobody", "password": "wrong password"}).want(t, 401, "")
		unknown := ops() - before
		if wrong != 1 || unknown != 1 {
			t.Fatalf("a wrong password ran %d hashes, an unknown user %d", wrong, unknown)
		}
	})
}

// typeFor is the content type a route's body must have, as extra headers.
func typeFor(rt api.RouteInfo) []string {
	if rt.Upload {
		return []string{"Content-Type", "application/octet-stream"}
	}
	if rt.Method == "POST" && rt.Pattern == "/api/auth/saml/{slug}/acs" {
		// The identity provider posts a form there.
		return []string{"Content-Type", "application/x-www-form-urlencoded"}
	}
	return nil
}

func TestEveryProtectedRouteWantsASession(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		sid := admin.personalSpace()
		anon := a.newClient("")
		fill := func(p string) string {
			return strings.NewReplacer("{space}", sid, "{id}", "01JD3K8Q7Z").Replace(p)
		}
		checked := 0
		for _, rt := range api.Routes() {
			path := fill(rt.Pattern)
			if rt.Public {
				continue
			}
			checked++
			r := anon.do(rt.Method, path, "{}", typeFor(rt)...)
			if r.code != 401 || r.body["error"] != "unauthenticated" {
				t.Errorf("%s %s without a session: %d %s", rt.Method, rt.Pattern, r.code, r.raw)
			}
		}
		if checked < 25 {
			t.Fatalf("only %d protected routes were checked: is the route list empty?", checked)
		}
		anon.do("GET", "/api/meta", nil).want(t, 200, "")
		anon.do("GET", "/api/session", nil).want(t, 200, "")
		// A cookie that names nothing is no session either.
		bad := a.newClient("")
		bad.cookies[a.auth.SessionCookieName()] = &http.Cookie{Name: a.auth.SessionCookieName(), Value: "forged"}
		bad.do("GET", "/api/spaces", nil).want(t, 401, "unauthenticated")
		bad.do("GET", "/api/meta", nil).want(t, 200, "") // meta just shows no spaces
	})
}

func TestMemberRoutesAreNotForOtherPeopleSpaces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		boss := a.setup("kazuha", pw) // an administrator
		tang := a.addUser("tang", pw, "member")
		tangSpace := tang.personalSpace()
		r := tang.do("POST", "/api/spaces/"+tangSpace+"/pages", map[string]any{"meta": map[string]any{"title": "Private"}, "body": "secret\n"}).want(t, 201, "")
		pageID := r.body["id"].(string)
		tang.do("PUT", "/api/spaces/"+tangSpace+"/pages/"+pageID+"/draft", map[string]any{"baseRev": r.body["rev"], "meta": map[string]any{"title": "Private"}, "body": "secret edit\n"}).want(t, 200, "")

		// What "no such space" looks like for a space that is not there at all.
		ghost := boss.do("GET", "/api/spaces/0000000000/tree", nil).want(t, 404, "not_found")

		n := 0
		for _, rt := range api.Routes() {
			if !rt.Space {
				continue
			}
			n++
			path := strings.NewReplacer("{space}", tangSpace, "{id}", pageID).Replace(rt.Pattern)
			// A member of nothing here is told the same as for a space that does not
			// exist. (An administrator is not refused: see TestAdministratorSeesEverySpace.)
			for who, c := range map[string]*client{"member": a.addUser(fmt.Sprintf("m%d", n), pw, "member")} {
				got := c.do(rt.Method, path, "{}", typeFor(rt)...)
				if got.code != 404 || got.raw != ghost.raw {
					t.Errorf("%s %s as %s: %d %s", rt.Method, rt.Pattern, who, got.code, got.raw)
				}
			}
		}
		if n < 12 {
			t.Fatalf("only %d space routes: is the route list empty?", n)
		}
		// Nothing of it changed, and the owner still sees everything.
		p := tang.do("GET", "/api/spaces/"+tangSpace+"/pages/"+pageID, nil).want(t, 200, "")
		if p.body["body"] != "secret\n" || p.body["draft"] == nil {
			t.Fatal(p.raw)
		}
		// The administrator's lists do not include it.
		for _, l := range boss.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if l.(map[string]any)["id"] == tangSpace {
				t.Fatal("an administrator can see another user's personal space")
			}
		}
		if l := boss.do("GET", "/api/meta", nil).body["spaces"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}
	})
}

func TestSpaces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		personal := c.personalSpace()
		r := c.do("POST", "/api/spaces", map[string]any{"name": "Tang"}).want(t, 201, "")
		if r.body["name"] != "Tang" || r.body["role"] != "owner" || r.body["personal"] != false || r.resp.Header.Get("Location") == "" {
			t.Fatal(r.raw)
		}
		tangID := r.body["id"].(string)
		c.do("POST", "/api/spaces", map[string]any{"name": "Alpha"}).want(t, 201, "")
		c.do("POST", "/api/spaces", map[string]any{"name": ""}).want(t, 400, "invalid")
		c.do("POST", "/api/spaces", map[string]any{"name": strings.Repeat("x", 81)}).want(t, 400, "invalid")
		c.do("POST", "/api/spaces", map[string]any{"name": "a\nb"}).want(t, 400, "invalid")
		l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any)
		var names []string
		for _, s := range l {
			names = append(names, s.(map[string]any)["name"].(string))
		}
		if len(l) != 3 || l[0].(map[string]any)["id"] != personal || strings.Join(names[1:], ",") != "Alpha,Tang" {
			t.Fatalf("order: %v", names)
		}
		// The new space works: pages, and it is its own repository.
		c.do("POST", "/api/spaces/"+tangID+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 201, "")
		if l := c.do("GET", "/api/spaces/"+tangID+"/pages", nil).body["items"].([]any); len(l) != 1 {
			t.Fatal("the page is not in the new space")
		}
		if l := c.do("GET", "/api/spaces/"+personal+"/pages", nil).body["items"].([]any); len(l) != 0 {
			t.Fatal("spaces share pages")
		}
		// Renaming shows at once in every list.
		r = c.do("PATCH", "/api/spaces/"+tangID, map[string]any{"name": "Zed"}).want(t, 200, "")
		if r.body["name"] != "Zed" || r.body["id"] != tangID {
			t.Fatal(r.raw)
		}
		if got := c.do("GET", "/api/meta", nil).raw; !strings.Contains(got, `"Zed"`) || strings.Contains(got, `"Tang"`) {
			t.Fatal(got)
		}
		c.do("PATCH", "/api/spaces/"+tangID, map[string]any{"name": ""}).want(t, 400, "invalid")
		// Someone else cannot rename it.
		other := a.addUser("lin", pw, "member")
		other.do("PATCH", "/api/spaces/"+tangID, map[string]any{"name": "Mine"}).want(t, 404, "not_found")
	})
}

func TestNoHalfMadeSpaceOrAccountWhenAStepFails(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		ctx := context.Background()
		count := func(table string) int {
			var n int
			if err := a.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		// Make the last step of each creation fail by hiding the table it writes.
		hide := func(table string) func() {
			if _, err := a.st.Q().Exec(ctx, `ALTER TABLE `+table+` RENAME TO `+table+`_hidden`); err != nil {
				t.Fatal(err)
			}
			return func() {
				if _, err := a.st.Q().Exec(ctx, `ALTER TABLE `+table+`_hidden RENAME TO `+table); err != nil {
					t.Fatal(err)
				}
			}
		}
		spaces0, dirs0 := count("spaces"), len(listDir(t, a.dir+"/spaces"))

		restore := hide("space_members")
		if _, err := a.mgr.Create(ctx, mustUserID(t, a, "kazuha"), "Doomed"); err == nil {
			t.Fatal("the space was made although a step failed")
		}
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "doomed", Role: "member", Password: pw}); err == nil {
			t.Fatal("the account was made although a step failed")
		}
		restore()
		if count("spaces") != spaces0 || count("users") != 1 || len(listDir(t, a.dir+"/spaces")) != dirs0 {
			t.Fatalf("a failure left something behind: %d spaces, %d users, %v", count("spaces"), count("users"), listDir(t, a.dir+"/spaces"))
		}
		if len(c.do("GET", "/api/spaces", nil).body["spaces"].([]any)) != 1 {
			t.Fatal("a half-made space is visible")
		}
		// And the same accounts can be made once the fault is gone.
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "doomed", Role: "member", Password: pw}); err != nil {
			t.Fatal(err)
		}
	})
}

func mustUserID(t *testing.T, a *acc, username string) string {
	t.Helper()
	us, err := a.auth.Users(context.Background(), store.AllTenants())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range us {
		if u.Username == username {
			return u.ID
		}
	}
	t.Fatalf("no user %s", username)
	return ""
}

func TestSpacesPerUserAreLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{maxSpaces: 3}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw) // the personal space is the first of three
		c.do("POST", "/api/spaces", map[string]any{"name": "B"}).want(t, 201, "")
		c.do("POST", "/api/spaces", map[string]any{"name": "C"}).want(t, 201, "")
		r := c.do("POST", "/api/spaces", map[string]any{"name": "D"}).want(t, 403, "quota_exceeded")
		if !strings.Contains(r.body["message"].(string), "3") || r.body["quota"] != "spaces" || r.body["limit"] != float64(3) || r.body["used"] != float64(3) {
			t.Fatalf("the message does not say what the limit is: %s", r.raw)
		}
		if len(c.do("GET", "/api/spaces", nil).body["spaces"].([]any)) != 3 {
			t.Fatal("a refused space is listed")
		}
		// Another user is not affected.
		a.addUser("tang", pw, "member").do("POST", "/api/spaces", map[string]any{"name": "B"}).want(t, 201, "")
	})
}

func mustUser(t *testing.T, a *acc, username string) store.User {
	t.Helper()
	u, err := store.GetUser(context.Background(), a.st.Q(), store.AllTenants(), mustUserID(t, a, username))
	if err != nil {
		t.Fatal(err)
	}
	return *u
}
