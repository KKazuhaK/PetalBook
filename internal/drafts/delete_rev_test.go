package drafts

import (
	"errors"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// A versioned delete that also names the file rev is refused once the file has
// moved: the client was about to give its draft up believing its text was the
// file's, and it is not any more.
func TestDeleteWithARevIsRefusedOnceTheFileMoved(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "typed\n")
		v := f.versions[p.ID]
		saved, err := f.svc.SaveFile(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, p.Rev, page.Meta{{Key: "title", Value: "T"}}, "elsewhere\n")
		if err != nil {
			t.Fatal(err)
		}
		var c *Conflict
		err = f.svc.Delete(ctx, f.sp, spaces.SingleID, p.ID, store.LocalUserID, v, p.Rev)
		if !errors.As(err, &c) || c.Draft == nil || c.Draft.Version != v || c.Current.Rev != saved.Rev || f.count() != 1 {
			t.Fatalf("want a conflict with the draft kept, got %v (drafts %d)", err, f.count())
		}
		// Naming the rev the file is at now, the person's choice goes through.
		if err := f.svc.Delete(ctx, f.sp, spaces.SingleID, p.ID, store.LocalUserID, v, saved.Rev); err != nil || f.count() != 0 {
			t.Fatalf("%v %d", err, f.count())
		}
	})
}
