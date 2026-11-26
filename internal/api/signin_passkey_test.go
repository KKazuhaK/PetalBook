package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// vkey is a virtual authenticator with one credential, for one person.
type vkey struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newVKey(userID string) *vkey {
	return &vkey{
		rp:   virtualwebauthn.RelyingParty{Name: "PetalBook", ID: "notes.example.test", Origin: "https://notes.example.test"},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: []byte(userID)}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

func optionsJSON(t *testing.T, r result) string {
	t.Helper()
	b, err := json.Marshal(r.body["options"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// register adds the key as a passkey of the signed-in client.
func (c *client) register(t *testing.T, v *vkey, name string) result {
	t.Helper()
	r := c.do("POST", "/api/account/passkeys/begin", map[string]any{"name": name}).want(t, 200, "")
	opts, err := virtualwebauthn.ParseAttestationOptions(optionsJSON(t, r))
	if err != nil {
		t.Fatal(err)
	}
	body := virtualwebauthn.CreateAttestationResponse(v.rp, v.auth, v.cred, *opts)
	v.auth.AddCredential(v.cred)
	return c.do("POST", "/api/account/passkeys/finish?ceremony="+url.QueryEscape(r.body["ceremony"].(string)), body)
}

// signInWithPasskey runs a whole passkey sign-in; username "" is the discoverable form.
func (c *client) signInWithPasskey(t *testing.T, v *vkey, username string, ret string) result {
	t.Helper()
	begin := map[string]any{}
	if username != "" {
		begin["username"] = username
	}
	r := c.do("POST", "/api/auth/passkey/begin", begin).want(t, 200, "")
	opts, err := virtualwebauthn.ParseAssertionOptions(optionsJSON(t, r))
	if err != nil {
		t.Fatal(err)
	}
	body := virtualwebauthn.CreateAssertionResponse(v.rp, v.auth, v.cred, *opts)
	p := "/api/auth/passkey/finish?ceremony=" + url.QueryEscape(r.body["ceremony"].(string))
	if ret != "" {
		p += "&return=" + url.QueryEscape(ret)
	}
	return c.do("POST", p, body)
}

func TestPasskeyRegistrationAndSignIn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		id := mustUserID(t, a, "tang")
		key := newVKey(id)

		tang.do("POST", "/api/account/passkeys/begin", map[string]any{"name": strings.Repeat("n", 65)}).want(t, 400, "invalid")
		r := tang.register(t, key, "MacBook").want(t, 201, "")
		pks := r.body["passkeys"].([]any)
		if len(pks) != 1 || pks[0].(map[string]any)["name"] != "MacBook" || pks[0].(map[string]any)["lastUsed"] != nil || pks[0].(map[string]any)["synced"] != false {
			t.Fatal(r.raw)
		}
		if strings.Contains(r.raw, "publicKey") || strings.Contains(r.raw, "credential") {
			t.Fatal("key material in the answer")
		}
		// The same credential twice is a conflict.
		tang.register(t, key, "again").want(t, 409, "exists")
		acct := tang.do("GET", "/api/account", nil).want(t, 200, "")
		if n := len(acct.body["passkeys"].([]any)); n != 1 {
			t.Fatal(acct.raw)
		}

		// Discoverable sign-in, no username; the return path is made safe.
		c := a.newClient("")
		s := c.signInWithPasskey(t, key, "", "//evil.example/x").want(t, 200, "")
		if s.user()["username"] != "tang" || s.body["return"] != "/" || signedInAs(t, c) != "tang" {
			t.Fatal(s.raw)
		}
		key.cred.Counter++
		d := a.newClient("")
		s = d.signInWithPasskey(t, key, "tang", "/s/01JD3K8Q7Z").want(t, 200, "")
		if s.body["return"] != "/s/01JD3K8Q7Z" {
			t.Fatal(s.raw)
		}
		acct = tang.do("GET", "/api/account", nil).want(t, 200, "")
		if acct.body["passkeys"].([]any)[0].(map[string]any)["lastUsed"] == nil {
			t.Fatal("last use not recorded")
		}
		if rows := tang.do("GET", "/api/admin/audit", nil); rows.code != 403 {
			t.Fatal("a member read the audit log")
		}

		// A ceremony is used once; made-up ones fail with the one code.
		r2 := a.newClient("").do("POST", "/api/auth/passkey/begin", map[string]any{}).want(t, 200, "")
		opts, _ := virtualwebauthn.ParseAssertionOptions(optionsJSON(t, r2))
		key.cred.Counter++
		body := virtualwebauthn.CreateAssertionResponse(key.rp, key.auth, key.cred, *opts)
		path := "/api/auth/passkey/finish?ceremony=" + url.QueryEscape(r2.body["ceremony"].(string))
		a.newClient("").do("POST", path, body).want(t, 200, "")
		a.newClient("").do("POST", path, body).want(t, 401, "passkey_failed")
		a.newClient("").do("POST", "/api/auth/passkey/finish?ceremony=nope", body).want(t, 401, "passkey_failed")
		a.newClient("").do("POST", "/api/auth/passkey/finish", "{}").want(t, 401, "passkey_failed")

		// User verification is required: the passkey is the only factor.
		key.cred.Counter++
		key.auth.Options.UserNotVerified = true
		a.newClient("").signInWithPasskey(t, key, "", "").want(t, 401, "passkey_failed")
		key.auth.Options.UserNotVerified = false

		// A counter that went backwards: refused, written down, and the stored counter does not move.
		high := key.cred.Counter
		key.cred.Counter = high + 5
		a.newClient("").signInWithPasskey(t, key, "", "").want(t, 200, "")
		key.cred.Counter = high + 2
		a.newClient("").signInWithPasskey(t, key, "", "").want(t, 401, "passkey_failed")
		key.cred.Counter = high + 6
		a.newClient("").signInWithPasskey(t, key, "", "").want(t, 200, "")
		admin := a.newClient("")
		admin.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		if rows := admin.audit(t, "passkey_clone"); len(rows) != 1 || rows[0]["user"] != id {
			t.Fatalf("%v", rows)
		}

		// A disabled account cannot, and says the same.
		kz := a.newClient("")
		kz.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		kz.do("PATCH", "/api/users/"+id, map[string]any{"disabled": true}).want(t, 200, "")
		key.cred.Counter = high + 7
		a.newClient("").signInWithPasskey(t, key, "tang", "").want(t, 401, "passkey_failed")
		kz.do("PATCH", "/api/users/"+id, map[string]any{"disabled": false}).want(t, 200, "")
		key.cred.Counter = high + 8
		a.newClient("").signInWithPasskey(t, key, "tang", "").want(t, 200, "")
		fails := admin.audit(t, "login_failed")
		reasons := ""
		for _, f := range fails {
			reasons += fmt.Sprint(f["detail"].(map[string]any)["reason"], ",")
		}
		for _, want := range []string{"passkey_failed", "disabled"} {
			if !strings.Contains(reasons, want) {
				t.Errorf("no %s in %s", want, reasons)
			}
		}
	})
}

func TestPasskeyBeginLooksTheSameForEveryone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		key := newVKey(mustUserID(t, a, "tang"))
		tang.register(t, key, "k").want(t, 201, "")
		shape := func(username string) (keys string, id string) {
			r := a.newClient("").do("POST", "/api/auth/passkey/begin", map[string]any{"username": username}).want(t, 200, "")
			o := r.body["options"].(map[string]any)
			allow := o["allowCredentials"].([]any)
			if len(allow) != 1 {
				t.Fatalf("%s: %v", username, allow)
			}
			cred := allow[0].(map[string]any)
			var ks, cks []string
			for k := range o {
				ks = append(ks, k)
			}
			for k := range cred {
				cks = append(cks, k)
			}
			sortStrings(ks)
			sortStrings(cks)
			if o["userVerification"] != "required" || o["rpId"] != "notes.example.test" {
				t.Fatalf("%v", o)
			}
			return strings.Join(ks, ",") + "|" + strings.Join(cks, ","), cred["id"].(string)
		}
		real, realID := shape("tang")
		none, noneID := shape("kazuha") // exists, has no passkey
		ghost, ghostID := shape("nobody-at-all")
		if real != none || real != ghost || len(realID) != len(noneID) || len(realID) != len(ghostID) {
			t.Fatalf("%s %s %s", real, none, ghost)
		}
		if _, again := shape("nobody-at-all"); again != ghostID {
			t.Fatal("the made-up credential id changes")
		}
		if _, other := shape("someone-else"); other == ghostID || noneID == ghostID {
			t.Fatal("the made-up id is the same for different names")
		}
		// Finishing with a made-up one fails like any other.
		c := a.newClient("")
		c.do("POST", "/api/auth/passkey/finish?ceremony=x", "{}").want(t, 401, "passkey_failed")
		// Too long a name is refused.
		c.do("POST", "/api/auth/passkey/begin", map[string]any{"username": strings.Repeat("x", 200)}).want(t, 400, "invalid")
	})
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestPasskeyManagement(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		id := mustUserID(t, a, "tang")
		keys := []*vkey{}
		for i := range 20 {
			k := newVKey(id)
			tang.register(t, k, fmt.Sprintf("key %d", i)).want(t, 201, "")
			keys = append(keys, k)
		}
		tang.do("POST", "/api/account/passkeys/begin", map[string]any{}).want(t, 409, "too_many_passkeys")
		l := tang.do("GET", "/api/account/passkeys", nil).want(t, 200, "")
		list := l.body["passkeys"].([]any)
		first := list[0].(map[string]any)["id"].(string)
		// Rename; another user's is not found.
		rn := tang.do("PATCH", "/api/account/passkeys/"+first, map[string]any{"name": "Renamed"}).want(t, 200, "")
		if rn.body["name"] != "Renamed" {
			t.Fatal(rn.raw)
		}
		tang.do("PATCH", "/api/account/passkeys/"+first, map[string]any{"name": ""}).want(t, 400, "invalid")
		other := a.addUser("pat", pw, "member")
		other.do("PATCH", "/api/account/passkeys/"+first, map[string]any{"name": "mine"}).want(t, 404, "not_found")
		other.do("DELETE", "/api/account/passkeys/"+first, nil).want(t, 404, "not_found")
		// The administrator sees them (names and dates only) and deletes one.
		ul := admin.do("GET", "/api/users/"+id+"/passkeys", nil).want(t, 200, "")
		if len(ul.body["passkeys"].([]any)) != 20 || strings.Contains(ul.raw, "credential") {
			t.Fatal(ul.raw)
		}
		admin.do("DELETE", "/api/users/"+id+"/passkeys/"+first, nil).want(t, 204, "")
		admin.do("DELETE", "/api/users/"+id+"/passkeys/"+first, nil).want(t, 404, "not_found")
		admin.do("GET", "/api/users/nope/passkeys", nil).want(t, 404, "not_found")
		tang.do("GET", "/api/users/"+id+"/passkeys", nil).want(t, 403, "forbidden")
		users := admin.do("GET", "/api/users", nil).want(t, 200, "")
		for _, u := range users.body["users"].([]any) {
			if u.(map[string]any)["username"] == "tang" && u.(map[string]any)["passkeys"] != float64(19) {
				t.Fatal(users.raw)
			}
		}
		// Registering needs a sign-in from the last ten minutes.
		a.clock.Advance(11 * time.Minute)
		tang.do("POST", "/api/account/passkeys/begin", map[string]any{}).want(t, 403, "recent_login_required")
		tang.do("POST", "/api/login", good("tang")).want(t, 200, "")
		tang.do("POST", "/api/account/passkeys/begin", map[string]any{}).want(t, 200, "")
		// Garbage does not register.
		b := tang.do("POST", "/api/account/passkeys/begin", map[string]any{}).want(t, 200, "")
		tang.do("POST", "/api/account/passkeys/finish?ceremony="+url.QueryEscape(b.body["ceremony"].(string)), "{}").want(t, 400, "passkey_failed")
		tang.do("POST", "/api/account/passkeys/finish?ceremony=unknown", "{}").want(t, 400, "passkey_failed")
		rows := admin.audit(t, "passkey_added")
		removed := admin.audit(t, "passkey_removed")
		if len(rows) != 20 || len(removed) != 1 || removed[0]["detail"].(map[string]any)["by"] != "admin" {
			t.Fatalf("%d %v", len(rows), removed)
		}
	})
}

