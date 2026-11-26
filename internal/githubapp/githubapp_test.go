package githubapp_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/githubapp/githubapptest"
)

var ctx = context.Background()

func setup(t *testing.T) (*githubapptest.Fake, *githubapptest.Clock, *githubapp.Client, *githubapp.App) {
	t.Helper()
	clk := githubapptest.NewClock()
	f := githubapptest.New(t, clk)
	c, err := githubapp.New(githubapp.Config{API: f.URL, Web: f.URL, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	app, err := c.NewApp("424242", f.Key)
	if err != nil {
		t.Fatal(err)
	}
	return f, clk, c, app
}

func TestClientRefusesPlainHTTPToARemoteHost(t *testing.T) {
	if _, err := githubapp.New(githubapp.Config{API: "http://example.com", Web: "https://github.com"}); err == nil {
		t.Fatal("http to a remote host accepted")
	}
	if _, err := githubapp.New(githubapp.Config{}); err != nil {
		t.Fatalf("the defaults: %v", err)
	}
	c, _ := githubapp.New(githubapp.Config{})
	if c.API() != "https://api.github.com" || c.Web() != "https://github.com" || c.RepoURL("a", "b") != "https://github.com/a/b.git" {
		t.Fatalf("%s %s", c.API(), c.Web())
	}
}

func TestAppInfoUsesAValidJWT(t *testing.T) {
	f, _, _, app := setup(t)
	info, err := app.AppInfo(ctx)
	if err != nil || info.ID != 424242 || info.ClientID != f.ClientID || info.Permissions["contents"] != "write" {
		t.Fatalf("%+v %v", info, err)
	}
	// A key that is not the App's is refused.
	c, _ := githubapp.New(githubapp.Config{API: f.URL, Web: f.URL})
	other, _ := c.NewApp("424242", githubapptest.NewKey(t))
	if _, err := other.AppInfo(ctx); !githubapp.IsStatus(err, http.StatusUnauthorized) {
		t.Fatalf("%v", err)
	}
}

func TestJWTIsReusedForEightMinutes(t *testing.T) {
	_, clk, _, app := setup(t)
	a, _ := app.JWT()
	b, _ := app.JWT()
	if a != b {
		t.Fatal("a second JWT was made at once")
	}
	clk.Advance(8*time.Minute + time.Second)
	c, _ := app.JWT()
	if c == a {
		t.Fatal("the JWT was kept past eight minutes")
	}
}

func TestInstallationTokenCacheRefreshAndSharing(t *testing.T) {
	f, clk, _, app := setup(t)
	f.AddUser("kazuha")
	id := f.Install("kazuha", "all")
	f.AddRepo("kazuha", "notes", true)
	repo := f.RepoByName("kazuha/notes")
	spec := githubapp.TokenSpec{Installation: id, RepoIDs: []int64{repo.ID}, Perms: map[string]string{"contents": "write"}}

	// Concurrent callers share one request.
	var wg sync.WaitGroup
	toks := make([]string, 8)
	for i := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if toks[i], err = app.Token(ctx, spec); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.Minted(); n != 1 {
		t.Fatalf("%d tokens minted for one wanted", n)
	}
	for _, tk := range toks {
		if tk != toks[0] || tk == "" {
			t.Fatalf("%q", toks)
		}
	}
	// Reused until five minutes before the end (the token lives an hour).
	clk.Advance(54 * time.Minute)
	if again, _ := app.Token(ctx, spec); again != toks[0] || f.Minted() != 1 {
		t.Fatal("the token was replaced too early")
	}
	clk.Advance(2 * time.Minute)
	fresh, err := app.Token(ctx, spec)
	if err != nil || fresh == toks[0] || f.Minted() != 2 {
		t.Fatalf("%q %v %d", fresh, err, f.Minted())
	}
	// Another repository, another token; a Once token is not kept.
	if _, err := app.Token(ctx, githubapp.TokenSpec{Installation: id, Perms: map[string]string{"administration": "write"}, Once: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Token(ctx, githubapp.TokenSpec{Installation: id, Perms: map[string]string{"administration": "write"}, Once: true}); err != nil {
		t.Fatal(err)
	}
	if f.Minted() != 4 {
		t.Fatalf("%d", f.Minted())
	}
	// A repository the installation does not reach is refused, and nothing is cached for it.
	other := f.AddRepo("someone", "else", false)
	if _, err := app.Token(ctx, githubapp.TokenSpec{Installation: id, RepoIDs: []int64{other.ID}, Perms: map[string]string{"contents": "write"}}); !githubapp.IsStatus(err, http.StatusUnprocessableEntity) {
		t.Fatalf("%v", err)
	}
}

func TestLookupsAreRemembered(t *testing.T) {
	f, clk, _, app := setup(t)
	f.AddOrg("kazuhahub")
	f.Install("kazuhahub", "all")
	for i := 0; i < 3; i++ {
		if _, err := app.OrgInstallation(ctx, "kazuhahub"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.Count("GET", "/orgs/kazuhahub/installation"); n != 1 {
		t.Fatalf("%d lookups", n)
	}
	clk.Advance(11 * time.Minute)
	app.OrgInstallation(ctx, "kazuhahub")
	if n := f.Count("GET", "/orgs/kazuhahub/installation"); n != 2 {
		t.Fatalf("%d lookups", n)
	}
	if _, err := app.UserInstallation(ctx, "nobody"); !githubapp.IsStatus(err, 404) {
		t.Fatalf("%v", err)
	}
}

func TestInstallationsArePagedOneRequestEach(t *testing.T) {
	f, _, _, app := setup(t)
	for i := 0; i < 65; i++ {
		f.AddOrg("org" + string(rune('a'+i%26)) + strings.Repeat("x", i/26))
		f.Install("org"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "all")
	}
	total, page := 0, 1
	for page > 0 {
		list, next, err := app.Installations(ctx, page)
		if err != nil {
			t.Fatal(err)
		}
		total += len(list)
		if len(list) > githubapp.PerPage {
			t.Fatalf("a page of %d", len(list))
		}
		page = next
	}
	if total != 65 || f.Count("GET", "/app/installations") != 3 {
		t.Fatalf("%d installations, %d requests", total, f.Count("GET", "/app/installations"))
	}
}

func TestCheckClient(t *testing.T) {
	f, _, c, _ := setup(t)
	if ok, err := c.CheckClient(ctx, f.ClientID, f.ClientSecret); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if ok, err := c.CheckClient(ctx, f.ClientID, "wrong"); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestRateLimitCarriesTheResetTime(t *testing.T) {
	f, clk, _, app := setup(t)
	f.LimitUntil(clk.Now().Add(90 * time.Second))
	_, err := app.AppInfo(ctx)
	var se *githubapp.StatusError
	if !asStatus(err, &se) || !se.RateLimited {
		t.Fatalf("%v", err)
	}
	if d := se.RetryAfter(clk.Now()); d < 89*time.Second || d > 91*time.Second {
		t.Fatalf("retry after %v", d)
	}
	clk.Advance(2 * time.Minute)
	if _, err := app.AppInfo(ctx); err != nil {
		t.Fatalf("after the reset: %v", err)
	}
}

func asStatus(err error, out **githubapp.StatusError) bool {
	for err != nil {
		if se, ok := err.(*githubapp.StatusError); ok {
			*out = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestOAuthWithPKCEAndRotation(t *testing.T) {
	f, _, c, _ := setup(t)
	f.AddUser("tang")
	verifier, challenge := githubapp.PKCE()
	if len(verifier) != 64 {
		t.Fatal(len(verifier))
	}
	authURL := c.AuthorizeURL(f.ClientID, "http://localhost/cb", "st", challenge, "tang")
	back := f.Authorize(authURL, "tang")
	code := back[strings.Index(back, "code=")+5 : strings.Index(back, "&state")]
	// The wrong verifier is refused.
	if _, err := c.ExchangeCode(ctx, f.ClientID, f.ClientSecret, code, "http://localhost/cb", "not-the-verifier"); err == nil {
		t.Fatal("a wrong PKCE verifier was accepted")
	}
	back = f.Authorize(authURL, "tang")
	code = back[strings.Index(back, "code=")+5 : strings.Index(back, "&state")]
	tok, err := c.ExchangeCode(ctx, f.ClientID, f.ClientSecret, code, "http://localhost/cb", verifier)
	if err != nil || tok.Access == "" || tok.Refresh == "" || tok.AccessExpires.IsZero() || tok.RefreshExpires.IsZero() {
		t.Fatalf("%+v %v", tok, err)
	}
	u, err := c.User(ctx, tok.Access)
	if err != nil || u.Login != "tang" {
		t.Fatalf("%+v %v", u, err)
	}
	// Renewal hands out a new pair; the old refresh token stops working.
	tok2, err := c.Refresh(ctx, f.ClientID, f.ClientSecret, tok.Refresh)
	if err != nil || tok2.Refresh == tok.Refresh || tok2.Access == tok.Access {
		t.Fatalf("%+v %v", tok2, err)
	}
	if _, err := c.Refresh(ctx, f.ClientID, f.ClientSecret, tok.Refresh); !githubapp.IsBadRefresh(err) {
		t.Fatalf("the old refresh token worked again: %v", err)
	}
	f.ExpireRefresh("tang")
	if _, err := c.Refresh(ctx, f.ClientID, f.ClientSecret, tok2.Refresh); !githubapp.IsBadRefresh(err) {
		t.Fatalf("%v", err)
	}
	if err := c.RevokeGrant(ctx, f.ClientID, f.ClientSecret, tok2.Access); err != nil {
		t.Fatal(err)
	}
	if _, err := c.User(ctx, tok2.Access); !githubapp.IsStatus(err, 401) {
		t.Fatalf("%v", err)
	}
}

func TestCreateRepoAndItsFailures(t *testing.T) {
	f, _, c, app := setup(t)
	f.AddUser("tang")
	f.AddOrg("kazuhahub", "tang")
	inst := f.Install("kazuhahub", "all")
	// With the installation's administration token.
	adm, err := app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"administration": "write"}, Once: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.CreateRepo(ctx, adm, "kazuhahub", githubapp.NewRepo{Name: "notes", Private: true, Description: "d"})
	if err != nil || r.FullName != "kazuhahub/notes" || !r.Private {
		t.Fatalf("%+v %v", r, err)
	}
	// A name taken is a 422 that says so.
	_, err = c.CreateRepo(ctx, adm, "kazuhahub", githubapp.NewRepo{Name: "notes"})
	var se *githubapp.StatusError
	if !asStatus(err, &se) || se.Status != 422 || !strings.Contains(strings.Join(se.Errors, " "), "already exists") {
		t.Fatalf("%v", err)
	}
	// An App without Administration cannot ask for the permission at all.
	f.AppPerms = map[string]string{"contents": "write", "metadata": "read"}
	if _, err := app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"administration": "write"}, Once: true}); !githubapp.IsStatus(err, 422) {
		t.Fatalf("%v", err)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// A call GitHub refuses is logged with the status, GitHub's own words and what
// kind of call it was; no token, key, code or name of an account is in it.
func TestRefusalsAreLoggedWithGitHubsWords(t *testing.T) {
	logs := &syncBuf{}
	clk := githubapptest.NewClock()
	f := githubapptest.New(t, clk)
	c, err := githubapp.New(githubapp.Config{API: f.URL, Web: f.URL, Now: clk.Now,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := c.NewApp("424242", f.Key)
	f.AddOrg("kazuhahub")
	inst := f.Install("kazuhahub", "all")
	f.AppPerms = map[string]string{"metadata": "read"}
	_, err = app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"contents": "write"}})
	if !githubapp.IsPermissionRefusal(err) {
		t.Fatalf("a 422 about permissions is not told as one: %v", err)
	}
	f.PermissionRefusal403 = true
	_, err = app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"contents": "write"}})
	if !githubapp.IsPermissionRefusal(err) {
		t.Fatalf("a 403 about the integration is not told as one: %v", err)
	}
	f.PermissionRefusal403 = false
	f.AppPerms = map[string]string{"administration": "write", "contents": "write", "metadata": "read"}
	// A repository the installation does not reach is not a permission refusal.
	f.AddOrg("sel")
	sel := f.Install("sel", "selected")
	repo := f.AddRepo("sel", "x", true)
	_, err = app.Token(ctx, githubapp.TokenSpec{Installation: sel, RepoIDs: []int64{repo.ID}, Perms: map[string]string{"contents": "write"}})
	if !githubapp.IsStatus(err, 422) || githubapp.IsPermissionRefusal(err) {
		t.Fatalf("%v", err)
	}
	tok, err := app.Token(ctx, githubapp.TokenSpec{Installation: inst, Perms: map[string]string{"administration": "write"}, Once: true})
	if err != nil {
		t.Fatal(err)
	}
	f.AddRepo("kazuhahub", "taken", false)
	if _, err = c.CreateRepo(ctx, tok, "kazuhahub", githubapp.NewRepo{Name: "taken"}); err == nil {
		t.Fatal("expected a refusal")
	}
	// An OAuth refusal and a missing page.
	_, _ = c.ExchangeCode(ctx, f.ClientID, f.ClientSecret, "a-code-nobody-made", "http://x/cb", "")
	_, _ = app.OrgInstallation(ctx, "nowhere")

	out := logs.String()
	for _, want := range []string{"installation_token", "status=422", "status=403", "The permissions requested are not granted to this installation.",
		"Resource not accessible by integration", "create_org_repo", "name already exists", "oauth_token", "bad_verification_code", "account_installation", "status=404"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range append(f.AllTokens(), tok, f.ClientSecret, f.KeyPEM(), "a-code-nobody-made", "Bearer", "Authorization") {
		if secret != "" && strings.Contains(out, secret) {
			t.Errorf("the log holds %q:\n%s", secret, out)
		}
	}
	// A token-like string in GitHub's own words is blanked out.
	if strings.Contains(out, "ghs_") {
		t.Errorf("a token reached the log")
	}
}

func TestConvertingAManifest(t *testing.T) {
	logs := &syncBuf{}
	f := githubapptest.New(t, nil)
	c, _ := githubapp.New(githubapp.Config{API: f.URL, Web: f.URL, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	manifest := `{"name":"Notes","redirect_url":"https://notes.example.com/api/github/manifest/callback"}`
	back := f.SubmitManifest(c.ManifestURL("", "st-1"), manifest)
	if !strings.HasPrefix(back, "https://notes.example.com/api/github/manifest/callback?") || !strings.Contains(back, "state=st-1") {
		t.Fatal(back)
	}
	code := back[strings.Index(back, "code=")+5:]
	code = code[:strings.Index(code, "&")]
	creds, err := c.ConvertManifest(ctx, code)
	if err != nil || creds.ID != 424242 || creds.ClientID != f.ClientID || creds.ClientSecret != f.ClientSecret || creds.PEM != f.KeyPEM() || creds.WebhookSecret == "" || creds.Name != "Notes" {
		t.Fatalf("%+v %v", creds, err)
	}
	// The code works once, and a refusal never shows it.
	if _, err := c.ConvertManifest(ctx, code); !githubapp.IsStatus(err, 404) {
		t.Fatalf("%v", err)
	} else if strings.Contains(err.Error(), code) || strings.Contains(logs.String(), code) {
		t.Fatalf("the code is in an error or a log: %v %s", err, logs.String())
	}
	if _, err := c.ConvertManifest(ctx, "a/b"); err == nil {
		t.Fatal("a code with a slash was sent")
	}
	if got := c.ManifestURL("kazuhahub", "a b"); got != f.URL+"/organizations/kazuhahub/settings/apps/new?state=a+b" {
		t.Fatal(got)
	}
	if got := c.ManifestURL("", "s"); got != f.URL+"/settings/apps/new?state=s" {
		t.Fatal(got)
	}
}
