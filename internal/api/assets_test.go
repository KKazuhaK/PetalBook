package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// Milestone 3.1 part 2 through the API: the attachment repository, the move,
// ETag serving, not_cached for what cannot be fetched, the admin fields.

func fileGet(c *client, path string, hdr ...string) (int, http.Header, string) {
	r := c.do("GET", path, nil, hdr...)
	return r.code, r.resp.Header, r.raw
}

func TestTheAttachmentRepositoryThroughTheAPI(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 1)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		pid := admin.do("GET", base+"/pages", nil).body["items"].([]any)[0].(map[string]any)["id"].(string)
		// A local space cannot have one.
		local := admin.personalSpace()
		admin.do("PUT", "/api/spaces/"+local+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 409, "remote_required")
		admin.do("GET", base+"/remote/assets", nil).want(t, 404, "not_found")
		// An attachment before the repository is in assets/ of the space.
		up := admin.upload(base, pid, "old.txt", []byte("OLD")).want(t, 201, "")
		oldPath := up.body["path"].(string)
		if got := admin.do("GET", "/api/spaces", nil).body["spaces"].([]any); len(got) < 2 {
			t.Fatal(got)
		}
		arem := filterRemote(t)
		put := admin.do("PUT", base+"/remote/assets", connectBody("file://"+arem)).want(t, 200, "")
		if put.body["state"] != "ok" || put.body["move"] != nil {
			t.Fatal(put.raw)
		}
		admin.do("GET", base+"/remote/assets", nil).want(t, 200, "")
		sp := admin.do("GET", "/api/spaces", nil).body["spaces"].([]any)
		found := false
		for _, s := range sp {
			m := s.(map[string]any)
			if m["id"] == sid && m["storage"].(map[string]any)["assets"] == "repository" {
				found = true
			}
		}
		if !found {
			t.Fatal("the Space object does not say storage.assets is repository")
		}
		// A new upload goes to the repository; the link is the same shape.
		up2 := admin.upload(base, pid, "new.txt", []byte("NEW")).want(t, 201, "")
		newPath := up2.body["path"].(string)
		code, h, body := fileGet(admin, base+"/files/"+strings.TrimPrefix(newPath, "/"))
		etag := h.Get("ETag")
		if code != 200 || body != "NEW" || etag != `"`+gitstore.BlobID([]byte("NEW"))+`"` || h.Get("Last-Modified") != "" {
			t.Fatalf("%d %q %q %v", code, body, etag, h)
		}
		if code, _, _ := fileGet(admin, base+"/files/"+strings.TrimPrefix(newPath, "/"), "If-None-Match", etag); code != 304 {
			t.Fatalf("a browser that has it: %d", code)
		}
		// The old one is still read from the space until moved.
		if code, h, body := fileGet(admin, base+"/files/"+strings.TrimPrefix(oldPath, "/")); code != 200 || body != "OLD" || h.Get("ETag") != "" || h.Get("Last-Modified") == "" {
			t.Fatalf("%d %q %v", code, body, h)
		}
		// Not in the table: 404 without asking the remote.
		admin.do("GET", base+"/files/assets/"+pid+"/nope.txt", nil).want(t, 404, "not_found")
		// The move: 202, then done, and the old attachment is served with a tag.
		mv := admin.do("POST", base+"/remote/assets/move", nil).want(t, 202, "")
		if m := mv.body["move"].(map[string]any); m["files"] != float64(1) {
			t.Fatal(mv.raw)
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			g := admin.do("GET", base+"/remote/assets", nil).want(t, 200, "")
			if m, _ := g.body["move"].(map[string]any); m != nil && m["state"] == "done" {
				if m["filesDone"] != float64(1) || m["error"] != "" {
					t.Fatal(g.raw)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the move did not end: " + g.raw)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if code, h, body := fileGet(admin, base+"/files/"+strings.TrimPrefix(oldPath, "/")); code != 200 || body != "OLD" || h.Get("ETag") == "" {
			t.Fatalf("after the move: %d %q %v", code, body, h)
		}
		// Nothing left to move; the repository cannot be removed.
		admin.do("POST", base+"/remote/assets/move", nil).want(t, 200, "")
		admin.do("DELETE", base+"/remote/assets", nil).want(t, 409, "conflict")
		// The administration sees the cache and where the attachments are.
		ad := admin.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "")
		st := ad.body["storage"].(map[string]any)
		if st["assets"] != "repository" || st["assetCacheBytes"].(float64) <= 0 {
			t.Fatal(ad.raw)
		}
		stat := admin.do("GET", "/api/admin/status", nil).want(t, 200, "")
		ss := stat.body["storage"].(map[string]any)
		if ss["assetCacheBytes"].(float64) <= 0 || ss["assetsBudget"] != float64(5<<30) {
			t.Fatal(stat.raw)
		}
		cfg := admin.do("PUT", "/api/admin/storage", map[string]any{"cache": map[string]any{"assetsBytes": 64 << 20}}).want(t, 200, "")
		if cfg.body["cache"].(map[string]any)["assetsBytes"] != float64(64<<20) || cfg.body["assets"] != nil {
			t.Fatal(cfg.raw)
		}
	})
}

func TestNotCachedAttachmentAnswers503WithRetryAfter(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 1)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		pid := admin.do("GET", base+"/pages", nil).body["items"].([]any)[0].(map[string]any)["id"].(string)
		admin.do("PUT", base+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
		up := admin.upload(base, pid, "a.txt", []byte("AAA")).want(t, 201, "")
		path := base + "/files/" + strings.TrimPrefix(up.body["path"].(string), "/")
		a.mgr.AssetCache().DropSpace(sid)
		n.set(true)
		r := admin.do("GET", path, nil).want(t, 503, "not_cached")
		if r.body["fetching"] != false || r.resp.Header.Get("Retry-After") != "60" {
			t.Fatalf("%s %v", r.raw, r.resp.Header)
		}
		// A browser that has it is told so without the contents being fetched.
		tag := `"` + gitstore.BlobID([]byte("AAA")) + `"`
		if code, h, _ := fileGet(admin, path, "If-None-Match", tag); code != 304 || h.Get("ETag") != tag {
			t.Fatalf("a browser that has it, offline: %d %v", code, h)
		}
		admin.do("GET", path, nil, "If-None-Match", `"`+gitstore.BlobID([]byte("other"))+`"`).want(t, 503, "not_cached")
		// The page text is read as usual; an upload answers unavailable and stores nothing.
		admin.do("GET", base+"/pages/"+pid, nil).want(t, 200, "")
		admin.upload(base, pid, "b.txt", []byte("BBB")).want(t, 503, "unavailable")
		n.set(false)
		time.Sleep(10 * time.Millisecond)
		if code, _, body := fileGet(admin, path); code != 200 || body != "AAA" {
			t.Fatalf("%d %q", code, body)
		}
	})
}
