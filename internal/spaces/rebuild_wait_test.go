package spaces

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

const rebuildPages = 1100

// rebuildSetup is a manager with one rebuild slot, which the test holds, and
// a closed space whose next open rebuilds its index (more files than a small
// sync reads, and no index).
type rebuildSetup struct {
	m     *Manager
	clk   *tclock
	st    *store.Store
	u     store.User
	id    string
	slots *index.SyncSlots
	held  atomic.Bool
}

func newRebuildSetup(t *testing.T) *rebuildSetup {
	t.Helper()
	index.SetBigSyncLimit(1)
	t.Cleanup(func() { index.SetBigSyncLimit(index.DefaultBigSyncs) })
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) {
		o.MaxSpaces, o.IdleAfter, o.RestoreWait = 0, time.Minute, 300*time.Millisecond
	})
	r := &rebuildSetup{m: m, clk: clk, st: st, u: addUser(t, st, "U1", "one")}
	in, err := m.Create(ctx, r.u.ID, "Big")
	if err != nil {
		t.Fatal(err)
	}
	r.id = in.ID
	clk.add(time.Hour)
	m.Trim()
	if m.OpenCount() != 0 {
		t.Fatal("the space was not closed")
	}
	root, state := m.paths(in.ID)
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range rebuildPages {
		body := pageFile(fmt.Sprintf("%010X", 0x500000000+i), fmt.Sprintf("Page %d", i), "x\n")
		if err := os.WriteFile(filepath.Join(root, "p", fmt.Sprintf("%d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(filepath.Join(state, "index.db") + s)
	}
	r.slots = index.BigSyncs()
	if err := r.slots.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	r.held.Store(true)
	t.Cleanup(r.release)
	return r
}

func (r *rebuildSetup) release() {
	if r.held.CompareAndSwap(true, false) {
		r.slots.Release()
	}
}

// A request of the HTTP layer for a space whose open needs a rebuild while
// every rebuild slot of the server is taken answers restoring, phase waiting,
// after the request's wait; the rebuild goes on in the background and the
// space opens once a slot is free.
func TestASpaceWaitingForARebuildSlotAnswersRestoringAndOpensLater(t *testing.T) {
	r := newRebuildSetup(t)
	m, u := r.m, r.u
	hctx := WithRestoringAnswer(ctx)
	start := time.Now()
	_, err := m.Open(hctx, u, r.id)
	var rs *RestoringError
	if !errors.As(err, &rs) || rs.Phase != "waiting" {
		t.Fatalf("open while the slot is taken: %v, want restoring (waiting)", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the request waited %v", waited)
	}
	// Asked again: the same answer, and still one open under way.
	if _, err := m.Open(hctx, u, r.id); !errors.As(err, &rs) || rs.Phase != "waiting" {
		t.Fatalf("second request: %v", err)
	}
	r.release()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		h, err := m.Open(hctx, u, r.id)
		if err == nil {
			n, err := h.Space.PageCount()
			h.Release()
			if err != nil || n != rebuildPages {
				t.Fatalf("pages %d, %v", n, err)
			}
			break
		}
		if !errors.As(err, &rs) || (rs.Phase != "waiting" && rs.Phase != "rebuilding") {
			t.Fatalf("while the rebuild runs: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the space never opened after the slot was freed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Callers that are not HTTP requests (an export, the draft sweeper's Acquire)
// wait for a rebuild that takes longer than the requests' wait, and succeed.
func TestBackgroundCallersWaitForARebuildLongerThanTheRequestsWait(t *testing.T) {
	r := newRebuildSetup(t)
	m := r.m
	acquired := make(chan error, 1)
	go func() {
		_, rel, err := m.Acquire(ctx, r.id)
		if err == nil {
			rel()
		}
		acquired <- err
	}()
	e, err := m.StartExport(ctx, ExportRequest{TenantID: store.DefaultTenantID, By: r.u.ID, ByName: "one", Sources: []store.ExportSource{{Space: r.id, SpaceID: r.id}}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * m.opt.RestoreWait) // well past the requests' wait
	select {
	case err := <-acquired:
		t.Fatalf("Acquire returned while the rebuild waited for its slot: %v", err)
	default:
	}
	r.release()
	if err := <-acquired; err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		got, err := store.GetExport(ctx, r.st.Q(), store.AllTenants(), e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == store.ExportReady {
			break
		}
		if got.State == store.ExportFailed {
			t.Fatalf("the export failed: %+v", got)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the export did not finish: %s", got.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Closing the manager drops the opens queued for a rebuild slot instead of
// waiting for every rebuild, and still flushes the spaces that are open.
func TestCloseDropsOpensQueuedForARebuildAndFlushesTheOpenSpaces(t *testing.T) {
	r := newRebuildSetup(t)
	m, u := r.m, r.u
	a, err := m.Create(ctx, u.ID, "Small")
	if err != nil {
		t.Fatal(err)
	}
	h, err := m.Open(ctx, u, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Pending"}}, Body: "x\n"}); err != nil {
		t.Fatal(err)
	}
	h.Release()
	root, _ := m.paths(a.ID)
	before := runGit(t, root, "rev-list", "--count", "--all")
	// The big space's open waits for the slot.
	if _, err := m.Open(WithRestoringAnswer(ctx), u, r.id); err == nil {
		t.Fatal("the big space opened with the slot taken")
	}
	// Given back late only so that a Close that waits for it ends at all.
	go func() { time.Sleep(15 * time.Second); r.release() }()
	start := time.Now()
	m.Close()
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("Close waited %v for an open queued for a rebuild slot", took)
	}
	if after := runGit(t, root, "rev-list", "--count", "--all"); after == before {
		t.Fatalf("the pending change of an open space was not committed at close (%s commits)", strings.TrimSpace(after))
	}
}

// A panic while a space is opened in the background is that open's error, not
// the end of the process; the next request opens the space.
func TestAPanicWhileOpeningASpaceIsAnErrorOfThatOpen(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	var panics atomic.Int32
	m, clk := limitedManager(t, st, func(o *Options) {
		o.MaxSpaces = 0
		o.OpenHook = func(string) {
			if panics.Add(1) == 1 {
				panic("boom")
			}
		}
	})
	u := addUser(t, st, "U1", "one")
	in, err := m.Create(ctx, u.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	clk.add(time.Hour)
	m.Trim()
	if _, err := m.Open(ctx, u, in.ID); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("open that panicked: %v", err)
	}
	h, err := m.Open(ctx, u, in.ID)
	if err != nil {
		t.Fatalf("the next open: %v", err)
	}
	h.Release()
}

// A panic after the space was opened (while its cache state or attachments are
// set up) closes the copy that was opened, so a later open never runs a second
// one beside it.
func TestAPanicAfterTheSpaceWasOpenedClosesIt(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) { o.MaxSpaces = 0 })
	u := addUser(t, st, "U1", "one")
	in, err := m.Create(ctx, u.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	clk.add(time.Hour)
	m.Trim()
	var leaked *space.Space
	m.testOpened = func(sp *space.Space) {
		if leaked == nil {
			leaked = sp
			panic("boom after open")
		}
	}
	if _, err := m.Open(ctx, u, in.ID); err == nil || !strings.Contains(err.Error(), "boom after open") {
		t.Fatalf("open that panicked: %v", err)
	}
	if _, err := leaked.Reindex(); !errors.Is(err, space.ErrClosed) {
		t.Fatalf("the copy opened before the panic is still open: %v", err)
	}
	h, err := m.Open(ctx, u, in.ID)
	if err != nil {
		t.Fatalf("the next open: %v", err)
	}
	h.Release()
}
