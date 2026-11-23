package api_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// Milestone 3.1 part 1 through the API: the site's settings, the requirement of
// a remote with its grace period, partial clones and old versions on demand,
// eviction and restore, and the limit on local spaces.

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func filterRemote(t *testing.T) string {
	t.Helper()
	r := bareRemote(t)
	rg(t, r, "config", "uploadpack.allowFilter", "true")
	rg(t, r, "config", "uploadpack.allowAnySHA1InWant", "true")
	return r
}

func pushVersions(t *testing.T, remote string, n int) {
	t.Helper()
	dir := t.TempDir() + "/o"
	rg(t, t.TempDir(), "clone", "-q", remote, dir)
	rg(t, dir, "checkout", "-q", "-B", "main")
	for i := 1; i <= n; i++ {
		writeFile(t, dir+"/Snow.md", "---\nid: SNAP000001\ntitle: Snow\n---\nversion "+string(rune('0'+i))+"\n")
		rg(t, dir, "add", "-A")
		rg(t, dir, "commit", "-qm", "v")
	}
	rg(t, dir, "push", "-q", "origin", "main")
}

// storageOpts is remoteOpts with the hooks the storage tests need.
func storageOpts(n *netSwitch, mod func(*spaces.Options)) accOpts {
	return accOpts{spaces: func(o *spaces.Options) {
		o.RemotePolicy = gitstore.Policy{Hosts: []string{"*"}, AllowUnsafe: true}
		o.RemoteRunner = n
		o.FetchFresh, o.FailFast = time.Nanosecond, time.Nanosecond
		if mod != nil {
			mod(o)
		}
	}}
}

func TestStorageSettingsEndpoint(t *testing.T) {
	t.Parallel()
	eachAccounts(t, storageOpts(&netSwitch{}, nil), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		m := a.addUser("mia", pw, "member")
		m.do("GET", "/api/admin/storage", nil).want(t, 403, "forbidden")
		g := c.do("GET", "/api/admin/storage", nil).want(t, 200, "")
		rem := g.body["remotes"].(map[string]any)
		cache := g.body["cache"].(map[string]any)
		if rem["require"] != nil || rem["scope"] != "new" || rem["graceDays"] != float64(30) || rem["since"] != nil ||
			cache["evictAfterDays"] != float64(14) || cache["minIdleHours"] != float64(24) || cache["clonesBytes"] != nil || cache["minFreeBytes"] != float64(10<<30) ||
			g.body["localSpaces"].(map[string]any)["maxBytes"] != nil {
			t.Fatal(g.raw)
		}
		if hosts := g.body["hosts"].(map[string]any); hosts["fromFlag"] != false {
			t.Fatal(g.raw)
		}
		git := g.body["git"].(map[string]any)
		if git["version"] == "" || git["partialClone"] != true {
			t.Fatal(g.raw)
		}
		aff := g.body["affected"].(map[string]any)
		if aff["inGrace"] != float64(0) || aff["overdue"] != float64(0) || aff["exempt"] != float64(0) {
			t.Fatal(g.raw)
		}
		// A part is changed; the rest stays, and the answer is the new state.
		p := c.do("PUT", "/api/admin/storage", map[string]any{"cache": map[string]any{"minIdleHours": 48, "evictAfterDays": nil}, "localSpaces": map[string]any{"maxBytes": 5 << 20}}).want(t, 200, "")
		cache = p.body["cache"].(map[string]any)
		if cache["minIdleHours"] != float64(48) || cache["evictAfterDays"] != nil || cache["minFreeBytes"] != float64(10<<30) || p.body["localSpaces"].(map[string]any)["maxBytes"] != float64(5<<20) {
			t.Fatal(p.raw)
		}
		if g2 := c.do("GET", "/api/admin/storage", nil).want(t, 200, ""); g2.raw != p.raw {
			t.Fatalf("GET differs from PUT:\n%s\n%s", g2.raw, p.raw)
		}
		// Ranges.
		for _, bad := range []map[string]any{
			{"remotes": map[string]any{"graceDays": 400}},
			{"remotes": map[string]any{"require": "gitlab"}},
			{"remotes": map[string]any{"scope": "most"}},
			{"localSpaces": map[string]any{"maxBytes": 1000}},
			{"cache": map[string]any{"evictAfterDays": 0}},
			{"cache": map[string]any{"minIdleHours": 0}},
			{"cache": map[string]any{"clonesBytes": 1 << 20}},
			{"cache": map[string]any{"minFreeBytes": -1}},
			{"cache": map[string]any{"nope": 1}},
			{"hosts": map[string]any{"allowed": []string{}}},
			{"hosts": map[string]any{"allowed": []string{"a b"}}},
			{"remotes": "x"},
			{"assets": map[string]any{"newSpaces": true}},
		} {
			c.do("PUT", "/api/admin/storage", bad).want(t, 400, "invalid")
		}
		// GitHub cannot be required while the server has no App.
		c.do("PUT", "/api/admin/storage", map[string]any{"remotes": map[string]any{"require": "github"}}).want(t, 409, "github_not_configured")
		// Hosts are stored and apply to the next request.
		c.do("PUT", "/api/admin/storage", map[string]any{"hosts": map[string]any{"allowed": []string{"git.example.test"}}}).want(t, 200, "")
		sid := c.personalSpace()
		c.do("PUT", "/api/spaces/"+sid+"/remote", connectBody("https://github.com/a/b.git")).want(t, 403, "forbidden")
	})
}

