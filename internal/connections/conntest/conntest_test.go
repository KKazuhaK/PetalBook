package conntest_test

import (
	"testing"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/connections/conntest"
)

func TestTheFakeProviderKeepsTheContract(t *testing.T) {
	un := conntest.NewFake("fake")
	un.Configured = false
	conntest.Run(t, conntest.Harness{Provider: conntest.NewFake("fake"), Unconfigured: un, User: "U1"})
}

func TestPlainGitKeepsTheContract(t *testing.T) {
	conntest.Run(t, conntest.Harness{Provider: connections.NewGit(), User: "U1"})
}

func TestTheRegistryOrdersAndFinds(t *testing.T) {
	g, a, b := connections.NewGit(), conntest.NewFake("zeta"), conntest.NewFake("alpha")
	r := connections.NewRegistry(g, a, b)
	var ids []string
	for _, p := range r.List() {
		ids = append(ids, p.Descriptor().ID)
	}
	// By name (the fakes' names follow their ids), the plain address last.
	if len(ids) != 3 || ids[0] != "alpha" || ids[1] != "zeta" || ids[2] != "git" {
		t.Fatalf("%v", ids)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("found a provider that is not there")
	}
	var nilReg *connections.Registry
	if _, ok := nilReg.Get("git"); ok || len(nilReg.List()) != 0 {
		t.Fatal("a nil registry has providers")
	}
	var got []string
	r.OnChange(func(id, user string) { got = append(got, id+":"+user) })
	a.Connect("U1", "x")
	if len(got) != 1 || got[0] != "zeta:U1" {
		t.Fatalf("%v", got)
	}
}
