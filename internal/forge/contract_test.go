package forge_test

import (
	"testing"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/connections/conntest"
	"petalbook.app/server/internal/forge"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Both implementations keep the contract of 2.7b's providers: the same checks
// run against the fake provider, the GitHub implementation, plain Git, and
// these two.
func TestTheForgesKeepTheProviderContract(t *testing.T) {
	needGit(t)
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				h := newHarness(t, st, kind)
				h.user("U1", "tang")
				p := h.add(kind)
				// A provider whose host failed its check is not set up: everything of
				// it answers provider_not_configured, as an App that is not saved does.
				other := newFake(t, kind, h.clk)
				un, _, err := h.mgr.CreateProvider(ctx, "", h.bodyFor(other, kind+"-down", nil))
				if err != nil {
					t.Fatal(err)
				}
				un.(interface{ MarkError() }).MarkError()
				conntest.Run(t, conntest.Harness{Provider: p, Unconfigured: un, User: "U1"})
			})
		})
	}
}

var _ connections.Creator = (*forge.Manager)(nil)