func TestTheHostsFlagWinsAndThePageIsReadOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, storageOpts(&netSwitch{}, func(o *spaces.Options) { o.HostsFromFlag = true }), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		g := c.do("GET", "/api/admin/storage", nil).want(t, 200, "")
		h := g.body["hosts"].(map[string]any)
		if h["fromFlag"] != true || h["allowed"].([]any)[0] != "*" {
			t.Fatal(g.raw)
		}
		c.do("PUT", "/api/admin/storage", map[string]any{"hosts": map[string]any{"allowed": []string{"x.test"}}}).want(t, 409, "conflict")
		// The other settings still change.
		c.do("PUT", "/api/admin/storage", map[string]any{"cache": map[string]any{"minIdleHours": 30}}).want(t, 200, "")
	})
}

func TestRemoteRequiredEverywhereTheContractLists(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		sid := admin.personalSpace()
		base := "/api/spaces/" + sid
		page := admin.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Before"}, "body": "x\n"}).want(t, 201, "")
		pid := page.body["id"].(string)
		rev := page.body["rev"].(string)
		// Require a remote of every space, with a grace period.
		p := admin.do("PUT", "/api/admin/storage", map[string]any{"remotes": map[string]any{"require": "any", "scope": "all", "graceDays": 10}}).want(t, 200, "")
		if p.body["remotes"].(map[string]any)["since"] == nil || p.body["affected"].(map[string]any)["inGrace"] != float64(1) {
			t.Fatal(p.raw)
		}
		// The notice is in the space, for every member.
		var note map[string]any
		for _, s := range admin.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if s.(map[string]any)["id"] == sid {
				note = s.(map[string]any)["storage"].(map[string]any)["remote"].(map[string]any)
			}
		}
		if note == nil || note["required"] != "any" || note["overdue"] != false || note["deadline"] == nil {
			t.Fatalf("%v", note)
		}
		// A new space without a remote is refused before anything is made.
		before := len(admin.do("GET", "/api/spaces", nil).body["spaces"].([]any))
		r := admin.do("POST", "/api/spaces", map[string]any{"name": "Local"}).want(t, 409, "remote_required")
		if r.body["require"] != "any" || r.body["deadline"] != nil {
			t.Fatal(r.raw)
		}
		if after := len(admin.do("GET", "/api/spaces", nil).body["spaces"].([]any)); after != before {
			t.Fatal("a refused space was made")
		}
		// In the grace period nothing changes.
		admin.do("PUT", base+"/pages/"+pid, map[string]any{"baseRev": rev, "meta": map[string]any{"id": pid, "title": "Grace"}, "body": "y\n"}).want(t, 200, "")
		// After the deadline the space is read only, with the deadline in the answer.
		a.clock.Advance(11 * 24 * time.Hour)
		for _, w := range []struct {
			method, path string
			body         any
		}{
			{"POST", base + "/pages", map[string]any{"meta": map[string]any{"title": "New"}, "body": "x"}},
			{"DELETE", base + "/pages/" + pid, nil},
			{"POST", base + "/folders", map[string]any{"path": "F"}},
		} {
			r := admin.do(w.method, w.path, w.body).want(t, 409, "remote_required")
			if r.body["require"] != "any" || r.body["deadline"] == nil {
				t.Fatalf("%s %s: %s", w.method, w.path, r.raw)
			}
		}
		if s := admin.do("GET", base+"/pages", nil).want(t, 200, ""); len(s.body["items"].([]any)) != 1 {
			t.Fatal(s.raw)
		}
		admin.do("GET", base+"/pages/"+pid, nil).want(t, 200, "")
		// A draft is saved all the same: it is not in the space.
		cur := admin.do("GET", base+"/pages/"+pid, nil).want(t, 200, "")
		admin.draft(base+"/pages/"+pid, cur.body["rev"], "Draft", "typing\n").want(t, 200, "")
		// Reading the status of the policy.
		if g := admin.do("GET", "/api/admin/storage", nil).want(t, 200, ""); g.body["affected"].(map[string]any)["overdue"] != float64(1) {
			t.Fatal(g.raw)
		}
		if l := admin.do("GET", "/api/admin/spaces?overdue=1", nil).want(t, 200, ""); len(l.body["items"].([]any)) != 1 {
			t.Fatal(l.raw)
		}
		// Connecting always works, and then the space is writable.
		remote := bareRemote(t)
		admin.do("PUT", base+"/remote", connectBody("file://"+remote)).want(t, 200, "")
		admin.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Now"}, "body": "ok\n"}).want(t, 201, "")
		if l := admin.do("GET", "/api/admin/spaces?overdue=1", nil).want(t, 200, ""); len(l.body["items"].([]any)) != 0 {
			t.Fatal(l.raw)
		}
		// Removing the remote is refused while the requirement stands.
		admin.do("DELETE", base+"/remote", nil).want(t, 409, "remote_required")
		// An exemption of the user lets them make a space of their own.
		uid := admin.do("GET", "/api/session", nil).body["user"].(map[string]any)["id"].(string)
		u := admin.do("PATCH", "/api/users/"+uid, map[string]any{"remoteExempt": true}).want(t, 200, "")
		if u.user()["remoteExempt"] != true {
			t.Fatal(u.raw)
		}
		admin.do("POST", "/api/spaces", map[string]any{"name": "Exempt"}).want(t, 201, "")
		admin.do("PATCH", "/api/users/"+uid, map[string]any{"remoteExempt": false}).want(t, 200, "")
		admin.do("POST", "/api/spaces", map[string]any{"name": "Again"}).want(t, 409, "remote_required")
		// Relaxing the requirement clears the notices at once.
		admin.do("PUT", "/api/admin/storage", map[string]any{"remotes": map[string]any{"require": nil}}).want(t, 200, "")
		admin.do("POST", "/api/spaces", map[string]any{"name": "Free"}).want(t, 201, "")
	})
}

