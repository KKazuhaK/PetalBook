package api_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// netSwitch is the real git with a switch that makes the network unreachable.
type netSwitch struct {
	mu      sync.Mutex
	down    bool
	refused bool // the remote answers, but does not accept the credential
	// hold, if set, is waited on inside every push; reached is told when one is waiting.
	hold    chan struct{}
	reached chan struct{}
}

func (n *netSwitch) set(v bool)    { n.mu.Lock(); n.down = v; n.mu.Unlock() }
func (n *netSwitch) refuse(v bool) { n.mu.Lock(); n.refused = v; n.mu.Unlock() }

func (n *netSwitch) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	sub, _ := gitstore.Subcommand(c.Args)
	n.mu.Lock()
	down, refused, hold, reached := n.down, n.refused, n.hold, n.reached
	n.mu.Unlock()
	if sub == "push" && hold != nil {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-hold
	}
	if down && (sub == "fetch" || sub == "push" || sub == "ls-remote") {
		return "", "fatal: unable to access 'https://example.test/x.git/': Could not resolve host: example.test", errors.New("exit status 128")
	}
	if refused && (sub == "fetch" || sub == "push" || sub == "ls-remote") {
		return "", "remote: Invalid username or password.\nfatal: Authentication failed for 'https://tok:" + secretToken + "@example.test/x.git/'", errors.New("exit status 128")
	}
	return gitstore.RunReal(ctx, c)
}

func bareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	os.MkdirAll(dir, 0o755)
	rg(t, dir, "init", "--bare", "-q", "-b", "main")
	return dir
}

func rg(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Other", "-c", "user.email=o@example.test", "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteOpts lets spaces follow bare repositories on disk, through the switch.
func remoteOpts(n *netSwitch, hosts []string, unsafe bool) accOpts {
	return accOpts{spaces: func(o *spaces.Options) {
		o.RemotePolicy = gitstore.Policy{Hosts: hosts, AllowUnsafe: unsafe}
		o.RemoteRunner = n
		o.FetchFresh, o.FailFast = time.Nanosecond, time.Nanosecond
	}}
}

const secretToken = "ghp_TopSecretToken_0123456789"

func connectBody(url string) map[string]any {
	return map[string]any{"url": url, "branch": "main", "credential": map[string]any{"type": "token", "token": secretToken}}
}

func TestRemoteEndpoints(t *testing.T) {
	eachAccounts(t, remoteOpts(&netSwitch{}, []string{"*"}, true), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		rurl := "/api/spaces/" + sid + "/remote"
		remote := bareRemote(t)

		// A local space has no remote, and says so everywhere.
		c.do("GET", rurl, nil).want(t, 404, "not_found")
		if l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any); l[0].(map[string]any)["sync"] != "local" {
			t.Fatalf("%v", l)
		}
		// The key can be asked for before connecting, and is the same the next time.
		k := c.do("POST", rurl+"/key", nil).want(t, 200, "")
		pub, fp := k.body["publicKey"].(string), k.body["fingerprint"].(string)
		if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasPrefix(fp, "SHA256:") || strings.Contains(k.raw, "PRIVATE") {
			t.Fatal(k.raw)
		}
		if again := c.do("POST", rurl+"/key", map[string]any{}).want(t, 200, ""); again.body["publicKey"] != pub {
			t.Fatal("the key changed")
		}
		if rot := c.do("POST", rurl+"/key", map[string]any{"rotate": true}).want(t, 200, ""); rot.body["publicKey"] == pub {
			t.Fatal("rotate did not make a new key")
		}
		c.do("GET", rurl, nil).want(t, 404, "not_found") // a key is not a remote

		// Mistakes in the request.
		c.do("PUT", rurl, map[string]any{"url": "git://github.com/a/b", "credential": map[string]any{"type": "token", "token": "x"}}).want(t, 400, "invalid")
		c.do("PUT", rurl, map[string]any{"url": remote, "credential": map[string]any{"type": "token"}}).want(t, 400, "invalid")
		c.do("PUT", rurl, map[string]any{"url": remote, "credential": map[string]any{"type": "nope", "token": "x"}}).want(t, 400, "invalid")
		c.do("PUT", rurl, map[string]any{"url": remote, "branch": "-x", "credential": map[string]any{"type": "token", "token": "x"}}).want(t, 400, "invalid")
		c.do("PUT", rurl, "{").want(t, 400, "invalid")
		c.do("GET", rurl, nil).want(t, 404, "not_found")

		// Connecting.
		r := c.do("PUT", rurl, connectBody(remote)).want(t, 200, "")
		if strings.Contains(r.raw, secretToken) {
			t.Fatal("the token is in the response")
		}
		cred := r.body["credential"].(map[string]any)
		if r.body["url"] != remote || r.body["branch"] != "main" || cred["type"] != "token" || cred["username"] != "x-access-token" ||
			r.body["state"] != "ok" || r.body["error"] != "" || r.body["rewritten"] != nil || r.body["lastSync"] == nil || cred["publicKey"] != nil {
			t.Fatal(r.raw)
		}
		if g := c.do("GET", rurl, nil).want(t, 200, ""); g.raw != r.raw {
			t.Fatalf("GET differs from PUT:\n%s\n%s", g.raw, r.raw)
		}
		if l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any); l[0].(map[string]any)["sync"] != "ok" {
			t.Fatalf("%v", l)
		}
		if l := c.do("GET", "/api/meta", nil).body["spaces"].([]any); l[0].(map[string]any)["sync"] != "ok" {
			t.Fatalf("%v", l)
		}
		// From now on a change is pushed before the answer.
		p := c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "Pushed"}, "body": "x\n"}).want(t, 201, "")
		if !strings.Contains(rg(t, remote, "ls-tree", "-r", "--name-only", "main"), "Pushed.md") {
			t.Fatal("the page was not pushed before the answer")
		}
		// Sync is a fetch now, and answers with the remote.
		c.do("POST", rurl+"/sync", nil).want(t, 200, "")
		// Nothing to accept when nothing was rewritten.
		c.do("POST", rurl+"/accept", map[string]any{"commit": "abcdef0"}).want(t, 409, "conflict")
		c.do("POST", rurl+"/accept", map[string]any{}).want(t, 400, "invalid")
		_ = p

		// Disconnecting.
		c.do("DELETE", rurl, nil).want(t, 204, "")
		c.do("GET", rurl, nil).want(t, 404, "not_found")
		if l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any); l[0].(map[string]any)["sync"] != "local" {
			t.Fatalf("%v", l)
		}
		c.do("POST", rurl+"/sync", nil).want(t, 404, "not_found")
	})
}

