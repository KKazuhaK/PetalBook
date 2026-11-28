package api_test

import (
	"testing"

	"petalbook.app/server/internal/store"
)

// insertProviderRow stores a sign-in provider row the running service has not
// loaded: it is not enabled, and the service does not know it.
func insertProviderRow(t *testing.T, a *acc, id string) {
	t.Helper()
	now := a.clock.Now()
	if err := store.InsertProvider(t.Context(), a.st.Q(), store.ProviderRow{ID: id, Owner: store.ProviderSite, Slug: id, Kind: "oidc", Name: id, Config: "{}", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

// audit returns the audit rows of a kind, newest first.
func (c *client) audit(t *testing.T, kind string) []map[string]any {
	t.Helper()
	path := "/api/admin/audit?limit=200"
	if kind != "" {
		path += "&kind=" + kind
	}
	r := c.do("GET", path, nil).want(t, 200, "")
	var out []map[string]any
	for _, it := range r.body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func bad(user string) map[string]any {
	return map[string]any{"identifier": user, "password": "wrong password here"}
}

func good(user string) map[string]any { return map[string]any{"identifier": user, "password": pw} }
