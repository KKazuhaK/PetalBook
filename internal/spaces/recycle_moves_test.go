package spaces

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The hourly job reads the items that are not at rest once and then works
// through them; a restore later in its list that finishes while the job is busy
// with an earlier item is left alone: its directories stay at their live place.
func TestTheHourlyJobLeavesARestoreThatFinishedMeanwhileAlone(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		a := f.fullSpace("olive", "edith", "mia")
		f.user("pat")
		b, err := f.m.Create(ctx, "pat", "Other")
		if err != nil {
			t.Fatal(err)
		}
		// A: a deletion cut short, left moving (the job's first item).
		f.crashAt("recycle:rows")
		if _, err := f.m.RecycleSpace(ctx, store.AllTenants(), a.ID, RecycleOptions{}); !errors.Is(err, errRecycleCrash) {
			t.Fatalf("%v", err)
		}
		f.crashAt("")
		itemA := f.itemOfSpace(a.ID)
		f.advance(time.Second)
		itemB, err := f.m.RecycleSpace(ctx, store.AllTenants(), b.ID, RecycleOptions{})
		if err != nil {
			t.Fatal(err)
		}

		restoreHeld, restoreGo := make(chan struct{}), make(chan struct{})
		jobHeld, jobGo := make(chan struct{}), make(chan struct{})
		var restoreOnce, jobOnce sync.Once
		f.mu.Lock()
		f.pause = func(step string) {
			switch step {
			case "restore:moved-space":
				restoreOnce.Do(func() { close(restoreHeld); <-restoreGo })
			case "recycle:moved-space":
				jobOnce.Do(func() { close(jobHeld); <-jobGo })
			}
		}
		f.mu.Unlock()

		// B's restore is under way (state restoring) when the job reads its list.
		restored := make(chan error, 1)
		go func() {
			_, err := f.m.RestoreSpace(ctx, store.AllTenants(), itemB.ID, "")
			restored <- err
		}()
		<-restoreHeld
		f.advance(2 * time.Minute)
		job := make(chan error, 1)
		go func() { job <- f.m.recoverRecycle(ctx, time.Minute) }()
		// The job is busy with A; B's restore finishes and lets B go.
		<-jobHeld
		close(restoreGo)
		if err := <-restored; err != nil {
			t.Fatalf("the restore failed: %v", err)
		}
		close(jobGo)
		if err := <-job; err != nil {
			t.Fatalf("the job: %v", err)
		}

		root, _ := f.m.paths(b.ID)
		if !pathExists(root) {
			t.Fatal("the restored space lost its directory")
		}
		if pathExists(f.m.RecycleDir(itemB.ID)) {
			t.Fatal("the job moved the restored space into the bin")
		}
		f.noItem(itemB.ID)
		if len(f.rowsOf("spaces", "id", b.ID)) != 1 {
			t.Fatal("the rows of the restored space are gone")
		}
		if it := f.item(itemA.ID); it.State != store.RecycleReady || len(f.m.Problems(it)) != 0 {
			t.Fatalf("A was not finished: %+v %v", it, f.m.Problems(it))
		}
	})
}

// treeOf is what a directory tree holds: each path with its mode and, for a
// file, its contents' hash.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		out[rel] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func treeDiff(a, b map[string]string) string {
	var out []string
	for k, v := range a {
		if b[k] != v {
			out = append(out, "-"+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "+"+k)
		}
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = append(out[:8], "...")
	}
	return strings.Join(out, " ")
}

// removeSome deletes about half of the files under dir, as a delete of the tree
// cut short does.
func removeSome(dir string) {
	var files []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	for i := 0; i < len(files); i += 2 {
		os.Remove(files[i])
	}
}