func TestConnectingToAnUnreachableOrRefusingRemoteChangesNothing(t *testing.T) {
	n := &netSwitch{}
	eachAccounts(t, remoteOpts(n, []string{"*"}, true), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		rurl := "/api/spaces/" + sid + "/remote"
		remote := bareRemote(t)
		n.set(true)
		r := c.do("PUT", rurl, connectBody(remote)).want(t, 503, "unavailable")
		if !strings.Contains(r.raw, "could not reach the remote") || strings.Contains(r.raw, secretToken) {
			t.Fatal(r.raw)
		}
		n.set(false)
		n.refuse(true)
		r = c.do("PUT", rurl, connectBody(remote)).want(t, 503, "unavailable")
		if !strings.Contains(r.raw, "refused the credential") || strings.Contains(r.raw, secretToken) || strings.Contains(r.raw, "tok:") {
			t.Fatalf("the message must say the credential was refused, and never show it: %s", r.raw)
		}
		c.do("GET", rurl, nil).want(t, 404, "not_found")
		if l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any); l[0].(map[string]any)["sync"] != "local" {
			t.Fatalf("%v", l)
		}
		if _, err := os.Stat(filepath.Join(a.dir, "spaces", sid, ".git")); err != nil {
			t.Fatal(err)
		}
		if out := rg(t, filepath.Join(a.dir, "spaces", sid), "remote"); out != "" {
			t.Fatalf("a remote was left configured: %q", out)
		}
		// A credential that is refused later (the token was revoked) is a state, not a crash.
		n.refuse(false)
		c.do("PUT", rurl, connectBody(remote)).want(t, 200, "")
		n.refuse(true)
		s := c.do("POST", rurl+"/sync", nil).want(t, 200, "")
		if s.body["state"] != "error" || !strings.Contains(s.body["error"].(string), "refused the credential") || strings.Contains(s.raw, secretToken) {
			t.Fatal(s.raw)
		}
		c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 503, "unavailable")
		n.refuse(false)
		if s := c.do("POST", rurl+"/sync", nil).want(t, 200, ""); s.body["state"] != "ok" {
			t.Fatal(s.raw)
		}
	})
}

