package spaces

import (
	"os"
	"testing"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/testguard"
)

// Most tests make spaces, and each space an index: they copy a prepared one
// instead of creating the tables again (see index.ReuseSchema). A test that
// runs away with memory fails at a fixed size (see testguard).
func TestMain(m *testing.M) {
	testguard.Heap()
	index.ReuseSchema()
	os.Exit(m.Run())
}
