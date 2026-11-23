package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func init() {
	passwordParams = auth.PasswordParams{MemoryKiB: 64, Time: 1, Threads: 1}
}

// syncBuffer is a log destination safe to read while the server writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func runIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// tempDir is a directory without "#" in its name (t.TempDir() puts the test name in it).
func tempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "petalbook-cmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

type running struct {
	base string
	log  *syncBuffer
	stop func() error
}

// start runs the server until the test ends or stop is called.
func start(t *testing.T, c serveConfig) *running {
	t.Helper()
	needGit(t)
	ctx, cancel := context.WithCancel(context.Background())
	log := &syncBuffer{}
	c.logger = slog.New(slog.NewTextHandler(log, nil))
	if c.listen == "" {
		c.listen = "127.0.0.1:0"
	}
	ready := make(chan net.Addr, 1)
	c.onReady = func(a net.Addr) { ready <- a }
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, c) }()
	var addr net.Addr
	select {
	case addr = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("the server stopped: %v\n%s", err, log)
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("the server did not start")
	}
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("the server did not stop")
			return nil
		}
	}
	t.Cleanup(func() { stop() })
	return &running{base: "http://" + addr.String(), log: log, stop: stop}
}

type browser struct {
	t       *testing.T
	base    string
	cookies []*http.Cookie
}

