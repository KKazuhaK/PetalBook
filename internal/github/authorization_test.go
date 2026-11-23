package github_test

import (
	"context"
	"errors"
	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestConnectingAnAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("U1", "kazuha")
		h.f.AddUser("kazuha-gh")
		if _, _, err := h.svc.StartAuthorization(ctx, "U1", "/"); code(err) != "github_not_configured" {
			t.Fatalf("%v", err)
		}
		h.configure()
		var fixed []string
		h.svc.OnChange(func(u string) { fixed = append(fixed, u) })

		authURL, binding, err := h.svc.StartAuthorization(ctx, "U1", "/s/ABC/settings")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(authURL)
		q := u.Query()
		if q.Get("client_id") != h.f.ClientID || q.Get("redirect_uri") != publicURL+"/api/github/authorization/callback" ||
			q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || len(q.Get("state")) < 40 || q.Get("login") != "" {
			t.Fatalf("%s", authURL)
		}
		back, _ := url.Parse(h.f.Authorize(authURL, "kazuha-gh"))
		// Another browser's cookie, or another user, does not end the attempt.
		if to, clear := h.svc.CompleteAuthorization(ctx, "not-the-binding", "U1", back.Query()); to != "/settings?github_error=expired" {
			t.Fatalf("%q %v", to, clear)
		}
		h.user("U2", "other")
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U2", back.Query()); to != "/settings?github_error=expired" {
			t.Fatalf("another user's session: %q", to)
		}
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "", back.Query()); to != "/settings?github_error=expired" {
			t.Fatalf("no session: %q", to)
		}
		to, clear := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query())
		if to != "/s/ABC/settings" || !clear {
			t.Fatalf("%q %v", to, clear)
		}
		if len(fixed) != 1 || fixed[0] != "U1" {
			t.Fatalf("%v", fixed)
		}
		// The attempt is used once.
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/settings?github_error=expired" {
			t.Fatalf("a replay: %q", to)
		}
		c, err := h.svc.Connection(ctx, "U1")
		if err != nil || c == nil || c.Login != "kazuha-gh" || c.State != "ok" || c.RefreshExpires.IsZero() || c.Spaces != 0 {
			t.Fatalf("%+v %v", c, err)
		}
		// Both tokens are in the database only sealed, and not in the log.
		row, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U1")
		for _, plain := range []string{h.f.AccessTokenOf("kazuha-gh"), h.f.RefreshTokenOf("kazuha-gh")} {
			if plain == "" || strings.Contains(row.AccessEnc+row.RefreshEnc, plain) {
				t.Fatalf("a token is stored in the clear: %q", plain)
			}
		}
		// A sealed token does not open for another user.
		if _, err := h.box.Open("U2", "github-access", row.AccessEnc); err == nil {
			t.Fatal("a token opened for another user")
		}
		h.mustNotLeak()

		// Reconnecting suggests the account.
		authURL, _, _ = h.svc.StartAuthorization(ctx, "U1", "/")
		if u, _ := url.Parse(authURL); u.Query().Get("login") != "kazuha-gh" {
			t.Fatalf("%s", authURL)
		}
	})
}

func TestAuthorizationFailures(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.user("U2", "b")
		h.f.AddUser("gh-a")
		h.f.AddUser("gh-b")

		// Cancelled on GitHub.
		authURL, binding, _ := h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ := url.Parse(h.f.Deny(authURL))
		if to, clear := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/settings?github_error=denied" || !clear {
			t.Fatalf("%q", to)
		}
		// More than ten minutes later.
		authURL, binding, _ = h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "gh-a"))
		h.clk.Advance(11 * time.Minute)
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/settings?github_error=expired" {
			t.Fatalf("%q", to)
		}
		// A repeated parameter.
		authURL, binding, _ = h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "gh-a"))
		qq := back.Query()
		qq.Add("code", "second")
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", qq); to != "/settings?github_error=expired" {
			t.Fatalf("%q", to)
		}
		// A code GitHub does not know.
		authURL, binding, _ = h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "gh-a"))
		qq = back.Query()
		qq.Set("code", "forged")
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", qq); to != "/settings?github_error=github_error" {
			t.Fatalf("%q", to)
		}
		// One GitHub account, one PetalBook user.
		if to := h.connect("U1", "gh-a"); to != "/s/ABC/settings" {
			t.Fatalf("%q", to)
		}
		authURL, binding, _ = h.svc.StartAuthorization(ctx, "U2", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "gh-a"))
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U2", back.Query()); to != "/settings?github_error=account_in_use" {
			t.Fatalf("%q", to)
		}
		// Another account while spaces sync through this one.
		h.space("S1", store.Remote{GHVia: "user", GHUserID: "U1", GHRepoID: 1})
		authURL, binding, _ = h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "gh-b"))
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/settings?github_error=different_account" {
			t.Fatalf("%q", to)
		}
		if c, _ := h.svc.Connection(ctx, "U1"); c.Login != "gh-a" || c.Spaces != 1 {
			t.Fatalf("%+v", c)
		}
		// The same account again replaces the tokens.
		old := h.f.AccessTokenOf("gh-a")
		h.connect("U1", "gh-a")
		if h.f.AccessTokenOf("gh-a") == old {
			t.Fatal("no new token")
		}
		tok, err := h.svc.UserToken(ctx, "U1")
		if err != nil || tok != h.f.AccessTokenOf("gh-a") {
			t.Fatalf("%q %v", tok, err)
		}
	})
}

