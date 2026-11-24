package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// A disabled or deleted person is never added to a space: the statement itself
// refuses them, so a person disabled for their deletion cannot be added between
// the check and the insert.
func TestAMemberIsNeverADisabledOrDeletedPerson(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		for _, id := range []string{"OWNER", "OFF", "GONE"} {
			mkUser(t, st, id, id, "member")
		}
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S000000001", "Team", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), "S000000001", "OWNER", "owner", now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `UPDATE users SET disabled = 1 WHERE id = 'OFF'`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `UPDATE users SET disabled = 1, deleted_at = 1 WHERE id = 'GONE'`); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"OFF", "GONE"} {
			if err := store.AddMember(ctx, st.Q(), "S000000001", id, "owner", now); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s was added: %v", id, err)
			}
		}
	})
}

// A deleted person is not "another owner": the last live owner cannot leave a
// space that a deleted person's leftover row would then own alone.
func TestADeletedPersonIsNotAnotherOwner(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		mkUser(t, st, "LIVE", "live", "member")
		mkUser(t, st, "GONE", "gone", "member")
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S000000001", "Team", "", "", now); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"LIVE", "GONE"} {
			if err := store.AddMember(ctx, st.Q(), "S000000001", id, "owner", now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.Q().Exec(ctx, `UPDATE users SET disabled = 1, deleted_at = 1 WHERE id = 'GONE'`); err != nil {
			t.Fatal(err)
		}
		err := st.Tx(ctx, func(q store.Q) error {
			_, _, err := store.LeaveSpace(ctx, q, "S000000001", "LIVE")
			return err
		})
		if !errors.Is(err, store.ErrLastOwnerOfSpace) {
			t.Fatalf("the last live owner left: %v", err)
		}
	})
}