func (b *browser) do(method, path string, body any) (int, map[string]any) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.base+path, rdr)
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, ck := range b.cookies {
		req.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, ck := range resp.Cookies() {
		b.cookies = append(b.cookies, ck)
	}
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestServeConfigurationIsCheckedBeforeAnythingIsCreated(t *testing.T) {
	data := filepath.Join(tempDir(t), "data")
	space := filepath.Join(tempDir(t), "space")
	secret := filepath.Join(tempDir(t), "secret")
	os.WriteFile(secret, []byte("s3cret\n"), 0o600)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"neither mode", []string{"serve"}, "exactly one"},
		{"both modes", []string{"serve", "--space", space, "--data", data}, "exactly one"},
		{"insecure in accounts mode", []string{"serve", "--data", data, "--insecure-no-auth"}, "--insecure-no-auth"},
		{"state dir in accounts mode", []string{"serve", "--data", data, "--state-dir", space}, "--state-dir"},
		{"public url in single mode", []string{"serve", "--space", space, "--public-url", "https://x.example"}, "accounts mode"},
		{"oidc in single mode", []string{"serve", "--space", space, "--oidc-issuer", "https://idp.example"}, "accounts mode"},
		{"trusted proxy in single mode", []string{"serve", "--space", space, "--trusted-proxy", "10.0.0.0/8"}, "accounts mode"},
		{"oidc without client", []string{"serve", "--data", data, "--public-url", "https://x.example", "--oidc-issuer", "https://idp.example"}, "--oidc-client-id"},
		{"oidc without public url", []string{"serve", "--data", data, "--oidc-issuer", "https://idp.example", "--oidc-client-id", "c"}, "--public-url"},
		{"oidc option alone", []string{"serve", "--data", data, "--public-url", "https://x.example", "--oidc-auto-create"}, "--oidc-issuer"},
		{"oidc issuer not https", []string{"serve", "--data", data, "--public-url", "https://x.example", "--oidc-issuer", "http://idp.example", "--oidc-client-id", "c", "--listen", "127.0.0.1:0"}, "not valid"},
		{"public url with a path", []string{"serve", "--data", data, "--public-url", "https://x.example/notes"}, "--public-url"},
		{"public url not a url", []string{"serve", "--data", data, "--public-url", "x.example"}, "--public-url"},
		{"bad trusted proxy", []string{"serve", "--data", data, "--trusted-proxy", "banana"}, "--trusted-proxy"},
		{"draft max below idle", []string{"serve", "--data", data, "--draft-idle", "5m", "--draft-max", "1m"}, "--draft-max"},
		{"negative upload limit", []string{"serve", "--space", space, "--max-upload", "-1"}, "--max-upload"},
		{"inbox in the attachments folder", []string{"serve", "--space", space, "--inbox", "assets"}, "--inbox"},
		{"hidden inbox", []string{"serve", "--space", space, "--inbox", ".inbox"}, "--inbox"},
		{"data path with #", []string{"serve", "--data", data + "#x"}, "'#'"},
		{"missing secret file", []string{"serve", "--data", data, "--public-url", "https://x.example", "--oidc-issuer", "https://idp.example", "--oidc-client-id", "c", "--oidc-client-secret-file", "/nonexistent/secret"}, "--oidc-client-secret-file"},
	} {
		_, err := run(t, append(c.args, "--listen", "127.0.0.1:0")...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want it to mention %q)", c.name, err, c.want)
		}
	}
	for _, p := range []string{data, space, data + "#x"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created although the configuration was refused", p)
		}
	}
	// The secret is read from the file or the environment, not both.
	cfg := serveConfig{data: data, publicURL: "https://x.example", oidcIssuer: "https://idp.example", oidcClientID: "c", oidcClientSecretFile: secret}
	got, err := cfg.check()
	if err != nil || got.oidc.ClientSecret != "s3cret" || got.publicURL != "https://x.example" {
		t.Fatalf("%+v %v", got, err)
	}
	cfg.oidcClientSecret = "from-env"
	if _, err := cfg.check(); err == nil {
		t.Fatal("two sources for the secret accepted")
	}
	cfg.oidcClientSecretFile = ""
	if got, err := cfg.check(); err != nil || got.oidc.ClientSecret != "from-env" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestEnvironmentSuppliesTheNewFlags(t *testing.T) {
	data := filepath.Join(tempDir(t), "data")
	t.Setenv("PETALBOOK_DATA", data)
	t.Setenv("PETALBOOK_INSECURE_NO_AUTH", "true")
	if _, err := run(t, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "--insecure-no-auth") {
		t.Fatalf("PETALBOOK_DATA and PETALBOOK_INSECURE_NO_AUTH not applied: %v", err)
	}
	t.Setenv("PETALBOOK_INSECURE_NO_AUTH", "")
	t.Setenv("PETALBOOK_OIDC_ISSUER", "https://idp.example")
	if _, err := run(t, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "--oidc-client-id") {
		t.Fatalf("PETALBOOK_OIDC_ISSUER not applied: %v", err)
	}
	t.Setenv("PETALBOOK_OIDC_ISSUER", "")
	t.Setenv("PETALBOOK_OIDC_CLIENT_SECRET", "x")
	if _, err := run(t, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "--oidc-issuer") {
		t.Fatalf("PETALBOOK_OIDC_CLIENT_SECRET not applied: %v", err)
	}
	t.Setenv("PETALBOOK_OIDC_CLIENT_SECRET", "")
	t.Setenv("PETALBOOK_DRAFT_IDLE", "banana")
	if _, err := run(t, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "PETALBOOK_DRAFT_IDLE") {
		t.Fatalf("%v", err)
	}
}

func TestServeHelpDescribesBothModes(t *testing.T) {
	out, err := run(t, "serve", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--data", "--space", "--database", "--public-url", "--trusted-proxy", "--oidc-issuer", "--oidc-client-id", "--oidc-client-secret-file",
		"--oidc-name", "--oidc-scopes", "--oidc-auto-create", "--draft-idle", "--draft-max", "--max-spaces", "--space-idle", "--max-open-spaces", "--commit-idle", "PETALBOOK_DATABASE", "exactly one", "accounts mode", "single-user mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help does not mention %q", want)
		}
	}
	if !strings.Contains(out, "--commit-idle duration") || !strings.Contains(out, "(default 5s)") {
		t.Errorf("commit-idle default:\n%s", out)
	}
}