func TestRemoteRefusesAddressesAndHostsByTheAdministrator(t *testing.T) {
	eachAccounts(t, remoteOpts(&netSwitch{}, []string{"github.com", "git.example.org"}, false), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		rurl := "/api/spaces/" + c.personalSpace() + "/remote"
		tok := map[string]any{"type": "token", "token": "x"}
		c.do("PUT", rurl, map[string]any{"url": "https://gitlab.com/a/b.git", "credential": tok}).want(t, 403, "forbidden")
		c.do("PUT", rurl, map[string]any{"url": "git@evil.example:a/b.git", "credential": map[string]any{"type": "ssh"}}).want(t, 403, "forbidden")
		c.do("PUT", rurl, map[string]any{"url": "https://github.com.evil.net/a/b.git", "credential": tok}).want(t, 403, "forbidden")
		for _, bad := range []string{"http://github.com/a/b.git", "https://x:y@github.com/a/b.git", "/srv/repo.git", "ext::sh -c id", "-oProxyCommand=x", "https://github.com/a/../b"} {
			c.do("PUT", rurl, map[string]any{"url": bad, "credential": tok}).want(t, 400, "invalid")
		}
		c.do("GET", rurl, nil).want(t, 404, "not_found")
	})
}

func TestChangesAnswer503WhileTheRemoteIsDownAndNothingIsChanged(t *testing.T) {
	n := &netSwitch{}
	eachAccounts(t, remoteOpts(n, []string{"*"}, true), func(t *testing.T, a *acc) {
		n.set(false)
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		base := "/api/spaces/" + sid
		remote := bareRemote(t)
		c.do("PUT", base+"/remote", connectBody(remote)).want(t, 200, "")
		p := c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Doc"}, "body": "v1\n"}).want(t, 201, "")
		id := p.body["id"].(string)
		purl := base + "/pages/" + id

		n.set(true)
		for name, r := range map[string]result{
			"create": c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "New"}, "body": "x\n"}),
			"save":   c.do("PUT", purl, map[string]any{"baseRev": p.body["rev"], "meta": map[string]any{"title": "Doc"}, "body": "v2\n"}),
			"move":   c.do("POST", purl+"/move", map[string]any{"folder": "F"}),
			"delete": c.do("DELETE", purl, nil),
		} {
			if r.code != 503 || r.body["error"] != "unavailable" || r.body["message"] == "" || !strings.Contains(r.raw, "could not reach the remote") {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		// Reads work, and nothing was changed.
		g := c.do("GET", purl, nil).want(t, 200, "")
		if g.body["body"] != "v1\n" || g.body["path"] != "Doc.md" {
			t.Fatal(g.raw)
		}
		if l := c.do("GET", base+"/pages", nil).body["items"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		if l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any); l[0].(map[string]any)["sync"] != "offline" {
			t.Fatalf("%v", l)
		}
		rr := c.do("GET", base+"/remote", nil).want(t, 200, "")
		if rr.body["state"] != "offline" || rr.body["error"] == "" {
			t.Fatal(rr.raw)
		}
		// sync now is not an error of its own: it reports the state.
		if s := c.do("POST", base+"/remote/sync", nil).want(t, 200, ""); s.body["state"] != "offline" {
			t.Fatal(s.raw)
		}

		// Drafts are not affected: typing keeps being stored; the commit answers 503 and keeps the draft.
		d := c.draft(purl, p.body["rev"], "Doc", "v2 draft\n")
		d.want(t, 200, "")
		cr := c.do("POST", purl+"/commit", map[string]any{"version": c.versions[purl]})
		if cr.code != 503 || cr.body["error"] != "unavailable" {
			t.Fatalf("%d %s", cr.code, cr.raw)
		}
		g = c.do("GET", purl, nil).want(t, 200, "")
		if g.body["draft"] == nil || g.body["draft"].(map[string]any)["body"] != "v2 draft\n" || g.body["draft"].(map[string]any)["stale"] != false {
			t.Fatalf("the draft is not intact: %s", g.raw)
		}

		// The sweeper keeps the draft too, retrying later with a growing delay.
		a.clock.Advance(time.Hour)
		if res := a.drafts.Sweep(context.Background()); res.Applied != 0 || res.Failed != 1 || res.Held != 0 {
			t.Fatalf("%+v", res)
		}
		g = c.do("GET", purl, nil).want(t, 200, "")
		if g.body["draft"] == nil {
			t.Fatal("the sweeper dropped or held the draft")
		}
		// Not due again at once.
		if res := a.drafts.Sweep(context.Background()); res.Applied+res.Failed+res.Held != 0 {
			t.Fatalf("%+v", res)
		}
		// The remote is back: the sweeper commits and pushes the draft.
		n.set(false)
		a.clock.Advance(15 * time.Minute)
		if res := a.drafts.Sweep(context.Background()); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
		if got := rg(t, remote, "show", "main:Doc.md"); !strings.Contains(got, "v2 draft") {
			t.Fatalf("the remote has:\n%s", got)
		}
		g = c.do("GET", purl, nil).want(t, 200, "")
		if g.body["draft"] != nil || g.body["body"] != "v2 draft\n" {
			t.Fatal(g.raw)
		}
		if s := c.do("GET", base+"/remote", nil).want(t, 200, ""); s.body["state"] != "ok" {
			t.Fatal(s.raw)
		}
	})
}

