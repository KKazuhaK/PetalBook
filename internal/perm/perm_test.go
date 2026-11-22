package perm

import (
	"fmt"
	"testing"
)

func TestRoles(t *testing.T) {
	for role, want := range map[string]string{
		"owner":  "[read write members manage]",
		"editor": "[read write]",
		"viewer": "[read]",
	} {
		set, ok := ForRole(role)
		if !ok || !ValidRole(role) || fmt.Sprint(set.Names()) != want {
			t.Errorf("%s: %v %v", role, ok, set.Names())
		}
	}
	for _, bad := range []string{"", "Owner", "admin", "member", "owner "} {
		if set, ok := ForRole(bad); ok || set != 0 || ValidRole(bad) {
			t.Errorf("%q is not a role", bad)
		}
	}
	if fmt.Sprint(RoleNames()) != "[owner editor viewer]" {
		t.Fatal(RoleNames())
	}
}

func TestSets(t *testing.T) {
	rw := Of(Read, Write)
	if !rw.Has(Read) || !rw.Has(Read, Write) || rw.Has(Manage) || rw.Has(Read, Members) || !rw.Has() {
		t.Fatal("Has")
	}
	if Set(0).Has(Read) || !All.Has(Read, Write, Members, Manage) {
		t.Fatal("empty and all")
	}
	if n := Set(0).Names(); n == nil || len(n) != 0 {
		t.Fatalf("an empty set lists as an empty array, not null: %#v", n)
	}
	if Manage.String() != "manage" || Perm(0).String() != "unknown" {
		t.Fatal("String")
	}
}
