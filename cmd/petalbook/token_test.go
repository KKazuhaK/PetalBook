package main

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// runSplit runs a command with standard output and standard error apart.
func runSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRoot()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

func withToken(t *testing.T, base, method, path, secret string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestTokenCommandsInSingleUserMode(t *testing.T) {
	needGit(t)
	dir := filepath.Join(tempDir(t), "notes")
	// The state database is made by the server; a command does not make one.
	if _, _, err := runSplit(t, "token", "create", "--space", dir, "--name", "x"); err == nil || !strings.Contains(err.Error(), "no state database") {
		t.Fatalf("%v", err)
	}
	srv := start(t, serveConfig{space: dir, committer: "T <t@example.test>", commitIdle: time.Hour, commitMax: time.Hour, draftIdle: time.Hour, draftMax: 2 * time.Hour})

	// The secret alone on standard output; what it is on standard error.
	out, errOut, err := runSplit(t, "token", "create", "--space", dir, "--name", "Desktop notes", "--access", "write", "--expires", "30d")
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSuffix(out, "\n")
	if !regexp.MustCompile(`^pbk_[0-9A-Za-z]{43}$`).MatchString(secret) {
		t.Fatalf("stdout %q", out)
	}
	if !regexp.MustCompile(`^token [0-9A-Z]{10} "Desktop notes" \(write, expires \d{4}-\d\d-\d\d\): shown once, keep it now\n$`).MatchString(errOut) || strings.Contains(errOut, secret) {
		t.Fatalf("stderr %q", errOut)
	}
	// It works against the server that is running, and a request without one still does.
	if code, body := withToken(t, srv.base, "GET", "/api/session", secret); code != 200 || !strings.Contains(body, `"name":"Desktop notes"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := withToken(t, srv.base, "GET", "/api/spaces/default/tree", secret); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := withToken(t, srv.base, "GET", "/api/spaces/default/tree", ""); code != 401 {
		t.Fatalf("an empty bearer: %d %s", code, body)
	}
	b := &browser{t: t, base: srv.base}
	if code, _ := b.do("GET", "/api/spaces/default/tree", nil); code != 200 {
		t.Fatal(code)
	}
	// And is refused for what a token does not do.
	if code, body := withToken(t, srv.base, "POST", "/api/spaces/default/reindex", secret); code != 403 || !strings.Contains(body, "token_scope") {
		t.Fatalf("%d %s", code, body)
	}

	// Read is the default, 90 days the default expiry; never is a choice.
	out2, errOut2, err := runSplit(t, "token", "create", "--space", dir, "--name", "Default")
	if err != nil || !strings.Contains(errOut2, "(read, expires ") {
		t.Fatalf("%q %q %v", errOut2, out2, err)
	}
	_, errOut3, err := runSplit(t, "token", "create", "--space", dir, "--name", "Forever", "--expires", "never")
	if err != nil || !strings.Contains(errOut3, "(read, expires never)") {
		t.Fatalf("%q %v", errOut3, err)
	}
	for _, bad := range [][]string{{"--expires", "0d"}, {"--expires", "366d"}, {"--expires", "90"}, {"--expires", "soon"}, {"--access", "admin"}, {"--name", " "}} {
		args := append([]string{"token", "create", "--space", dir, "--name", "x"}, bad...)
		if _, _, err := runSplit(t, args...); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	if _, _, err := runSplit(t, "token", "create", "--space", dir); err == nil {
		t.Error("a token without a name")
	}

	// The listing.
	list, _, err := runSplit(t, "token", "list", "--space", dir)
	if err != nil || !strings.HasPrefix(list, "ID ") || !strings.Contains(list, "PREFIX") || !strings.Contains(list, "LAST USED") ||
		!strings.Contains(list, "Desktop notes") || !strings.Contains(list, "write") || !strings.Contains(list, secret[:12]) ||
		!strings.Contains(list, "active") || strings.Contains(list, secret) {
		t.Fatalf("%q %v", list, err)
	}
	if strings.Contains(list, "never") != true {
		t.Fatalf("%q", list)
	}

	// Revoke by prefix; the id is in the listing.
	revOut, _, err := runSplit(t, "token", "revoke", "--space", dir, secret[:12])
	if err != nil || !regexp.MustCompile(`^revoked [0-9A-Z]{10} "Desktop notes"\n$`).MatchString(revOut) {
		t.Fatalf("%q %v", revOut, err)
	}
	list, _, _ = runSplit(t, "token", "list", "--space", dir)
	if strings.Contains(list, "Desktop notes") {
		t.Fatalf("a revoked token is listed without --all:\n%s", list)
	}
	list, _, _ = runSplit(t, "token", "list", "--space", dir, "--all")
	if !strings.Contains(list, "Desktop notes") || !strings.Contains(list, "revoked") {
		t.Fatalf("%q", list)
	}
	// A token the server has not looked at yet is refused at once. (One it has in
	// memory is refused within 30 seconds, which the service tests cover.)
	other, _, err := runSplit(t, "token", "create", "--space", dir, "--name", "Other")
	if err != nil {
		t.Fatal(err)
	}
	other = strings.TrimSpace(other)
	if _, _, err := runSplit(t, "token", "revoke", "--space", dir, other[:12]); err != nil {
		t.Fatal(err)
	}
	if code, body := withToken(t, srv.base, "GET", "/api/spaces/default/tree", other); code != 401 || !strings.Contains(body, "token_revoked") {
		t.Fatalf("%d %s", code, body)
	}

	// Ids and prefixes that match nothing, or too much, exit with an error.
	list, _, _ = runSplit(t, "token", "list", "--space", dir)
	id := strings.Fields(strings.Split(list, "\n")[1])[0]
	for _, c := range []struct{ arg, want string }{{"NOSUCHTOKN", "all 12 characters"}, {"pbk_", "all 12 characters"}, {secret[:11], "all 12 characters"}, {"", "all 12 characters"}} {
		if _, _, err := runSplit(t, "token", "revoke", "--space", dir, c.arg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.arg, err)
		}
	}
	if _, _, err := runSplit(t, "token", "revoke", "--space", dir, id); err != nil {
		t.Fatal(err)
	}
	// Flags: one mode at a time.
	if _, _, err := runSplit(t, "token", "list"); err == nil {
		t.Error("no mode")
	}
	if _, _, err := runSplit(t, "token", "list", "--space", dir, "--data", dir); err == nil {
		t.Error("both modes")
	}
	if _, _, err := runSplit(t, "token", "list", "--space", dir, "--user", "x"); err == nil {
		t.Error("--user in single-user mode")
	}
}

func TestTokenCommandsWithAccounts(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	const password = "a long enough password"
	if _, err := runIn(t, password+"\n", "user", "add", "--data", data, "--username", "kazuha", "--admin"); err != nil {
		t.Fatal(err)
	}
	srv := start(t, serveConfig{data: data, committer: "Svc <svc@example.test>", commitIdle: time.Hour, commitMax: time.Hour})
	b := &browser{t: t, base: srv.base}
	if code, r := b.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": password}); code != 200 {
		t.Fatal(code, r)
	}
	_, r := b.do("GET", "/api/spaces", nil)
	sid := r["spaces"].([]any)[0].(map[string]any)["id"].(string)
	code, r := b.do("POST", "/api/account/tokens", map[string]any{"name": "from the app", "spaces": []string{sid}})
	if code != 201 {
		t.Fatal(code, r)
	}
	secret := r["secret"].(string)

	// Tokens of accounts are made in the app.
	if _, _, err := runSplit(t, "token", "create", "--data", data, "--user", "kazuha", "--name", "x"); err == nil || !strings.Contains(err.Error(), "in the app") {
		t.Fatalf("%v", err)
	}
	// An administrator who cannot sign in lists and revokes, naming the person.
	if _, _, err := runSplit(t, "token", "list", "--data", data); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("%v", err)
	}
	if _, _, err := runSplit(t, "token", "list", "--data", data, "--user", "nobody"); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("%v", err)
	}
	list, _, err := runSplit(t, "token", "list", "--data", data, "--user", "Kazuha")
	if err != nil || !strings.Contains(list, "from the app") || !strings.Contains(list, secret[:12]) {
		t.Fatalf("%q %v", list, err)
	}
	if _, _, err := runSplit(t, "token", "revoke", "--data", data, "--user", "kazuha", secret[:12]); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := runSplit(t, "token", "list", "--data", data, "--user", "kazuha"); strings.Contains(l, "from the app") {
		t.Fatalf("%q", l)
	}
	// Not another person's, whatever its prefix.
	if _, err := runIn(t, password+"\n", "user", "add", "--data", data, "--username", "tang"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSplit(t, "token", "revoke", "--data", data, "--user", "tang", secret[:12]); err == nil {
		t.Fatal("revoked another person's token")
	}
}
