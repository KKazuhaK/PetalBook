package spaces

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// argvRunner runs the real git and remembers every command line and the logs.
type argvRunner struct {
	mu   sync.Mutex
	argv [][]string
}

func (r *argvRunner) Run(c context.Context, cmd gitstore.Cmd) (string, string, error) {
	r.mu.Lock()
	r.argv = append(r.argv, append([]string(nil), cmd.Args...))
	r.mu.Unlock()
	return gitstore.RunReal(c, cmd)
}

func (r *argvRunner) all() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.argv...)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// ghEnv is a manager of accounts mode with a configured GitHub App, the user
// U1 ("kazuha") connected as the GitHub user "tang" (a member of the
// organisation "kazuhahub"), and the fake GitHub behind it.
type ghEnv struct {
	t       *testing.T
	st      *store.Store
	f       *githubapptest.Fake
	clk     *githubapptest.Clock
	svc     *github.Service
	m       *Manager
	run     *argvRunner
	logs    *syncBuf
	dir     string
	user    store.User
	hook    func(step string)
	kazuhub int64
}

func newGHEnv(t *testing.T, st *store.Store, mod ...func(*Options)) *ghEnv {
	t.Helper()
	needGit(t)
	base, err := os.MkdirTemp("", "petalbook-github-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	clk := githubapptest.NewClock()
	f := githubapptest.New(t, clk)
	c, err := githubapp.New(githubapp.Config{API: f.URL, Web: f.URL, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	box := secret.NewBoxWithKey([32]byte{9, 9, 9})
	svc, err := github.New(github.Options{Store: st, Secrets: box, Client: c, PublicURL: "https://notes.example.com", Now: clk.Now, Logger: log, SettingsTTL: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	e := &ghEnv{t: t, st: st, f: f, clk: clk, svc: svc, run: &argvRunner{}, logs: logs, dir: filepath.Join(base, "data")}
	opt := Options{Store: st, DataDir: e.dir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour,
		Now: clk.Now, PullInterval: time.Minute, RemoteRunner: e.run, FetchFresh: time.Nanosecond, FailFast: time.Nanosecond, Logger: log, Secrets: box,
		RemotePolicy: gitstore.Policy{Hosts: []string{"*"}, AllowUnsafe: true}, Connections: connections.NewRegistry(svc, connections.NewGit()), PushRetry: []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond},
		SyncHook: func(step string) {
			if e.hook != nil {
				e.hook(step)
			}
		}}
	for _, m := range mod {
		m(&opt)
	}
	if e.m, err = NewAccounts(opt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.m.Close() })
	svc.OnChange(e.m.ConnectionFixed)
	id := f.ClientID
	if _, err := svc.Save(ctx, github.SaveRequest{AppID: "424242", PrivateKey: f.KeyPEM(), ClientID: &id, ClientSecret: f.ClientSecret}); err != nil {
		t.Fatal(err)
	}
	e.user = addUser(t, st, "U1", "kazuha")
	f.AddUser("tang")
	f.AddOrg("kazuhahub", "tang")
	e.kazuhub = f.Install("kazuhahub", "all")
	e.connect()
	return e
}

func (e *ghEnv) connect() {
	e.t.Helper()
	authURL, binding, err := e.svc.StartAuthorization(ctx, e.user.ID, "/")
	if err != nil {
		e.t.Fatal(err)
	}
	back, _ := url.Parse(e.f.Authorize(authURL, "tang"))
	if to, _ := e.svc.CompleteAuthorization(ctx, binding, e.user.ID, back.Query()); to != "/" {
		e.t.Fatalf("connecting: %s", to)
	}
}

func (e *ghEnv) create(name, owner, repo string, create bool) (Info, error) {
	return e.m.CreateWithSync(ctx, e.user.ID, name, "", SyncCreate{Provider: "github", Owner: owner, Name: repo, Private: true, Description: "Notes", Create: create})
}

func (e *ghEnv) spaceDirs() []string {
	var out []string
	ents, _ := os.ReadDir(filepath.Join(e.dir, "spaces"))
	for _, d := range ents {
		out = append(out, d.Name())
	}
	return out
}

func (e *ghEnv) rows() int {
	var n int
	if err := e.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM spaces`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// nothingLeft says no space exists anywhere on the server: not as a row, not as a directory, not in a list.
func (e *ghEnv) nothingLeft() {
	e.t.Helper()
	if d := e.spaceDirs(); len(d) != 0 {
		e.t.Fatalf("directories left behind: %v", d)
	}
	if st, _ := os.ReadDir(filepath.Join(e.dir, "cache")); len(st) != 0 {
		e.t.Fatalf("state left behind: %v", st)
	}
	if n := e.rows(); n != 0 {
		e.t.Fatalf("%d rows left behind", n)
	}
	if l, _ := e.m.List(ctx, e.user.ID); len(l) != 0 {
		e.t.Fatalf("listed: %v", l)
	}
}

func ghCode(err error) string {
	var he *connections.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func ghRepo(err error) *connections.RepoRef {
	var he *connections.Error
	if errors.As(err, &he) {
		return he.Repo
	}
	return nil
}

// noLeak looks for every token the fake handed out in the command lines Git
// was started with, in every file of every clone's .git directory (its
// configuration included), and in the log.
func (e *ghEnv) noLeak() {
	e.t.Helper()
	tokens := e.f.AllTokens()
	for _, cmd := range e.run.all() {
		line := strings.Join(cmd, " ")
		for _, tk := range tokens {
			if strings.Contains(line, tk) {
				e.t.Fatalf("a token is on a command line: %q", cmd)
			}
		}
		if strings.Contains(line, "x-access-token@") || strings.Contains(line, "://x-access-token:") {
			e.t.Fatalf("a user name and secret in an address: %q", cmd)
		}
	}
	root := filepath.Join(e.dir, "spaces")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(p, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, tk := range tokens {
			if bytes.Contains(b, []byte(tk)) {
				e.t.Fatalf("a token is stored in %s", p)
			}
		}
		return nil
	})
	if cfg, err := os.ReadFile(filepath.Join(root, e.onlySpace(), ".git", "config")); err == nil {
		if strings.Contains(string(cfg), "credential") || strings.Contains(string(cfg), "@") {
			e.t.Fatalf(".git/config holds credential settings:\n%s", cfg)
		}
	}
	logs := e.logs.String()
	for _, tk := range tokens {
		if strings.Contains(logs, tk) {
			e.t.Fatalf("a token reached the log: %q", tk)
		}
	}
	if strings.Contains(logs, e.f.ClientSecret) || strings.Contains(logs, "PRIVATE KEY") {
		e.t.Fatal("the App's secret reached the log")
	}
}

func (e *ghEnv) onlySpace() string {
	if d := e.spaceDirs(); len(d) > 0 {
		return d[0]
	}
	return "none"
}

func TestCreateASpaceWithAnOrganisationsNewRepository(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		in, err := e.create("Tang", "kazuhahub", "tang-notes", true)
		if err != nil {
			t.Fatal(err)
		}
		if in.Name != "Tang" || in.Sync != "ok" || in.Role != "owner" {
			t.Fatalf("%+v", in)
		}
		// The repository was made private, with the description, and the space's first commit is its first.
		repo := e.f.RepoByName("kazuhahub/tang-notes")
		if repo == nil || !repo.Private || repo.Desc != "Notes" {
			t.Fatalf("%+v", repo)
		}
		bare := e.f.BareDir("kazuhahub/tang-notes")
		if got := gitCmd(t, bare, "rev-list", "--count", "main"); got != "1" {
			t.Fatalf("%s commits on the repository's main", got)
		}
		// The space follows it: the organisation's repository syncs with the installation's token.
		row, err := store.GetRemote(ctx, st.Q(), in.ID)
		if err != nil || row.CredType != "connection" || row.GHVia != "app" || row.GHInstallationID != e.kazuhub || row.GHRepoID != repo.ID ||
			row.TokenEnc != "" || row.PrivKeyEnc != "" || row.URL != e.f.URL+"/kazuhahub/tang-notes.git" || row.State != "ok" || row.Branch != "main" {
			t.Fatalf("%+v %v", row, err)
		}
		if l, _ := e.m.List(ctx, e.user.ID); len(l) != 1 || l[0].Sync != "ok" {
			t.Fatalf("%+v", l)
		}
		// A change made in the space is pushed before it is acknowledged.
		h, err := e.m.Open(ctx, e.user, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Pushed"}}}); err != nil {
			t.Fatal(err)
		}
		if got := gitCmd(t, bare, "rev-list", "--count", "main"); got != "2" {
			t.Fatalf("%s commits after a change", got)
		}
		v, err := e.m.Remote(ctx, h)
		if err != nil || v.CredType != "connection" || v.Connection == nil || v.Connection.Via != "app" || v.Connection.Account != "kazuhahub" || v.Connection.RepoID != repo.ID || v.ErrorCode != "" {
			t.Fatalf("%+v %v", v, err)
		}
		// One repository made, with the caller's token; the installation's token was narrowed to it.
		if n := e.f.Count("POST", "/orgs/kazuhahub/repos"); n != 1 {
			t.Fatalf("%d repository creations", n)
		}
		e.noLeak()
	})
}

func TestCreateASpaceInTheUsersOwnAccount(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		in, err := e.create("Mine", "tang", "notes", true)
		if err != nil {
			t.Fatal(err)
		}
		row, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if row.GHVia != "user" || row.GHUserID != "U1" || row.GHInstallationID != 0 {
			t.Fatalf("%+v", row)
		}
		h, _ := e.m.Open(ctx, e.user, in.ID)
		defer h.Release()
		v, _ := e.m.Remote(ctx, h)
		if v.Connection.Via != "user" || v.Connection.Username != "kazuha" || v.Connection.Login != "tang" {
			t.Fatalf("%+v", v.Connection)
		}
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Pushed"}}}); err != nil {
			t.Fatal(err)
		}
		if got := gitCmd(t, e.f.BareDir("tang/notes"), "rev-list", "--count", "main"); got != "2" {
			t.Fatalf("%s", got)
		}
		e.noLeak()
		// Installed on the personal account later: new spaces there use the installation.
		inst := e.f.Install("tang", "all")
		in2, err := e.create("Mine 2", "tang", "notes2", true)
		if err != nil {
			t.Fatal(err)
		}
		row, _ = store.GetRemote(ctx, st.Q(), in2.ID)
		if row.GHVia != "app" || row.GHInstallationID != inst {
			t.Fatalf("%+v", row)
		}
	})
}

func TestNothingIsLeftWhenGitHubRefusesTheRepository(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.AddRepo("kazuhahub", "taken", true)
		_, err := e.create("A", "kazuhahub", "taken", true)
		if ghCode(err) != "name_taken" || ghRepo(err) != nil {
			t.Fatalf("%v", err)
		}
		e.nothingLeft()
		e.f.SetAccount("kazuhahub", func(a *githubapptest.Account) { a.NoCreate = true })
		_, err = e.create("A", "kazuhahub", "fresh", true)
		if ghCode(err) != "provider_refused" || ghRepo(err) != nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("%v", err)
		}
		e.nothingLeft()
		// Not an owner of the caller's.
		if _, err = e.create("A", "someone-else", "fresh", true); ghCode(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		// A bad name never reaches GitHub.
		before := len(e.f.Requests())
		for _, name := range []string{"", "a b", "x.git", "..", strings.Repeat("a", 101)} {
			if _, err = e.create("A", "tang", name, true); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%q: %v", name, err)
			}
		}
		if _, err = e.m.CreateWithSync(ctx, e.user.ID, "A", "", SyncCreate{Provider: "github", Owner: "tang", Name: "ok", Description: "bad\x01", Create: true}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v", err)
		}
		if len(e.f.Requests()) != before {
			t.Fatal("GitHub was asked about a name that was refused locally")
		}
		e.nothingLeft()
		// Without an App there is nothing to do, and no directory is made.
		if err := e.svc.Delete(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = e.create("A", "tang", "x", true); ghCode(err) != "provider_not_configured" {
			t.Fatalf("%v", err)
		}
		e.nothingLeft()
	})
}

func TestARepositoryThatIsNotFoundForAFewSeconds(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.HiddenGit = 2 // the first two tries with credentials find nothing
		in, err := e.create("Slow", "kazuhahub", "slow", true)
		if err != nil {
			t.Fatal(err)
		}
		if e.f.HiddenServed() != 2 {
			t.Fatalf("%d not-found answers", e.f.HiddenServed())
		}
		if got := gitCmd(t, e.f.BareDir("kazuhahub/slow"), "rev-list", "--count", "main"); got != "1" {
			t.Fatal(got)
		}
		if in.Sync != "ok" {
			t.Fatalf("%+v", in)
		}
		e.noLeak()
	})
}

func TestAnEmptyRepositoryIsLeftAndNamedWhenThePushNeverWorks(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.HiddenGit = 100 // more than the four tries
		_, err := e.create("Slow", "kazuhahub", "slow", true)
		repo := ghRepo(err)
		if ghCode(err) != "push_failed" || repo == nil || repo.FullName != "kazuhahub/slow" || repo.ID == 0 || repo.HTMLURL == "" {
			t.Fatalf("%v %+v", err, repo)
		}
		var he *connections.Error
		if !errors.As(err, &he) || he.Status != 502 {
			t.Fatalf("%v", err)
		}
		// Four tries, not more.
		if n := e.f.HiddenServed(); n != 4 {
			t.Fatalf("%d tries", n)
		}
		e.nothingLeft()
		// The repository stays, empty, and nothing was deleted.
		if e.f.RepoByName("kazuhahub/slow") == nil || e.f.Count("DELETE", "/repos") != 0 {
			t.Fatal("the repository is gone")
		}
		if out := gitCmd(t, e.f.BareDir("kazuhahub/slow"), "rev-parse", "--verify", "-q", "main"); out != "" {
			t.Fatalf("the repository has a commit: %q", out)
		}
		// Once GitHub finds it, the same request with create: false connects a new space to it.
		e.f.HideRepo("kazuhahub/slow", 0)
		in, err := e.create("Slow", "kazuhahub", "slow", false)
		if err != nil {
			t.Fatal(err)
		}
		if in.Sync != "ok" || e.f.Count("POST", "/orgs/kazuhahub/repos") != 1 {
			t.Fatalf("%+v", in)
		}
		if got := gitCmd(t, e.f.BareDir("kazuhahub/slow"), "rev-list", "--count", "main"); got != "1" {
			t.Fatal(got)
		}
		e.noLeak()
	})
}

func TestARefusedCredentialIsNotRetried(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.RejectGit = true
		before := e.f.Count("GET", "/kazuhahub/refused.git")
		_, err := e.create("R", "kazuhahub", "refused", true)
		if ghCode(err) != "push_failed" || ghRepo(err) == nil {
			t.Fatalf("%v", err)
		}
		// One try: one request without credentials, one with.
		if n := e.f.Count("GET", "/kazuhahub/refused.git") - before; n != 2 {
			t.Fatalf("%d requests: the push was tried again", n)
		}
		e.nothingLeft()
	})
}

func TestSelectedRepositoriesInstallation(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.AddOrg("sel", "tang")
		sel := e.f.Install("sel", "selected")
		in, err := e.create("Sel", "sel", "notes", true)
		if err != nil {
			t.Fatal(err)
		}
		row, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if row.GHVia != "app" || row.GHInstallationID != sel {
			t.Fatalf("%+v", row)
		}
		// Where the repository cannot be added: a 409 that names it, and no space.
		e.f.SetInstall(sel, func(i *githubapptest.Install) { i.NoAdd = true })
		_, err = e.create("Sel2", "sel", "stuck", true)
		repo := ghRepo(err)
		if ghCode(err) != "repo_not_in_installation" || repo == nil || repo.FullName != "sel/stuck" {
			t.Fatalf("%v", err)
		}
		if l, _ := e.m.List(ctx, e.user.ID); len(l) != 1 {
			t.Fatalf("%+v", l)
		}
		if e.rows() != 1 || len(e.spaceDirs()) != 1 {
			t.Fatalf("%d rows, %v", e.rows(), e.spaceDirs())
		}
		// The administrator adds it, and the retry works.
		e.f.SetInstall(sel, func(i *githubapptest.Install) { i.NoAdd = false })
		if _, err = e.create("Sel2", "sel", "stuck", false); err != nil {
			t.Fatal(err)
		}
		e.noLeak()
	})
}

func TestAFailureAfterTheRepositoryLeavesNoSpace(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		// The record of the space cannot be written.
		e.hook = func(step string) {
			if step == "connected" {
				if _, err := st.Q().Exec(ctx, `ALTER TABLE remotes RENAME TO remotes_away`); err != nil {
					t.Error(err)
				}
			}
		}
		_, err := e.create("Late", "kazuhahub", "late", true)
		repo := ghRepo(err)
		var he *connections.Error
		if !errors.As(err, &he) || he.Code != "internal" || he.Status != 500 || repo == nil || repo.FullName != "kazuhahub/late" {
			t.Fatalf("%v", err)
		}
		e.hook = nil
		if _, err := st.Q().Exec(ctx, `ALTER TABLE remotes_away RENAME TO remotes`); err != nil {
			t.Fatal(err)
		}
		e.nothingLeft()
		// The repository has the first commit by now: a new space takes its content.
		in, err := e.create("Late", "kazuhahub", "late", false)
		if err != nil || in.Sync != "ok" {
			t.Fatalf("%+v %v", in, err)
		}
	})
}

func TestTheLimitCountsSpacesBeingMade(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st, func(o *Options) { o.MaxSpaces = 1 })
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.create("S"+string(rune('A'+i)), "kazuhahub", "r"+string(rune('a'+i)), true)
			}()
		}
		wg.Wait()
		ok, limited := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrLimit):
				limited++
			default:
				t.Fatal(err)
			}
		}
		if ok != 1 || limited != 1 {
			t.Fatalf("%d made, %d refused: %v", ok, limited, errs)
		}
		// Refused before anything was asked of GitHub, or made: one repository exists.
		if e.rows() != 1 {
			t.Fatalf("%d rows", e.rows())
		}
	})
}

// A site administrator connecting another tenant's space through the App is
// judged by the space's tenant: the share must reach it, whichever tenant the
// administrator belongs to.
func TestConnectingAnotherTenantsSpaceChecksTheSpacesTenant(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.AddRepo("kazuhahub", "wiki", true)
		now := time.Now()
		const tenB = "T000000002"
		if err := st.CreateTenant(ctx, store.Tenant{ID: tenB, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		owner := store.User{ID: "UB", Username: "bee", Name: "bee", Role: "member", Created: now, TenantID: tenB}
		if err := store.InsertUser(ctx, st.Q(), owner); err != nil {
			t.Fatal(err)
		}
		admin := store.User{ID: "UA", Username: "root", Name: "root", Role: "admin", Created: now, TenantID: store.DefaultTenantID}
		if err := store.InsertUser(ctx, st.Q(), admin); err != nil {
			t.Fatal(err)
		}
		in, err := e.m.Create(ctx, owner.ID, "Acme wiki")
		if err != nil {
			t.Fatal(err)
		}
		h, err := e.m.Open(ctx, owner, in.ID) // the handle is the space's; the request names the administrator
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		req := ConnectRequest{URL: e.f.URL + "/kazuhahub/wiki.git", CredType: CredConnection, Provider: "github", Via: "app", UserID: admin.ID}
		// Shared with the administrator's tenant only: the space's tenant has no right to it.
		if _, err := e.svc.SetTenants(ctx, e.kazuhub, []string{store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.Connect(ctx, h, req); err == nil {
			t.Fatal("the share of the administrator's tenant was used for another tenant's space")
		}
		// Shared with the space's tenant: allowed, although the administrator's own tenant has no share.
		if _, err := e.svc.SetTenants(ctx, e.kazuhub, []string{tenB}); err != nil {
			t.Fatal(err)
		}
		if v, err := e.m.Connect(ctx, h, req); err != nil || v.Connection == nil || v.Connection.Via != "app" {
			t.Fatalf("%+v %v", v, err)
		}
	})
}

func TestSwitchingASpaceToTheApp(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		// A space that follows a repository with a token, as before.
		repo := e.f.AddRepo("kazuhahub", "wiki", true)
		in, err := e.m.Create(ctx, e.user.ID, "Wiki")
		if err != nil {
			t.Fatal(err)
		}
		h, _ := e.m.Open(ctx, e.user, in.ID)
		defer h.Release()
		const tok = "ghp_ClassicToken0123456789abcdef"
		pushTok := e.f.ClientSecret // any string the fake would accept is not available: use a user token
		_ = pushTok
		userTok, err := e.svc.UserToken(ctx, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		v, err := e.m.Connect(ctx, h, ConnectRequest{URL: e.f.URL + "/kazuhahub/wiki.git", Branch: "main", CredType: CredToken, Token: userTok})
		if err != nil {
			t.Fatal(err)
		}
		if v.CredType != CredToken {
			t.Fatalf("%+v", v)
		}
		_ = tok
		before, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if before.TokenEnc == "" {
			t.Fatal("no token stored")
		}
		// The same repository, now through the App: the address and credential are replaced in place.
		v, err = e.m.Connect(ctx, h, ConnectRequest{URL: "git@" + strings.Split(strings.TrimPrefix(e.f.URL, "http://"), ":")[0] + ":kazuhahub/wiki.git", CredType: CredConnection, Provider: "github", Via: "app", UserID: e.user.ID})
		if err != nil {
			t.Fatal(err)
		}
		if v.CredType != CredConnection || v.Connection == nil || v.Connection.Via != "app" || v.URL != e.f.URL+"/kazuhahub/wiki.git" || v.State != "ok" {
			t.Fatalf("%+v", v)
		}
		row, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if row.TokenEnc != "" || row.Username != "" || row.GHRepoID != repo.ID || row.GHInstallationID != e.kazuhub {
			t.Fatalf("the old token was kept: %+v", row)
		}
		// It syncs with the installation's token now.
		h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Through the App"}}})
		if got := gitCmd(t, e.f.BareDir("kazuhahub/wiki"), "rev-list", "--count", "main"); got < "2" {
			t.Fatalf("%s", got)
		}
		if e.f.Minted() == 0 {
			t.Fatal("no installation token was used")
		}
		// Back to a token, and to the App again, through the same call.
		if v, err = e.m.Connect(ctx, h, ConnectRequest{URL: e.f.URL + "/kazuhahub/wiki.git", Branch: "main", CredType: CredToken, Token: userTok}); err != nil || v.CredType != CredToken {
			t.Fatalf("%+v %v", v, err)
		}
		row, _ = store.GetRemote(ctx, st.Q(), in.ID)
		if row.GHVia != "" || row.GHInstallationID != 0 || row.GHRepoID != 0 {
			t.Fatalf("%+v", row)
		}
		// A repository outside GitHub, or one the App does not reach, is refused.
		if _, err = e.m.Connect(ctx, h, ConnectRequest{URL: "https://example.com/a/b.git", CredType: CredConnection, Provider: "github", Via: "app", UserID: e.user.ID}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v", err)
		}
		e.f.AddOrg("none")
		e.f.AddRepo("none", "x", true)
		if _, err = e.m.Connect(ctx, h, ConnectRequest{URL: e.f.URL + "/none/x.git", CredType: CredConnection, Provider: "github", Via: "app", UserID: e.user.ID}); ghCode(err) != "repo_not_in_installation" {
			t.Fatalf("%v", err)
		}
		if _, err = e.m.Connect(ctx, h, ConnectRequest{URL: e.f.URL + "/kazuhahub/wiki.git", CredType: CredConnection, Provider: "github", Via: "both", UserID: e.user.ID}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v", err)
		}
		e.noLeak()
	})
}

func TestAnExpiredAuthorizationPutsTheSpaceInErrorAndKeepsEverything(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		in, err := e.create("Mine", "tang", "notes", true)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := e.m.Open(ctx, e.user, in.ID)
		defer h.Release()
		keep, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Before"}}})
		if err != nil {
			t.Fatal(err)
		}
		// Eight hours on, with the refresh token revoked.
		e.f.ExpireRefresh("tang")
		e.clk.Advance(9 * time.Hour)
		var ue *space.UnavailableError
		if err := h.Space.Pull(ctx, true); !errors.As(err, &ue) {
			t.Fatalf("%v", err)
		}
		v, _ := e.m.Remote(ctx, h)
		if v.State != "error" || v.ErrorCode != "connection_expired" || v.Error == "" {
			t.Fatalf("%+v", v)
		}
		if l, _ := e.m.List(ctx, e.user.ID); l[0].Sync != "error" {
			t.Fatalf("%+v", l)
		}
		// Reads go on, writes are refused and change nothing.
		if _, err := h.Space.Get(keep.ID); err != nil {
			t.Fatalf("a read failed: %v", err)
		}
		headBefore := gitCmd(t, e.f.BareDir("tang/notes"), "rev-parse", "main")
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "During"}}}); !errors.As(err, &ue) {
			t.Fatalf("a write was not refused: %v", err)
		}
		if _, err := h.Space.Save(space.Author{}, keep.ID, keep.Rev, page.Meta{{Key: "title", Value: "Edited"}}, "x"); !errors.As(err, &ue) {
			t.Fatalf("a save was not refused: %v", err)
		}
		if got, _ := h.Space.Get(keep.ID); got.Meta.String("title") != "Before" {
			t.Fatalf("a refused write changed the page: %+v", got.Meta)
		}
		if gitCmd(t, e.f.BareDir("tang/notes"), "rev-parse", "main") != headBefore {
			t.Fatal("the remote moved")
		}
		// Connecting again fixes it at once: the space is fetched without waiting.
		e.connect()
		waitFor(t, func() bool { v, _ := e.m.Remote(ctx, h); return v.State == "ok" && v.ErrorCode == "" })
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "After"}}}); err != nil {
			t.Fatalf("a write after reconnecting: %v", err)
		}
		e.noLeak()
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("it did not happen")
}

func TestASpaceWhoseCredentialIsMissingIsTriedAgainEveryTenMinutes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st, func(o *Options) { o.ConnectionRetry = 10 * time.Minute })
		in, err := e.create("Mine", "tang", "notes", true)
		if err != nil {
			t.Fatal(err)
		}
		h, _ := e.m.Open(ctx, e.user, in.ID)
		defer h.Release()
		if err := e.svc.Disconnect(ctx, e.user.ID); err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(9 * time.Hour)
		h.Space.Pull(ctx, true)
		v, _ := e.m.Remote(ctx, h)
		if v.State != "error" || v.ErrorCode != "connection_missing" {
			t.Fatalf("%+v", v)
		}
		// The puller looked at it once more and then waits ten minutes (jittered), not one.
		e.m.pullDue(ctx)
		e.clk.Advance(time.Minute + 30*time.Second)
		e.m.pullDue(ctx)
		if d := e.m.nextPullOf(in.ID).Sub(e.clk.Now()); d < 6*time.Minute || d > 13*time.Minute {
			t.Fatalf("the next try is in %v", d)
		}
	})
}

// nextPullOf is when the puller looks at an open space next.
func (m *Manager) nextPullOf(id string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.open[id]; e != nil {
		return e.nextPull
	}
	return time.Time{}
}

func newGit(dir string, args ...string) *exec.Cmd {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	return c
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGitQuiet(dir, args...)
}

func runGitQuiet(dir string, args ...string) string {
	cmd := newGit(dir, args...)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// A github credential is held to --remote-host like any other remote: when the
// GitHub host is not allowed, neither connecting nor creating gets past it, and
// GitHub is not asked.
func TestAGitHubCredentialObeysTheRemoteHostPolicy(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st, func(o *Options) { o.RemotePolicy = gitstore.Policy{Hosts: []string{"gitlab.com"}, AllowUnsafe: true} })
		e.f.AddRepo("kazuhahub", "wiki", true)
		before := len(e.f.Requests())
		var he *gitstore.HostError
		if _, err := e.create("Notes", "kazuhahub", "notes", true); !errors.As(err, &he) {
			t.Fatalf("creating: %v", err)
		}
		e.nothingLeft()
		in, err := e.m.Create(ctx, e.user.ID, "Wiki")
		if err != nil {
			t.Fatal(err)
		}
		h, _ := e.m.Open(ctx, e.user, in.ID)
		defer h.Release()
		if _, err := e.m.Connect(ctx, h, ConnectRequest{URL: e.f.URL + "/kazuhahub/wiki.git", CredType: CredConnection, Provider: "github", Via: "app", UserID: e.user.ID}); !errors.As(err, &he) {
			t.Fatalf("connecting: %v", err)
		}
		if n := len(e.f.Requests()) - before; n != 0 {
			t.Fatalf("GitHub was asked %d times", n)
		}
	})
}

// hangingPush is a runner whose pushes hang until their context ends.
type hangingPush struct{ argvRunner }

func (r *hangingPush) Run(c context.Context, cmd gitstore.Cmd) (string, string, error) {
	for _, a := range cmd.Args {
		if a == "push" {
			<-c.Done()
			return "", "", c.Err()
		}
	}
	return r.argvRunner.Run(c, cmd)
}

// firstPushWithin makes a space with a new repository and fails the test when
// it takes much longer than a 300 ms deadline.
func firstPushWithin(t *testing.T, e *ghEnv, repo string) error {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := e.create("D", "kazuhahub", repo, true); done <- err }()
	select {
	case err := <-done:
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %v", d)
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the first push went past its deadline")
		return nil
	}
}

// The first push keeps to one overall deadline: the waits between tries count
// against it ...
func TestTheFirstPushWaitsKeepToItsDeadline(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st, func(o *Options) {
			o.PushRetry = []time.Duration{time.Second, 3 * time.Second, 7 * time.Second}
			o.PushDeadline = 300 * time.Millisecond
		})
		e.f.HiddenGit = 100
		if err := firstPushWithin(t, e, "slow"); ghCode(err) != "push_failed" || ghRepo(err) == nil {
			t.Fatalf("%v", err)
		}
		e.nothingLeft()
	})
}

// ... and so does a push that hangs.
func TestAHangingFirstPushKeepsToItsDeadline(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st, func(o *Options) {
			o.RemoteRunner = &hangingPush{}
			o.PushDeadline = 300 * time.Millisecond
		})
		if err := firstPushWithin(t, e, "hang"); ghCode(err) != "push_failed" || ghRepo(err) == nil {
			t.Fatalf("%v", err)
		}
		e.nothingLeft()
	})
}

// What is tried again: only the brand-new repository's "not found", GitHub's
// own failures and an unreachable GitHub; never another refusal, nor a
// credential that could not be had (even one whose message says "not found").
func TestWhatTheFirstPushTriesAgain(t *testing.T) {
	t.Parallel()
	git := func(stderr string) error {
		return &space.UnavailableError{Msg: "x", Err: &gitstore.GitError{Stderr: stderr}}
	}
	for _, c := range []struct {
		err   error
		retry bool
	}{
		{git("remote: Repository not found.\nfatal: repository 'https://github.com/o/n.git/' not found"), true},
		{git("fatal: unable to access 'https://github.com/o/n.git/': The requested URL returned error: 502"), true},
		{git("fatal: Authentication failed for 'https://github.com/o/n.git/'"), false},
		{git("fatal: unable to access 'https://github.com/o/n.git/': The requested URL returned error: 403"), false},
		{&space.UnavailableError{Msg: "the installation was not found", Err: &gitstore.CredentialError{Code: "installation_missing", Msg: "the installation was not found"}}, false},
		{errors.New("not found"), false},
	} {
		if got := retryable(c.err); got != c.retry {
			t.Errorf("%v: retryable %v, want %v", c.err, got, c.retry)
		}
	}
}

// Milestone 3.1, part 2: a space made on GitHub can get its attachment
// repository in the same request, by the same path and credential.
func TestCreateASpaceWithAnAttachmentRepositoryOnGitHub(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		in, err := e.m.CreateWithSync(ctx, e.user.ID, "Tang", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "tang-notes", Private: true, Create: true, Assets: &SyncAssets{}})
		if err != nil {
			t.Fatal(err)
		}
		if in.Assets != "repository" {
			t.Fatalf("%+v", in)
		}
		ar := e.f.RepoByName("kazuhahub/tang-notes-assets")
		if ar == nil || !ar.Private {
			t.Fatalf("%+v", ar)
		}
		abare := e.f.BareDir("kazuhahub/tang-notes-assets")
		gitCmd(t, abare, "config", "uploadpack.allowFilter", "true")
		gitCmd(t, abare, "config", "uploadpack.allowAnySHA1InWant", "true")
		row, err := store.GetAssetRemote(ctx, st.Q(), in.ID)
		if err != nil || row.CredType != "connection" || row.Provider != "github" || row.GHVia != "app" || row.GHInstallationID != e.kazuhub || row.GHRepoID != ar.ID ||
			row.TokenEnc != "" || row.URL != e.f.URL+"/kazuhahub/tang-notes-assets.git" {
			t.Fatalf("%+v %v", row, err)
		}
		h, err := e.m.Open(ctx, e.user, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		p, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "P"}}})
		if err != nil {
			t.Fatal(err)
		}
		as, err := h.Space.PutAsset(space.Author{}, p.ID, "a.png", []byte("PNG"))
		if err != nil {
			t.Fatal(err)
		}
		if got := gitCmd(t, abare, "ls-tree", "-r", "--name-only", "main"); got != p.ID+"/"+as.Name {
			t.Fatalf("the attachment repository has %q", got)
		}
		if strings.Contains(gitCmd(t, e.f.BareDir("kazuhahub/tang-notes"), "ls-tree", "-r", "--name-only", "main"), "assets/") {
			t.Fatal("the space's repository has the attachment")
		}
		// After the cache loses it, it comes back by its id through the same credential.
		e.m.AssetCache().DropSpace(in.ID)
		if body, _, err := readAsset(t, h.Space, as.Rel); err != nil || body != "PNG" {
			t.Fatalf("%q %v", body, err)
		}
		e.noLeak()
	})
}

// A request that does not say gets an attachment repository: separate is the default.
func TestAttachmentsGoToTheirOwnRepositoryByDefault(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		in, err := e.m.CreateWithSync(ctx, e.user.ID, "A", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "a", Private: true, Create: true, Assets: &SyncAssets{Default: true}})
		if err != nil || in.Assets != "repository" || e.f.RepoByName("kazuhahub/a-assets") == nil {
			t.Fatalf("%+v %v", in, err)
		}
		// A request that says false makes none.
		in, err = e.m.CreateWithSync(ctx, e.user.ID, "B", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "b", Private: true, Create: true})
		if err != nil || in.Assets == "repository" || e.f.RepoByName("kazuhahub/b-assets") != nil {
			t.Fatalf("%+v %v", in, err)
		}
		if _, _, err := e.m.SetStorage(ctx, func(s *StorageSettings) error { s.Cache.AssetsBytes = 1 << 20; return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a cache under 64 MiB: %v", err)
		}
	})
}

// Where a second repository cannot be had, the default falls back to attachments
// inside the space, with no error; only a request that asks for one is refused.
func TestTheDefaultFallsBackToTheSpaceWhereASecondRepositoryIsImpossible(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		// Connecting to an existing repository that has no <name>-assets beside it.
		e.f.AddRepo("kazuhahub", "plain", true)
		in, err := e.m.CreateWithSync(ctx, e.user.ID, "Plain", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "plain", Private: true, Create: false, Assets: &SyncAssets{Default: true}})
		if err != nil || in.Assets == "repository" {
			t.Fatalf("%+v %v", in, err)
		}
		// The name of the attachment repository is taken.
		e.f.AddRepo("kazuhahub", "taken-assets", true)
		in, err = e.m.CreateWithSync(ctx, e.user.ID, "Taken", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "taken", Private: true, Create: true, Assets: &SyncAssets{Default: true}})
		if err != nil || in.Assets == "repository" {
			t.Fatalf("%+v %v", in, err)
		}
		// The same request asking for one is an error, as before.
		e.f.AddRepo("kazuhahub", "plain2", true)
		if _, err := e.m.CreateWithSync(ctx, e.user.ID, "Plain2", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "plain2", Private: true, Create: false, Assets: &SyncAssets{}}); err == nil {
			t.Fatal("a requested attachment repository that cannot be had was passed over")
		}
	})
}

// Under a lock to the separate repository there is no falling back: the space is not made without it.
func TestALockToTheRepositoryIsNotFallenBackFrom(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.m.SetAssetsPolicy(func(context.Context, string) (AssetsPolicy, error) {
			return AssetsPolicy{Default: "repository", Allowed: []string{"repository"}, Locked: true}, nil
		})
		e.f.AddRepo("kazuhahub", "plain", true) // no plain-assets beside it
		if _, err := e.m.CreateWithSync(ctx, e.user.ID, "Plain", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "plain", Private: true, Create: false, Assets: &SyncAssets{Default: true}}); err == nil {
			t.Fatal("the lock was passed over")
		}
		e.f.AddRepo("kazuhahub", "taken-assets", true)
		if _, err := e.m.CreateWithSync(ctx, e.user.ID, "Taken", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "taken", Private: true, Create: true, Assets: &SyncAssets{Default: true}}); err == nil {
			t.Fatal("the lock was passed over")
		}
		if l, _ := e.m.List(ctx, e.user.ID); len(l) != 0 {
			t.Fatalf("a space was left: %+v", l)
		}
	})
}

func TestTheSettingsDecideWhatARequestThatDoesNotSayGets(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		var got AssetsPolicy
		e.m.SetAssetsPolicy(func(_ context.Context, tenant string) (AssetsPolicy, error) { return got, nil })
		create := func(name string, a *SyncAssets) (Info, error) {
			return e.m.CreateWithSync(ctx, e.user.ID, name, "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: strings.ToLower(name), Private: true, Create: true, Assets: a})
		}
		// A default of "space" gives a request that does not say none.
		got = AssetsPolicy{Default: "space", Allowed: []string{"space", "repository"}}
		in, err := create("A", &SyncAssets{Default: true})
		if err != nil || in.Assets == "repository" || e.f.RepoByName("kazuhahub/a-assets") != nil {
			t.Fatalf("%+v %v", in, err)
		}
		// The owner may still ask for one (and for none).
		if in, err = create("B", &SyncAssets{}); err != nil || in.Assets != "repository" {
			t.Fatalf("%+v %v", in, err)
		}
		// A lock to the repository refuses a request for none, and a default makes it.
		got = AssetsPolicy{Default: "repository", Allowed: []string{"repository"}, Locked: true}
		_, err = create("C", nil)
		var se *settings.Error
		if !errors.As(err, &se) || se.Code != settings.CodeLocked || e.f.RepoByName("kazuhahub/c") != nil {
			t.Fatalf("%v", err)
		}
		if in, err = create("D", &SyncAssets{Default: true}); err != nil || in.Assets != "repository" {
			t.Fatalf("%+v %v", in, err)
		}
		// A lock to the space refuses a request for a repository; a restriction names what is left.
		got = AssetsPolicy{Default: "space", Allowed: []string{"space"}, Locked: true}
		if _, err = create("E", &SyncAssets{Name: "e-notes"}); !errors.As(err, &se) || se.Code != settings.CodeLocked || e.f.RepoByName("kazuhahub/e") != nil {
			t.Fatalf("%v", err)
		}
		got = AssetsPolicy{Default: "space", Allowed: []string{"space"}}
		if _, err = create("F", &SyncAssets{}); !errors.As(err, &se) || se.Code != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
		// Nothing is left behind by a refusal.
		if l, _ := e.m.List(ctx, e.user.ID); len(l) != 3 {
			t.Fatalf("%+v", l)
		}
	})
}

func TestBothRepositoriesAreNamedWhenTheSelectedInstallationCannotTakeTheSecond(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.f.AddOrg("sel", "tang")
		sel := e.f.Install("sel", "selected")
		e.f.SetInstall(sel, func(i *githubapptest.Install) { i.NoAdd = true })
		_, err := e.m.CreateWithSync(ctx, e.user.ID, "S", "", SyncCreate{Provider: "github", Owner: "sel", Name: "notes", Private: true, Create: true, Assets: &SyncAssets{}})
		var he *connections.Error
		if !errors.As(err, &he) || he.Code != connections.CodeRepoNotInInstall || he.Repo == nil && he.AssetsRepo == nil {
			t.Fatalf("%v", err)
		}
		if l, _ := e.m.List(ctx, e.user.ID); len(l) != 0 {
			t.Fatalf("a space was left: %+v", l)
		}
		// The retry with create false uses what was made.
		e.f.SetInstall(sel, func(i *githubapptest.Install) { i.NoAdd = false })
		in, err := e.m.CreateWithSync(ctx, e.user.ID, "S", "", SyncCreate{Provider: "github", Owner: "sel", Name: "notes", Private: true, Create: false, Assets: &SyncAssets{}})
		if err != nil || in.Assets != "repository" {
			t.Fatalf("%+v %v", in, err)
		}
	})
}

// GitHub refuses to make the attachment repository after the space's was made:
// the answer keeps GitHub's own error (and its wait), names the space's
// repository, and the retry with create false says which repository is missing.
func TestAFailureToMakeTheAttachmentRepositoryKeepsGitHubsError(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		e.hook = func(step string) {
			if step == "repo-created" {
				e.f.LimitUntil(e.clk.Now().Add(time.Minute))
			}
		}
		_, err := e.m.CreateWithSync(ctx, e.user.ID, "Tang", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "tang-notes", Private: true, Create: true, Assets: &SyncAssets{}})
		var he *connections.Error
		if !errors.As(err, &he) || he.Code != connections.CodeRateLimited || he.RetryAfter <= 0 || he.Repo == nil || he.Repo.FullName != "kazuhahub/tang-notes" || he.AssetsRepo != nil {
			t.Fatalf("%+v", err)
		}
		e.hook = nil
		e.f.LimitUntil(time.Time{})
		_, err = e.m.CreateWithSync(ctx, e.user.ID, "Tang", "", SyncCreate{Provider: "github", Owner: "kazuhahub", Name: "tang-notes", Private: true, Create: false, Assets: &SyncAssets{}})
		if !errors.As(err, &he) || !strings.Contains(he.Msg, "kazuhahub/tang-notes-assets") || he.Repo == nil || he.Repo.FullName != "kazuhahub/tang-notes" {
			t.Fatalf("the retry does not say which repository is missing: %+v", err)
		}
		e.nothingLeft()
	})
}

// A repository left empty by a failure after it was made is remembered for the
// person, until a space connects to it.
func TestAFailureAfterTheRepositoryWasMadeLeavesAReminder(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		leftovers := func() []store.GitHubLeftover {
			t.Helper()
			l, err := store.ListGitHubLeftovers(ctx, st.Q(), e.user.ID)
			if err != nil {
				t.Fatal(err)
			}
			return l
		}
		// Failures before the repository exists leave nothing to remember.
		e.f.AddRepo("kazuhahub", "taken", false)
		if _, err := e.create("T", "kazuhahub", "taken", true); ghCode(err) != "name_taken" {
			t.Fatalf("%v", err)
		}
		if _, err := e.create("T", "nobody-here", "x", true); ghCode(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		if l := leftovers(); len(l) != 0 {
			t.Fatalf("%+v", l)
		}
		// The push never works: the repository stays, and so does a reminder of it.
		e.f.HiddenGit = 100
		_, err := e.create("Slow notes", "kazuhahub", "slow", true)
		repo := ghRepo(err)
		if ghCode(err) != "push_failed" || repo == nil {
			t.Fatalf("%v", err)
		}
		l := leftovers()
		if len(l) != 1 || l[0].RepoID != repo.ID || l[0].FullName != "kazuhahub/slow" || l[0].HTMLURL != repo.HTMLURL || l[0].SpaceName != "Slow notes" || l[0].Created.IsZero() {
			t.Fatalf("%+v (repository %+v)", l, repo)
		}
		// Trying again with create: false and failing again does not add or change it.
		if _, err := e.create("Slow notes", "kazuhahub", "slow", false); err == nil {
			t.Fatal("the push should still fail")
		}
		if l2 := leftovers(); len(l2) != 1 || !l2[0].Created.Equal(l[0].Created) {
			t.Fatalf("%+v", l2)
		}
		// Another failure on another new repository adds its own.
		if _, err := e.create("Other", "kazuhahub", "other", true); ghCode(err) != "push_failed" {
			t.Fatalf("%v", err)
		}
		if n := len(leftovers()); n != 2 {
			t.Fatalf("%d reminders", n)
		}
		// A space connecting to the repository clears its reminder, and only that one.
		e.f.HideRepo("kazuhahub/slow", 0)
		if _, err := e.create("Slow notes", "kazuhahub", "slow", false); err != nil {
			t.Fatal(err)
		}
		if l = leftovers(); len(l) != 1 || l[0].FullName != "kazuhahub/other" {
			t.Fatalf("%+v", l)
		}
	})
}

// 2.7b with 3.1: remotes.require names a provider; only a connection through
// that provider meets it, and the provider must be set up to be required.
func TestARequirementOfAProviderTakesOnlyAConnectionThroughIt(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		r := "github"
		if _, _, err := e.m.SetStorage(ctx, func(s *StorageSettings) error { s.Remotes.Require = &r; return nil }); err != nil {
			t.Fatal(err)
		}
		var re *RequiredError
		if _, err := e.m.Create(ctx, e.user.ID, "Local"); !errors.As(err, &re) || re.Require != "github" {
			t.Fatalf("a local space under a requirement of github: %v", err)
		}
		if _, err := e.m.CreateWithRemote(ctx, e.user.ID, "Plain", "", ConnectRequest{URL: "https://example.test/x.git", CredType: CredToken, Token: "t"}); !errors.As(err, &re) {
			t.Fatalf("a plain address under a requirement of github: %v", err)
		}
		in, err := e.create("Synced", "kazuhahub", "synced", true)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := e.m.Notice(ctx, in.ID); err != nil || n != nil {
			t.Fatalf("a space synced through github is asked for more: %+v %v", n, err)
		}
		// A provider the server does not have is not a value of the setting.
		other := "gitlab"
		if _, _, err := e.m.SetStorage(ctx, func(s *StorageSettings) error { s.Remotes.Require = &other; return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v", err)
		}
	})
}

func TestAHostsGitUserNameIsKnownBeforeItsProviderIsLoaded(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newGHEnv(t, st)
		now := e.clk.Now()
		for _, typ := range []string{"gitlab", "gitea"} {
			row := store.ConnProviderRow{ID: "h-" + typ, Type: typ, Host: typ + ".example.com", Scheme: "https", Name: typ, Owner: store.ConnProviderSite,
				ClientID: "c", SecretEnc: "s", State: "ok", PartialSync: "unknown", Created: now, Updated: now}
			if err := store.InsertConnProviderRow(ctx, st.Q(), row); err != nil {
				t.Fatal(err)
			}
			// No provider of this id is in the registry (a restart that has not loaded it yet).
			cred := e.m.credential("S1", &store.Remote{SpaceID: "S1", CredType: CredConnection, Provider: row.ID, URL: "https://" + row.Host + "/a/b.git"})
			tok, ok := cred.(gitstore.Token)
			if !ok || tok.Username != "oauth2" {
				t.Fatalf("%s: %+v", typ, cred)
			}
			if _, err := tok.Get(); err == nil {
				t.Fatalf("%s: a token without a provider", typ)
			}
		}
		// GitHub keeps its own name.
		cred := e.m.credential("S1", &store.Remote{SpaceID: "S1", CredType: CredConnection, Provider: "github", URL: "https://github.com/a/b.git"})
		if tok, ok := cred.(gitstore.Token); !ok || tok.Username != "x-access-token" {
			t.Fatalf("%+v", cred)
		}
	})
}