func TestAttemptsAreBounded(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		var first string
		for i := 0; i < 10_010; i++ {
			_, b, err := h.svc.StartAuthorization(ctx, "U1", "/")
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				first = b
			}
		}
		if n := h.svc.Attempts(); n > 10_000 {
			t.Fatalf("%d attempts held", n)
		}
		if to, _ := h.svc.CompleteAuthorization(ctx, first, "U1", url.Values{"state": {"x"}, "code": {"y"}}); to != "/settings?github_error=expired" {
			t.Fatalf("the oldest attempt survived: %q", to)
		}
	})
}

func TestDisconnectRevokesAndDeletes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		access := h.f.AccessTokenOf("gh-a")
		if err := h.svc.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if c, _ := h.svc.Connection(ctx, "U1"); c != nil {
			t.Fatal("still connected")
		}
		if h.f.AccessTokenOf("gh-a") != "" || access == "" {
			t.Fatal("GitHub was not asked to revoke the grant")
		}
		if _, err := h.svc.UserToken(ctx, "U1"); !errors.Is(err, github.ErrNoAuthorization) {
			t.Fatalf("%v", err)
		}
		// Even when GitHub refuses, the tokens are gone.
		h.connect("U1", "gh-a")
		h.f.RevokeUser("gh-a")
		if err := h.svc.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetGitHubAuthorization(ctx, st.Q(), "U1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		// Disconnecting twice is fine.
		if err := h.svc.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTokenIsRenewedBeforeItEndsAndTheNewPairIsStoredFirst(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		first, _ := h.svc.UserToken(ctx, "U1")
		// Reused while it has more than five minutes left, without a request.
		h.clk.Advance(7*time.Hour + 50*time.Minute)
		before := h.f.Count("POST", "/login/oauth/access_token")
		if again, _ := h.svc.UserToken(ctx, "U1"); again != first || h.f.Count("POST", "/login/oauth/access_token") != before {
			t.Fatal("a token with ten minutes left was renewed")
		}
		h.clk.Advance(6 * time.Minute) // four minutes left
		refreshBefore := h.f.RefreshTokenOf("gh-a")
		fresh, err := h.svc.UserToken(ctx, "U1")
		if err != nil || fresh == first || fresh != h.f.AccessTokenOf("gh-a") {
			t.Fatalf("%q %v", fresh, err)
		}
		// What was stored is the pair GitHub holds now: the old refresh token is dead.
		row, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U1")
		access, err := h.box.Open("U1", "github-access", row.AccessEnc)
		if err != nil || string(access) != fresh {
			t.Fatalf("the stored access token is not the one in use: %v", err)
		}
		refresh, err := h.box.Open("U1", "github-refresh", row.RefreshEnc)
		if err != nil || string(refresh) != h.f.RefreshTokenOf("gh-a") || string(refresh) == refreshBefore {
			t.Fatalf("the stored refresh token is not GitHub's current one: %v", err)
		}
		if row.Version != 2 || !row.AccessExpires.After(h.clk.Now().Add(7*time.Hour)) {
			t.Fatalf("%+v", row)
		}
		h.mustNotLeak(fresh, string(refresh))
	})
}

func TestOneRenewalAtATimePerUser(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		h.clk.Advance(9 * time.Hour)
		before := h.f.Count("POST", "/login/oauth/access_token")
		var wg sync.WaitGroup
		got := make([]string, 12)
		for i := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				if got[i], err = h.svc.UserToken(ctx, "U1"); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if n := h.f.Count("POST", "/login/oauth/access_token") - before; n != 1 {
			t.Fatalf("%d renewals", n)
		}
		for _, g := range got {
			if g != got[0] || g != h.f.AccessTokenOf("gh-a") {
				t.Fatalf("%q", got)
			}
		}
	})
}