// A move across filesystems whose delete of its source is cut short (an error,
// or a crash) never leaves the space partial: after the recovery it is whole,
// restored or back in the bin, and it can be restored whole.
func TestACopyAcrossFilesystemsWhoseDeleteIsCutShortKeepsTheSpaceWhole(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"error", "crash"} {
		t.Run(mode, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				f := newRbFix(t, st)
				info := f.fullSpace("olive", "edith", "mia")
				item, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{})
				if err != nil {
					t.Fatal(err)
				}
				binSpace := filepath.Join(f.m.RecycleDir(item.ID), "space")
				whole := treeOf(t, binSpace)
				root, _ := f.m.paths(info.ID)

				cut, cutGo := make(chan struct{}), make(chan struct{})
				var once sync.Once
				f.mu.Lock()
				f.rename = func(oldpath, newpath string) error {
					return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
				}
				f.removeAll = func(p string) error {
					hit := false
					once.Do(func() { hit = true })
					if !hit {
						return os.RemoveAll(p)
					}
					removeSome(p)
					if mode == "crash" {
						// The process is gone from here: its hold ends and the
						// next start's recovery runs on what is on disk.
						close(cut)
						<-cutGo
					}
					return errors.New("the delete was cut short")
				}
				f.mu.Unlock()

				done := make(chan error, 1)
				go func() {
					_, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, "")
					done <- err
				}()
				if mode == "crash" {
					<-cut
					f.m.letItem(item.ID)
					if err := f.m.recoverRecycle(ctx, 0); err != nil {
						t.Fatal(err)
					}
					close(cutGo)
				}
				if err := <-done; err == nil {
					t.Fatal("the restore whose delete was cut short succeeded")
				}
				if err := f.m.recoverRecycle(ctx, 0); err != nil {
					t.Fatal(err)
				}
				f.mu.Lock()
				f.rename, f.removeAll = nil, nil
				f.mu.Unlock()

				switch {
				case pathExists(binSpace):
					if got := treeOf(t, binSpace); !sameTree(whole, got) {
						t.Fatalf("the bin holds a partial space: %s", treeDiff(whole, got))
					}
				case pathExists(root):
					if got := treeOf(t, root); !sameTree(whole, got) {
						t.Fatalf("the live place holds a partial space: %s", treeDiff(whole, got))
					}
					t.Fatal("the space is at its live place with its item still in the bin")
				default:
					t.Fatal("the space is nowhere")
				}
				leftovers, _ := filepath.Glob(filepath.Join(f.m.RecycleDir(item.ID), "*"+movedSuffix))
				if len(leftovers) > 0 {
					t.Fatalf("leftovers: %v", leftovers)
				}
				if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); err != nil {
					t.Fatal(err)
				}
				if got := treeOf(t, root); !sameTree(whole, got) {
					t.Fatalf("restored partial: %s", treeDiff(whole, got))
				}
				sp, release, err := f.m.Acquire(ctx, info.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				if l, err := sp.List(index.Query{Recursive: true}); err != nil || len(l.Items) != 1 {
					t.Fatalf("the pages: %v %v", l, err)
				}
			})
		})
	}
}

// The same cut short on the way to the bin: the recovery finishes the move and
// leaves no leftover at the live place.
func TestACopyToTheBinWhoseDeleteIsCutShortIsFinished(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		info := f.fullSpace("olive", "edith", "mia")
		root, _ := f.m.paths(info.ID)
		whole := treeOf(t, root)
		var once sync.Once
		f.mu.Lock()
		f.rename = func(oldpath, newpath string) error {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		f.removeAll = func(p string) error {
			hit := false
			once.Do(func() { hit = true })
			if !hit {
				return os.RemoveAll(p)
			}
			removeSome(p)
			return errors.New("the delete was cut short")
		}
		f.mu.Unlock()
		if _, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{}); err != nil {
			t.Fatal(err)
		}
		item := f.itemOfSpace(info.ID)
		if err := f.m.recoverRecycle(ctx, 0); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.rename, f.removeAll = nil, nil
		f.mu.Unlock()
		it := f.item(item.ID)
		if it.State != store.RecycleReady || len(f.m.Problems(it)) != 0 {
			t.Fatalf("%+v %v", it, f.m.Problems(it))
		}
		if got := treeOf(t, filepath.Join(f.m.RecycleDir(item.ID), "space")); !sameTree(whole, got) {
			t.Fatalf("the bin holds a partial space: %s", treeDiff(whole, got))
		}
		left, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "*"))
		for _, p := range left {
			if strings.HasPrefix(filepath.Base(p), info.ID) {
				t.Fatalf("a leftover at the live place: %s", p)
			}
		}
	})
}