func TestASpaceCanBeMadeWithARemoteAndOverdueSpacesAreWritableAfterAnExemption(t *testing.T) {
	t.Parallel()
	eachAccounts(t, storageOpts(&netSwitch{}, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 2)
		admin.do("PUT", "/api/admin/storage", map[string]any{"remotes": map[string]any{"require": "any", "scope": "new"}}).want(t, 200, "")
		// remote and github together are a mistake.
		admin.do("POST", "/api/spaces", map[string]any{"name": "X", "remote": connectBody("file://" + remote), "github": map[string]any{"owner": "o", "name": "n"}}).want(t, 400, "invalid")
		r := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "")
		if r.body["sync"] != "ok" {
			t.Fatal(r.raw)
		}
		sid := r.body["id"].(string)
		if got := admin.do("GET", "/api/spaces/"+sid+"/pages", nil).want(t, 200, ""); len(got.body["items"].([]any)) != 1 {
			t.Fatal(got.raw)
		}
		// An ssh credential cannot be given here.
		admin.do("POST", "/api/spaces", map[string]any{"name": "Y", "remote": map[string]any{"url": "git@example.test:a/b.git", "credential": map[string]any{"type": "ssh"}}}).want(t, 400, "invalid")
	})
}

func TestEvictAndRestoreThroughTheAPI(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	var clk *clock
	eachAccounts(t, storageOpts(n, func(o *spaces.Options) { o.EvictEvery = -1 }), func(t *testing.T, a *acc) {
		clk = a.clock
		_ = clk
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 3)
		local := admin.personalSpace()
		// A local space is never evicted.
		admin.do("POST", "/api/admin/spaces/"+local+"/evict", nil).want(t, 409, "conflict")
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		a.clock.Advance(48 * time.Hour)
		// The status and the item before.
		item := admin.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "")
		st := item.body["storage"].(map[string]any)
		if st["pinned"] != false || st["exempt"] != false || st["lastUsed"] == nil {
			t.Fatal(item.raw)
		}
		time.Sleep(200 * time.Millisecond)
		item = admin.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "")
		if c := item.body["storage"].(map[string]any)["cache"]; c != "partial" {
			t.Fatalf("cache %v: %s", c, item.raw)
		}
		// Pinning keeps it: it cannot be evicted; unpinned it can.
		pin := admin.do("PUT", "/api/admin/spaces/"+sid+"/storage", map[string]any{"pin": true}).want(t, 200, "")
		if pin.body["storage"].(map[string]any)["pinned"] != true {
			t.Fatal(pin.raw)
		}
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 409, "conflict")
		time.Sleep(500 * time.Millisecond)
		admin.do("PUT", "/api/admin/spaces/"+sid+"/storage", map[string]any{"pin": false}).want(t, 200, "")
		admin.do("POST", "/api/admin/spaces/NOSUCHSPACE/evict", nil).want(t, 404, "not_found")
		// A draft keeps it: busy.
		cur := admin.do("GET", base+"/pages", nil).want(t, 200, "")
		pid := cur.body["items"].([]any)[0].(map[string]any)["id"].(string)
		pg := admin.do("GET", base+"/pages/"+pid, nil).want(t, 200, "")
		admin.draft(base+"/pages/"+pid, pg.body["rev"], "Snow", "typing\n").want(t, 200, "")
		r := admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 409, "busy")
		if !strings.Contains(r.body["message"].(string), "draft") {
			t.Fatal(r.raw)
		}
		admin.do("DELETE", base+"/pages/"+pid+"/draft", nil).want(t, 204, "")
		// The remote cannot be reached: it is not evicted.
		n.set(true)
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 503, "unavailable")
		n.set(false)
		time.Sleep(10 * time.Millisecond)
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 204, "")
		// The space says so, everywhere.
		item = admin.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "")
		if item.body["storage"].(map[string]any)["cache"] != "evicted" {
			t.Fatal(item.raw)
		}
		var mine map[string]any
		for _, s := range admin.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if s.(map[string]any)["id"] == sid {
				mine = s.(map[string]any)
			}
		}
		if mine["storage"].(map[string]any)["cache"] != "evicted" {
			t.Fatalf("%v", mine)
		}
		stat := admin.do("GET", "/api/admin/status", nil).want(t, 200, "")
		if sp := stat.body["storage"].(map[string]any)["spaces"].(map[string]any); sp["evicted"] != float64(1) || sp["restoring"] != float64(0) {
			t.Fatal(stat.raw)
		}
		// Lists and the tree come from the index: nothing is restored.
		if l := admin.do("GET", base+"/pages?recursive=true", nil).want(t, 200, ""); len(l.body["items"].([]any)) != 1 {
			t.Fatal(l.raw)
		}
		admin.do("GET", base+"/tree", nil).want(t, 200, "")
		admin.do("GET", base+"/tags", nil).want(t, 200, "")
		admin.do("GET", base+"/checks", nil).want(t, 200, "")
		if admin.do("GET", "/api/admin/spaces/"+sid, nil).body["storage"].(map[string]any)["cache"] != "evicted" {
			t.Fatal("an index-only read restored the space")
		}
		// Reading a page restores it.
		got := admin.do("GET", base+"/pages/"+pid, nil).want(t, 200, "")
		if !strings.Contains(got.body["body"].(string), "version 3") {
			t.Fatal(got.raw)
		}
		if admin.do("GET", "/api/admin/spaces/"+sid, nil).body["storage"].(map[string]any)["cache"] != "partial" {
			t.Fatal("not restored as a partial clone")
		}
		// And the history of the old versions is fetched on demand.
		h := admin.do("GET", base+"/pages/"+pid+"/history", nil).want(t, 200, "")
		if items := h.body["items"].([]any); len(items) != 3 {
			t.Fatal(h.raw)
		}
		// With the remote down, an old version that was never fetched is not_cached.
		n.set(true)
		rest := admin.do("GET", base+"/pages/"+pid+"/history?limit=1", nil)
		if rest.code != 200 {
			t.Fatalf("versions that were fetched are read while the remote is down: %s", rest.raw)
		}
		n.set(false)
	})
}

