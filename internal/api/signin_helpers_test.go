package api_test

import (
	"testing"
)

// put sets the security settings as the administrator (a JSON object, as in the API).
func (c *client) putSecurity(t *testing.T, body any) result {
	t.Helper()
	return c.do("PUT", "/api/admin/security", body).want(t, 200, "")
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
	return map[string]any{"username": user, "password": "wrong password here"}
}

func good(user string) map[string]any { return map[string]any{"username": user, "password": pw} }
