package store_test

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A disabled or deleted person is never added to a space: the statement itself
// refuses them, so a person disabled for their deletion cannot be added between
// the check and the insert.
func TestAMemberIsNeverADisabledOrDeletedPerson(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// The record of a deletion under way is dropped when the person is enabled
// again, and a deletion of a person who is not disabled starts afresh: a later
// deletion never takes an old attempt's "was disabled".
func TestAStaleDeletionRecordIsNotReused(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		mkUser(t, st, "OLIVE", "olive", "member")
		// A deletion of a disabled person, cut short; an administrator then
		// enables them again.
		if err := store.SetDisabled(ctx, q, "OLIVE", true); err != nil {
			t.Fatal(err)
		}
		if _, began, err := store.BeginUserDeletion(ctx, q, "OLIVE", true); err != nil || !began {
			t.Fatalf("%v %v", began, err)
		}
		if err := store.SetDisabled(ctx, q, "OLIVE", false); err != nil {
			t.Fatal(err)
		}
		p, began, err := store.BeginUserDeletion(ctx, q, "OLIVE", false)
		if err != nil || !began || p.WasDisabled {
			t.Fatalf("after enabling: %+v %v %v", p, began, err)
		}

		// A record left on a person who is not disabled (the attempt never
		// disabled them) is not reused either.
		if _, err := q.Exec(ctx, `UPDATE users SET delete_pending = '{"wasDisabled":true}', disabled = 0 WHERE id = 'OLIVE'`); err != nil {
			t.Fatal(err)
		}
		p, began, err = store.BeginUserDeletion(ctx, q, "OLIVE", false)
		if err != nil || !began || p.WasDisabled {
			t.Fatalf("enabled with a record: %+v %v %v", p, began, err)
		}

		// A deletion under way (the person disabled by it) is carried on: what
		// it recorded stays.
		if err := store.SetDisabled(ctx, q, "OLIVE", true); err != nil {
			t.Fatal(err)
		}
		if err := store.NoteUserDeletion(ctx, q, "OLIVE", &store.UserTransfer{Space: "S1", Name: "Team", To: "X"}, nil); err != nil {
			t.Fatal(err)
		}
		p, began, err = store.BeginUserDeletion(ctx, q, "OLIVE", true)
		if err != nil || began || p.WasDisabled || len(p.Transfers) != 1 {
			t.Fatalf("under way: %+v %v %v", p, began, err)
		}
	})
}

// The preview of a deletion fails when it cannot read the person's connections,
// rather than showing a plan that leaves some out.
func TestTheDeletionPreviewReturnsAConnectionsError(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		if _, err := store.PlanUserDeletion(ctx, st.Q(), store.AllTenants(), "U1", "", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `DROP TABLE provider_authorizations`); err != nil {
			t.Fatal(err)
		}
		if p, err := store.PlanUserDeletion(ctx, st.Q(), store.AllTenants(), "U1", "", nil); err == nil {
			t.Fatalf("no error, plan %+v", p)
		}
	})
}
