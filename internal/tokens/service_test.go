package tokens_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
)

var ctx = context.Background()

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// users is the owner lookup: it counts how often it is asked and can say a
// user is disabled or gone.
type users struct {
	mu       sync.Mutex
	disabled map[string]bool
	gone     map[string]bool
}

func (u *users) CachedUser(_ context.Context, id string) (*store.User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gone[id] {
		return nil, store.ErrNotFound
	}
	return &store.User{ID: id, Username: id, Disabled: u.disabled[id]}, nil
}

type fixture struct {
	t   *testing.T
	st  *store.Store
	clk *clock
	us  *users
	svc *tokens.Service
}

func newFixture(t *testing.T, st *store.Store) *fixture {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)}
	us := &users{disabled: map[string]bool{}, gone: map[string]bool{}}
	for _, id := range []string{"A", "B"} {
		if err := store.InsertUser(ctx, st.Q(), store.User{ID: id, Username: strings.ToLower(id), Name: id, Role: "member", Created: clk.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	svc := tokens.New(tokens.Config{Store: st, Users: us, Now: clk.Now})
	return &fixture{t: t, st: st, clk: clk, us: us, svc: svc}
}

func (f *fixture) create(user, access string, days int, spaces ...string) (store.APIToken, string) {
	f.t.Helper()
	tok, secret, err := f.svc.Create(ctx, tokens.CreateParams{UserID: user, Name: "n", Access: access, Spaces: spaces, ExpiresIn: time.Duration(days) * 24 * time.Hour})
	if err != nil {
		f.t.Fatal(err)
	}
	return tok, secret
}

func (f *fixture) auth(secret string) error {
	_, err := f.svc.Authenticate(ctx, secret, "198.51.100.1")
	return err
}

func code(err error) string {
	var te *tokens.Error
	if errors.As(err, &te) {
		return te.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func each(t *testing.T, fn func(t *testing.T, f *fixture)) {
	storetest.Each(t, func(t *testing.T, st *store.Store) { fn(t, newFixture(t, st)) })
}

func TestAuthenticate(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		tok, secret := f.create("A", "write", 90, "S2", "S1")
		p, err := f.svc.Authenticate(ctx, secret, "198.51.100.1")
		if err != nil {
			t.Fatal(err)
		}
		g := p.Grant
		if p.User.ID != "A" || g == nil || g.Kind != "token" || g.ID != tok.ID || !g.Write || g.Access() != "write" ||
			fmt.Sprint(g.Spaces) != "[S1 S2]" || !g.Names("S1") || g.Names("S3") || p.SessionID != "" {
			t.Fatalf("%+v %+v", p, g)
		}
		// What is stored is the hash, never the secret.
		var n int
		if err := f.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM api_tokens WHERE hash = ? OR prefix = ? OR name = ?`, secret, secret, secret).Scan(&n); err != nil || n != 0 {
			t.Fatal(n, err)
		}
		if tok.Prefix != secret[:12] || tok.Hash != tokens.HashSecret(secret) {
			t.Fatalf("%+v", tok)
		}
	})
}

func TestReadIsTheDefaultAccess(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "", 0, "S1")
		p, err := f.svc.Authenticate(ctx, secret, "ip")
		if err != nil || p.Grant.Write || p.Grant.Perms().Has(2) {
			t.Fatalf("%+v %v", p, err)
		}
	})
}

func TestRefusals(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, good := f.create("A", "read", 90, "S1")
		_, expiring := f.create("A", "read", 1, "S1")
		rtok, revoked := f.create("A", "read", 90, "S1")
		if ok, err := f.svc.Revoke(ctx, rtok.ID); err != nil || !ok {
			t.Fatal(ok, err)
		}
		f.clk.Advance(25 * time.Hour)
		unknown := "pbk_" + strings.Repeat("z", 43)
		for _, c := range []struct{ name, secret, want string }{
			{"valid", good, ""},
			{"empty", "", "token_invalid"},
			{"malformed", "pbk_short", "token_invalid"},
			{"another prefix", "rbk_" + strings.Repeat("a", 43), "token_invalid"},
			{"unknown", unknown, "token_invalid"},
			{"expired", expiring, "token_expired"},
			{"revoked", revoked, "token_revoked"},
		} {
			f.svc = tokens.New(tokens.Config{Store: f.st, Users: f.us, Now: f.clk.Now}) // a process that has seen nothing
			if got := code(f.auth(c.secret)); got != c.want {
				t.Errorf("%s: %q, want %q", c.name, got, c.want)
			}
		}
	})
}

func TestExpiryHappensInMemoryToo(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "read", 1, "S1")
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(24*time.Hour - time.Second)
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(2 * time.Second) // the entry in memory is only seconds old
		if got := code(f.auth(secret)); got != "token_expired" {
			t.Fatal(got)
		}
	})
}

func TestNeverExpires(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		tok, secret := f.create("A", "read", 0, "S1")
		if !tok.Expires.IsZero() {
			t.Fatal(tok.Expires)
		}
		f.clk.Advance(5 * 365 * 24 * time.Hour)
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRevokeThroughTheServiceIsImmediate(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		tok, secret := f.create("A", "write", 90, "S1")
		if err := f.auth(secret); err != nil { // in memory now
			t.Fatal(err)
		}
		if ok, err := f.svc.Revoke(ctx, tok.ID); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if got := code(f.auth(secret)); got != "token_revoked" {
			t.Fatal(got)
		}
		if ok, err := f.svc.Revoke(ctx, tok.ID); err != nil || !ok { // again: fine
			t.Fatal(ok, err)
		}
		if ok, err := f.svc.Revoke(ctx, "NOSUCHTOKN"); err != nil || ok {
			t.Fatal(ok, err)
		}
	})
}

func TestRevokeByAnotherProcessWithinTheTimeToLive(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		tok, secret := f.create("A", "write", 90, "S1")
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		// The command line, another process, revokes it in the database.
		if _, err := store.RevokeAPIToken(ctx, f.st.Q(), tok.ID, f.clk.Now()); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(20 * time.Second)
		if err := f.auth(secret); err != nil {
			t.Fatalf("a request inside the time to live reads memory only: %v", err)
		}
		f.clk.Advance(11 * time.Second)
		if got := code(f.auth(secret)); got != "token_revoked" {
			t.Fatal(got)
		}
	})
}

func TestACachedTokenCostsNoQuery(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "write", 90, "S1")
		// A new process: the first sight is one query.
		f.svc = tokens.New(tokens.Config{Store: f.st, Users: f.us, Now: f.clk.Now})
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		// Take the row away behind the service's back: only a request that
		// asks the database could notice.
		if _, err := f.st.Q().Exec(ctx, `DELETE FROM api_tokens`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 100; i++ {
			if err := f.auth(secret); err != nil {
				t.Fatalf("request %d asked the database: %v", i, err)
			}
		}
		f.clk.Advance(31 * time.Second) // read again
		if got := code(f.auth(secret)); got != "token_invalid" {
			t.Fatal(got)
		}
	})
}

func TestUnknownIsRememberedAndAFreshOneIsFound(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		secret, _ := tokens.Generate()
		if got := code(f.auth(secret)); got != "token_invalid" {
			t.Fatal(got)
		}
		// Not asked again for 30 seconds: a token made by the command line in
		// that time is found after it.
		if _, err := f.st.Q().Exec(ctx, `INSERT INTO api_tokens(id, user_id, name, prefix, hash, access, created) VALUES ('X1', 'A', 'n', 'p', ?, 'read', 0)`, tokens.HashSecret(secret)); err != nil {
			t.Fatal(err)
		}
		if got := code(f.auth(secret)); got != "token_invalid" {
			t.Fatal(got)
		}
		f.clk.Advance(31 * time.Second)
		if got := code(f.auth(secret)); got != "" { // no expiry, owner A: valid
			t.Fatal(got)
		}
	})
}

func TestOwnerDisabledOrGone(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "read", 90, "S1")
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		f.us.mu.Lock()
		f.us.disabled["A"] = true
		f.us.mu.Unlock()
		if got := code(f.auth(secret)); got != "token_owner_disabled" {
			t.Fatal(got)
		}
		// Enabled again: the token works again.
		f.us.mu.Lock()
		f.us.disabled["A"] = false
		f.us.mu.Unlock()
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		f.us.mu.Lock()
		f.us.gone["A"] = true
		f.us.mu.Unlock()
		if got := code(f.auth(secret)); got != "token_invalid" {
			t.Fatal(got)
		}
	})
}

func TestTheLimiterBlocksAfterTenFailures(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "read", 90, "S1")
		bad := "pbk_" + strings.Repeat("q", 43)
		for i := 0; i < 10; i++ {
			if _, err := f.svc.Authenticate(ctx, bad, "203.0.113.9"); code(err) != "token_invalid" {
				t.Fatalf("attempt %d: %v", i, err)
			}
		}
		// Even the right token is turned away from that address ...
		_, err := f.svc.Authenticate(ctx, secret, "203.0.113.9")
		var te *tokens.Error
		if !errors.As(err, &te) || te.Status != 429 || te.Code != "rate_limited" || te.RetryAfter < time.Second || te.RetryAfter > time.Minute+time.Second {
			t.Fatalf("%+v", err)
		}
		// ... and no other.
		if _, err := f.svc.Authenticate(ctx, secret, "203.0.113.10"); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(time.Minute + time.Second)
		if _, err := f.svc.Authenticate(ctx, secret, "203.0.113.9"); err != nil {
			t.Fatalf("still blocked after the window: %v", err)
		}
	})
}

func TestOnlyFailedTokensCount(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "read", 90, "S1")
		f.us.mu.Lock()
		f.us.disabled["A"] = true
		f.us.mu.Unlock()
		for i := 0; i < 30; i++ { // a disabled owner is not a guess
			if got := code(f.auth(secret)); got != "token_owner_disabled" {
				t.Fatalf("attempt %d: %q", i, got)
			}
		}
	})
}

func TestSwitchedOff(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		_, secret := f.create("A", "read", 90, "S1")
		a, err := f.svc.Access(ctx)
		if err != nil || !a.Tokens || !a.MCP {
			t.Fatalf("%+v %v", a, err)
		}
		off := false
		if a, err = f.svc.SetAccess(ctx, &off, nil); err != nil || a.Tokens || !a.MCP {
			t.Fatalf("%+v %v", a, err)
		}
		if got := code(f.auth(secret)); got != "tokens_off" {
			t.Fatal(got)
		}
		// Nothing was deleted; on again, it works. A second process sees the
		// change within 30 seconds.
		other := tokens.New(tokens.Config{Store: f.st, Users: f.us, Now: f.clk.Now})
		if _, err := other.Authenticate(ctx, secret, "ip"); code(err) != "tokens_off" {
			t.Fatal(err)
		}
		on := true
		if _, err = f.svc.SetAccess(ctx, &on, nil); err != nil {
			t.Fatal(err)
		}
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(31 * time.Second)
		if _, err := other.Authenticate(ctx, secret, "ip"); err != nil {
			t.Fatal(err)
		}
		// MCP alone is the other switch.
		if a, err = f.svc.SetAccess(ctx, nil, &off); err != nil || !a.Tokens || a.MCP {
			t.Fatalf("%+v %v", a, err)
		}
	})
}

func TestLastUsedIsWrittenOncePerHour(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		tok, secret := f.create("A", "read", 90, "S1")
		lastUsed := func() time.Time {
			rows, err := f.svc.List(ctx, store.TokenFilter{ID: tok.ID})
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			return rows[0].LastUsed
		}
		if !lastUsed().IsZero() {
			t.Fatal("used before use")
		}
		if err := f.auth(secret); err != nil {
			t.Fatal(err)
		}
		first := lastUsed()
		if !first.Equal(f.clk.Now()) {
			t.Fatalf("%v", first)
		}
		f.clk.Advance(20 * time.Minute)
		f.auth(secret)
		if !lastUsed().Equal(first) {
			t.Fatal("written again within the hour")
		}
		f.clk.Advance(41 * time.Minute)
		f.auth(secret)
		if !lastUsed().Equal(f.clk.Now()) {
			t.Fatalf("%v", lastUsed())
		}
	})
}

func TestCreateRules(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		bad := func(name string, p tokens.CreateParams) {
			t.Helper()
			_, _, err := f.svc.Create(ctx, p)
			var inv *tokens.Invalid
			if !errors.As(err, &inv) {
				t.Errorf("%s: %v", name, err)
			}
		}
		ok := tokens.CreateParams{UserID: "A", Name: "n", Spaces: []string{"S1"}}
		with := func(f func(*tokens.CreateParams)) tokens.CreateParams { p := ok; f(&p); return p }
		bad("no name", with(func(p *tokens.CreateParams) { p.Name = "  " }))
		bad("long name", with(func(p *tokens.CreateParams) { p.Name = strings.Repeat("x", 81) }))
		bad("control character", with(func(p *tokens.CreateParams) { p.Name = "a\nb" }))
		bad("access", with(func(p *tokens.CreateParams) { p.Access = "admin" }))
		bad("no spaces", with(func(p *tokens.CreateParams) { p.Spaces = nil }))
		bad("empty space id", with(func(p *tokens.CreateParams) { p.Spaces = []string{""} }))
		many := make([]string, 101)
		for i := range many {
			many[i] = fmt.Sprintf("S%03d", i)
		}
		bad("101 spaces", with(func(p *tokens.CreateParams) { p.Spaces = many }))
		hundred := with(func(p *tokens.CreateParams) { p.Spaces = many[:100] })
		if tok, _, err := f.svc.Create(ctx, hundred); err != nil || len(tok.Spaces) != 100 {
			t.Fatal(err)
		}
		// Repeats are one space; the name is trimmed; 80 characters (of any width) is fine.
		tok, _, err := f.svc.Create(ctx, tokens.CreateParams{UserID: "A", Name: " " + strings.Repeat("花", 80) + " ", Spaces: []string{"S1", "S1"}})
		if err != nil || len(tok.Spaces) != 1 || tok.Name != strings.Repeat("花", 80) {
			t.Fatalf("%+v %v", tok, err)
		}
	})
}

func TestAtMostFiftyLiveTokens(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		var ids []string
		for i := 0; i < 50; i++ {
			tok, _ := f.create("A", "read", 90, "S1")
			ids = append(ids, tok.ID)
			f.clk.Advance(time.Second)
		}
		_, _, err := f.svc.Create(ctx, tokens.CreateParams{UserID: "A", Name: "n", Spaces: []string{"S1"}})
		if !errors.Is(err, tokens.ErrLimit) {
			t.Fatal(err)
		}
		// Somebody else is not held back by it.
		f.create("B", "read", 90, "S1")
		// A revoked token no longer counts.
		if _, err := f.svc.Revoke(ctx, ids[0]); err != nil {
			t.Fatal(err)
		}
		f.create("A", "read", 90, "S1")
		// Nor does an expired one.
		if _, _, err := f.svc.Create(ctx, tokens.CreateParams{UserID: "A", Name: "n", Spaces: []string{"S1"}}); !errors.Is(err, tokens.ErrLimit) {
			t.Fatal(err)
		}
		f.clk.Advance(91 * 24 * time.Hour)
		f.create("A", "read", 90, "S1")
	})
}

func TestSweepKeepsEndedTokensForNinetyDays(t *testing.T) {
	each(t, func(t *testing.T, f *fixture) {
		revoked, secret := f.create("A", "read", 90, "S1")
		keep, keepSecret := f.create("A", "read", 0, "S1")
		if _, err := f.svc.Revoke(ctx, revoked.ID); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(89 * 24 * time.Hour)
		f.svc.Sweep(ctx)
		rows, _ := f.svc.List(ctx, store.TokenFilter{UserID: "A"})
		if len(rows) != 2 {
			t.Fatalf("%d tokens after 89 days", len(rows))
		}
		// Until then the token answers revoked, not "unknown".
		f2 := tokens.New(tokens.Config{Store: f.st, Users: f.us, Now: f.clk.Now})
		if _, err := f2.Authenticate(ctx, secret, "ip"); code(err) != "token_revoked" {
			t.Fatal(err)
		}
		f.clk.Advance(2 * 24 * time.Hour)
		f.svc.Sweep(ctx)
		rows, _ = f.svc.List(ctx, store.TokenFilter{UserID: "A"})
		if len(rows) != 1 || rows[0].ID != keep.ID {
			t.Fatalf("%+v", rows)
		}
		f3 := tokens.New(tokens.Config{Store: f.st, Users: f.us, Now: f.clk.Now})
		if _, err := f3.Authenticate(ctx, secret, "ip"); code(err) != "token_invalid" {
			t.Fatal(err)
		}
		if _, err := f3.Authenticate(ctx, keepSecret, "ip"); err != nil {
			t.Fatal(err)
		}
	})
}
