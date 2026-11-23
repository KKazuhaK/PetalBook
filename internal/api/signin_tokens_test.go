package api_test

import (
	"context"
	"testing"
)

// A held session (someone who must register a passkey and has none) makes no
// API token and approves no OAuth consent. Tokens and grants made before are
// independent of the hold: they keep working, as docs/api.md says ("Held
// sessions, API tokens and OAuth grants").
func TestAHeldSessionMakesNoTokenOrGrantAndOldOnesKeepWorking(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		g := grant(t, a, tm.owner, "read", tm.sid)

		admin.do("PATCH", "/api/users/"+tm.ownerID, map[string]any{"passkeyRequired": true}).want(t, 200, "")
		c := a.newClient("")
		c.do("POST", "/api/login", good("olive")).want(t, 200, "")
		if h := c.do("GET", "/api/session", nil).body["hold"]; h != "passkey" {
			t.Fatalf("hold %v", h)
		}

		// No new token.
		c.do("POST", "/api/account/tokens", map[string]any{"name": "x", "access": "read", "spaces": []string{tm.sid}}).want(t, 403, "passkey_required")
		c.do("GET", "/api/account/tokens", nil).want(t, 403, "passkey_required")
		// No new grant: the authorization request reaches the consent page, whose
		// endpoints a held session cannot use (the web app shows the passkey page).
		cid := registerClient(t, a, "Held helper")
		_, ch := pkcePair("held")
		req := startConsent(t, c, params(cid, ch))
		c.do("GET", "/api/oauth/consent?req="+req, nil).want(t, 403, "passkey_required")
		decide(t, c, req, true, "read", tm.sid).want(t, 403, "passkey_required")

		// What was made before the requirement still works.
		a.bearer(secret, "").do("GET", "/api/spaces/"+tm.sid+"/pages", nil).want(t, 200, "")
		a.bearer(g.access, "").tool("folders_list", map[string]any{"space": tm.sid}).ok(t)

		// Once the passkey is registered, the same request can be approved.
		c.register(t, newVKey(tm.ownerID), "key").want(t, 201, "")
		decide(t, c, req, true, "read", tm.sid).want(t, 200, "")
	})
}

// Making and revoking an API token, and revoking an OAuth grant, are written
// to the audit log.
func TestTokensAndGrantsAreAudited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		id, _ := mkToken(t, tm.owner, "write", nil, tm.sid)
		id2, _ := mkToken(t, tm.owner, "read", nil, tm.sid)
		tm.owner.do("DELETE", "/api/account/tokens/"+id, nil).want(t, 204, "")
		// A second revocation changes nothing and writes nothing.
		tm.owner.do("DELETE", "/api/account/tokens/"+id, nil).want(t, 204, "")
		admin.do("DELETE", "/api/admin/tokens/"+id2, nil).want(t, 204, "")

		grant(t, a, tm.owner, "read", tm.sid)
		gid := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any)[0].(map[string]any)["id"].(string)
		tm.owner.do("DELETE", "/api/account/grants/"+gid, nil).want(t, 204, "")
		a.auth.FlushAudit(context.Background())

		created := admin.audit(t, "token_created")
		if len(created) != 2 {
			t.Fatalf("token_created %v", created)
		}
		d := created[1]["detail"].(map[string]any)
		if created[1]["actor"] != tm.ownerID || created[1]["user"] != tm.ownerID || d["token"] != id || d["access"] != "write" || d["name"] != "script" {
			t.Fatalf("%v", created[1])
		}
		if sp := d["spaces"].([]any); len(sp) != 1 || sp[0] != tm.sid {
			t.Fatalf("spaces %v", sp)
		}
		revoked := admin.audit(t, "token_revoked")
		if len(revoked) != 2 {
			t.Fatalf("token_revoked %v", revoked)
		}
		adminID := mustUserID(t, a, "kazuha")
		if r := revoked[0]; r["actor"] != adminID || r["user"] != tm.ownerID || r["detail"].(map[string]any)["by"] != "admin" || r["detail"].(map[string]any)["token"] != id2 {
			t.Fatalf("%v", r)
		}
		if r := revoked[1]; r["actor"] != tm.ownerID || r["detail"].(map[string]any)["by"] != "user" || r["detail"].(map[string]any)["token"] != id {
			t.Fatalf("%v", r)
		}
		gr := admin.audit(t, "grant_revoked")
		if len(gr) != 1 || gr[0]["user"] != tm.ownerID || gr[0]["detail"].(map[string]any)["grant"] != gid || gr[0]["detail"].(map[string]any)["client"] != "Notes helper" {
			t.Fatalf("grant_revoked %v", gr)
		}
	})
}
