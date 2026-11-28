package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
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

// A read of something that is not there writes no row and fills no table: only
// a page or file that was found is a read.
func TestActivityNeedsTheThingToExist(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		for _, k := range []string{"audit.reads", "audit.downloads"} {
			f.admA.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/"+k, map[string]any{"value": true}).want(t, 200, "")
		}
		base := "/api/spaces/" + f.spaceA
		f.memA.do("GET", base+"/pages/"+fakeID, nil).want(t, 404, "")
		f.memA.do("GET", base+"/files/nothing/here.txt", nil).want(t, 404, "")
		f.memA.do("GET", base+"/files/nothing/here.zip", nil).want(t, 404, "")
		a.auth.FlushAudit(ctx)
		for _, k := range []string{"page_read", "file_read", "file_downloaded"} {
			if rows := f.admA.audit(t, k); len(rows) != 0 {
				t.Fatalf("%s rows for what is not there: %v", k, rows)
			}
		}
		id := f.admA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "T"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)
		f.memA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(ctx)
		if rows := f.admA.audit(t, "page_read"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
	})
}

// A file's path is a name the uploader chose: the audit list and the download
// show it to a reader who could open the space, and "a file in <space>" to one
// who could not.
func TestAuditShowsFilePathsOnlyToWhoCanOpenTheSpace(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		f.admA.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/audit.reads", map[string]any{"value": true}).want(t, 200, "")
		base := "/api/spaces/" + f.spaceA
		id := f.admA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "P"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)
		file := f.memA.upload(base, id, "secret-merger.txt", []byte("hello")).want(t, 201, "").body["path"].(string)
		f.memA.do("GET", base+"/files/"+strings.TrimPrefix(file, "/"), nil).want(t, 200, "")
		a.auth.FlushAudit(ctx)

		// A tenant administrator who is not a member of the space.
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "adm-a2", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		other := a.newClient("")
		other.do("POST", "/api/login", map[string]any{"identifier": "adm-a2", "password": pw}).want(t, 200, "")
		shown := func(c *client) (list, csv string) {
			rows := c.audit(t, "file_read")
			if len(rows) != 1 {
				t.Fatalf("%v", rows)
			}
			return jsonText(rows[0]), c.do("GET", "/api/admin/audit/download?kind=file_read", nil).want(t, 200, "").raw
		}
		hidden := func(c *client, redacted bool) {
			t.Helper()
			list, csv := shown(c)
			for _, text := range []string{list, csv} {
				if got := strings.Contains(text, "secret-merger"); got == redacted {
					t.Fatalf("redacted=%v but the text is %s", redacted, text)
				}
				if redacted && !strings.Contains(text, "a file in "+f.spaceA) {
					t.Fatalf("no placeholder: %s", text)
				}
			}
		}
		hidden(other, true)
		hidden(f.admA, false) // a member (the owner)
		hidden(f.root, false) // a server administrator, who holds the content key
		other.do("POST", "/api/admin/spaces/"+f.spaceA+"/view", map[string]any{"reason": "a request"}).want(t, 200, "")
		hidden(other, false) // with an audited view open
	})
}

// The row of a read is written in the tenant of the space and follows that
// tenant's own setting, whoever reads.
func TestActivityFollowsTheSpacesTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		baseB := "/api/spaces/" + f.spaceB
		id := f.admB.do("POST", baseB+"/pages", map[string]any{"meta": map[string]any{"title": "B"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)
		rootID := mustUserID(t, a, "root")
		// A person is a member only inside their own tenant (store.AddMember sees to it), so
		// the row that puts the site administrator in B's space is written as a
		// restore of old data would: it shows the row follows the space, not the person.
		if err := a.st.Tx(ctx, func(q store.Q) error {
			_, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, 'viewer', ?)`, f.spaceB, rootID, a.clock.Now().UnixMilli())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		// The site administrator's tenant (A) has reads on; B's are off.
		f.root.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/audit.reads", map[string]any{"value": true}).want(t, 200, "")
		f.root.do("GET", baseB+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(ctx)
		if n := len(f.root.audit(t, "page_read")); n != 0 {
			t.Fatalf("%d rows although the space's tenant has reads off", n)
		}
		// B switches them on: the row is B's, and tenant A's administrator never sees it.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/audit.reads", map[string]any{"value": true}).want(t, 200, "")
		a.clock.Advance(2 * time.Hour)
		f.root.do("GET", baseB+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(ctx)
		rows := f.admB.audit(t, "page_read")
		if len(rows) != 1 || rows[0]["actor"] != rootID {
			t.Fatalf("%v", rows)
		}
		if n := len(f.admA.audit(t, "page_read")); n != 0 {
			t.Fatalf("tenant A sees %d rows of B's", n)
		}
	})
}

// A tool call that reads a page records it as the REST read does, once an hour
// for the person and page between the two.
func TestMCPPageReadIsRecordedLikeTheRESTRead(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		base := "/api/spaces/" + f.spaceA
		id := f.admA.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "T"}, "body": "x\n"}).want(t, 201, "").body["id"].(string)
		f.admA.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/audit.reads", map[string]any{"value": true}).want(t, 200, "")
		f.tokA.tool("page_get", map[string]any{"space": f.spaceA, "id": id}).ok(t)
		f.tokA.tool("page_get", map[string]any{"space": f.spaceA, "id": id}).ok(t)
		f.tokA.tool("page_get", map[string]any{"space": f.spaceA, "id": fakeID}) // nothing there: no row
		a.auth.FlushAudit(ctx)
		rows := f.admA.audit(t, "page_read")
		if len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		if d := rows[0]["detail"].(map[string]any); d["page"] != id || d["space"] != f.spaceA || rows[0]["actor"] != f.userA {
			t.Fatalf("%v", rows[0])
		}
		// The same person reading it over REST within the hour is the same read.
		f.memA.do("GET", base+"/pages/"+id, nil).want(t, 200, "")
		a.auth.FlushAudit(ctx)
		if n := len(f.admA.audit(t, "page_read")); n != 1 {
			t.Fatalf("%d rows", n)
		}
	})
}