func TestNotCachedAnswersWithRetryAfterWhenTheRemoteIsDown(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 3)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		pid := admin.do("GET", base+"/pages", nil).body["items"].([]any)[0].(map[string]any)["id"].(string)
		n.set(true)
		r := admin.do("GET", base+"/pages/"+pid+"/history", nil).want(t, 503, "not_cached")
		if r.body["fetching"] != false || r.resp.Header.Get("Retry-After") != "60" {
			t.Fatalf("%s %v", r.raw, r.resp.Header)
		}
		// Reading the current text is not affected.
		admin.do("GET", base+"/pages/"+pid, nil).want(t, 200, "")
		n.set(false)
		time.Sleep(10 * time.Millisecond)
		admin.do("GET", base+"/pages/"+pid+"/history", nil).want(t, 200, "")
	})
}

// fetchHold makes every fetch wait until released.
type fetchHold struct {
	netSwitch
	mu2  sync.Mutex
	hold chan struct{}
}

func (f *fetchHold) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	f.mu2.Lock()
	h := f.hold
	f.mu2.Unlock()
	if sub, _ := gitstore.Subcommand(c.Args); sub == "fetch" && h != nil {
		select {
		case <-h:
		case <-ctx.Done():
		}
	}
	return f.netSwitch.Run(ctx, c)
}

