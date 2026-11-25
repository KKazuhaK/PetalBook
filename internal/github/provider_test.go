package github_test

import (
	"testing"

	"petalbook.app/server/internal/connections/conntest"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The GitHub implementation keeps the same contract as every provider.
func TestGitHubKeepsTheProviderContract(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("U1", "tang")
		// Nothing set up, then the App saved: the same store, so one after the other.
		t.Run("not set up", func(t *testing.T) {
			conntest.Run(t, conntest.Harness{Provider: h.svc, Unconfigured: h.svc, User: "U1"})
		})
		h.configure()
		t.Run("set up", func(t *testing.T) { conntest.Run(t, conntest.Harness{Provider: h.svc, User: "U1"}) })
	})
}
