package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

func userNames(t *testing.T, c *client, path string) (names []string, next string) {
	t.Helper()
	r := c.do("GET", path, nil).want(t, 200, "")
	for _, u := range r.body["users"].([]any) {
		names = append(names, u.(map[string]any)["username"].(string))
	}
	next, _ = r.body["next"].(string)
	return names, next
}

// The administration's list of people pages by cursor in every sort and takes
// the filters; one page costs the same whatever the page.
func TestUsersListPagesSortsAndFilters(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		for i := 1; i <= 6; i++ {
			role := "member"
			if i == 3 {
				role = "admin"
			}
			a.addUser(fmt.Sprintf("u%d", i), pw, role)
		}
		id := mustUserID(t, a, "u2")
		root.do("PATCH", "/api/users/"+id, map[string]any{"disabled": true}).want(t, 200, "")
		root.do("PATCH", "/api/users/"+mustUserID(t, a, "u3"), map[string]any{"siteAdmin": false}).want(t, 200, "")

		// Pages of two follow one another to the end, in the order asked.
		var got []string
		path := "/api/users?limit=2&sort=name&order=desc"
		for n := 0; n < 10; n++ {
			page, next := userNames(t, root, path)
			got = append(got, page...)
			if next == "" {
				break
			}
			path = "/api/users?limit=2&sort=name&order=desc&cursor=" + next
		}
		whole, next := userNames(t, root, "/api/users?sort=name&order=desc")
		if next != "" || len(whole) != 7 || strings.Join(got, ",") != strings.Join(whole, ",") {
			t.Fatalf("pages %v, whole %v, next %q", got, whole, next)
		}
		if whole[0] != "u6" || whole[6] != "root" {
			t.Fatalf("not by name, descending: %v", whole)
		}
		// Without parameters: the first 50, oldest first.
		if names, _ := userNames(t, root, "/api/users"); strings.Join(names, ",") != "root,u1,u2,u3,u4,u5,u6" {
			t.Fatalf("%v", names)
		}
		for path, want := range map[string]string{
			"/api/users?role=admin":                "root,u3",
			"/api/users?role=site":                 "root",
			"/api/users?role=member&state=enabled": "u1,u4,u5,u6",
			"/api/users?state=disabled":            "u2",
			"/api/users?q=U4":                      "u4",
			"/api/users?q=u&limit=2":               "u1,u2",
			"/api/users?enabled=1&limit=3":         "root,u1,u3",
		} {
			if names, _ := userNames(t, root, path); strings.Join(names, ",") != want {
				t.Fatalf("%s: %v, want %s", path, names, want)
			}
		}
		// Last active: written once in a while as people use the server.
		a.clock.Advance(48 * time.Hour)
		if err := a.st.NoteActive(t.Context(), mustUserID(t, a, "u4"), a.clock.Now()); err != nil {
			t.Fatal(err)
		}
		// (The administrator asking is active too.)
		if names, _ := userNames(t, root, "/api/users?activeDays=1"); strings.Join(names, ",") != "root,u4" {
			t.Fatalf("%v", names)
		}
		if names, _ := userNames(t, root, "/api/users?inactiveDays=1"); strings.Contains(strings.Join(names, ","), "u4") || !strings.Contains(strings.Join(names, ","), "u1") {
			t.Fatalf("%v", names)
		}
		if names, _ := userNames(t, root, "/api/users?sort=lastActive"); names[0] != "root" && names[0] != "u4" || names[len(names)-1] != "u6" {
			t.Fatalf("%v", names)
		}
		if r := root.do("GET", "/api/users?sort=lastActive", nil).want(t, 200, ""); r.body["users"].([]any)[0].(map[string]any)["lastActive"] == nil {
			t.Fatalf("no lastActive: %s", r.raw)
		}
		// What the list does not have.
		for _, bad := range []string{"sort=age", "order=up", "role=king", "state=gone", "inactiveDays=x", "activeDays=-1", "cursor=nonsense", "sort=name&cursor=created.asc:1:x"} {
			root.do("GET", "/api/users?"+bad, nil).want(t, 400, "invalid")
		}
		// One person, with what the list has.
		r := root.do("GET", "/api/users/"+id, nil).want(t, 200, "")
		if u := r.user(); u["username"] != "u2" || u["disabled"] != true || u["passkeys"] != float64(0) {
			t.Fatalf("%s", r.raw)
		}
		root.do("GET", "/api/users/"+fakeID, nil).want(t, 404, "not_found")
		// A person who is deleted is not listed.
		root.do("DELETE", "/api/users/"+mustUserID(t, a, "u6"), map[string]any{}).want(t, 200, "")
		if names, _ := userNames(t, root, "/api/users?q=u6"); len(names) != 0 {
			t.Fatalf("%v", names)
		}
	})
}

