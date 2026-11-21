package drafts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/PetalBook/internal/page"
	"github.com/KazuhaHub/PetalBook/internal/space"
	"github.com/KazuhaHub/PetalBook/internal/spaces"
	"github.com/KazuhaHub/PetalBook/internal/store"
	"github.com/KazuhaHub/PetalBook/internal/store/storetest"
)

var ctx = context.Background()

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type fixture struct {
	t        *testing.T
	st       *store.Store
	sp       *space.Space
	mgr      *spaces.Manager
	svc      *Service
	clk      *clock
	versions map[string]string // page id -> version of the draft the test last stored
}

func newFixture(t *testing.T, st *store.Store, cfg Config) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	sp, err := space.Open(space.Options{Root: filepath.Join(t.TempDir(), "s"), CommitIdle: time.Hour, CommitMax: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Close() })
	mgr, err := spaces.NewSingle(ctx, st, sp, nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}
	if cfg.Now == nil {
		cfg.Now = clk.now
	}
	svc, err := New(ctx, st, mgr, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t, st, sp, mgr, svc, clk, map[string]string{}}
}

func (f *fixture) page(title, body string) *space.Page {
	f.t.Helper()
	p, err := f.sp.Create(space.CreateInput{Meta: page.Meta{{Key: "title", Value: title}}, Body: body})
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// put stores a draft replacing the one the test stored before, if any.
func (f *fixture) put(p *space.Page, rev, body string) {
	f.t.Helper()
	meta := page.Meta{{Key: "title", Value: p.Meta.String("title")}}
	st, err := f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, f.versions[p.ID], rev, meta, body)
	if err != nil {
		f.t.Fatal(err)
	}
	f.versions[p.ID] = st.Version
}

