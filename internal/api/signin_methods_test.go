package api_test

import (
	"strings"
	"testing"
)

// methodsOf is the account's one list of sign-in methods, by type.
func methodsOf(t *testing.T, c *client) ([]map[string]any, bool) {
	t.Helper()
	r := c.do("GET", "/api/account", nil).want(t, 200, "")
	raw, _ := r.body["methods"].([]any)
	out := make([]map[string]any, len(raw))
	for i, m := range raw {
		out[i] = m.(map[string]any)
	}
	return out, r.body["canUnlink"] == true
}

func typesOf(ms []map[string]any) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i], _ = m["type"].(string)
	}
	return out
}

func TestTheAccountListsItsSignInMethodsWithLastUsed(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		// A password-only account: one method, and nothing to remove.
		pwOnly := a.addUser("pat", pw, "member")
		ms, can := methodsOf(t, pwOnly)
		if len(ms) != 1 || ms[0]["type"] != "password" || can || ms[0]["added"] == nil || ms[0]["lastUsed"] == nil /* addUser signs them in */ {
			t.Fatalf("%v %v", ms, can)
		}

		// An account made by a sign-in at the provider holds that identity only.
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		ms, can = methodsOf(t, c)
		if got := typesOf(ms); len(got) != 1 || got[0] != "identity" || can {
			t.Fatalf("%v %v", ms, can)
		}
		id := ms[0]
		if id["id"] == "" || id["providerSlug"] != "sso" || id["kind"] != "oidc" || id["label"] != "alice@example.test" || id["added"] == nil || id["lastUsed"] == nil || id["removable"] != false {
			t.Fatalf("%v", id)
		}
		c.do("DELETE", "/api/account/links/"+id["id"].(string), nil).want(t, 409, "last_method")

		// Setting a password and registering a passkey add rows; the list is in
		// the order password, identities, passkeys.
		c.do("POST", "/api/account/password", map[string]any{"new": pw}).want(t, 204, "")
		key := newVKey(mustUserID(t, a, "alice"))
		c.register(t, key, "MacBook").want(t, 201, "")
		ms, can = methodsOf(t, c)
		if got := typesOf(ms); len(got) != 3 || got[0] != "password" || got[1] != "identity" || got[2] != "passkey" || !can {
			t.Fatalf("%v %v", ms, can)
		}
		if ms[0]["lastUsed"] != nil || ms[2]["name"] != "MacBook" || ms[2]["lastUsed"] != nil || ms[2]["added"] == nil || ms[1]["removable"] != true || ms[2]["removable"] != true {
			t.Fatalf("%v", ms)
		}

		// Each way of signing in records its own use, and only its own.
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "alice", "password": pw}).want(t, 200, "")
		ms, _ = methodsOf(t, c)
		if ms[0]["lastUsed"] == nil || ms[2]["lastUsed"] != nil {
			t.Fatalf("the password sign-in: %v", ms)
		}
		a.newClient("").signInWithPasskey(t, key, "", "").want(t, 200, "")
		ms, _ = methodsOf(t, c)
		if ms[2]["lastUsed"] == nil {
			t.Fatalf("the passkey sign-in: %v", ms)
		}
		// Every sign-in through a link writes its time, even when the last one is recent.
		if _, err := a.st.Q().Exec(t.Context(), `UPDATE linked_accounts SET last_used = 1000`); err != nil {
			t.Fatal(err)
		}
		f.signIn(t, a.newClient(""), "alice", map[string]any{})
		ms, _ = methodsOf(t, c)
		if ms[1]["lastUsed"] == nil || ms[1]["lastUsed"] == "1970-01-01T00:00:01Z" {
			t.Fatalf("the link sign-in: %v", ms)
		}
		// A failed password does not count as a use.
		before := ms[0]["lastUsed"]
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "alice", "password": "wrong password here"}).want(t, 401, "")
		if ms, _ = methodsOf(t, c); ms[0]["lastUsed"] != before {
			t.Fatalf("%v", ms)
		}

		// Another account never sees these rows.
		other, _ := methodsOf(t, pwOnly)
		if len(other) != 1 || other[0]["type"] != "password" {
			t.Fatalf("%v", other)
		}
		raw := pwOnly.do("GET", "/api/account", nil).raw
		for _, s := range []string{"MacBook", "alice@example.test", ms[1]["id"].(string), ms[2]["id"].(string)} {
			if strings.Contains(raw, s) {
				t.Fatalf("an account sees another's method %q: %s", s, raw)
			}
		}
		// The identity may go now that two other ways remain; the list follows.
		c.do("DELETE", "/api/account/links/"+ms[1]["id"].(string), nil).want(t, 204, "")
		if ms, can = methodsOf(t, c); len(ms) != 2 || !can {
			t.Fatalf("%v %v", ms, can)
		}
	})
}