func TestCommitOfADraftIsPushedWhenTheRemoteIsUp(t *testing.T) {
	eachAccounts(t, remoteOpts(&netSwitch{}, []string{"*"}, true), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		remote := bareRemote(t)
		c.do("PUT", "/api/spaces/"+sid+"/remote", connectBody(remote)).want(t, 200, "")
		purl, p := draftPage(t, c, "Draft", "v1\n")
		c.draft(purl, p["rev"], "Draft", "v2\n").want(t, 200, "")
		c.commit(purl).want(t, 200, "")
		if got := rg(t, remote, "show", "main:Draft.md"); !strings.Contains(got, "v2") {
			t.Fatal(got)
		}
		// A conflict on the remote's side is the ordinary conflict.
		o := filepath.Join(t.TempDir(), "o")
		rg(t, t.TempDir(), "clone", "-q", remote, o)
		rg(t, o, "checkout", "-q", "-B", "main")
		raw, _ := os.ReadFile(filepath.Join(o, "Draft.md"))
		os.WriteFile(filepath.Join(o, "Draft.md"), []byte(strings.Replace(string(raw), "v2", "edited on the remote", 1)), 0o644)
		rg(t, o, "commit", "-qam", "remote edit")
		rg(t, o, "push", "-q", "origin", "main")
		c.do("POST", "/api/spaces/"+sid+"/remote/sync", nil).want(t, 200, "")
		cur := c.do("GET", purl, nil).want(t, 200, "")
		c.draft(purl, p["rev"], "Draft", "v3 mine\n").want(t, 200, "") // based on the old rev
		r := c.commit(purl)
		if r.code != 409 || r.body["error"] != "conflict" || r.body["current"].(map[string]any)["body"] != "edited on the remote\n" || cur.body["body"] != "edited on the remote\n" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
	})
}

func TestRewrittenRemoteThroughTheAPI(t *testing.T) {
	eachAccounts(t, remoteOpts(&netSwitch{}, []string{"*"}, true), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		base := "/api/spaces/" + sid
		remote := bareRemote(t)
		c.do("PUT", base+"/remote", connectBody(remote)).want(t, 200, "")
		c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Old"}}).want(t, 201, "")
		o := filepath.Join(t.TempDir(), "o")
		rg(t, t.TempDir(), "clone", "-q", remote, o)
		rg(t, o, "checkout", "-q", "--orphan", "fresh")
		rg(t, o, "rm", "-rfq", ".")
		os.WriteFile(filepath.Join(o, "New.md"), []byte("---\nid: NEWER00001\ntitle: New\n---\nn\n"), 0o644)
		rg(t, o, "add", "-A")
		rg(t, o, "commit", "-qm", "rewritten")
		rg(t, o, "push", "-q", "--force", "origin", "fresh:main")
		tip := rg(t, remote, "rev-parse", "main")

		s := c.do("POST", base+"/remote/sync", nil).want(t, 200, "")
		rw, _ := s.body["rewritten"].(map[string]any)
		if s.body["state"] != "rewritten" || rw == nil || rw["remote"] != tip || rw["lastGood"] == "" {
			t.Fatal(s.raw)
		}
		c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Nope"}}).want(t, 503, "unavailable")
		if l := c.do("GET", base+"/pages", nil).body["items"].([]any); len(l) != 1 {
			t.Fatalf("reads must keep working: %v", l)
		}
		c.do("POST", base+"/remote/accept", map[string]any{"commit": rw["lastGood"]}).want(t, 409, "conflict")
		acc := c.do("POST", base+"/remote/accept", map[string]any{"commit": tip}).want(t, 200, "")
		if acc.body["state"] != "ok" || acc.body["rewritten"] != nil {
			t.Fatal(acc.raw)
		}
		if l := c.do("GET", base+"/pages", nil).body["items"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != "NEWER00001" {
			t.Fatalf("%v", l)
		}
		c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "After"}}).want(t, 201, "")
	})
}

