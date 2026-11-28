package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A tenant's administrators pin only within the tenant's storage.pinnedBytes
// (0 by default: they cannot pin until the site allows it): over it the answer
// is 409 quota_exceeded naming pinnedBytes, with the limit and what the pins
// would take. A site administrator is not held to it, and a tenant's
// administrator reaches no other tenant's space and never sets an exemption.
func TestPinningIsHeldToThePinnedBytesOfTheTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.putSetting("features.tenancy", true).want(t, 200, "")
		tenB, adm := auditTenant(t, a, root, "globex")
		mk := func(name string, size int64) string {
			t.Helper()
			id := adm.do("POST", "/api/spaces", map[string]any{"name": name}).want(t, 201, "").body["id"].(string)
			if _, err := a.st.Q().Exec(context.Background(), `UPDATE spaces SET size_bytes = ? WHERE id = ?`, size, id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		s1, s2, s3 := mk("One", 100), mk("Two", 60), mk("Three", 0)
		pin := func(c *client, id string, on bool) result {
			return c.do("PUT", "/api/admin/spaces/"+id+"/storage", map[string]any{"pin": on})
		}

		// 0 by default: not even an empty space.
		r := pin(adm, s3, true).want(t, 409, "quota_exceeded")
		if r.body["quota"] != "pinnedBytes" || r.body["limit"] != float64(0) {
			t.Fatalf("%s", r.raw)
		}
		pin(adm, s1, false).want(t, 200, "") // unpinning is always open

		// The site allows 150.
		root.do("PUT", "/api/admin/tenants/"+tenB+"/settings/storage.pinnedBytes", map[string]any{"siteRule": map[string]any{"value": 150}}).want(t, 200, "")
		pin(adm, s1, true).want(t, 200, "")
		pin(adm, s1, true).want(t, 200, "") // pinned again: not counted twice
		r = pin(adm, s2, true).want(t, 409, "quota_exceeded")
		if r.body["quota"] != "pinnedBytes" || r.body["limit"] != float64(150) || r.body["used"] != float64(160) {
			t.Fatalf("%s", r.raw)
		}
		pin(adm, s3, true).want(t, 200, "") // nothing to add
		pin(adm, s1, false).want(t, 200, "")
		pin(adm, s2, true).want(t, 200, "")
		pin(adm, s1, true).want(t, 409, "quota_exceeded") // 60 + 100
		// A site administrator is not held to the tenant's limit.
		pin(root, s1, true).want(t, 200, "")
		// Another tenant's administrator reaches nothing of this one, and no
		// tenant administrator sets an exemption.
		_, other := auditTenant(t, a, root, "initech")
		pin(other, s2, false).want(t, 404, "not_found")
		adm.do("PUT", "/api/admin/spaces/"+s2+"/storage", map[string]any{"exempt": true}).want(t, 403, "forbidden")
	})
}

// Pins that arrive together cannot take more than the limit: the check and the
// pin are one step.
func TestConcurrentPinsStayWithinTheLimit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.putSetting("features.tenancy", true).want(t, 200, "")
		tenB, adm := auditTenant(t, a, root, "globex")
		root.do("PUT", "/api/admin/tenants/"+tenB+"/settings/storage.pinnedBytes", map[string]any{"siteRule": map[string]any{"value": 150}}).want(t, 200, "")
		var ids []string
		for i := range 8 {
			id := adm.do("POST", "/api/spaces", map[string]any{"name": fmt.Sprintf("S%d", i)}).want(t, 201, "").body["id"].(string)
			if _, err := a.st.Q().Exec(context.Background(), `UPDATE spaces SET size_bytes = 60 WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		var wg sync.WaitGroup
		codes := make(chan int, len(ids))
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest("PUT", "/api/admin/spaces/"+id+"/storage", strings.NewReader(`{"pin":true}`))
				req.Host = strings.TrimPrefix(strings.TrimPrefix(a.public, "https://"), "http://")
				req.Header.Set("Content-Type", "application/json")
				for _, ck := range adm.cookies {
					req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
				}
				rec := httptest.NewRecorder()
				a.h.ServeHTTP(rec, req)
				codes <- rec.Code
			}()
		}
		wg.Wait()
		close(codes)
		ok := 0
		for c := range codes {
			if c == 200 {
				ok++
			} else if c != 409 {
				t.Fatalf("status %d", c)
			}
		}
		if ok != 2 {
			t.Fatalf("%d spaces pinned, 150 bytes hold two of 60", ok)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM spaces WHERE pinned <> 0 AND tenant_id = ?`, tenB); n != 2 {
			t.Fatalf("%d pinned in the database", n)
		}
	})
}
