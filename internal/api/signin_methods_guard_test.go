package api_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The list says a method is removable exactly when the server lets it go: both
// ask the same count of the ways that work now.
func TestRemovableIsWhatTheGuardsAllow(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		key := newVKey(mustUserID(t, a, "alice"))
		c.register(t, key, "MacBook").want(t, 201, "")
		ms, _ := methodsOf(t, c)
		if len(ms) != 2 || ms[0]["removable"] != true || ms[1]["removable"] != true {
			t.Fatalf("a link and a passkey, both working: %v", ms)
		}
		// The provider stops working: the passkey is the only way in, and the
		// list and the guard both say it stays. The dead link may go.
		admin.do("PATCH", "/api/admin/providers/sso", map[string]any{"enabled": false}).want(t, 200, "")
		ms, can := methodsOf(t, c)
		if ms[0]["type"] != "identity" || ms[0]["removable"] != true || ms[1]["removable"] != false || can {
			t.Fatalf("%v %v", ms, can)
		}
		c.do("DELETE", "/api/account/passkeys/"+ms[1]["id"].(string), nil).want(t, 409, "conflict")
		// Passkeys switched off as well: nothing works, and nothing may be
		// removed (the guard of a link refuses, so the list does).
		a.setSetting(t, "features.passkeys", false)
		ms, _ = methodsOf(t, c)
		if ms[0]["removable"] != false || ms[1]["removable"] != false {
			t.Fatalf("nothing works: %v", ms)
		}
		c.do("DELETE", "/api/account/links/"+ms[0]["id"].(string), nil).want(t, 409, "last_method")
		c.do("DELETE", "/api/account/passkeys/"+ms[1]["id"].(string), nil).want(t, 409, "conflict")
		// With a password the dead ways may go, the guard and the list agreeing.
		c.do("POST", "/api/account/password", map[string]any{"new": pw}).want(t, 204, "")
		ms, _ = methodsOf(t, c)
		if len(ms) != 3 || ms[1]["removable"] != true || ms[2]["removable"] != true {
			t.Fatalf("%v", ms)
		}
		c.do("DELETE", "/api/account/passkeys/"+ms[2]["id"].(string), nil).want(t, 204, "")
		c.do("DELETE", "/api/account/links/"+ms[1]["id"].(string), nil).want(t, 204, "")
	})
}

// A sign-in that is refused records no use of its method.
func TestARefusedSignInRecordsNoLastUsed(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		id := mustUserID(t, a, "alice")
		key := newVKey(id)
		c.register(t, key, "MacBook").want(t, 201, "")
		uses := func() (link, pk time.Time) {
			ls, err := store.ListLinks(t.Context(), a.st.Q(), id)
			if err != nil || len(ls) != 1 {
				t.Fatal(err, ls)
			}
			ps, err := store.ListPasskeys(t.Context(), a.st.Q(), id)
			if err != nil || len(ps) != 1 {
				t.Fatal(err, ps)
			}
			return ls[0].LastUsed, ps[0].LastUsed
		}
		l0, p0 := uses()
		admin.do("PATCH", "/api/users/"+id, map[string]any{"disabled": true}).want(t, 200, "")
		a.clock.Advance(time.Hour)
		if loc := f.signIn(t, a.newClient(""), "alice", map[string]any{}); loc == "/" {
			t.Fatalf("a disabled account signed in: %s", loc)
		}
		key.cred.Counter++
		a.newClient("").signInWithPasskey(t, key, "", "").want(t, 401, "passkey_failed")
		l1, p1 := uses()
		if !l1.Equal(l0) || !p1.Equal(p0) {
			t.Fatalf("a refused sign-in recorded a use: link %v -> %v, passkey %v -> %v", l0, l1, p0, p1)
		}
	})
}
