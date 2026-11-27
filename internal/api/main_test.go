package api_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/testguard"
)

// Most tests make spaces, and each space an index: they copy a prepared one
// instead of creating the tables again (see index.ReuseSchema). A test that
// runs away with memory fails at a fixed size (see testguard).
func TestMain(m *testing.M) {
	testguard.Heap()
	index.ReuseSchema()
	violations := store.CheckAuditAddresses()
	code := m.Run()
	// An address in a place a kind does not declare is not redacted for a
	// tenant's administrator: declare it (auditlog.Kind.AddressFields) or keep it out.
	if v := violations(); len(v) > 0 {
		fmt.Fprintln(os.Stderr, "audit rows with an address outside the declared fields of their kind:\n  "+strings.Join(v, "\n  "))
		code = 1
	}
	os.Exit(code)
}
