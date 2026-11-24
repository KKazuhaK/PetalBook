package api_test

import (
	"os"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

// Most tests make spaces, and each space an index: they copy a prepared one
// instead of creating the tables again (see index.ReuseSchema).
func TestMain(m *testing.M) {
	index.ReuseSchema()
	os.Exit(m.Run())
}
