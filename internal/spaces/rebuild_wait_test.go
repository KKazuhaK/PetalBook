package spaces

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// A space whose open needs a rebuild while every rebuild slot of the server is
// taken answers restoring, phase waiting, after the request's wait; the
// rebuild goes on in the background and the space opens once a slot is free.
func TestASpaceWaitingForARebuildSlotAnswersRestoringAndOpensLater(t *testing.T) {
	index.SetBigSyncLimit(1)
	t.Cleanup(func() { index.SetBigSyncLimit(index.DefaultBigSyncs) })
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) {
		o.MaxSpaces, o.IdleAfter, o.RestoreWait = 0, time.Minute, 300*time.Millisecond
	})
	u := addUser(t, st, "U1", "one")
	in, err := m.Create(ctx, u.ID, "Big")
	if err != nil {
		t.Fatal(err)
	}
	clk.add(time.Hour)
	m.Trim()
	if m.OpenCount() != 0 {
		t.Fatal("the space was not closed")
	}
	root, state := m.paths(in.ID)
	const pages = 1100
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range pages {
		body := pageFile(fmt.Sprintf("%010X", 0x500000000+i), fmt.Sprintf("Page %d", i), "x\n")
		if err := os.WriteFile(filepath.Join(root, "p", fmt.Sprintf("%d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		os.Remove(filepath.Join(state, "index.db") + s)
	}

	slots := index.BigSyncs()
	if err := slots.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			slots.Release()
		}
	}()
	start := time.Now()
	_, err = m.Open(ctx, u, in.ID)
	var rs *RestoringError
	if !errors.As(err, &rs) || rs.Phase != "waiting" {
		t.Fatalf("open while the slot is taken: %v, want restoring (waiting)", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the request waited %v", waited)
	}
	// Asked again: the same answer, and still one open under way.
	if _, err := m.Open(ctx, u, in.ID); !errors.As(err, &rs) || rs.Phase != "waiting" {
		t.Fatalf("second request: %v", err)
	}
	held = false
	slots.Release()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		h, err := m.Open(ctx, u, in.ID)
		if err == nil {
			n, err := h.Space.PageCount()
			h.Release()
			if err != nil || n != pages {
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
