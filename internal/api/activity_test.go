package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The optional categories write nothing while a tenant has them off; with them
// on they write one row per person and thing an hour, in the person's tenant,
// by id.
func TestOptionalAuditCategoriesWriteRowsOnlyWhenOn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		base := "/api/spaces/" + f.spaceA
		id := f.admA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Secret plan"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)
		file := f.memA.upload(base, id, "notes.txt", []byte("hello")).want(t, 201, "").body["path"].(string)
		fileURL := base + "/files/" + strings.TrimPrefix(file, "/")
		zip := f.memA.upload(base, id, "pack.zip", []byte("PK")).want(t, 201, "").body["path"].(string)
		zipURL := base + "/files/" + strings.TrimPrefix(zip, "/")
		counts := func(c *client) map[string]int {
			a.auth.FlushAudit(ctx)
			out := map[string]int{}
			for _, k := range []string{"page_read", "file_read", "file_downloaded", "api_call"} {
				out[k] = len(c.audit(t, k))
			}
			return out
		}
		use := func() {
			f.memA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
			f.memA.do("GET", fileURL, nil).want(t, 200, "")
			f.memA.do("GET", zipURL, nil).want(t, 200, "")
			f.tokA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		}

		// Off (the default): nothing is queued, so there is nothing to write.
		use()
		if n := counts(f.admA); n["page_read"]+n["file_read"]+n["file_downloaded"]+n["api_call"] != 0 {
			t.Fatalf("rows with the categories off: %v", n)
		}

		// Tenant A switches them on; tenant B's own are still off.
		for _, k := range []string{"audit.reads", "audit.downloads", "audit.api"} {
			f.admA.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/"+k, map[string]any{"value": true}).want(t, 200, "")
		}
		use()
		use() // a reload loop: the read and the download are noted once
		// A text file is read inline, a zip is an attachment. The token's read is the same
		// person's page, so it is deduped with the member's; its two calls are
		// both recorded.
		if n := counts(f.admA); n["page_read"] != 1 || n["file_downloaded"] != 1 || n["file_read"] != 1 || n["api_call"] != 2 {
			t.Fatalf("rows with the categories on: %v", n)
		}
		var viaToken map[string]any
		for _, r := range f.admA.audit(t, "api_call") {
			if d := r["detail"].(map[string]any); d["via"] == "token" && d["route"] == "/api/spaces/{space}/pages/{id}" {
				viaToken = r
			}
		}
		if viaToken == nil {
			t.Fatalf("no row of the token's call")
		}
		d := viaToken["detail"].(map[string]any)
		if d["method"] != "GET" || d["status"] != float64(200) || viaToken["category"] != "api" || viaToken["actor"] != f.userA {
			t.Fatalf("%v", viaToken)
		}
		for _, r := range f.admA.audit(t, "page_read") {
			d := r["detail"].(map[string]any)
			if d["page"] != id || d["space"] != f.spaceA || r["category"] != "reads" || r["actor"] != f.userA {
				t.Fatalf("%v", r)
			}
			if strings.Contains(jsonText(r), "Secret plan") {
				t.Fatalf("a title in the row: %v", r)
			}
		}

		// An hour later the same page is noted again.
		a.clock.Advance(61 * time.Minute)
		f.memA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		if n := counts(f.admA); n["page_read"] != 2 {
			t.Fatalf("%v", n)
		}

		// A person's own space is never noted, and tenant B (off) writes nothing.
		own := f.memA.personalSpace()
		pid := f.memA.do("POST", "/api/spaces/"+own+"/pages", map[string]any{"meta": map[string]any{"title": "Diary"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)
		f.memA.do("GET", "/api/spaces/"+own+"/pages/"+pid, nil).want(t, 200, "")
		f.memB.do("GET", "/api/spaces/"+f.spaceB+"/pages", nil).want(t, 200, "")
		for _, r := range func() []map[string]any { counts(f.admA); return f.admA.audit(t, "page_read") }() {
			if r["detail"].(map[string]any)["space"] == own {
				t.Fatalf("a personal space was noted: %v", r)
			}
		}
		if n := counts(f.admB); n["page_read"]+n["api_call"]+n["file_downloaded"] != 0 {
			t.Fatalf("tenant B: %v", n)
		}
	})
}
