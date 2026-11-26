package auth

import (
	"context"
	"net/url"
	"sync"
	"time"

	"petalbook.app/server/internal/store"
)

// A test sign-in lets an administrator see what a provider says about them (the
// subject, the address and whether the trust rules would verify it, the name)
// without making, linking or signing in anything: it is how an address attribute
// is mapped, and how a provider that is still off is tried. What it found is
// kept in memory for the administrator who ran it, to read once.

const (
	testTTL  = 5 * time.Minute
	testKeep = 64
)

// TestResult is what a provider said in a test sign-in.
type TestResult struct {
	Subject      string
	Email        string
	EmailTrusted bool
	Name         string
	Username     string
	At           time.Time
}

type providerTests struct {
	mu sync.Mutex
	m  map[string]TestResult
}

func testKey(userID, providerID string) string { return userID + "\x00" + providerID }

func (s *Service) keepTest(userID, providerID string, c claimed) {
	now := s.now()
	r := TestResult{Subject: c.subject, Email: c.email, EmailTrusted: c.emailVerified, Name: clip(c.name, 100), Username: clip(c.preferred, 100), At: now}
	s.ptests.mu.Lock()
	defer s.ptests.mu.Unlock()
	if s.ptests.m == nil {
		s.ptests.m = map[string]TestResult{}
	}
	for k, v := range s.ptests.m {
		if now.Sub(v.At) > testTTL {
			delete(s.ptests.m, k)
		}
	}
	for len(s.ptests.m) >= testKeep { // bounded: the oldest goes
		oldest, at := "", now
		for k, v := range s.ptests.m {
			if oldest == "" || v.At.Before(at) {
				oldest, at = k, v.At
			}
		}
		delete(s.ptests.m, oldest)
	}
	s.ptests.m[testKey(userID, providerID)] = r
}

// TestBegin starts a test sign-in at a site provider, on or off, for the
// administrator u. The browser comes back to the providers page.
func (s *Service) TestBegin(ctx context.Context, providerID string, u *store.User, ip string) (*BeginResult, error) {
	rt := s.provs.getID(providerID)
	if rt == nil || rt.row.Owner != store.ProviderSite {
		return nil, errf(NotFound, "No such provider.")
	}
	ret := "/admin/security/providers?provider_test=" + url.QueryEscape(rt.row.ID)
	return s.SSOBegin(ctx, rt.row.Kind, rt.row.Slug, BeginParams{Intent: "test", Return: ret, User: u}, ip)
}

// TenantDeleted re-reads every provider after a tenant was deleted: the
// providers the tenant owned are gone (with the accounts linked to them), and
// one that put new accounts there went back to the default tenant.
func (s *Service) TenantDeleted(ctx context.Context) {
	providerMu.Lock()
	defer providerMu.Unlock()
	var ids []string
	for _, rt := range s.provs.all() {
		ids = append(ids, rt.row.ID)
	}
	if err := s.reloadProviders(ctx, ids...); err != nil {
		s.log.Warn("could not reload the providers after a tenant was deleted", "error", err)
	}
}

// TestResultOf is what the last test sign-in of the administrator at the
// provider found, once: nil when there is none, or it is older than five minutes.
func (s *Service) TestResultOf(userID, providerID string) *TestResult {
	s.ptests.mu.Lock()
	defer s.ptests.mu.Unlock()
	r, ok := s.ptests.m[testKey(userID, providerID)]
	if !ok {
		return nil
	}
	delete(s.ptests.m, testKey(userID, providerID))
	if s.now().Sub(r.At) > testTTL {
		return nil
	}
	return &r
}