// The owner-only rule is the middleware's: every owner route refuses a member
// who is not an owner with 403, and everyone else's space is not there.
func TestOwnerRoutesAreForOwnersOnly(t *testing.T) {
	eachAccounts(t, remoteOpts(&netSwitch{}, []string{"*"}, true), func(t *testing.T, a *acc) {
		boss := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		tangSpace := tang.personalSpace()
		// A second person is a plain member of tang's space.
		helper := a.addUser("helper", pw, "member")
		helperID := mustUserID(t, a, "helper")
		if err := store.AddMember(context.Background(), a.st.Q(), tangSpace, helperID, "editor", time.Now()); err != nil {
			t.Fatal(err)
		}
		a.dropMembershipCache(t, "helper")
		stranger := a.addUser("stranger", pw, "member")
		ghost := boss.do("GET", "/api/spaces/0000000000/remote", nil).want(t, 404, "not_found")

		n := 0
		for _, rt := range api.Routes() {
			if !rt.Owner {
				continue
			}
			n++
			if !rt.Space || !rt.AccountsOnly {
				t.Errorf("%s %s: an owner route must be a space route of accounts mode", rt.Method, rt.Pattern)
			}
			path := strings.NewReplacer("{space}", tangSpace, "{id}", "01JD3K8Q7Z").Replace(rt.Pattern)
			if got := helper.do(rt.Method, path, "{}"); got.code != 403 || got.body["error"] != "forbidden" {
				t.Errorf("%s %s as a member who is not an owner: %d %s", rt.Method, rt.Pattern, got.code, got.raw)
			}
			for who, cl := range map[string]*client{"admin": boss, "stranger": stranger} {
				if got := cl.do(rt.Method, path, "{}"); got.code != 404 || got.raw != ghost.raw {
					t.Errorf("%s %s as %s: %d %s", rt.Method, rt.Pattern, who, got.code, got.raw)
				}
			}
		}
		if n != 6 {
			t.Fatalf("%d owner routes, want the six of the remote", n)
		}
		// Nothing was changed by any of it, and the owner is not refused.
		tang.do("GET", "/api/spaces/"+tangSpace+"/remote", nil).want(t, 404, "not_found")
		// The member sees the space and its history; only the owner's routes are closed to them.
		helper.do("GET", "/api/spaces/"+tangSpace+"/tree", nil).want(t, 200, "")
	})
}

func TestRemoteEndpointsDoNotExistInSingleUserMode(t *testing.T) {
	f := newFixture(t)
	for _, rt := range api.Routes() {
		if !rt.Owner {
			continue
		}
		path := strings.NewReplacer("{space}", "default").Replace(rt.Pattern)
		if code, r, raw := f.do(rt.Method, path, "{}"); code != 404 || r["error"] != "not_found" {
			t.Errorf("%s %s: %d %s", rt.Method, rt.Pattern, code, raw)
		}
	}
	if code, r, _ := f.do("GET", "/api/spaces", nil); code != 200 || r["spaces"].([]any)[0].(map[string]any)["sync"] != "local" {
		t.Fatalf("%v", r)
	}
}

