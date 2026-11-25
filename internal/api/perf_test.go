package api_test

import (
	"os"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// TestPerRequestCostOfOneTenant measures what a request costs on a server with
// one tenant: reading the session (Authenticate) and the permission check
// (opening a space), the two places the contract says tenants add nothing. It
// prints ns/op and allocs/op; set PETALBOOK_PERF=1 to run it, on this tree and
// on the one before tenants, and compare.
// Not parallel: it is a measurement of cost, which other tests running would distort.
func TestPerRequestCostOfOneTenant(t *testing.T) {
	if os.Getenv("PETALBOOK_PERF") == "" {
		t.Skip("set PETALBOOK_PERF=1 to measure")
	}
	needGit(t)
	st := storetest.Open(t, "sqlite")
	a := newAcc(t, st, accOpts{})
	c := a.setup("root", pw)
	sid := c.personalSpace()
	for _, m := range []struct{ name, path string }{{"session", "/api/session"}, {"open a space", "/api/spaces/" + sid + "/tags"}} {
		res := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if r := c.do("GET", m.path, nil); r.code != 200 {
					b.Fatal(r.raw)
				}
			}
		})
		t.Logf("PERF %-14s %8d ns/op %6d B/op %4d allocs/op", m.name, res.NsPerOp(), res.AllocedBytesPerOp(), res.AllocsPerOp())
	}
}
