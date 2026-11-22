package drafts

import (
	"fmt"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// A change to the user (here: disabled) reaches the sweeper at once, not after
// the minute the user is cached for.
func TestSweeperSeesAUserChangeOnTheNextSweep(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newShared(t, st)
		if ok, err := f.mgr.MayWrite(ctx, f.editor.ID, f.space); err != nil || !ok { // fills the cache
			t.Fatal(ok, err)
		}
		if err := store.SetDisabled(ctx, st.Q(), f.editor.ID, true); err != nil {
			t.Fatal(err)
		}
		f.editor.Disabled = true
		f.mgr.UserChanged(ctx, f.editor)
		if r := f.svc.Sweep(ctx); r.Applied != 0 || r.Denied != 1 || f.body() != "base\n" {
			t.Fatalf("%+v %q", r, f.body())
		}
	})
}

// Many refused drafts, older than the one that can be applied, do not hold it
// back: a sweep goes on to further batches while they fill the window.
func TestRefusedDraftsDoNotStarveOthers(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newShared(t, st)
		n := 3 * batch
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("X%03d", i)
			f.user(id, "gone"+id)
			if _, err := st.Q().Exec(ctx, `INSERT INTO drafts(space_id, page_id, user_id, version, base_rev, meta, body, created, updated, held, due)
				SELECT space_id, page_id, ?, 'v', base_rev, meta, body, created, updated, 0, due - 1000 FROM drafts WHERE user_id = ?`, id, f.editor.ID); err != nil {
				t.Fatal(err)
			}
		}
		r := f.svc.Sweep(ctx)
		if r.Denied != n || r.Applied != 1 || f.body() != "typed by the editor\n" {
			t.Fatalf("%+v %q", r, f.body())
		}
	})
}
