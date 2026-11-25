package index

import (
	"os"
	"testing"

	"petalbook.app/server/internal/testguard"
)

// A test that runs away with memory fails here at a fixed size instead of
// growing until the machine's OOM killer stops it (see testguard).
func TestMain(m *testing.M) {
	testguard.Heap()
	os.Exit(m.Run())
}
