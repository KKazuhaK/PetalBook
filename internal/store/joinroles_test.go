package store_test

import (
	"slices"
	"testing"

	"petalbook.app/server/internal/store"
)

// The roles a person joining by domain may be given are account roles that
// exist and are not the administrator's; the default is one of them.
func TestJoinRolesAreNonAdministratorAccountRoles(t *testing.T) {
	if len(store.JoinRoles) == 0 || !slices.Contains(store.JoinRoles, store.DefaultJoinRole) {
		t.Fatalf("%v %q", store.JoinRoles, store.DefaultJoinRole)
	}
	for _, r := range store.JoinRoles {
		if store.IsAdminRole(r) || !slices.Contains(store.UserRoles, r) {
			t.Errorf("%q is not a non-administrator account role", r)
		}
	}
}
