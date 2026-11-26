package api_test

import "testing"

// The four sync endpoints of a space the caller reads.
func syncRoutes(f *tenFx, space string) map[string]string {
	return map[string]string{
		"sync/catalog": "/api/spaces/" + space + "/sync/catalog",
		"changes":      "/api/spaces/" + space + "/changes?since=" + f.cursorB,
		"sync/pages":   "/api/spaces/" + space + "/sync/pages?ids=0000000AAA",
		"heads":        "/api/sync/heads?space=" + space,
	}
}

// allowed checks the route is not refused by the switch (it may answer anything
// else: the cursor here is another space's).
func allowed(t *testing.T, c *client, path string) {
	t.Helper()
	if r := c.do("GET", path, nil); r.code == 403 && r.body["error"] == "offline_off" {
		t.Fatalf("%s refused although offline copies are allowed: %s", path, r.raw)
	}
}

func refused(t *testing.T, c *client, path string) {
	t.Helper()
	c.do("GET", path, nil).want(t, 403, "offline_off")
}

func TestOfflineCopiesSwitchRefusesTheSyncEndpoints(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		ten := func(id, key string) string { return "/api/admin/tenants/" + id + "/settings/" + key }
		const key = "offline.copies"
		inB, inA := syncRoutes(f, f.spaceB), syncRoutes(f, f.spaceA)

		// On by default: every route answers, and the setting is there to read.
		f.memB.do("GET", inB["sync/catalog"], nil).want(t, 200, "")
		f.memB.do("GET", inB["heads"], nil).want(t, 200, "")
		f.memB.do("GET", "/api/spaces/"+f.spaceB+"/sync/pages?ids=0000000AAA", nil).want(t, 200, "")
		if s := settingOf(t, f.memB.do("GET", "/api/settings", nil).want(t, 200, ""), key); s["effective"] != true || s["space"] != nil || s["person"] != nil {
			t.Fatalf("a person only reads it: %v", s)
		}
		sd := settingOf(t, f.root.do("GET", "/api/admin/settings", nil).want(t, 200, ""), key)
		if sd["kind"] != "boolean" || sd["site"].(map[string]any)["value"] != true || len(sd["layers"].([]any)) != 2 {
			t.Fatalf("site and tenant layers only, on by default: %v", sd)
		}

		// A tenant turns it off for itself: all four refuse its people, nobody else's.
		f.admB.do("PUT", ten(f.tenB, key), map[string]any{"value": false}).want(t, 200, "")
		for name, p := range inB {
			t.Run("B off "+name, func(t *testing.T) { refused(t, f.memB, p); refused(t, f.admB, p) })
		}
		for _, p := range inA {
			allowed(t, f.memA, p)
		}
		allowed(t, f.tokA, inA["heads"])
		f.memA.do("GET", inA["sync/catalog"], nil).want(t, 200, "")
		// Everything else of B still works, and B's own setting says so.
		f.memB.do("GET", "/api/spaces/"+f.spaceB+"/pages", nil).want(t, 200, "")
		if s := settingOf(t, f.memB.do("GET", "/api/settings", nil).want(t, 200, ""), key); s["effective"] != false {
			t.Fatal(s)
		}
		if s := settingOf(t, f.memA.do("GET", "/api/settings", nil).want(t, 200, ""), key); s["effective"] != true {
			t.Fatal("a tenant's switch is not its own")
		}
		// A token of the tenant is refused too; another tenant's space still does not exist for A.
		refused(t, a.bearer(f.secretB, ""), inB["heads"])
		f.memA.do("GET", inB["sync/catalog"], nil).want(t, 404, "not_found")
		// And a tenant administrator is not a site one.
		f.admB.do("PUT", "/api/admin/settings/"+key, map[string]any{"value": false}).want(t, 403, "forbidden")

		// On again.
		f.admB.do("PUT", ten(f.tenB, key), map[string]any{"value": true}).want(t, 200, "")
		for _, p := range inB {
			allowed(t, f.memB, p)
		}

		// The site's off is the default of the tenants that say nothing: B said on, and stays on; locked, it is off for all.
		f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"value": false}).want(t, 200, "")
		for _, p := range inB {
			allowed(t, f.memB, p)
		}
		f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		for _, p := range inA {
			refused(t, f.memA, p)
		}
		for _, p := range inB {
			refused(t, f.memB, p)
		}
		refused(t, f.root, inA["heads"])

		// Back on; a site lock stops the tenant from turning it off, a restriction to off keeps it from turning it on.
		f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"value": true}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		f.admB.do("PUT", ten(f.tenB, key), map[string]any{"value": false}).want(t, 409, "setting_locked")
		allowed(t, f.memB, inB["heads"])
		f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"lower": map[string]any{"mode": "restrict", "options": []any{false}}}).want(t, 200, "")
		f.admB.do("PUT", ten(f.tenB, key), map[string]any{"value": false}).want(t, 200, "")
		refused(t, f.memB, inB["heads"])
		f.admB.do("PUT", ten(f.tenB, key), map[string]any{"value": true}).want(t, 409, "setting_restricted")
		// A tenant that says nothing gets the site's on held to the restriction: off.
		refused(t, f.memA, inA["heads"])
	})
}