func TestARefusedRefreshTokenExpiresTheConnection(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		h.f.ExpireRefresh("gh-a")
		h.clk.Advance(9 * time.Hour)
		if _, err := h.svc.UserToken(ctx, "U1"); !errors.Is(err, github.ErrAuthorizationExpired) {
			t.Fatalf("%v", err)
		}
		row, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U1")
		if row.State != "expired" || row.AccessEnc != "" || row.RefreshEnc != "" {
			t.Fatalf("%+v", row)
		}
		c, _ := h.svc.Connection(ctx, "U1")
		if c.State != "expired" {
			t.Fatalf("%+v", c)
		}
		// Not asked again while expired; reconnecting fixes it.
		n := h.f.Count("POST", "/login/oauth/access_token")
		h.svc.UserToken(ctx, "U1")
		if h.f.Count("POST", "/login/oauth/access_token") != n {
			t.Fatal("an expired connection asked GitHub again")
		}
		h.connect("U1", "gh-a")
		if _, err := h.svc.UserToken(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnreachableGitHubChangesNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		h.clk.Advance(9 * time.Hour)
		h.f.LimitUntil(h.clk.Now().Add(time.Minute))
		_, err := h.svc.UserToken(ctx, "U1")
		var he *github.Error
		if !errors.As(err, &he) || he.Code != "rate_limited" || he.RetryAfter < 59*time.Second {
			t.Fatalf("%v", err)
		}
		if row, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U1"); row.State != "ok" || row.Version != 1 {
			t.Fatalf("%+v", row)
		}
		h.clk.Advance(2 * time.Minute)
		if _, err := h.svc.UserToken(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		// A reply that never comes: the refresh token is spent but the pair was not used; the
		// next try reports an expired connection rather than corrupting anything.
		h.clk.Advance(9 * time.Hour)
		h.f.BreakRefreshReply = true
		if _, err := h.svc.UserToken(ctx, "U1"); err == nil {
			t.Fatal("a broken reply was accepted")
		}
		if row, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U1"); row.State != "ok" {
			t.Fatalf("%+v", row)
		}
	})
}

func TestDailyRenewalKeepsLongClosedConnections(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		for _, n := range []string{"1", "2", "3"} {
			h.user("U"+n, "u"+n)
			h.f.AddUser("gh" + n)
			h.connect("U"+n, "gh"+n)
		}
		// Nothing is due while the refresh tokens have months left, and no request is made for it.
		before := h.f.Count("POST", "/login/oauth/access_token")
		if n := h.svc.RenewDue(ctx); n != 0 || h.f.Count("POST", "/login/oauth/access_token") != before {
			t.Fatalf("%d renewed early", n)
		}
		// Five months on, the refresh tokens end within 30 days.
		h.clk.Advance(155 * 24 * time.Hour)
		oldRefresh := h.f.RefreshTokenOf("gh1")
		h.f.ExpireRefresh("gh3") // one of them was revoked meanwhile
		if n := h.svc.RenewDue(ctx); n != 2 {
			t.Fatalf("%d renewed", n)
		}
		if h.f.RefreshTokenOf("gh1") == oldRefresh || h.f.RefreshTokenOf("gh1") == "" {
			t.Fatal("no new refresh token")
		}
		row1, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U1")
		if row1.State != "ok" || !row1.RefreshExpires.After(h.clk.Now().Add(170*24*time.Hour)) {
			t.Fatalf("%+v", row1)
		}
		row3, _ := store.GetGitHubAuthorization(ctx, st.Q(), "U3")
		if row3.State != "expired" {
			t.Fatalf("%+v", row3)
		}
		// Run again at once: nobody is due now.
		before = h.f.Count("POST", "/login/oauth/access_token")
		if n := h.svc.RenewDue(ctx); n != 0 || h.f.Count("POST", "/login/oauth/access_token") != before {
			t.Fatalf("%d renewed twice", n)
		}
		h.mustNotLeak()
	})
}

func TestAnAppThatDoesNotExpireTokensGivesNoRefreshToken(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.f.NoRefresh = true
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		h.clk.Advance(400 * 24 * time.Hour)
		tok, err := h.svc.UserToken(ctx, "U1")
		if err != nil || tok == "" || h.f.Count("POST", "/login/oauth/access_token") != 1 {
			t.Fatalf("%q %v", tok, err)
		}
		if n := h.svc.RenewDue(ctx); n != 0 {
			t.Fatal(n)
		}
	})
}

func TestTheDailyJobRuns(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "a")
		h.f.AddUser("gh-a")
		h.connect("U1", "gh-a")
		h.clk.Advance(160 * 24 * time.Hour)
		old := h.f.RefreshTokenOf("gh-a")
		c, _ := githubapp.New(githubapp.Config{API: h.f.URL, Web: h.f.URL, Now: h.clk.Now})
		svc, err := github.New(github.Options{Store: st, Secrets: h.box, Client: c, PublicURL: publicURL, Now: h.clk.Now, RenewEvery: 20 * time.Millisecond, SettingsTTL: time.Nanosecond})
		if err != nil {
			t.Fatal(err)
		}
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { svc.RunRenew(runCtx); close(done) }()
		defer func() { stop(); <-done }()
		for i := 0; i < 200 && h.f.RefreshTokenOf("gh-a") == old; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if h.f.RefreshTokenOf("gh-a") == old {
			t.Fatal("the job did not renew the connection")
		}
	})
}