// While a commit waits for the remote, typing in other pages keeps being
// stored at once. A store for the page being committed waits for the commit
// and is then told the draft it meant to replace is gone, with the file as it
// is now: stored during the push, it would sit on the rev that is being
// replaced, and the person would later be asked to choose between their own
// older and newer text.
func TestTypingWhileACommitWaitsForTheRemote(t *testing.T) {
	n := &netSwitch{}
	eachAccounts(t, remoteOpts(n, []string{"*"}, true), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		c.do("PUT", "/api/spaces/"+sid+"/remote", connectBody(bareRemote(t))).want(t, 200, "")
		purl, p := draftPage(t, c, "Typing", "v1\n")
		ourl, o := draftPage(t, c, "Other", "o1\n")
		c.draft(purl, p["rev"], "Typing", "first\n").want(t, 200, "")
		first := c.versions[purl]

		n.mu.Lock()
		n.hold, n.reached = make(chan struct{}), make(chan struct{}, 1)
		hold, reached := n.hold, n.reached
		n.mu.Unlock()
		done := make(chan result, 1)
		go func() { done <- c.do("POST", purl+"/commit", map[string]any{"version": first}) }()
		<-reached // the commit is in the middle of its push

		// Another page: stored without waiting.
		other := make(chan result, 1)
		go func() {
			other <- c.do("PUT", ourl+"/draft", map[string]any{"prev": "", "baseRev": o["rev"], "meta": map[string]any{"title": "Other"}, "body": "typed meanwhile\n"})
		}()
		select {
		case r := <-other:
			r.want(t, 200, "")
		case <-time.After(5 * time.Second):
			close(hold)
			t.Fatal("storing a draft of another page waited for the push")
		}

		// The same page: waits.
		typed := make(chan result, 1)
		go func() {
			typed <- c.do("PUT", purl+"/draft", map[string]any{"prev": first, "baseRev": p["rev"], "meta": map[string]any{"title": "Typing"}, "body": "second\n"})
		}()
		select {
		case r := <-typed:
			close(hold)
			t.Fatalf("a store for the page being committed did not wait: %s", r.raw)
		case <-time.After(300 * time.Millisecond):
		}
		close(hold)
		r := <-done
		r.want(t, 200, "")
		if r.body["body"] != "first\n" {
			t.Fatal(r.raw)
		}
		// ... and is refused: its draft was committed, and here is the file.
		tr := <-typed
		tr.want(t, 409, "conflict")
		cur, _ := tr.body["current"].(map[string]any)
		if tr.body["draft"] != nil || cur == nil || cur["body"] != "first\n" || cur["rev"] != r.body["rev"] {
			t.Fatalf("%s", tr.raw)
		}
		// The client stores again on the new rev: an ordinary, committable draft.
		again := c.do("PUT", purl+"/draft", map[string]any{"prev": "", "baseRev": cur["rev"], "meta": map[string]any{"title": "Typing"}, "body": "second\n"})
		again.want(t, 200, "")
		if again.body["stale"] != false {
			t.Fatal(again.raw)
		}
	})
}

// Changing a connected space from a token to an SSH key makes a key first and
// then connects. When that connection fails (the key is not on the repository
// yet, as it cannot be), the space must still be recorded as following its
// remote: forgotten, it would be opened as local after a restart and commit
// changes that are never pushed.
func TestAFailedChangeOfCredentialKeepsTheSpaceConnected(t *testing.T) {
	n := &netSwitch{}
	eachAccounts(t, remoteOpts(n, []string{"*"}, true), func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		rurl := "/api/spaces/" + sid + "/remote"
		url := bareRemote(t)
		c.do("PUT", rurl, connectBody(url)).want(t, 200, "")

		n.set(true) // the new address cannot be reached
		c.do("PUT", rurl, map[string]any{"url": "git@example.test:kazuha/notes.git", "branch": "main", "credential": map[string]any{"type": "ssh"}}).want(t, 503, "unavailable")
		n.set(false)

		g := c.do("GET", rurl, nil).want(t, 200, "")
		if g.body["url"] != url || g.body["credential"].(map[string]any)["type"] != "token" {
			t.Fatalf("the space forgot the remote it follows: %s", g.raw)
		}
		row, err := store.GetRemote(context.Background(), a.st.Q(), sid)
		if err != nil || !row.Connected() || row.URL != url || row.CredType != "token" || row.TokenEnc == "" {
			t.Fatalf("stored row: %+v %v", row, err)
		}
		// The key that was made is kept for the next try.
		if row.PubKey == "" || row.PrivKeyEnc == "" {
			t.Fatal("the new key was not kept")
		}
		// Changes still reach the remote.
		_, p := draftPage(t, c, "Still pushed", "x\n")
		if out := rg(t, url, "log", "-1", "--format=%s", "main"); !strings.Contains(out, "Still pushed") {
			t.Fatalf("the change was not pushed: %q (%v)", out, p["path"])
		}
		// A stored token is not carried to another host.
		c.do("PUT", rurl, map[string]any{"url": "https://other.example.test/kazuha/notes.git", "branch": "main", "credential": map[string]any{"type": "token"}}).want(t, 400, "invalid")
	})
}
