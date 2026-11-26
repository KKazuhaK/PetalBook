package drafts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// sharedFixture is a shared space of accounts mode with an owner and an editor
// who has typed a draft that is due.
type sharedFixture struct {
	t      *testing.T
	st     *store.Store
	mgr    *spaces.Manager
	svc    *Service
	clk    *clock
	owner  store.User
	editor store.User
	space  string
	page   string
}

func newShared(t *testing.T, st *store.Store) *sharedFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir, err := os.MkdirTemp("", "petalbook-drafts-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	mgr, err := spaces.NewAccounts(spaces.Options{Store: st, DataDir: filepath.Join(dir, "data"), Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	clk := &clock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}
	svc, err := New(ctx, st, mgr, Config{Now: clk.now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &sharedFixture{t: t, st: st, mgr: mgr, svc: svc, clk: clk}
	f.owner = f.user("U1", "owner")
	f.editor = f.user("U2", "editor")
	info, err := mgr.Create(ctx, f.owner.ID, "Team")
	if err != nil {
		t.Fatal(err)
	}
	f.space = info.ID
	h := f.open(f.owner)
	defer h.Release()
	if _, err := mgr.AddMember(ctx, h, "", f.editor.Username, perm.Editor); err != nil {
		t.Fatal(err)
	}
	p, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "P"}}, Body: "base\n"})
	if err != nil {
		t.Fatal(err)
	}
	f.page = p.ID
	// The editor's draft, due once the clock has moved on.
	eh := f.open(f.editor)
	defer eh.Release()
	if _, err := svc.Put(ctx, eh.Space, f.space, f.editor.ID, p.ID, "", p.Rev, page.Meta{{Key: "title", Value: "P"}}, "typed by the editor\n"); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(DefaultMax + time.Second)
	return f
}

func (f *sharedFixture) user(id, name string) store.User {
	u := store.User{ID: id, Username: name, Name: name, Role: "member", Created: time.Now()}
	if err := store.InsertUser(ctx, f.st.Q(), u); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *sharedFixture) open(u store.User) *spaces.Handle {
	f.t.Helper()
	h, err := f.mgr.Open(ctx, u, f.space)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func (f *sharedFixture) body() string {
	f.t.Helper()
	h := f.open(f.owner)
	defer h.Release()
	p, err := h.Space.Get(f.page)
	if err != nil {
		f.t.Fatal(err)
	}
	return p.Body
}

func (f *sharedFixture) setRole(role string) {
	f.t.Helper()
	h := f.open(f.owner)
	defer h.Release()
	if _, err := f.mgr.SetMemberRole(ctx, h, "", f.editor.ID, role); err != nil {
		f.t.Fatal(err)
	}
}

func TestSweeperDoesNotApplyADraftOfARemovedMember(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newShared(t, st)
		h := f.open(f.owner)
		if err := f.mgr.RemoveMember(ctx, f.owner, h, f.editor.ID); err != nil {
			t.Fatal(err)
		}
		h.Release()
		r := f.svc.Sweep(ctx)
		if r.Applied != 0 || f.body() != "base\n" {
			t.Fatalf("a removed member's draft was applied: %+v %q", r, f.body())
		}
		// Kept, for "petalbook draft list" and "export", not deleted.
		if _, _, err := store.PageDrafts(ctx, st.Q(), f.space, f.page); err != nil {
			t.Fatal(err)
		}
		ds, _, _ := store.PageDrafts(ctx, st.Q(), f.space, f.page)
		if len(ds) != 1 {
			t.Fatalf("%d drafts kept", len(ds))
		}
		// And not tried again at once: the next sweep finds nothing due.
		if due, _ := store.DueDrafts(ctx, st.Q(), f.clk.t, 10); len(due) != 0 {
			t.Fatalf("the sweeper would spin on it: %d due", len(due))
		}
	})
}

func TestSweeperDoesNotApplyADraftOfAViewerUntilPromotedAgain(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newShared(t, st)
		f.setRole(perm.Viewer)
		if r := f.svc.Sweep(ctx); r.Applied != 0 || f.body() != "base\n" {
			t.Fatalf("a viewer's draft was applied: %+v %q", r, f.body())
		}
		f.setRole(perm.Editor)
		f.clk.t = f.clk.t.Add(deniedRetry + time.Second)
		if r := f.svc.Sweep(ctx); r.Applied != 1 || f.body() != "typed by the editor\n" {
			t.Fatalf("the draft was not applied after the role came back: %+v %q", r, f.body())
		}
	})
}

func TestSweeperDoesNotApplyADraftOfADisabledUser(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newShared(t, st)
		if err := store.SetDisabled(ctx, st.Q(), f.editor.ID, true); err != nil {
			t.Fatal(err)
		}
		if r := f.svc.Sweep(ctx); r.Applied != 0 || f.body() != "base\n" {
			t.Fatalf("a disabled user's draft was applied: %+v %q", r, f.body())
		}
	})
}