func TestAccountsModeEndToEnd(t *testing.T) {
	data := filepath.Join(tempDir(t), "data")
	srv := start(t, serveConfig{data: data, committer: "Svc <svc@example.test>", commitIdle: time.Hour, commitMax: time.Hour})

	// The set-up link is logged once, with the token.
	m := regexp.MustCompile(`link=\S*/setup#(\S+)`).FindStringSubmatch(srv.log.String())
	if m == nil {
		t.Fatalf("no set-up link in the log:\n%s", srv.log)
	}
	token := strings.Trim(m[1], `"`)
	if n := strings.Count(srv.log.String(), token); n != 1 {
		t.Fatalf("the setup token is in the log %d times", n)
	}
	b := &browser{t: t, base: srv.base}
	if code, r := b.do("GET", "/api/session", nil); code != 200 || r["setup"] != true || r["mode"] != "accounts" {
		t.Fatal(r)
	}
	if code, r := b.do("GET", "/api/spaces", nil); code != 401 {
		t.Fatalf("%d %v", code, r)
	}
	const password = "a long enough password"
	if code, r := b.do("POST", "/api/setup", map[string]any{"token": token, "username": "kazuha", "name": "Kazuha Z", "password": password}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	_, r := b.do("GET", "/api/spaces", nil)
	sid := r["spaces"].([]any)[0].(map[string]any)["id"].(string)
	if code, r := b.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "退出前写的"}, "body": "x"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := b.do("POST", "/api/spaces", map[string]any{"name": "Second"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if _, err := os.Stat(filepath.Join(data, "petalbook.db")); err != nil {
		t.Fatal("no state database in the data directory:", err)
	}
	for _, p := range []string{filepath.Join(data, "spaces", sid, ".git"), filepath.Join(data, "cache", sid, "index.db")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	// Shutdown committed the page, as its owner, as the service.
	out, err := exec.Command("git", "-C", filepath.Join(data, "spaces", sid), "log", "--format=%an <%ae>|%cn <%ce>|%s").Output()
	if err != nil || !strings.Contains(string(out), "Kazuha Z <kazuha@users.petalbook.invalid>|Svc <svc@example.test>|Add 退出前写的") {
		t.Fatalf("%q %v", out, err)
	}
	// Nothing secret was logged.
	for _, secret := range []string{password, b.cookies[0].Value} {
		if strings.Contains(srv.log.String(), secret) {
			t.Fatalf("a secret is in the log:\n%s", srv.log)
		}
	}
	// After a restart there is no set-up link: an account exists.
	srv2 := start(t, serveConfig{data: data, commitIdle: time.Hour, commitMax: time.Hour})
	if strings.Contains(srv2.log.String(), "/setup#") {
		t.Fatalf("set-up link logged although an account exists:\n%s", srv2.log)
	}
	b2 := &browser{t: t, base: srv2.base}
	if code, _ := b2.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": password}); code != 200 {
		t.Fatal("cannot sign in after a restart")
	}
}

func TestAccountsModeMayListenBeyondLoopback(t *testing.T) {
	data := filepath.Join(tempDir(t), "data")
	srv := start(t, serveConfig{data: data, listen: "0.0.0.0:0", publicURL: "https://notes.example.test", commitIdle: time.Hour, commitMax: time.Hour})
	if strings.Contains(srv.log.String(), "without authentication") {
		t.Fatalf("warned about missing authentication:\n%s", srv.log)
	}
	// The Host check is off (not loopback-only), and the public origin is the server's own.
	req, _ := http.NewRequest("POST", srv.base+"/api/login", strings.NewReader(`{"username":"x","password":"y"}`))
	req.Host = "notes.example.test"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://notes.example.test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("own origin: %d", resp.StatusCode)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = http.DefaultClient.Do(req.Clone(context.Background()))
	if resp != nil {
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("foreign origin: %d", resp.StatusCode)
		}
	}
	// The single-user rule is unchanged.
	if _, err := run(t, "serve", "--space", filepath.Join(tempDir(t), "s"), "--listen", "0.0.0.0:0"); err == nil || !strings.Contains(err.Error(), "refusing to listen") {
		t.Fatalf("%v", err)
	}
}

func TestAnUnreachableDatabaseNeverShowsItsPassword(t *testing.T) {
	needGit(t)
	log := &syncBuffer{}
	err := runServe(context.Background(), serveConfig{
		data: filepath.Join(tempDir(t), "data"), listen: "127.0.0.1:0",
		database: "postgres://petal:s3cretpw@127.0.0.1:1/petalbook?connect_timeout=1&sslmode=disable",
		logger:   slog.New(slog.NewTextHandler(log, nil)),
	})
	if err == nil {
		t.Fatal("started on a database that is not there")
	}
	if strings.Contains(err.Error(), "s3cretpw") || strings.Contains(log.String(), "s3cretpw") {
		t.Fatalf("the password leaked: %v\n%s", err, log)
	}
}

func TestSingleUserStateDatabaseNextToTheIndex(t *testing.T) {
	needGit(t)
	dir := filepath.Join(tempDir(t), "notes")
	srv := start(t, serveConfig{space: dir, committer: "T <t@example.test>", commitIdle: time.Hour, commitMax: time.Hour, draftIdle: time.Hour, draftMax: 2 * time.Hour})
	b := &browser{t: t, base: srv.base}
	_, p := b.do("POST", "/api/spaces/default/pages", map[string]any{"meta": map[string]any{"title": "Draft me"}, "body": "one\n"})
	url := "/api/spaces/default/pages/" + p["id"].(string)
	if code, r := b.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Draft me"}, "body": "two\n"}); code != 200 {
		t.Fatalf("%d %v", code, r)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, ".git", "petalbook-state", "state.db")
	if _, err := os.Stat(state); err != nil {
		t.Fatal("no state database next to the index:", err)
	}
	// A draft that was not due is not forced into the file at shutdown ...
	file, _ := os.ReadFile(filepath.Join(dir, "Draft me.md"))
	if !strings.Contains(string(file), "one\n") || strings.Contains(string(file), "two") {
		t.Fatalf("the draft was applied at shutdown:\n%s", file)
	}
	// ... it is still in the database, and the database stays out of Git.
	st, err := store.Open(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := store.GetDraft(context.Background(), st.Q(), "default", p["id"].(string), "local")
	if err != nil || d.Body != "two\n" {
		t.Fatalf("%v %v", d, err)
	}
	if out, _ := exec.Command("git", "-C", dir, "ls-files").Output(); strings.Contains(string(out), "state.db") {
		t.Fatalf("the state database is in Git:\n%s", out)
	}
	// After a restart the draft is served again.
	srv2 := start(t, serveConfig{space: dir, committer: "T <t@example.test>", commitIdle: time.Hour, commitMax: time.Hour, draftIdle: time.Hour, draftMax: 2 * time.Hour})
	b2 := &browser{t: t, base: srv2.base}
	if _, g := b2.do("GET", url, nil); g["draft"] == nil || g["draft"].(map[string]any)["body"] != "two\n" {
		t.Fatalf("%v", g)
	}
}

func TestADraftReachesGitWithinSecondsOfBeingDue(t *testing.T) {
	needGit(t)
	dir := filepath.Join(tempDir(t), "notes")
	srv := start(t, serveConfig{space: dir, committer: "T <t@example.test>", commitIdle: 50 * time.Millisecond, commitMax: time.Hour,
		draftIdle: 40 * time.Millisecond, draftMax: time.Hour, draftTick: 10 * time.Millisecond})
	b := &browser{t: t, base: srv.base}
	_, p := b.do("POST", "/api/spaces/default/pages", map[string]any{"meta": map[string]any{"title": "Slow"}, "body": "one\n"})
	url := "/api/spaces/default/pages/" + p["id"].(string)
	b.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Slow"}, "body": "two\n"})
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, _ := exec.Command("git", "-C", dir, "log", "--format=%s").Output()
		committed, _ := exec.Command("git", "-C", dir, "show", "HEAD:Slow.md").Output()
		file, _ := os.ReadFile(filepath.Join(dir, "Slow.md"))
		// Both steps happened by themselves: the draft became the file, and the
		// file reached a commit (possibly together with the page's creation).
		if strings.Contains(string(file), "two") && strings.Contains(string(committed), "two") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not committed in time; log:\n%s\nfile:\n%s", out, file)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, g := b.do("GET", url, nil); g["draft"] != nil || g["body"] != "two\n" {
		t.Fatalf("%v", g)
	}
}