func TestAPasskeyIsNotTheOnlyWayInWhenItWouldBeTheLast(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		id := mustUserID(t, a, "alice")
		key := newVKey(id)
		c.register(t, key, "key").want(t, 201, "")
		acct := c.do("GET", "/api/account", nil).want(t, 200, "")
		// With a passkey the identity may go; then the passkey is the only way in.
		ident := acct.body["identities"].([]any)[0].(map[string]any)["id"].(string)
		c.do("DELETE", "/api/account/identities/"+ident, nil).want(t, 204, "")
		pk := acct.body["passkeys"].([]any)[0].(map[string]any)["id"].(string)
		c.do("DELETE", "/api/account/passkeys/"+pk, nil).want(t, 409, "conflict")
		// An administrator may remove it (a lost device); the person then has to be let in another way.
		kz := a.newClient("")
		kz.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		kz.do("DELETE", "/api/users/"+id+"/passkeys/"+pk, nil).want(t, 204, "")
	})
}

func TestPasskeysAreRequiredByRoleAndByPerson(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		tid := mustUserID(t, a, "tang")
		admin.putSecurity(t, map[string]any{"passkeys": map[string]any{"requireRoles": []string{"member"}}})
		u := admin.do("PATCH", "/api/users/"+tid, map[string]any{"email": "t@example.test"}).want(t, 200, "").user()
		if u["passkeyRequired"] != true {
			t.Fatalf("%v", u)
		}
		sec := admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
		pk := sec.body["passkeys"].(map[string]any)
		if pk["required"] != float64(1) || pk["requiredWithout"] != float64(1) || pk["available"] != true || pk["enabled"] != true {
			t.Fatal(sec.raw)
		}

		// Password sign-in works but into a held session: only the passkey part of the account and sign-out.
		c := a.newClient("")
		c.do("POST", "/api/login", good("tang")).want(t, 200, "")
		sess := c.do("GET", "/api/session", nil).want(t, 200, "")
		if sess.body["hold"] != "passkey" || sess.user()["username"] != "tang" {
			t.Fatal(sess.raw)
		}
		for _, p := range []string{"/api/spaces", "/api/users", "/api/admin/status"} {
			c.do("GET", p, nil).want(t, 403, "passkey_required")
		}
		c.do("POST", "/api/spaces", map[string]any{"name": "x"}).want(t, 403, "passkey_required")
		for _, p := range []string{"/api/meta", "/api/auth/providers", "/api/account", "/api/account/passkeys"} {
			c.do("GET", p, nil).want(t, 200, "")
		}
		key := newVKey(tid)
		c.register(t, key, "new").want(t, 201, "")
		// The first passkey lifts the hold at once, for this session.
		if h := c.do("GET", "/api/session", nil).body["hold"]; h != nil {
			t.Fatalf("hold %v", h)
		}
		c.do("GET", "/api/spaces", nil).want(t, 200, "")
		// Now the password alone is not enough, and a wrong one is the ordinary answer.
		d := a.newClient("")
		d.do("POST", "/api/login", bad("tang")).want(t, 401, "unauthenticated")
		d.do("POST", "/api/login", good("tang")).want(t, 403, "passkey_required")
		if sessAfter := d.do("GET", "/api/session", nil).want(t, 200, ""); sessAfter.user() != nil {
			t.Fatal("a session was started")
		}
		key.cred.Counter++
		d.signInWithPasskey(t, key, "tang", "").want(t, 200, "")
		// Single sign-in is not held: the provider decides how its people prove who they are.
		f.addUser("tang-sso", testidp.Claims{"sub": "s-tang", "preferred_username": "tang-sso"})
		if u, err := a.auth.CreateUser(t.Context(), auth.CreateParams{Username: "ssoman", Role: "member", Identity: &auth.IdentityParams{Issuer: f.srv.URL, Subject: "s-tang", Label: "x"}}); err != nil || u == nil {
			t.Fatal(err)
		}
		e := a.newClient("")
		if loc := f.signIn(t, e, "tang-sso", map[string]any{}); loc != "/" {
			t.Fatal(loc)
		}
		if e.do("GET", "/api/session", nil).body["hold"] != nil {
			t.Fatal("single sign-in was held")
		}
		e.do("GET", "/api/spaces", nil).want(t, 200, "")
		// Losing the passkey: an administrator deletes it, and the next password sign-in is held again.
		l := admin.do("GET", "/api/users/"+tid+"/passkeys", nil).want(t, 200, "")
		admin.do("DELETE", "/api/users/"+tid+"/passkeys/"+l.body["passkeys"].([]any)[0].(map[string]any)["id"].(string), nil).want(t, 204, "")
		g := a.newClient("")
		g.do("POST", "/api/login", good("tang")).want(t, 200, "")
		if g.do("GET", "/api/session", nil).body["hold"] != "passkey" {
			t.Fatal("not held again")
		}
		g.do("POST", "/api/logout", "{}").want(t, 204, "")
		_ = tang

		// Per person; lifting the role's requirement leaves it.
		pat := a.addUser("pat", pw, "admin")
		_ = pat
		admin.putSecurity(t, map[string]any{"passkeys": map[string]any{"requireRoles": []string{}}})
		h := a.newClient("")
		h.do("POST", "/api/login", good("tang")).want(t, 200, "")
		if h.do("GET", "/api/session", nil).body["hold"] != nil {
			t.Fatal("still held without a requirement")
		}
		w := admin.do("PATCH", "/api/users/"+tid, map[string]any{"passkeyRequired": true}).want(t, 200, "").user()
		if w["passkeyRequired"] != true {
			t.Fatal(w)
		}
		i := a.newClient("")
		i.do("POST", "/api/login", good("tang")).want(t, 200, "")
		if i.do("GET", "/api/session", nil).body["hold"] != "passkey" {
			t.Fatal("a person required alone was not held")
		}
		// Switching passkeys off is refused while someone must use one.
		admin.do("PUT", "/api/admin/security", map[string]any{"passkeys": map[string]any{"enabled": false}}).want(t, 400, "passkey_requirement_unsatisfiable")
		admin.do("PATCH", "/api/users/"+tid, map[string]any{"passkeyRequired": false}).want(t, 200, "")
		admin.putSecurity(t, map[string]any{"passkeys": map[string]any{"enabled": false}})
		if p := a.newClient("").do("GET", "/api/auth/providers", nil).body["passkey"]; p != false {
			t.Fatalf("passkey %v", p)
		}
		a.newClient("").do("POST", "/api/auth/passkey/begin", map[string]any{}).want(t, 404, "not_found")
		admin.do("PATCH", "/api/users/"+tid, map[string]any{"passkeyRequired": true}).want(t, 400, "passkey_unavailable")
		admin.do("PUT", "/api/admin/security", map[string]any{"passkeys": map[string]any{"requireRoles": []string{"admin"}}}).want(t, 400, "passkey_requirement_unsatisfiable")
		admin.do("PUT", "/api/admin/security", map[string]any{"passkeys": map[string]any{"requireRoles": []string{"nobody"}}}).want(t, 400, "invalid")
	})
}