// A tenant's administrator sees their tenant's people and their memberships, and
// never a person's sessions or connected accounts (answered as for no one).
func TestUserSessionsAndIdentitiesAreForSiteAdministrators(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// Two devices of one person.
		desktop := a.newClient("")
		desktop.headers["User-Agent"] = "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0"
		desktop.do("POST", "/api/login", good("mem-a")).want(t, 200, "")
		phone := a.newClient("")
		phone.headers["User-Agent"] = "Mozilla/5.0 (iPhone) Safari/605"
		phone.do("POST", "/api/login", good("mem-a")).want(t, 200, "")
		path := "/api/users/" + f.userA

		// The tenant's administrator: the person and where they belong, not their sign-ins.
		f.admA.do("GET", path, nil).want(t, 200, "")
		m := f.admA.do("GET", path+"/memberships", nil).want(t, 200, "")
		spacesOf := m.body["spaces"].([]any)
		for _, sp := range spacesOf {
			if sp.(map[string]any)["personal"] == true {
				t.Fatalf("a tenant's administrator saw a personal space: %s", m.raw)
			}
		}
		for _, p := range []string{"/sessions", "/identities"} {
			f.admA.do("GET", path+p, nil).want(t, 404, "not_found")
		}
		f.admA.do("DELETE", path+"/sessions", nil).want(t, 404, "not_found")
		f.admA.do("DELETE", path+"/sessions/"+fakeID, nil).want(t, 404, "not_found")
		f.admA.do("DELETE", path+"/identities/"+fakeID, nil).want(t, 404, "not_found")
		f.memA.do("GET", path+"/sessions", nil).want(t, 403, "forbidden")
		desktop.do("GET", "/api/account", nil).want(t, 200, "")

		// A site administrator sees both devices, newest first, with how they signed in.
		r := f.root.do("GET", path+"/sessions", nil).want(t, 200, "")
		sessions := r.body["sessions"].([]any)
		if len(sessions) != 3 { // the two, and the one the fixture signed in with
			t.Fatalf("%s", r.raw)
		}
		var firefox, safari map[string]any
		for _, s := range sessions {
			m := s.(map[string]any)
			switch {
			case strings.Contains(m["userAgent"].(string), "Firefox"):
				firefox = m
			case strings.Contains(m["userAgent"].(string), "Safari"):
				safari = m
			}
			if m["method"] != "password" {
				t.Fatalf("%s", r.raw)
			}
		}
		if firefox == nil || safari == nil {
			t.Fatalf("%s", r.raw)
		}
		if m := f.root.do("GET", "/api/users/"+f.userB+"/sessions", nil).want(t, 200, ""); len(m.body["sessions"].([]any)) == 0 {
			t.Fatalf("a site administrator sees another tenant's sessions: %s", m.raw)
		}

		// End one: its browser is told it was signed out elsewhere; the other stays in.
		f.root.do("DELETE", path+"/sessions/"+firefox["id"].(string), nil).want(t, 204, "")
		wantReason(t, desktop, "signed_out_elsewhere")
		phone.do("GET", "/api/account", nil).want(t, 200, "")
		f.root.do("DELETE", path+"/sessions/"+firefox["id"].(string), nil).want(t, 404, "not_found")
		f.root.do("DELETE", path+"/sessions/"+fakeID, nil).want(t, 404, "not_found")
		// Not a session of another person.
		other := f.root.do("GET", "/api/users/"+f.userB+"/sessions", nil).want(t, 200, "").body["sessions"].([]any)[0].(map[string]any)["id"].(string)
		f.root.do("DELETE", path+"/sessions/"+other, nil).want(t, 404, "not_found")

		// The administrator's own session is marked, and only that one.
		own := f.root.do("GET", "/api/users/"+mustUserID(t, a, "root")+"/sessions", nil).want(t, 200, "").body["sessions"].([]any)
		if len(own) == 0 {
			t.Fatal("the administrator has a session")
		}
		for _, s := range own {
			if s.(map[string]any)["current"] != true {
				t.Fatalf("the session of this request is marked: %v", own)
			}
		}
		for _, s := range f.root.do("GET", path+"/sessions", nil).want(t, 200, "").body["sessions"].([]any) {
			if s.(map[string]any)["current"] != nil {
				t.Fatalf("another person's sessions are not: %v", s)
			}
		}

		// End all of them.
		r = f.root.do("DELETE", path+"/sessions", nil).want(t, 200, "")
		if r.body["ended"] != float64(2) {
			t.Fatalf("%s", r.raw)
		}
		wantReason(t, phone, "signed_out_elsewhere")
		if m := f.root.do("GET", path+"/sessions", nil).want(t, 200, ""); len(m.body["sessions"].([]any)) != 0 {
			t.Fatalf("%s", m.raw)
		}
		a.auth.FlushAudit(t.Context())
		var n int
		if err := a.st.Q().QueryRow(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE kind = 'sessions_ended' AND user_id = ?`, f.userA).Scan(&n); err != nil || n != 2 {
			t.Fatalf("%d audit rows, %v", n, err)
		}

		// Connected accounts: listed, and disconnected (the person has a password, so it is not their only way in).
		if err := store.InsertIdentity(t.Context(), a.st.Q(), store.Identity{ID: "IDENT00001", UserID: f.userA, Issuer: "https://idp.example", Subject: "abc", Label: "mem@idp", Created: a.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		ids := f.root.do("GET", path+"/identities", nil).want(t, 200, "").body["identities"].([]any)
		if len(ids) != 1 || ids[0].(map[string]any)["id"] != "IDENT00001" || ids[0].(map[string]any)["label"] != "mem@idp" {
			t.Fatalf("%v", ids)
		}
		f.admA.do("DELETE", path+"/identities/IDENT00001", nil).want(t, 404, "not_found")
		f.root.do("DELETE", path+"/identities/"+fakeID, nil).want(t, 404, "not_found")
		f.root.do("DELETE", path+"/identities/IDENT00001", nil).want(t, 204, "")
		if ids := f.root.do("GET", path+"/identities", nil).want(t, 200, "").body["identities"].([]any); len(ids) != 0 {
			t.Fatalf("%v", ids)
		}

		// Only the ways that work now count: a person with no password whose two
		// connected accounts are of providers that are not set up has no way in
		// left once one of them goes, so neither can be disconnected.
		if err := store.InsertUser(t.Context(), a.st.Q(), store.User{ID: "NOPASS0001", Username: "nopass", Name: "No Pass", Role: "member", Created: a.clock.Now(), TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"IDENT00002", "IDENT00003"} {
			if err := store.InsertIdentity(t.Context(), a.st.Q(), store.Identity{ID: id, UserID: "NOPASS0001", Issuer: "https://gone.example/" + id, Subject: id, Created: a.clock.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		f.root.do("DELETE", "/api/users/NOPASS0001/identities/IDENT00002", nil).want(t, 409, "conflict")
	})
}