func TestUserCommands(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	const pw1, pw2 = "first password here", "second password here"

	out, err := runIn(t, pw1+"\n", "user", "add", "--data", data, "--username", "Kazuha", "--admin", "--name", "Kazuha Z")
	if err != nil || !strings.Contains(out, "added kazuha (admin)") || strings.Contains(out, pw1) {
		t.Fatalf("%q %v", out, err)
	}
	if out, err = runIn(t, pw1+"\r\n", "user", "add", "--data", data, "--username", "tang"); err != nil || !strings.Contains(out, "added tang (member)") {
		t.Fatalf("%q %v", out, err)
	}
	// Refusals.
	for _, c := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{pw1, []string{"user", "add", "--data", data, "--username", "tang"}, "taken"},
		{"short", []string{"user", "add", "--data", data, "--username", "newbie"}, "at least 8"},
		{"", []string{"user", "add", "--data", data, "--username", "newbie"}, "no password"},
		{pw1, []string{"user", "add", "--data", data, "--username", "bad name"}, "username"},
		{pw1, []string{"user", "add", "--data", data}, "username"},
		{pw1, []string{"user", "add", "--username", "x1"}, "data"},
		{pw2, []string{"user", "passwd", "--data", data, "--username", "nobody"}, "no user"},
	} {
		if _, err := runIn(t, c.stdin, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (want %q)", c.args, err, c.want)
		}
	}
	list, err := runIn(t, "", "user", "list", "--data", data)
	if err != nil || !strings.Contains(list, "kazuha") || !strings.Contains(list, "admin") || !strings.Contains(list, "tang") || !strings.Contains(list, "member") || !strings.Contains(list, "enabled") || strings.Contains(list, "newbie") {
		t.Fatalf("%q %v", list, err)
	}
	if strings.Contains(list, pw1) {
		t.Fatal("a password in the listing")
	}

	// Their accounts work in the server, personal spaces included.
	srv := start(t, serveConfig{data: data, commitIdle: time.Hour, commitMax: time.Hour})
	if strings.Contains(srv.log.String(), "/setup#") {
		t.Fatal("a set-up link although the command line made the first account")
	}
	b := &browser{t: t, base: srv.base}
	if code, _ := b.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw1}); code != 200 {
		t.Fatal("the new account cannot sign in")
	}
	if _, r := b.do("GET", "/api/spaces", nil); len(r["spaces"].([]any)) != 1 {
		t.Fatalf("%v", r)
	}
	if _, r := b.do("GET", "/api/session", nil); r["user"].(map[string]any)["role"] != "admin" || r["user"].(map[string]any)["name"] != "Kazuha Z" {
		t.Fatalf("%v", r)
	}

	// A new password ends sessions and replaces the old one; the server notices without a restart.
	if out, err := runIn(t, pw2+"\n", "user", "passwd", "--data", data, "--username", "kazuha"); err != nil || !strings.Contains(out, "password set for kazuha") || strings.Contains(out, pw2) {
		t.Fatalf("%q %v", out, err)
	}
	b2 := &browser{t: t, base: srv.base}
	if code, _ := b2.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw1}); code != 401 {
		t.Fatal("the old password still works")
	}
	if code, _ := b2.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw2}); code != 200 {
		t.Fatal("the new password does not work")
	}
	// The data directory and database also come from the environment.
	t.Setenv("PETALBOOK_DATA", data)
	if out, err := run(t, "user", "list"); err != nil || !strings.Contains(out, "tang") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestUserAddWithDatabaseFromTheEnvironmentNeverPrintsIt(t *testing.T) {
	needGit(t)
	t.Setenv("PETALBOOK_DATABASE", "postgres://petal:s3cretpw@127.0.0.1:1/petalbook?connect_timeout=1&sslmode=disable")
	out, err := runIn(t, "a long enough password\n", "user", "add", "--data", filepath.Join(tempDir(t), "data"), "--username", "x1")
	if err == nil {
		t.Fatal("added a user to a database that is not there")
	}
	if strings.Contains(out, "s3cretpw") || strings.Contains(err.Error(), "s3cretpw") {
		t.Fatalf("the password leaked: %q %v", out, err)
	}
}