func TestPasskeysNeedAPublicURLTheyCanWorkWith(t *testing.T) {
	t.Parallel()
	for name, public := range map[string]string{"http host": "http://notes.example.test", "no url": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o := accOpts{public: public}
			if public == "" {
				o.mod = func(c *auth.Config) { c.PublicURL = "" }
			}
			eachAccounts(t, o, func(t *testing.T, a *acc) {
				admin := a.setup("kazuha", pw)
				if p := a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "").body["passkey"]; p != false {
					t.Fatalf("passkey %v", p)
				}
				a.newClient("").do("POST", "/api/auth/passkey/begin", map[string]any{}).want(t, 404, "not_found")
				admin.do("POST", "/api/account/passkeys/begin", map[string]any{}).want(t, 404, "not_found")
				admin.do("PUT", "/api/admin/security", map[string]any{"passkeys": map[string]any{"requireRoles": []string{"member"}}}).want(t, 400, "passkey_unavailable")
				sec := admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
				if sec.body["passkeys"].(map[string]any)["available"] != false {
					t.Fatal(sec.raw)
				}
			})
		})
	}
	// http on a loopback name is for trying things out.
	eachAccounts(t, accOpts{public: "http://localhost:8080"}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		if p := a.newClient("").do("GET", "/api/auth/providers", nil).body["passkey"]; p != true {
			t.Fatalf("passkey %v", p)
		}
	})
}