func (f *fixture) count() int {
	var n int
	f.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM drafts`).Scan(&n)
	return n
}

func TestConfigValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if _, err := New(ctx, st, nil, Config{Idle: time.Hour, Max: time.Minute}, nil); err == nil {
			t.Fatal("a maximum shorter than the idle time was accepted")
		}
		s, err := New(ctx, st, nil, Config{}, nil)
		if err != nil || s.cfg.Idle != 2*time.Minute || s.cfg.Max != 10*time.Minute || s.cfg.Tick != 15*time.Second {
			t.Fatalf("%+v %v", s, err)
		}
	})
}

func TestACommitJustBeforeTheSweepIsNotUndone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "two\n")
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		// The user commits in the gap between the sweeper's query and its work.
		var committed *space.Page
		f.svc.hook = func(sp, pg, user string) {
			var err error
			if committed, err = f.svc.Commit(ctx, f.sp, sp, user, pg, f.versions[p.ID]); err != nil {
				t.Error(err)
			}
		}
		res := f.svc.Sweep(ctx)
		if res != (SweepResult{}) {
			t.Fatalf("the sweeper acted on a draft that was already gone: %+v", res)
		}
		got, _ := f.sp.Get(p.ID)
		if committed == nil || got.Body != "two\n" || got.Rev != committed.Rev || f.count() != 0 {
			t.Fatalf("%+v %d", got, f.count())
		}
	})
}

func TestAnEditDuringTheApplyIsKept(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "two\n")
		due, _ := store.DueDrafts(ctx, st.Q(), f.clk.t.Add(time.Hour), 10)
		// The person types again after the sweeper read the draft.
		f.clk.t = f.clk.t.Add(time.Second)
		f.put(p, p.Rev, "three\n")
		if _, err := f.svc.apply(ctx, f.sp, &due[0]); err != nil {
			t.Fatal(err)
		}
		if f.count() != 1 {
			t.Fatal("the newer draft was deleted with the older one")
		}
		d, _ := f.svc.Get(ctx, spaces.SingleID, p.ID, store.LocalUserID, "")
		if d.Body != "three\n" {
			t.Fatalf("%q", d.Body)
		}
	})
}

func TestSweepIsBoundedAndTakesTheRestNextTime(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		const n = batch + 5
		for i := 0; i < n; i++ {
			p := f.page(fmt.Sprintf("P%d", i), "x\n")
			f.put(p, p.Rev, "y\n")
		}
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		if res := f.svc.Sweep(ctx); res.Applied != batch {
			t.Fatalf("%+v", res)
		}
		if res := f.svc.Sweep(ctx); res.Applied != 5 {
			t.Fatalf("%+v", res)
		}
		if res := f.svc.Sweep(ctx); res != (SweepResult{}) || f.count() != 0 {
			t.Fatalf("%+v %d", res, f.count())
		}
	})
}

func TestAFailureIsRetriedLaterNotInATightLoop(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		// A draft in a space this server cannot open.
		if err := store.InsertSpace(ctx, st.Q(), "OTHERSPACE", "other", "", f.clk.t); err != nil {
			t.Fatal(err)
		}
		d, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: "OTHERSPACE", PageID: "P", UserID: store.LocalUserID, BaseRev: "r", Meta: "{}", Body: "b", Updated: f.clk.t}, "", time.Minute, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		f.clk.t = d.Due.Add(time.Second)
		if res := f.svc.Sweep(ctx); res.Failed != 1 {
			t.Fatalf("%+v", res)
		}
		if res := f.svc.Sweep(ctx); res != (SweepResult{}) {
			t.Fatalf("tried again at once: %+v", res)
		}
		f.clk.t = f.clk.t.Add(retryAfter + time.Second)
		if res := f.svc.Sweep(ctx); res.Failed != 1 {
			t.Fatalf("not tried again later: %+v", res)
		}
		if f.count() != 1 {
			t.Fatal("a draft was lost because it could not be applied")
		}
	})
}

func TestChangedFlagsApplyToStoredDrafts(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{Idle: time.Hour, Max: 2 * time.Hour})
		p := f.page("T", "x\n")
		f.put(p, p.Rev, "y\n")
		// A restart with shorter times.
		svc, err := New(ctx, st, f.mgr, Config{Idle: time.Minute, Max: 5 * time.Minute, Now: f.clk.now}, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.clk.t = f.clk.t.Add(2 * time.Minute)
		if res := svc.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
	})
}

func TestRunSweepsAndStopsWithoutForcingTheRest(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		// Real time: one draft falls due within milliseconds, one an hour away.
		f := newFixture(t, st, Config{Idle: 30 * time.Millisecond, Max: time.Hour, Tick: 5 * time.Millisecond, Now: time.Now})
		soon := f.page("Soon", "x\n")
		f.put(soon, soon.Rev, "applied by the sweeper\n")

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { f.svc.Run(runCtx); close(done) }()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if got, _ := f.sp.Get(soon.ID); got.Body == "applied by the sweeper\n" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the sweeper never applied a due draft")
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		<-done

		// A draft that is not due stays when the sweeper stops, and when it
		// starts again later it is still there.
		late := f.page("Late", "x\n")
		f.svc.cfg.Idle = time.Hour
		f.put(late, late.Rev, "not yet\n")
		runCtx, cancel = context.WithCancel(ctx)
		done = make(chan struct{})
		go func() { f.svc.Run(runCtx); close(done) }()
		cancel()
		<-done
		if got, _ := f.sp.Get(late.ID); got.Body != "x\n" {
			t.Fatal("a draft that was not due was applied at shutdown")
		}
		if d, err := f.svc.Get(ctx, spaces.SingleID, late.ID, store.LocalUserID, late.Rev); err != nil || d == nil || d.Body != "not yet\n" {
			t.Fatalf("%v %v", d, err)
		}
	})
}

func TestGetReportsStaleness(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "x\n")
		f.put(p, p.Rev, "y\n")
		if d, _ := f.svc.Get(ctx, spaces.SingleID, p.ID, store.LocalUserID, p.Rev); d.Stale {
			t.Fatal("stale on the current rev")
		}
		if d, _ := f.svc.Get(ctx, spaces.SingleID, p.ID, store.LocalUserID, "other"); !d.Stale {
			t.Fatal("not stale on another rev")
		}
		if d, err := f.svc.Get(ctx, spaces.SingleID, p.ID, "nobody", p.Rev); d != nil || err != nil {
			t.Fatalf("%v %v", d, err)
		}
	})
}

func TestADraftIsNeverDeletedBecauseItsPageIsMissing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "x\n")
		f.put(p, p.Rev, "my only copy of this text\n")
		if err := f.sp.Delete(p.ID); err != nil { // the file goes (here through the space; outside the server it is the same to the draft)
			t.Fatal(err)
		}
		// Committing it says the page is gone and keeps the text.
		if _, err := f.svc.Commit(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, f.versions[p.ID]); !errors.Is(err, space.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if f.count() != 1 {
			t.Fatal("a commit on a missing page deleted the draft")
		}
		// The sweeper holds it, and keeps holding it.
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		if res := f.svc.Sweep(ctx); res.Held != 1 {
			t.Fatalf("%+v", res)
		}
		f.clk.t = f.clk.t.Add(time.Hour)
		if res := f.svc.Sweep(ctx); res != (SweepResult{}) {
			t.Fatalf("%+v", res)
		}
		if d, err := store.GetDraft(ctx, st.Q(), spaces.SingleID, p.ID, store.LocalUserID); err != nil || !d.Held || d.Body != "my only copy of this text\n" {
			t.Fatalf("%v %v", d, err)
		}
		// Only deleting the page through the service removes drafts.
		if err := f.svc.DeletePage(ctx, spaces.SingleID, p.ID); err != nil || f.count() != 0 {
			t.Fatalf("%v %d", err, f.count())
		}
	})
}

func TestADraftThatSaysWhatTheFileSaysIsNotAConflict(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		// Through commit.
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "same text\n")
		// Another device saved exactly that text meanwhile, so the draft's base is stale.
		moved, err := f.sp.Save(p.ID, p.Rev, page.Meta{{Key: "title", Value: "T"}}, "same text\n")
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.svc.Commit(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, f.versions[p.ID])
		if err != nil || got.Rev != moved.Rev {
			t.Fatalf("%v %v", got, err)
		}
		if f.count() != 0 {
			t.Fatal("the draft was kept")
		}
		// Through the sweeper.
		q := f.page("U", "one\n")
		f.put(q, q.Rev, "same text\n")
		if _, err := f.sp.Save(q.ID, q.Rev, page.Meta{{Key: "title", Value: "U"}}, "same text\n"); err != nil {
			t.Fatal(err)
		}
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		if res := f.svc.Sweep(ctx); res.Applied != 1 || res.Held != 0 || f.count() != 0 {
			t.Fatalf("%+v %d", res, f.count())
		}
		// A draft that differs is still a conflict.
		r := f.page("V", "one\n")
		f.put(r, r.Rev, "mine\n")
		f.sp.Save(r.ID, r.Rev, page.Meta{{Key: "title", Value: "V"}}, "theirs\n")
		var c *Conflict
		if _, err := f.svc.Commit(ctx, f.sp, spaces.SingleID, store.LocalUserID, r.ID, f.versions[r.ID]); !errors.As(err, &c) || c.Draft == nil || !c.Draft.Stale || c.Current.Body != "theirs\n" {
			t.Fatalf("%v", err)
		}
	})
}

func TestBookkeepingFinishesWhenTheClientGoesAway(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "two\n")
		// The request is cancelled after the file was written and before the
		// draft row was removed.
		cctx, cancel := context.WithCancel(ctx)
		f.svc.afterSave = cancel
		if _, err := f.svc.Commit(cctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, f.versions[p.ID]); err != nil {
			t.Fatal(err)
		}
		if f.count() != 0 {
			t.Fatal("the draft stayed although its text is in the file; it would show a false conflict later")
		}
		// The same for a save of the file and for a sweep cut short by shutdown.
		q := f.page("U", "one\n")
		f.put(q, q.Rev, "draft\n")
		cctx, cancel = context.WithCancel(ctx)
		f.svc.afterSave = cancel
		if _, err := f.svc.SaveFile(cctx, f.sp, spaces.SingleID, store.LocalUserID, q.ID, q.Rev, page.Meta{{Key: "title", Value: "U"}}, "draft\n"); err != nil {
			t.Fatal(err)
		}
		if f.count() != 0 {
			t.Fatal("a save of the draft's own text left the draft behind")
		}
		r := f.page("V", "one\n")
		f.put(r, r.Rev, "swept\n")
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		cctx, cancel = context.WithCancel(ctx)
		f.svc.afterSave = cancel
		if res := f.svc.Sweep(cctx); res.Applied != 1 || f.count() != 0 {
			t.Fatalf("%+v %d", res, f.count())
		}
	})
}

// A direct save never takes a draft away: the text may exist nowhere else.
func TestSaveFileKeepsADraftThatSaysSomethingElse(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "only in the draft\n")
		saved, err := f.svc.SaveFile(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, p.Rev, page.Meta{{Key: "title", Value: "T"}}, "saved elsewhere\n")
		if err != nil {
			t.Fatal(err)
		}
		d, err := f.svc.Get(ctx, spaces.SingleID, p.ID, store.LocalUserID, saved.Rev)
		if err != nil || d == nil || d.Body != "only in the draft\n" || !d.Stale {
			t.Fatalf("the draft should stay, stale: %+v %v", d, err)
		}
		// The sweeper leaves it for the person to decide.
		f.clk.t = f.clk.t.Add(time.Hour)
		if res := f.svc.Sweep(ctx); res.Applied != 0 || f.count() != 1 {
			t.Fatalf("%+v %d", res, f.count())
		}
	})
}

func TestSaveFileLeavesADraftStoredMeanwhile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "first\n")
		meta := page.Meta{{Key: "title", Value: "T"}}
		// A draft stored from another device while the save is being written.
		f.svc.afterSave = func() {
			if _, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: spaces.SingleID, PageID: p.ID, UserID: store.LocalUserID,
				BaseRev: p.Rev, Meta: "{}", Body: "newer", Updated: f.clk.t}, f.versions[p.ID], time.Minute, time.Hour); err != nil {
				t.Error(err)
			}
		}
		if _, err := f.svc.SaveFile(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, p.Rev, meta, "saved\n"); err != nil {
			t.Fatal(err)
		}
		d, err := store.GetDraft(ctx, st.Q(), spaces.SingleID, p.ID, store.LocalUserID)
		if err != nil || d.Body != "newer" {
			t.Fatalf("the save deleted a draft it had not seen: %v %v", d, err)
		}
	})
}

func TestPutAnswersAConflictWithTheDraftAndThePage(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		meta := page.Meta{{Key: "title", Value: "T"}}
		first, err := f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, "", p.Rev, meta, "mine\n")
		if err != nil {
			t.Fatal(err)
		}
		var c *Conflict
		_, err = f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, "", p.Rev, meta, "other device\n")
		if !errors.As(err, &c) || c.Draft == nil || c.Draft.Version != first.Version || c.Draft.Body != "mine\n" || c.Current.Rev != p.Rev {
			t.Fatalf("%v", err)
		}
		// Committed (or deleted) meanwhile: no draft, and the page as it is.
		if _, err := f.svc.Commit(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, first.Version); err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, first.Version, p.Rev, meta, "late\n")
		if !errors.As(err, &c) || c.Draft != nil || c.Current.Body != "mine\n" {
			t.Fatalf("%v", err)
		}
		// A delete that names another version refuses, and one that names none does not.
		v2, _ := f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, "", c.Current.Rev, meta, "again\n")
		if err := f.svc.Delete(ctx, f.sp, spaces.SingleID, p.ID, store.LocalUserID, "other", ""); !errors.As(err, &c) || c.Draft.Version != v2.Version {
			t.Fatalf("%v", err)
		}
		if f.count() != 1 {
			t.Fatal("a refused delete deleted")
		}
		if err := f.svc.Delete(ctx, f.sp, spaces.SingleID, p.ID, store.LocalUserID, v2.Version, ""); err != nil || f.count() != 0 {
			t.Fatalf("%v", err)
		}
		// A versioned delete with no draft there is told so, with the page.
		if err := f.svc.Delete(ctx, f.sp, spaces.SingleID, p.ID, store.LocalUserID, "any", ""); !errors.As(err, &c) || c.Draft != nil || c.Current == nil {
			t.Fatalf("%v", err)
		}
		if err := f.svc.Delete(ctx, f.sp, spaces.SingleID, p.ID, store.LocalUserID, "", ""); err != nil { // unversioned: fine
			t.Fatal(err)
		}
	})
}

func TestAStoreWaitingOnTheLockSeesTheFileAsItIsWhenItRuns(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		meta := page.Meta{{Key: "title", Value: "T"}}
		l := f.svc.lock(spaces.SingleID, p.ID, store.LocalUserID)
		l.Lock() // a direct save is in progress
		reached := make(chan struct{})
		f.svc.beforeLock = func() { close(reached) }
		type answer struct {
			st  *Stored
			err error
		}
		done := make(chan answer, 1)
		go func() {
			s, err := f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, "", p.Rev, meta, "draft\n")
			done <- answer{s, err}
		}()
		<-reached // the store is about to wait for the lock
		if _, err := f.sp.Save(p.ID, p.Rev, meta, "saved meanwhile\n"); err != nil {
			t.Fatal(err)
		}
		l.Unlock()
		a := <-done
		if a.err != nil || !a.st.Stale {
			t.Fatalf("the draft is stale (its base was saved over) but was reported %+v %v", a.st, a.err)
		}
	})
}

func TestAStoreRacingAPageDeleteLeavesNoDraftBehind(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		for range 5 {
			f := newFixture(t, st, Config{})
			p := f.page("T", "one\n")
			meta := page.Meta{{Key: "title", Value: "T"}}
			l := f.svc.lock(spaces.SingleID, p.ID, store.LocalUserID)
			l.Lock()
			reached := make(chan struct{})
			f.svc.beforeLock = func() { close(reached) }
			putDone := make(chan error, 1)
			go func() {
				_, err := f.svc.Put(ctx, f.sp, spaces.SingleID, store.LocalUserID, p.ID, "", p.Rev, meta, "draft\n")
				putDone <- err
			}()
			<-reached
			delDone := make(chan error, 1)
			go func() { delDone <- f.svc.DeletePageFile(ctx, f.sp, spaces.SingleID, p.ID) }()
			l.Unlock()
			perr, derr := <-putDone, <-delDone
			if derr != nil {
				t.Fatal(derr)
			}
			// Either the store came first and was removed with the page, or it found the page gone.
			if perr != nil && !errors.Is(perr, space.ErrNotFound) {
				t.Fatal(perr)
			}
			if n := f.count(); n != 0 {
				t.Fatalf("%d draft rows left for a deleted page (store answered %v)", n, perr)
			}
		}
	})
}

func TestDeletingAPageRemovesEveryUsersDrafts(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "mine\n")
		store.InsertUser(ctx, st.Q(), store.User{ID: "other", Username: "other", Name: "o", Role: "member", Created: f.clk.t})
		store.AddMember(ctx, st.Q(), spaces.SingleID, "other", "owner", f.clk.t)
		if _, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: spaces.SingleID, PageID: p.ID, UserID: "other", BaseRev: p.Rev, Meta: "{}", Body: "theirs", Updated: f.clk.t}, "", time.Minute, time.Hour); err != nil {
			t.Fatal(err)
		}
		if f.count() != 2 {
			t.Fatal(f.count())
		}
		if err := f.svc.DeletePageFile(ctx, f.sp, spaces.SingleID, p.ID); err != nil || f.count() != 0 {
			t.Fatalf("%v %d", err, f.count())
		}
		if err := f.svc.DeletePageFile(ctx, f.sp, spaces.SingleID, p.ID); !errors.Is(err, space.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestAHeldDraftOfAMissingPageIsLoggedOnceWithoutItsText(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFixture(t, st, Config{})
		var buf syncBuf
		f.svc.log = slog.New(slog.NewTextHandler(&buf, nil))
		p := f.page("T", "one\n")
		f.put(p, p.Rev, "private words in the draft\n")
		if err := f.sp.Delete(p.ID); err != nil {
			t.Fatal(err)
		}
		f.clk.t = f.clk.t.Add(3 * time.Minute)
		f.svc.Sweep(ctx)
		f.clk.t = f.clk.t.Add(time.Hour)
		f.svc.Sweep(ctx)
		f.svc.Sweep(ctx)
		out := buf.String()
		if strings.Count(out, "draft is kept") != 1 || !strings.Contains(out, p.ID) || !strings.Contains(out, spaces.SingleID) || !strings.Contains(out, "level=WARN") {
			t.Fatalf("log:\n%s", out)
		}
		if strings.Contains(out, "private words") {
			t.Fatal("the draft's text is in the log")
		}
	})
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestASlowSpaceOpenInTheSweeperDoesNotBlockOtherDrafts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	st := storetest.Open(t, "sqlite")
	dir, err := os.MkdirTemp("", "petalbook-drafts-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	opt := spaces.Options{Store: st, DataDir: dir, Committer: "S <s@example.test>", CommitIdle: time.Hour, CommitMax: time.Hour}
	creator, err := spaces.NewAccounts(opt)
	if err != nil {
		t.Fatal(err)
	}
	store.InsertUser(ctx, st.Q(), store.User{ID: "U1", Username: "u1", Name: "u1", Role: "member", Created: time.Now()})
	a, err := creator.Create(ctx, "U1", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := creator.Create(ctx, "U1", "B")
	if err != nil {
		t.Fatal(err)
	}
	creator.Close()

	slow := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	opt.OpenHook = func(id string) {
		if id == a.ID {
			once.Do(func() { close(entered) })
			<-slow // opening A takes as long as an index rebuild
		}
	}
	mgr, err := spaces.NewAccounts(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	clk := &clock{t: time.Now()}
	svc, err := New(ctx, st, mgr, Config{Now: clk.now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := mgr.Open(ctx, "U1", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer hb.Release()
	pb, err := hb.Space.Create(space.CreateInput{Meta: page.Meta{{Key: "title", Value: "B page"}}, Body: "x\n"})
	if err != nil {
		t.Fatal(err)
	}
	// A draft in A that is due, on a page that hashes to the same lock as B's page.
	var pageA string
	for i := 0; ; i++ {
		pageA = fmt.Sprintf("%010d", i)
		if svc.lock(a.ID, pageA, "U1") == svc.lock(b.ID, pb.ID, "U1") {
			break
		}
	}
	if _, err := store.PutDraft(ctx, st.Q(), store.Draft{SpaceID: a.ID, PageID: pageA, UserID: "U1", BaseRev: "r", Meta: "{}", Body: "x", Updated: clk.t}, "", time.Minute, time.Hour); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Hour)
	swept := make(chan SweepResult, 1)
	go func() { swept <- svc.Sweep(ctx) }()
	<-entered // the sweeper is inside the slow open of A

	stored := make(chan error, 1)
	go func() {
		_, err := svc.Put(ctx, hb.Space, b.ID, "U1", pb.ID, "", pb.Rev, page.Meta{{Key: "title", Value: "B page"}}, "draft\n")
		stored <- err
	}()
	select {
	case err := <-stored:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a draft store in another space waited for the sweeper's slow open")
	}
	close(slow)
	<-swept
}
