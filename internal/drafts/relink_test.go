package drafts

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// A draft on a page whose links the service rewrites (a page it links to was
// renamed) is not made stale by it: the person is never asked to choose, and
// what is written has the new path, with the draft's text.
func TestDraftsSurviveALinkRewrite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		tg := f.page("目标", "x\n")
		ref := f.page("引用", "见[目标](目标.md)。\n")
		other := f.page("另一篇", "[t](目标.md)\n")
		// A draft held by the server, and an editor open on another referrer.
		f.put(ref, ref.Rev, "见[目标](目标.md)。\n\n草稿里的新段。\n")
		if _, err := f.sp.Save(space.Author{}, tg.ID, tg.Rev, page.Meta{{Key: "title", Value: "新名"}}, "x\n"); err != nil {
			t.Fatal(err)
		}
		cur, _ := f.sp.Get(ref.ID)
		if cur.Rev == ref.Rev || !strings.Contains(cur.Body, "新名.md") {
			t.Fatalf("not rewritten: %q", cur.Body)
		}

		// Opening the page: the draft is not stale and shows the new path.
		d, err := f.svc.Get(ctx, f.sp, spaces.SingleID, ref.ID, store.LocalUserID, cur.Rev)
		if err != nil || d == nil || d.Stale || d.BaseRev != cur.Rev || !strings.Contains(d.Body, "[目标](新名.md)") || !strings.Contains(d.Body, "草稿里的新段") {
			t.Fatalf("%+v %v", d, err)
		}
		// Typing on in the open editor, still on the old base: not stale.
		meta := page.Meta{{Key: "title", Value: "引用"}}
		stored, err := f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, ref.ID, f.versions[ref.ID], ref.Rev, meta, "见[目标](目标.md)。\n\n草稿里的新段，又改了。\n")
		if err != nil || stored.Stale {
			t.Fatalf("%+v %v", stored, err)
		}

		// The sweeper applies it: nothing held, the text kept, the new path.
		f.clk.t = f.clk.t.Add(time.Hour)
		if res := f.svc.Sweep(ctx); res.Applied != 1 || res.Held != 0 {
			t.Fatalf("%+v", res)
		}
		got, _ := f.sp.Get(ref.ID)
		if !strings.Contains(got.Body, "见[目标](新名.md \""+tg.ID+"\")。\n\n草稿里的新段，又改了。") || strings.Contains(got.Body, "(目标.md") {
			t.Fatalf("%q", got.Body)
		}

		// An open editor saving with the rev it loaded (PUT page).
		p, err := f.svc.SaveFile(ctx, f.sp, spaces.SingleID, store.LocalUserID, other.ID, other.Rev, page.Meta{{Key: "title", Value: "另一篇"}}, "[t](目标.md) 加字\n")
		if err != nil {
			t.Fatalf("save with the old rev: %v", err)
		}
		if !strings.Contains(p.Body, "[t](新名.md \""+tg.ID+"\") 加字") {
			t.Fatalf("%q", p.Body)
		}
	})
}