// A passkey made for another host cannot be used here, so it does not count:
// a person required to use one signs in with the password into a held session
// and registers a new one.
func TestAPasskeyOfAnOldHostDoesNotStopTheHeldSession(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("tang", pw, "member")
		tid := mustUserID(t, a, "tang")
		if err := store.InsertPasskey(context.Background(), a.st.Q(), store.PasskeyRow{ID: "old-1", UserID: tid, CredentialID: "b2xk", Credential: `{"id":"b2xk"}`, Name: "Old host", RPID: "old.example.test", Created: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		admin.do("PATCH", "/api/users/"+tid, map[string]any{"passkeyRequired": true}).want(t, 200, "")
		c := a.newClient("")
		c.do("POST", "/api/login", good("tang")).want(t, 200, "")
		if h := c.do("GET", "/api/session", nil).body["hold"]; h != "passkey" {
			t.Fatalf("hold %v", h)
		}
		c.register(t, newVKey(tid), "new").want(t, 201, "")
		if h := c.do("GET", "/api/session", nil).body["hold"]; h != nil {
			t.Fatalf("hold after registering %v", h)
		}
		// With one for this host, the password alone is refused again.
		a.newClient("").do("POST", "/api/login", good("tang")).want(t, 403, "passkey_required")
	})
}

