package store_test

import (
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestRemotes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "admin")
		now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
		for _, id := range []string{"S1", "S2"} {
			if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, id, id, "", "", now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.GetRemote(ctx, st.Q(), "S1"); err != store.ErrNotFound {
			t.Fatalf("%v", err)
		}
		// A row that only holds a key pair is not a connected remote.
		if err := store.PutRemote(ctx, st.Q(), store.Remote{SpaceID: "S1", Branch: "main", CredType: "ssh", PubKey: "ssh-ed25519 AAAA", PrivKeyEnc: "enc", State: store.RemoteLocal, Updated: now}); err != nil {
			t.Fatal(err)
		}
		r, err := store.GetRemote(ctx, st.Q(), "S1")
		if err != nil || r.Connected() || r.PubKey != "ssh-ed25519 AAAA" || !r.LastSync.IsZero() {
			t.Fatalf("%+v %v", r, err)
		}
		if m, err := store.RemoteStates(ctx, st.Q()); err != nil || len(m) != 0 {
			t.Fatalf("%v %v", m, err)
		}
		// Connecting replaces the row; secrets are stored as given (encrypted by the caller).
		r.URL, r.State, r.TokenEnc, r.CredType, r.Username, r.LastGood = "https://github.com/a/b.git", store.RemoteOK, "tok-enc", "token", "x-access-token", "abc"
		r.Updated = now.Add(time.Minute)
		if err := store.PutRemote(ctx, st.Q(), *r); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetRemote(ctx, st.Q(), "S1")
		if err != nil || !got.Connected() || got.TokenEnc != "tok-enc" || got.PrivKeyEnc != "enc" || got.Username != "x-access-token" || got.LastGood != "abc" {
			t.Fatalf("%+v %v", got, err)
		}
		// A status update changes the status and keeps what it is not given.
		sync := now.Add(2 * time.Minute)
		if err := store.UpdateRemoteStatus(ctx, st.Q(), "S1", store.RemoteStatus{State: store.RemoteOffline, LastError: "could not reach the remote", LastSync: sync}, now); err != nil {
			t.Fatal(err)
		}
		got, _ = store.GetRemote(ctx, st.Q(), "S1")
		if got.State != "offline" || got.LastError != "could not reach the remote" || got.LastGood != "abc" || !got.LastSync.Equal(sync) {
			t.Fatalf("%+v", got)
		}
		if err := store.UpdateRemoteStatus(ctx, st.Q(), "S1", store.RemoteStatus{State: store.RemoteRewritten, ErrorKind: "", RemoteTip: "def", LastGood: "abc2"}, now); err != nil {
			t.Fatal(err)
		}
		got, _ = store.GetRemote(ctx, st.Q(), "S1")
		if got.State != "rewritten" || got.RemoteTip != "def" || got.LastGood != "abc2" || !got.LastSync.Equal(sync) {
			t.Fatalf("%+v", got)
		}
		m, err := store.RemoteStates(ctx, st.Q())
		if err != nil || len(m) != 1 || m["S1"] != "rewritten" {
			t.Fatalf("%v %v", m, err)
		}
		// An update for a space with no row does nothing.
		if err := store.UpdateRemoteStatus(ctx, st.Q(), "S2", store.RemoteStatus{State: store.RemoteOK}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetRemote(ctx, st.Q(), "S2"); err != store.ErrNotFound {
			t.Fatal("a row appeared")
		}
		// Disconnecting deletes everything, and so does deleting the space.
		if err := store.DeleteRemote(ctx, st.Q(), "S1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetRemote(ctx, st.Q(), "S1"); err != store.ErrNotFound {
			t.Fatal(err)
		}
	})
}

// The upsert and the clearing of the reminders are one step: when the second
// fails, the row is not left changed.
func TestPutRemoteIsAtomic(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `DROP TABLE github_leftovers`); err != nil {
			t.Fatal(err)
		}
		r := store.Remote{SpaceID: "S1", URL: "https://github.com/o/r.git", Branch: "main", CredType: "connection", State: store.RemoteOK, Updated: now, GHVia: "app", GHInstallationID: 1, GHRepoID: 7}
		if err := store.PutRemote(ctx, st.Q(), r); err == nil {
			t.Fatal("the reminders could not be cleared, and it did not fail")
		}
		if _, err := store.GetRemote(ctx, st.Q(), "S1"); err != store.ErrNotFound {
			t.Fatalf("the row stayed although clearing the reminders failed: %v", err)
		}
	})
}