func TestDraftCommandsRecoverHeldDrafts(t *testing.T) {
	needGit(t)
	dir := filepath.Join(tempDir(t), "notes")
	srv := start(t, serveConfig{space: dir, committer: "T <t@example.test>", commitIdle: time.Hour, commitMax: time.Hour, draftIdle: 500 * time.Millisecond, draftMax: time.Hour, draftTick: 10 * time.Millisecond})
	b := &browser{t: t, base: srv.base}
	_, p := b.do("POST", "/api/spaces/default/pages", map[string]any{"meta": map[string]any{"title": "Lost"}, "body": "one\n"})
	id := p["id"].(string)
	b.do("PUT", "/api/spaces/default/pages/"+id+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Lost"}, "body": "only copy of this text\n"})
	// The file is removed outside the server before the draft falls due: the
	// sweeper sets it aside and says so.
	if err := os.Remove(filepath.Join(dir, "Lost.md")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(srv.log.String(), "its page file is gone") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "draft", "list", "--space", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "default") || !strings.Contains(out, id) || !strings.Contains(out, "local") {
		t.Fatalf("%q", out)
	}
	root := newRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"draft", "export", "default", id, "--space", dir})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "only copy of this text\n" || !strings.Contains(stderr.String(), `"title":"Lost"`) {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	if _, err := run(t, "draft", "export", "default", id, "--space", dir, "--user", "nobody"); err == nil {
		t.Fatal("exported the draft of a user who has none")
	}
	if _, err := run(t, "draft", "list", "--space", filepath.Join(tempDir(t), "none")); err == nil || !strings.Contains(err.Error(), "no state database") {
		t.Fatalf("%v", err)
	}
	if _, err := run(t, "draft", "list"); err == nil {
		t.Fatal("no mode given")
	}
}