// The accounts made by first-run setup and by an invite are held like a
// password sign-in when their role requires a passkey.
func TestSetupAndInvitesStartAHeldSessionWhenAPasskeyIsRequired(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		// Stored before anyone exists, as a restored database could have it.
		if _, err := store.PutRecord(context.Background(), a.st.Q(), "security", `{"passkeys":{"enabled":true,"requireRoles":["admin","member"]}}`, time.Now()); err != nil {
			t.Fatal(err)
		}
		a.clock.Advance(6 * time.Minute) // past the settings cache
		admin := a.newClient("")
		admin.do("POST", "/api/setup", map[string]any{"token": a.auth.SetupToken(), "username": "kazuha", "password": pw}).want(t, 201, "")
		if h := admin.do("GET", "/api/session", nil).body["hold"]; h != "passkey" {
			t.Fatalf("setup: hold %v", h)
		}
		admin.register(t, newVKey(mustUserID(t, a, "kazuha")), "admin key").want(t, 201, "")
		r := admin.do("POST", "/api/invites", map[string]any{"role": "member", "days": 7}).want(t, 201, "")
		c := a.newClient("")
		c.do("POST", "/api/invites/accept", map[string]any{"token": r.body["token"], "username": "lin", "password": pw}).want(t, 201, "")
		if h := c.do("GET", "/api/session", nil).body["hold"]; h != "passkey" {
			t.Fatalf("invite: hold %v", h)
		}
		c.do("GET", "/api/spaces", nil).want(t, 403, "passkey_required")
	})
}
