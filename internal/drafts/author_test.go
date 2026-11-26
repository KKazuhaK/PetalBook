package drafts

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func gitLog(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--format=%an|%s").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// The sweeper writes a draft as the person whose draft it is, not as the
// space's owner and not as whoever else is in the space.
func TestSweeperWritesADraftAsItsUser(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}
		ann := store.User{ID: "u-ann", Username: "ann", Name: "Ann Lee", Email: "ann@example.test", Role: "member", Created: time.Now()}
		bo := store.User{ID: "u-bo", Username: "bo", Name: "Bo Chen", Email: "bo@example.test", Role: "member", Created: time.Now()}
		for _, u := range []store.User{ann, bo} {
			if err := store.InsertUser(ctx, st.Q(), u); err != nil {
				t.Fatal(err)
			}
		}
		mgr, err := spaces.NewAccounts(spaces.Options{Store: st, DataDir: filepath.Join(t.TempDir(), "data"), Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mgr.Close() })
		info, err := mgr.Create(ctx, ann.ID, "Team")
		if err != nil {
			t.Fatal(err)
		}
		h, err := mgr.Open(ctx, ann, info.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		sp := h.Space
		if _, err := mgr.AddMember(ctx, h, "", bo.Username, "editor"); err != nil {
			t.Fatal(err)
		}
		clk := &clock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}
		svc, err := New(ctx, st, mgr, Config{Now: clk.now}, nil)
		if err != nil {
			t.Fatal(err)
		}
		mk := func(title string) *space.Page {
			p, err := sp.Create(mgr.AuthorOf(ann), space.CreateInput{Meta: page.Meta{{Key: "title", Value: title}}, Body: "x\n"})
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		pa, pb := mk("ForAnn"), mk("ForBo")
		if err := sp.Flush(); err != nil {
			t.Fatal(err)
		}
		put := func(u store.User, p *space.Page, body string) {
			if _, err := svc.Put(ctx, sp, info.ID, u.ID, p.ID, "", p.Rev, page.Meta{{Key: "title", Value: p.Meta.String("title")}}, body); err != nil {
				t.Fatal(err)
			}
		}
		put(ann, pa, "ann writes\n")
		put(bo, pb, "bo writes\n")
		clk.t = clk.t.Add(3 * time.Minute)
		if res := svc.Sweep(ctx); res.Applied != 2 {
			t.Fatalf("%+v", res)
		}
		if err := sp.Flush(); err != nil {
			t.Fatal(err)
		}
		log := gitLog(t, sp.Root())
		if !strings.HasPrefix(log, "Bo Chen|Edit ForBo\nAnn Lee|Edit ForAnn\n") && !strings.HasPrefix(log, "Ann Lee|Edit ForAnn\nBo Chen|Edit ForBo\n") {
			t.Fatalf("%s", log)
		}
	})
}

// In single-user mode a draft is written as the space's own author, as before.
func TestSweeperInSingleModeUsesTheSpacesAuthor(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("Solo", "one\n")
		f.sp.Flush()
		f.put(p, p.Rev, "two\n")
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		if res := f.svc.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
		f.sp.Flush()
		if log := gitLog(t, f.sp.Root()); !strings.HasPrefix(log, "PetalBook|Edit Solo\n") {
			t.Fatalf("%s", log)
		}
	})
}