func TestRestoringAnswersWithThePhaseAndRetryAfter(t *testing.T) {
	t.Parallel()
	n := &fetchHold{}
	old := spaces.RestoreWait
	spaces.RestoreWait = 100 * time.Millisecond
	defer func() { spaces.RestoreWait = old }()
	eachAccounts(t, storageOpts(&n.netSwitch, func(o *spaces.Options) { o.RemoteRunner = n }), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		pushVersions(t, remote, 2)
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		pid := admin.do("GET", base+"/pages", nil).body["items"].([]any)[0].(map[string]any)["id"].(string)
		a.clock.Advance(48 * time.Hour)
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 204, "")
		n.mu2.Lock()
		n.hold = make(chan struct{})
		hold := n.hold
		n.mu2.Unlock()
		r := admin.do("GET", base+"/pages/"+pid, nil).want(t, 503, "restoring")
		if r.body["phase"] != "fetching" || r.resp.Header.Get("Retry-After") != "5" {
			t.Fatalf("%s %v", r.raw, r.resp.Header)
		}
		close(hold)
		n.mu2.Lock()
		n.hold = nil
		n.mu2.Unlock()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if admin.do("GET", base+"/pages/"+pid, nil).code == 200 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the restore did not finish")
			}
			time.Sleep(50 * time.Millisecond)
		}
		// A restore that cannot reach the remote fails at once with 503 unavailable.
		a.clock.Advance(48 * time.Hour)
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 204, "")
		n.set(true)
		admin.do("GET", base+"/pages/"+pid, nil).want(t, 503, "unavailable")
		admin.do("GET", base+"/tree", nil).want(t, 200, "")
		n.set(false)
	})
}

func TestALimitOnLocalSpacesRefusesUploadsAndImportsButNotText(t *testing.T) {
	t.Parallel()
	eachAccounts(t, storageOpts(&netSwitch{}, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		sid := admin.personalSpace()
		base := "/api/spaces/" + sid
		admin.do("PUT", "/api/admin/storage", map[string]any{"localSpaces": map[string]any{"maxBytes": 1 << 20}}).want(t, 200, "")
		pg := admin.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "T"}, "body": "x\n"}).want(t, 201, "")
		pid := pg.body["id"].(string)
		admin.upload(base, pid, "small.png", []byte("12345")).want(t, 201, "")
		big := make([]byte, 2<<20)
		admin.upload(base, pid, "big.png", big).want(t, 507, "space_full")
		// Saving text is never refused.
		text := strings.Repeat("word ", 300000)
		admin.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Long"}, "body": text}).want(t, 201, "")
		admin.do("PUT", "/api/admin/storage", map[string]any{"localSpaces": map[string]any{"maxBytes": nil}}).want(t, 200, "")
		admin.upload(base, pid, "big.png", big).want(t, 201, "")
	})
}

