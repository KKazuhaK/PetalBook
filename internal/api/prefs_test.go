package api_test

import (
	"bytes"
	"encoding/json"
	"github.com/KKazuhaK/PetalBook/internal/api"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func getPrefs(t *testing.T, c *client) (personal bool, prefs map[string]any) {
	t.Helper()
	r := c.do("GET", "/api/prefs", nil).want(t, 200, "")
	return r.body["personal"] == true, r.body["prefs"].(map[string]any)
}

func setPrefsAPI(t *testing.T, c *client, set map[string]any) result {
	t.Helper()
	return c.do("PATCH", "/api/prefs", map[string]any{"set": set})
}

func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
	return nil
}

func TestPrefsLiveInTheOwnPersonalSpaceInOneQuietCommit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{prefsQuiet: 300 * time.Millisecond}, func(t *testing.T, a *acc) {
		u := a.setup("kazuha", pw)
		pid := u.personalSpace()
		file := filepath.Join(a.dir, "spaces", pid, ".petalbook", "prefs.yml")
		sp, release, err := a.mgr.Acquire(ctxBG, pid)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := sp.Flush(); err != nil { // what the space made for itself is committed apart from it
			t.Fatal(err)
		}
		if personal, p := getPrefs(t, u); !personal || len(p) != 0 {
			t.Fatalf("%v %v", personal, p)
		}
		// A burst of writes: what is read is what was written last, at once.
		for i := 0; i < 12; i++ {
			r := setPrefsAPI(t, u, map[string]any{"pb.table.users": map[string]any{"pageSize": 25 + i}, "pb.onboarding": "done"}).want(t, 200, "")
			if r.body["personal"] != true {
				t.Fatalf("%s", r.raw)
			}
		}
		if _, p := getPrefs(t, u); p["pb.table.users"].(map[string]any)["pageSize"] != float64(36) || p["pb.onboarding"] != "done" {
			t.Fatalf("%v", p)
		}
		// ...and one write of the file after the quiet period.
		body := string(waitFile(t, file))
		if !strings.Contains(body, "pageSize: 36") || !strings.Contains(body, "pb.onboarding: done") {
			t.Fatalf("%s", body)
		}
		if err := sp.Flush(); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("git", "-C", filepath.Join(a.dir, "spaces", pid), "log", "--format=%s%n%an", "--", ".petalbook/prefs.yml").Output()
		if err != nil || strings.TrimSpace(string(out)) != "Update prefs.yml\nkazuha" {
			t.Fatalf("the commits of the file: %q %v", out, err)
		}
		// A value of null takes the key away; the committed file follows.
		setPrefsAPI(t, u, map[string]any{"pb.onboarding": nil}).want(t, 200, "")
		if _, p := getPrefs(t, u); p["pb.onboarding"] != nil || p["pb.table.users"] == nil {
			t.Fatalf("%v", p)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && strings.Contains(string(waitFile(t, file)), "pb.onboarding") {
			time.Sleep(20 * time.Millisecond)
		}
		if strings.Contains(string(waitFile(t, file)), "pb.onboarding") {
			t.Fatal("the key was not removed from the file")
		}
	})
}