func TestGitHubFlagsAndTheAppThroughTheServer(t *testing.T) {
	// The base URLs of GitHub are for accounts mode.
	if _, err := (serveConfig{space: tempDir(t), githubAPI: "http://127.0.0.1:1"}).check(); err == nil {
		t.Fatal("a single-user server accepted --github-api-url")
	}
	f := githubapptest.New(t, githubapptest.SystemClock())
	data := filepath.Join(tempDir(t), "data")
	srv := start(t, serveConfig{data: data, committer: "Svc <svc@example.test>", commitIdle: time.Hour, commitMax: time.Hour,
		githubAPI: f.URL, githubWeb: f.URL, githubRenewEvery: 50 * time.Millisecond})
	m := regexp.MustCompile(`link=\S*/setup#(\S+)`).FindStringSubmatch(srv.log.String())
	if m == nil {
		t.Fatalf("no set-up link in the log:\n%s", srv.log)
	}
	b := &browser{t: t, base: srv.base}
	if code, r := b.do("POST", "/api/setup", map[string]any{"token": strings.Trim(m[1], `"`), "username": "kazuha", "password": "a long enough password"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := b.do("GET", "/api/admin/github", nil); code != 200 || r["configured"] != false {
		t.Fatalf("%d %v", code, r)
	}
	code, r := b.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": f.KeyPEM()})
	if code != 200 || r["configured"] != true || r["slug"] != "petalbook-notes" {
		t.Fatalf("%d %v", code, r)
	}
	// The key is stored sealed with the server's key, in the state database.
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(data, "petalbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, ok, err := store.GetSiteSetting(context.Background(), st.Q(), "github_app")
	if err != nil || !ok || strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "424242\\n") {
		t.Fatalf("%v %v %s", ok, err, raw)
	}
	if strings.Contains(srv.log.String(), "PRIVATE KEY") {
		t.Fatal("the key reached the log")
	}
}