// Every route the index answers works on an evicted space, without restoring
// it: links and backlinks too, which on a cached space read the files.
func TestEveryIndexOnlyRouteAnswersOnAnEvictedSpace(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, func(o *spaces.Options) { o.EvictEvery = -1 }), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		remote := filterRemote(t)
		dir := t.TempDir() + "/o"
		rg(t, t.TempDir(), "clone", "-q", remote, dir)
		rg(t, dir, "checkout", "-q", "-B", "main")
		writeFile(t, dir+"/From.md", "---\nid: PAGEA00001\ntitle: From\ntags: [a]\n---\nSee [the target](To.md) here.\n")
		writeFile(t, dir+"/To.md", "---\nid: PAGEB00002\ntitle: To\n---\ntarget\n")
		rg(t, dir, "add", "-A")
		rg(t, dir, "commit", "-qm", "two pages")
		rg(t, dir, "push", "-q", "origin", "main")
		sid := admin.do("POST", "/api/spaces", map[string]any{"name": "Followed", "remote": connectBody("file://" + remote)}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sid
		// On the cached space first: what the evicted one must answer the same.
		cachedLinks := admin.do("GET", base+"/pages/PAGEA00001/links", nil).want(t, 200, "")
		cachedBack := admin.do("GET", base+"/pages/PAGEB00002/backlinks", nil).want(t, 200, "")
		if b := cachedBack.body["items"].([]any); len(b) != 1 || b[0].(map[string]any)["excerpt"] == "" {
			t.Fatal(cachedBack.raw)
		}
		a.clock.Advance(48 * time.Hour)
		admin.do("POST", "/api/admin/spaces/"+sid+"/evict", nil).want(t, 204, "")

		links := admin.do("GET", base+"/pages/PAGEA00001/links", nil).want(t, 200, "")
		if links.raw != cachedLinks.raw {
			t.Fatalf("links of an evicted space:\n%s\nof the cached one:\n%s", links.raw, cachedLinks.raw)
		}
		back := admin.do("GET", base+"/pages/PAGEB00002/backlinks", nil).want(t, 200, "")
		items := back.body["items"].([]any)
		if len(items) != 1 || back.body["total"] != float64(1) {
			t.Fatal(back.raw)
		}
		it := items[0].(map[string]any)
		if it["id"] != "PAGEA00001" || it["path"] != "From.md" || it["title"] != "From" || it["excerpt"] != "" {
			t.Fatalf("a backlink of an evicted space has its page and no excerpt: %s", back.raw)
		}
		admin.do("GET", base+"/pages/NOSUCHPAGE/links", nil).want(t, 404, "not_found")
		admin.do("GET", base+"/pages/NOSUCHPAGE/backlinks", nil).want(t, 404, "not_found")

		query := map[string]string{"GET /api/spaces/{space}/pages/groups": "?key=title&tag=a"}
		for _, rt := range api.IndexOnlyRoutes() {
			method, pattern, _ := strings.Cut(rt, " ")
			p := strings.ReplaceAll(pattern, "{space}", sid)
			p = strings.ReplaceAll(p, "{id}", "PAGEB00002")
			if strings.Contains(p, "{") {
				t.Fatalf("a route with a parameter this test does not fill: %s", rt)
			}
			r := admin.do(method, p+query[rt], nil)
			if r.code != 200 {
				t.Errorf("%s on an evicted space: %d %s", rt, r.code, r.raw)
			}
		}
		if c := admin.do("GET", "/api/admin/spaces/"+sid, nil).body["storage"].(map[string]any)["cache"]; c != "evicted" {
			t.Fatalf("an index-only read restored the space (%v)", c)
		}
	})
}