func TestPrefsOfOneAreNeverReadOrWrittenByAnother(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{prefsQuiet: 100 * time.Millisecond}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		pid := f.memA.personalSpace()
		setPrefsAPI(t, f.memA, map[string]any{"secret": "mine"}).want(t, 200, "")
		waitFile(t, filepath.Join(a.dir, "spaces", pid, ".petalbook", "prefs.yml"))
		// Everyone else asking gets their own: the route names no person and no space.
		for name, c := range map[string]*client{"tenant admin": f.admA, "site admin": f.root, "other tenant": f.memB} {
			if _, p := getPrefs(t, c); p["secret"] != nil {
				t.Errorf("%s reads %v", name, p)
			}
		}
		setPrefsAPI(t, f.admA, map[string]any{"secret": "theirs"}).want(t, 200, "")
		if _, p := getPrefs(t, f.memA); p["secret"] != "mine" {
			t.Fatalf("overwritten by another: %v", p)
		}
		// The file is no file of the space to anyone, a site administrator's view of the space included.
		path := "/api/spaces/" + pid + "/files/.petalbook/prefs.yml"
		f.memA.do("GET", path, nil).want(t, 404, "not_found")
		f.admA.do("GET", path, nil).want(t, 404, "not_found")
		f.root.do("POST", "/api/admin/spaces/"+pid+"/view", map[string]any{"reason": "a court order"}).want(t, 200, "")
		f.root.do("GET", path, nil).want(t, 404, "not_found")
		f.root.do("GET", "/api/spaces/"+pid+"/files/.petalbook/%2e%2e/.petalbook/prefs.yml", nil).want(t, 404, "not_found")
		// The pages of a space are not where it is: it is in no listing, search or sync catalogue.
		for _, q := range []string{"/pages", "/tree", "/sync/catalog?limit=50"} {
			if r := f.memA.do("GET", "/api/spaces/"+pid+q, nil); r.code != 200 || strings.Contains(r.raw, "prefs") {
				t.Errorf("%s: %d %s", q, r.code, r.raw)
			}
		}
		// And an export an administrator makes of the space does not carry it.
		a.clock.Advance(time.Minute)
		r := f.root.do("POST", "/api/admin/exports", map[string]any{"spaces": []string{pid}}).want(t, 202, "")
		done := waitExport(t, f.root, r.body["id"].(string))
		if done["state"] != "ready" {
			t.Fatalf("%v", done)
		}
		dl := f.root.do("GET", "/api/admin/exports/"+r.body["id"].(string)+"/download", nil)
		for name, body := range unzipped(t, []byte(dl.raw)) {
			if strings.HasSuffix(name, "prefs.yml") || strings.Contains(body, "secret: mine") && !strings.HasSuffix(name, "history.bundle") {
				t.Errorf("the export holds %s", name)
			}
		}
	})
}

func TestPrefsAreCheckedBeforeTheyAreKept(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{prefsQuiet: time.Hour}, func(t *testing.T, a *acc) {
		u := a.setup("kazuha", pw)
		cases := map[string]map[string]any{
			"empty":         {},
			"bad key":       map[string]any{"has space": 1},
			"dot dot":       map[string]any{"../x": 1},
			"long key":      map[string]any{strings.Repeat("k", 121): 1},
			"too big":       map[string]any{"k": strings.Repeat("v", 33<<10)},
			"too many keys": manyKeys(101),
		}
		for name, set := range cases {
			if r := setPrefsAPI(t, u, set); r.code != 400 || r.body["error"] != "invalid" {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		u.do("PATCH", "/api/prefs", `{"set": "no"}`).want(t, 400, "invalid")
		u.do("PATCH", "/api/prefs", `{`).want(t, 400, "")
		setPrefsAPI(t, u, manyKeys(100)).want(t, 200, "")
		a.newClient("").do("GET", "/api/prefs", nil).want(t, 401, "")
		a.newClient("").do("PATCH", "/api/prefs", map[string]any{"set": map[string]any{"a": 1}}).want(t, 401, "")
	})
}

func manyKeys(n int) map[string]any {
	m := map[string]any{}
	for i := 0; i < n; i++ {
		m["k"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	return m
}

// What was written and not yet committed is committed when the server closes.
func TestPendingPrefsAreCommittedWhenTheServerCloses(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{prefsQuiet: time.Hour}, func(t *testing.T, a *acc) {
		u := a.setup("kazuha", pw)
		pid := u.personalSpace()
		file := filepath.Join(a.dir, "spaces", pid, ".petalbook", "prefs.yml")
		setPrefsAPI(t, u, map[string]any{"kept": true}).want(t, 200, "")
		if _, err := os.Stat(file); err == nil {
			t.Fatal("written before the quiet period")
		}
		a.h.Close()
		if b, err := os.ReadFile(file); err != nil || !bytes.Contains(b, []byte("kept: true")) {
			t.Fatalf("%q %v", b, err)
		}
		// After the close nothing more is taken.
		setPrefsAPI(t, u, map[string]any{"late": 1}).want(t, 503, "unavailable")
	})
}

func TestSingleUserPrefsLiveInTheOneSpace(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(o *api.Options) { o.PrefsQuiet = 100 * time.Millisecond })
	code, r, raw := f.do("GET", "/api/prefs", nil)
	if code != 200 || r["personal"] != true || len(r["prefs"].(map[string]any)) != 0 {
		t.Fatalf("%d %s", code, raw)
	}
	body, _ := json.Marshal(map[string]any{"set": map[string]any{"pb.table.x": map[string]any{"hidden": []string{"a"}}}})
	if code, _, raw = f.do("PATCH", "/api/prefs", string(body)); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	b := waitFile(t, filepath.Join(f.dir, ".petalbook", "prefs.yml"))
	if !strings.Contains(string(b), "pb.table.x") {
		t.Fatalf("%s", b)
	}
}
