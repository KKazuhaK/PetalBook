package spaces

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// Milestone 3.1, part 2: the attachment repository and the attachment cache.
// Remotes are local bare repositories that serve filters and fetches by id.

const (
	pageA = "SNAP000001"
	pageB = "ETHR000001"
)

// assetFixture is a space that follows a remote with two pages, and an
// attachment repository that is another remote.
type assetFixture struct {
	*rm
	in      Info
	h       *Handle
	remote  string
	aRemote string
}

func newAssetFixture(t *testing.T, st *store.Store, mod ...func(*Options)) *assetFixture {
	t.Helper()
	f := remoteManager(t, st, mod...)
	remote := filterBare(t)
	seedRemote(t, remote, 1)
	in, h := f.connected("Notes", remote)
	return &assetFixture{rm: f, in: in, h: h, remote: remote, aRemote: filterBare(t)}
}

func (a *assetFixture) connectAssets() *AssetsView {
	a.t.Helper()
	v, err := a.m.ConnectAssets(ctx, a.h, fileTokenReq(a.aRemote))
	if err != nil {
		a.t.Fatal(err)
	}
	return v
}

// remoteFiles lists the files of a remote's main branch.
func remoteFiles(t *testing.T, remote string) []string {
	t.Helper()
	if exec.Command("git", "-C", remote, "rev-parse", "-q", "--verify", "refs/heads/main").Run() != nil {
		return nil // nothing was ever pushed
	}
	out := runGit(t, remote, "ls-tree", "-r", "--name-only", "main")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func hasFile(t *testing.T, remote, rel string) bool {
	t.Helper()
	for _, f := range remoteFiles(t, remote) {
		if f == rel {
			return true
		}
	}
	return false
}

func readAsset(t *testing.T, sp *space.Space, rel string) (string, string, error) {
	t.Helper()
	f, _, etag, err := sp.OpenFileTag(ctx, rel)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), etag, err
}

func TestAnAttachmentGoesToTheAttachmentRepositoryAndIsServedFromTheCache(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		spaceTip := runGit(t, a.remote, "rev-parse", "main")
		v := a.connectAssets()
		if v.State != "ok" || v.Move != nil {
			t.Fatalf("%+v", v)
		}
		if got := a.m.AssetsMode(ctx, a.in.ID); got != "repository" {
			t.Fatalf("assets %q", got)
		}
		as, err := a.h.Space.PutAsset(space.Author{}, pageA, "street.png", []byte("PNGDATA"))
		if err != nil || !as.Created || as.Rel != "assets/"+pageA+"/"+as.Name {
			t.Fatalf("%+v %v", as, err)
		}
		// Pushed before the answer; the space's own repository did not change and
		// has no assets/ folder.
		if !hasFile(t, a.aRemote, pageA+"/"+as.Name) {
			t.Fatalf("the attachment repository has %v", remoteFiles(t, a.aRemote))
		}
		if runGit(t, a.remote, "rev-parse", "main") != spaceTip {
			t.Fatal("an upload changed the space's repository")
		}
		if exists(filepath.Join(a.root(a.in.ID), "assets")) {
			t.Fatal("an upload made an assets/ folder in the space")
		}
		// Links stay the same: the path is the one of the files API.
		body, etag, err := readAsset(t, a.h.Space, as.Rel)
		if err != nil || body != "PNGDATA" || etag != gitstore.BlobID([]byte("PNGDATA")) {
			t.Fatalf("%q %q %v", body, etag, err)
		}
		// The same bytes again store nothing new.
		again, err := a.h.Space.PutAsset(space.Author{}, pageA, "street.png", []byte("PNGDATA"))
		if err != nil || again.Created {
			t.Fatalf("%+v %v", again, err)
		}
		// A path the table does not have is not found without asking the remote.
		n := a.r.fetchCount()
		if _, _, err := readAsset(t, a.h.Space, "assets/"+pageA+"/missing.png"); !errors.Is(err, space.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if a.r.fetchCount() != n {
			t.Fatal("a path that is not in the table asked the remote")
		}
	})
}

func TestAttachmentsThatAreNotCachedAreFetchedOrAnswerNotCached(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		a.connectAssets()
		as, err := a.h.Space.PutAsset(space.Author{}, pageA, "a.bin", []byte("AAAA"))
		if err != nil {
			t.Fatal(err)
		}
		bs, err := a.h.Space.PutAsset(space.Author{}, pageA, "b.bin", []byte("BBBB"))
		if err != nil {
			t.Fatal(err)
		}
		cache := a.m.AssetCache()
		if cache.SpaceBytes(a.in.ID) != 8 || a.m.AssetBytes(a.in.ID) != 8 {
			t.Fatalf("cache bytes %d", cache.SpaceBytes(a.in.ID))
		}
		// The cache loses everything (a restart that dropped it, a trim).
		if err := cache.DropSpace(a.in.ID); err != nil {
			t.Fatal(err)
		}
		// The remote cannot be reached: a read that is not cached says so.
		a.r.setDown(true)
		_, _, err = readAsset(t, a.h.Space, as.Rel)
		var nc *space.NotCachedError
		if !errors.As(err, &nc) || nc.Fetching {
			t.Fatalf("want not cached, got %v", err)
		}
		// Uploads answer unavailable and store nothing.
		if _, err := a.h.Space.PutAsset(space.Author{}, pageA, "c.bin", []byte("CCCC")); err == nil {
			t.Fatal("an upload worked with the remote down")
		} else {
			var ue *space.UnavailableError
			if !errors.As(err, &ue) {
				t.Fatalf("%v", err)
			}
		}
		a.r.setDown(false)
		if hasFile(t, a.aRemote, pageA+"/"+space.AssetName("c.bin", []byte("CCCC"))) {
			t.Fatal("a failed upload left a file on the remote")
		}
		// Back up: the next read fetches it by its id, and others share the fetch.
		body, _, err := readAsset(t, a.h.Space, as.Rel)
		if err != nil || body != "AAAA" {
			t.Fatalf("%q %v", body, err)
		}
		if cache.SpaceBytes(a.in.ID) != 4 {
			t.Fatalf("only what was read is cached: %d", cache.SpaceBytes(a.in.ID))
		}
		a.r.setDown(true)
		if body, _, err := readAsset(t, a.h.Space, as.Rel); err != nil || body != "AAAA" {
			t.Fatalf("a cached attachment must be served offline: %q %v", body, err)
		}
		a.r.setDown(false)
		if body, _, err := readAsset(t, a.h.Space, bs.Rel); err != nil || body != "BBBB" {
			t.Fatalf("%q %v", body, err)
		}
		// The failed fetch put the attachment repository in the state offline and the
		// next success brings it back, without pausing the space.
		if v, err := a.m.AssetsRemote(ctx, a.h); err != nil || v.State != "ok" {
			t.Fatalf("%+v %v", v, err)
		}
		_ = fmt.Sprint
		_ = os.Stat
		_ = page.ValidID
		_ = time.Second
	})
}

// uploadBeforeMove puts attachments in assets/ of the space, as a space that
// never had an attachment repository does.
func (a *assetFixture) uploadBeforeMove() map[string]string {
	a.t.Helper()
	want := map[string]string{}
	for _, up := range []struct{ id, name, data string }{
		{pageA, "one.png", "ONE-" + strings.Repeat("x", 3000)},
		{pageA, "two.pdf", "TWO-" + strings.Repeat("y", 5000)},
		{pageB, "three.txt", "THREE"},
	} {
		as, err := a.h.Space.PutAsset(space.Author{}, up.id, up.name, []byte(up.data))
		if err != nil {
			a.t.Fatal(err)
		}
		want[as.Rel] = up.data
	}
	return want
}

func (a *assetFixture) waitMove(state string) *MoveView {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		v, err := a.m.AssetsRemote(ctx, a.h)
		if err == nil && v.Move != nil && v.Move.State == state {
			return v.Move
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("the move did not reach %s: %+v %v", state, v, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// nothingLost checks that every attachment is on a remote, in one place at
// least: the space's repository (assets/) or the attachment repository.
func nothingLost(t *testing.T, a *assetFixture, want map[string]string) {
	t.Helper()
	for rel, data := range want {
		inSpace := hasFile(t, a.remote, rel)
		inAssets := hasFile(t, a.aRemote, strings.TrimPrefix(rel, "assets/"))
		if !inSpace && !inAssets {
			t.Fatalf("%s is on neither remote", rel)
		}
		if inAssets {
			id := runGit(t, a.aRemote, "rev-parse", "main:"+strings.TrimPrefix(rel, "assets/"))
			if id != gitstore.BlobID([]byte(data)) {
				t.Fatalf("%s has other contents in the attachment repository", rel)
			}
		}
	}
}

func TestMovingAttachmentsIntoTheAttachmentRepository(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		want := a.uploadBeforeMove()
		for rel := range want {
			if !hasFile(t, a.remote, rel) {
				t.Fatalf("%s is not in the space's repository", rel)
			}
		}
		a.connectAssets()
		// Connecting moves nothing.
		if len(remoteFiles(t, a.aRemote)) != 0 {
			t.Fatal("connecting moved files")
		}
		for rel, data := range want { // served from assets/ of the space until moved
			if body, etag, err := readAsset(t, a.h.Space, rel); err != nil || body != data || etag != "" {
				t.Fatalf("%s: %v etag %q", rel, err, etag)
			}
		}
		if _, err := a.m.ConnectAssets(ctx, a.h, fileTokenReq(a.aRemote)); err != nil {
			t.Fatalf("the same repository with the same credential: %v", err)
		}
		v, started, err := a.m.StartMove(ctx, a.h)
		if err != nil || !started || v.Move == nil || v.Move.Files != 3 {
			t.Fatalf("%+v %v %v", v, started, err)
		}
		if _, _, err := a.m.StartMove(ctx, a.h); err != nil && !errors.Is(err, ErrAssetsConflict) {
			t.Fatalf("%v", err)
		}
		mv := a.waitMove(store.MoveDone)
		if mv.Files != 3 || mv.FilesDone != 3 || mv.Bytes == 0 || mv.BytesDone != mv.Bytes {
			t.Fatalf("%+v", mv)
		}
		nothingLost(t, a, want)
		for rel := range want {
			if hasFile(t, a.remote, rel) {
				t.Fatalf("%s is still in the space's HEAD", rel)
			}
			if !hasFile(t, a.aRemote, strings.TrimPrefix(rel, "assets/")) {
				t.Fatalf("%s is not in the attachment repository", rel)
			}
		}
		// The history of the space still has them, and .gitignore keeps assets/ out.
		if log := runGit(t, a.remote, "log", "--all", "--name-only", "--format="); !strings.Contains(log, "assets/"+pageA+"/") {
			t.Fatalf("history lost the attachments:\n%s", log)
		}
		if gi := runGit(t, a.remote, "cat-file", "-p", "main:.gitignore"); !strings.Contains(gi, "/assets/") {
			t.Fatalf(".gitignore: %q", gi)
		}
		if runGit(t, a.remote, "log", "-n1", "--format=%s", "main") != space.SwitchMessage {
			t.Fatal("the last commit is not the switch")
		}
		if exists(filepath.Join(a.root(a.in.ID), "assets")) {
			t.Fatal("assets/ is still on disk")
		}
		// The same links, now from the repository, with a tag.
		for rel, data := range want {
			body, etag, err := readAsset(t, a.h.Space, rel)
			if err != nil || body != data || etag != gitstore.BlobID([]byte(data)) {
				t.Fatalf("%s: %q %v", rel, etag, err)
			}
		}
		// The pages are unchanged and the space is writable and in sync.
		if p, err := a.h.Space.Get(pageA); err != nil || p.Body != "version 1\n" {
			t.Fatalf("%v %v", p, err)
		}
		if _, err := a.h.Space.PutAsset(space.Author{}, pageB, "four.txt", []byte("FOUR")); err != nil {
			t.Fatal(err)
		}
		// A second move has nothing to do; the repository cannot be taken away.
		if v, started, err := a.m.StartMove(ctx, a.h); err != nil || started {
			t.Fatalf("%+v %v %v", v, started, err)
		}
		if err := a.m.DisconnectAssets(ctx, a.h); !errors.Is(err, ErrAssetsConflict) {
			t.Fatalf("%v", err)
		}
		// Step 3: the clone is replaced by a partial one that lacks the old
		// attachments; they are read from the repository after the restore.
		a.h.Release()
		a.idle(72 * time.Hour)
		if err := a.m.Evict(ctx, a.in.ID, EvictOptions{}); err != nil && !errors.Is(err, ErrNotEvictable) {
			t.Fatal(err)
		}
		h2, err := a.m.Open(ctx, a.user, a.in.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h2.Release()
		for rel, data := range want {
			if body, _, err := readAsset(t, h2.Space, rel); err != nil || body != data {
				t.Fatalf("after the restore %s: %v", rel, err)
			}
		}
		if exists(filepath.Join(a.root(a.in.ID), "assets")) {
			t.Fatal("the restored clone has assets/")
		}
	})
}

func TestACrashAtEveryStepOfMovingAttachmentsLosesNone(t *testing.T) {
	steps := []string{"copied", "copied-all", "switching", "switch-verified", "switch-committed", "switch-reset", "switch-pushed"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				old := assetrepoChunk(1500) // so that the copy takes several commits
				defer old()
				var crash string
				crashed := make(chan struct{}, 1)
				a := newAssetFixture(t, st, func(o *Options) {
					o.AssetsHook = func(s string) error {
						if s == crash {
							select {
							case crashed <- struct{}{}:
							default:
							}
							return errors.New("crash at " + s)
						}
						return nil
					}
				})
				want := a.uploadBeforeMove()
				a.connectAssets()
				crash = step
				if _, _, err := a.m.StartMove(ctx, a.h); err != nil {
					t.Fatal(err)
				}
				select {
				case <-crashed:
				case <-time.After(20 * time.Second):
					t.Fatalf("the move never reached %s", step)
				}
				// Wait until the run has ended, as a stopped process would have.
				deadline := time.Now().Add(10 * time.Second)
				for a.m.moveRunning(a.in.ID) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				nothingLost(t, a, want)
				// The move was not recorded as a failure: it goes on at the next start.
				row, err := store.GetAssetRemote(ctx, st.Q(), a.in.ID)
				if err != nil || !row.Move.Running() {
					t.Fatalf("the move after a crash at %s: %+v %v", step, row, err)
				}
				a.h.Release()
				a.m.Close()
				crash = ""
				m2 := a.open(serving)
				h, err := m2.Open(ctx, a.user, a.in.ID)
				if err != nil {
					t.Fatalf("the space does not open after a crash at %s: %v", step, err)
				}
				defer h.Release()
				a.m, a.h = m2, h
				a.waitMove(store.MoveDone)
				nothingLost(t, a, want)
				for rel, data := range want {
					if body, _, err := readAsset(t, h.Space, rel); err != nil || body != data {
						t.Fatalf("after a crash at %s, %s: %v", step, rel, err)
					}
					if hasFile(t, a.remote, rel) {
						t.Fatalf("%s is still in the space's repository after the move", rel)
					}
				}
				if p, err := h.Space.Get(pageA); err != nil || p.Body != "version 1\n" {
					t.Fatalf("%v %v", p, err)
				}
				// The space is in good order: writable, in sync with its remote.
				if _, err := h.Space.PutAsset(space.Author{}, pageB, "after.txt", []byte("AFTER")); err != nil {
					t.Fatalf("not writable after a crash at %s: %v", step, err)
				}
				if st, _ := h.Space.Remote(); st.State != "ok" {
					t.Fatalf("remote %+v", st)
				}
			})
		})
	}
}

func assetrepoChunk(n int64) func() {
	old := assetrepoMaxCommit()
	setAssetrepoMaxCommit(n)
	return func() { setAssetrepoMaxCommit(old) }
}

// movedFixture is a space whose attachments have moved to the attachment
// repository, idle and ready to be evicted.
func movedFixture(t *testing.T, st *store.Store, mod ...func(*Options)) (*assetFixture, map[string]string) {
	t.Helper()
	a := newAssetFixture(t, st, mod...)
	want := a.uploadBeforeMove()
	a.connectAssets()
	if _, _, err := a.m.StartMove(ctx, a.h); err != nil {
		t.Fatal(err)
	}
	a.waitMove(store.MoveDone)
	a.h.Release()
	a.idle(72 * time.Hour)
	return a, want
}

func TestACrashAtEveryStepOfAnEvictionWithAnAttachmentRepositoryLosesNone(t *testing.T) {
	for _, step := range []string{"held", "verified", "marked", "moved", "recorded", "removed"} {
		t.Run(step, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				var crash string
				a, want := movedFixture(t, st, func(o *Options) {
					o.EvictHook = func(s string) error {
						if s == crash {
							return errors.New("crash at " + s)
						}
						return nil
					}
				})
				// The move's own attempt to replace the clone may already have done it.
				_ = a.m.Evict(ctx, a.in.ID, EvictOptions{})
				a.m.Close()
				a = reopen(t, a, serving)
				// An evicted space is restored first; evict it again with a crash.
				h, err := a.m.Open(ctx, a.user, a.in.ID)
				if err != nil {
					t.Fatal(err)
				}
				for rel, data := range want {
					if body, _, err := readAsset(t, h.Space, rel); err != nil || body != data {
						t.Fatalf("%s: %v", rel, err)
					}
				}
				h.Release()
				a.idle(72 * time.Hour)
				crash = step
				a.m.Close()
				a = reopen(t, a, serving, func(o *Options) {
					o.EvictHook = func(s string) error {
						if s == crash {
							return errors.New("crash at " + s)
						}
						return nil
					}
				})
				if err := a.m.Evict(ctx, a.in.ID, EvictOptions{}); err == nil || !strings.Contains(err.Error(), "crash at "+step) {
					t.Fatalf("%v", err)
				}
				a.m.Close()
				crash = ""
				a = reopen(t, a, serving)
				h, err = a.m.Open(ctx, a.user, a.in.ID)
				if err != nil {
					t.Fatalf("the space does not open after a crash at %s: %v", step, err)
				}
				defer h.Release()
				for rel, data := range want {
					if body, etag, err := readAsset(t, h.Space, rel); err != nil || body != data || etag == "" {
						t.Fatalf("after a crash at %s, %s: %q %v", step, rel, etag, err)
					}
				}
				nothingLost(t, a, want)
			})
		})
	}
}

// reopen starts another manager on the same data, as a restarted server does.
func reopen(t *testing.T, a *assetFixture, mod ...func(*Options)) *assetFixture {
	t.Helper()
	b := *a
	b.rm = &rm{t: a.t, st: a.st, r: a.r, clk: a.clk, user: a.user, dir: a.dir}
	b.m = b.rm.open(mod...)
	return &b
}

func TestNothingIsEvictedWhileTheAttachmentRepositoryMayHoldTheOnlyCopy(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		// A move that has not ended: the space's assets/ is the only complete copy.
		hold := make(chan struct{})
		var released bool
		release := func() {
			if !released {
				released = true
				close(hold)
			}
		}
		defer release()
		var once bool
		a := newAssetFixture(t, st, func(o *Options) {
			o.AssetsHook = func(s string) error {
				if s == "copied-all" && !once {
					once = true
					<-hold
				}
				return nil
			}
		})
		a.uploadBeforeMove()
		a.connectAssets()
		if _, _, err := a.m.StartMove(ctx, a.h); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		a.h.Release()
		a.idle(72 * time.Hour)
		err := a.m.Evict(ctx, a.in.ID, EvictOptions{})
		if !errors.Is(err, ErrBusy) || !exists(a.root(a.in.ID)) {
			t.Fatalf("a space whose attachments are being moved was evicted or not refused as busy: %v", err)
		}
		release()
		a.waitMove(store.MoveDone)
		time.Sleep(100 * time.Millisecond)
	})
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a, _ := movedFixture(t, st)
		a.m.Close()
		a = reopen(t, a, serving)
		h, err := a.m.Open(ctx, a.user, a.in.ID)
		if err != nil {
			t.Fatal(err)
		}
		h.Release()
		a.idle(72 * time.Hour)
		root := a.root(a.in.ID)
		// A file left in assets/ after the move would go with the clone.
		os.MkdirAll(filepath.Join(root, "assets", pageA), 0o755)
		os.WriteFile(filepath.Join(root, "assets", pageA, "stray.png"), []byte("stray"), 0o644)
		mustBusy(t, a.m.Evict(ctx, a.in.ID, EvictOptions{}), root, "assets/")
		os.RemoveAll(filepath.Join(root, "assets"))
		// An upload that was cut short left its scratch repository.
		scratch := filepath.Join(a.m.assetsDir(a.in.ID), ".git", "petalbook-scratch", "1-x")
		os.MkdirAll(scratch, 0o755)
		mustBusy(t, a.m.Evict(ctx, a.in.ID, EvictOptions{}), root, "not finished")
		os.RemoveAll(scratch)
		// The attachment repository cannot be reached: not evicted without checking it.
		a.r.setDown(true)
		err = a.m.Evict(ctx, a.in.ID, EvictOptions{})
		a.r.setDown(false)
		var ue *space.UnavailableError
		if !errors.As(err, &ue) && !errors.Is(err, ErrBusy) {
			t.Fatalf("%v", err)
		}
		if !exists(root) {
			t.Fatal("the space was evicted without checking the attachment repository")
		}
		if err := a.m.Evict(ctx, a.in.ID, EvictOptions{}); err != nil {
			t.Fatalf("%v", err)
		}
		if exists(root) {
			t.Fatal("not evicted")
		}
		if a.m.AssetBytes(a.in.ID) != 0 {
			t.Fatal("the cached attachments of an evicted space stayed")
		}
	})
}

func TestAnImportPushesItsAttachmentsBeforeItsPages(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		a.connectAssets()
		item := func(title string) space.ImportPage {
			return space.ImportPage{Meta: page.Meta{{Key: "title", Value: title}}, Body: "see ![x](TOKEN)\n",
				Assets: []space.ImportAsset{{Token: "TOKEN", Name: title + ".png", Data: []byte("IMG-" + title)}}}
		}
		// The space's remote refuses the push of the pages: the import fails, with
		// no page made, and the attachments stay on their remote, referred to by nothing.
		hook := filepath.Join(a.remote, "hooks", "pre-receive")
		os.MkdirAll(filepath.Dir(hook), 0o755)
		os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755)
		before := runGit(t, a.remote, "rev-parse", "main")
		if _, _, err := a.h.Space.Import(space.Author{}, "", []space.ImportPage{item("one")}); err == nil {
			t.Fatal("the import worked although the remote refused the pages")
		}
		if runGit(t, a.remote, "rev-parse", "main") != before {
			t.Fatal("the space's remote changed")
		}
		files := remoteFiles(t, a.aRemote)
		if len(files) != 1 || !strings.Contains(files[0], "one-") {
			t.Fatalf("the attachment repository has %v", files)
		}
		if exists(filepath.Join(a.root(a.in.ID), "assets")) {
			t.Fatal("an assets/ folder was made in the space")
		}
		// Without the refusal: attachments first (already on the remote), then the pages.
		os.Remove(hook)
		pages, n, err := a.h.Space.Import(space.Author{}, "", []space.ImportPage{item("two")})
		if err != nil || pages != 1 || n != 1 {
			t.Fatalf("%d %d %v", pages, n, err)
		}
		if got := len(remoteFiles(t, a.aRemote)); got != 2 {
			t.Fatalf("the attachment repository has %d files", got)
		}
		if runGit(t, a.remote, "rev-parse", "main") == before {
			t.Fatal("the pages were not pushed")
		}
		// The imported page's link resolves through the repository.
		res, _ := a.h.Space.List(index.Query{Recursive: true})
		for _, it := range res.Items {
			if it.Title == "two" {
				p, _ := a.h.Space.Get(it.ID)
				link := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(p.Body, "see ![x](")), "/"), ")")
				if body, _, err := readAsset(t, a.h.Space, link); err != nil || body != "IMG-two" {
					t.Fatalf("%q: %q %v", link, body, err)
				}
			}
		}
	})
}

// The attachment repository loses its branch while the move is switching (the
// branch deleted, or the repository made again, empty): the switch must not take
// assets/ out of the space on the strength of the table the server had.
func TestAMoveWhoseAttachmentRepositoryLostItsBranchLosesNone(t *testing.T) {
	cases := map[string]func(remote string){
		"branch deleted": func(remote string) { runGitQuiet(remote, "update-ref", "-d", "refs/heads/main") },
		"recreated empty": func(remote string) {
			os.RemoveAll(remote)
			os.MkdirAll(remote, 0o755)
			runGitQuiet(remote, "init", "--bare", "-q", "-b", "main")
			runGitQuiet(remote, "config", "uploadpack.allowFilter", "true")
			runGitQuiet(remote, "config", "uploadpack.allowAnySHA1InWant", "true")
		},
	}
	for name, lose := range cases {
		t.Run(name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				var aRemote string
				var once bool
				a := newAssetFixture(t, st, func(o *Options) {
					o.AssetsHook = func(s string) error {
						if s == "switching" && !once {
							once = true
							lose(aRemote)
						}
						return nil
					}
				})
				defer a.h.Release()
				aRemote = a.aRemote
				want := a.uploadBeforeMove()
				a.connectAssets()
				if _, _, err := a.m.StartMove(ctx, a.h); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(20 * time.Second)
				for a.m.moveRunning(a.in.ID) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !once {
					t.Fatal("the move never reached switching")
				}
				nothingLost(t, a, want)
				for rel, data := range want {
					if body, _, err := readAsset(t, a.h.Space, rel); err != nil || body != data {
						t.Fatalf("%s: %q %v", rel, body, err)
					}
				}
			})
		})
	}
}

// An upload whose attachment repository is gone answers unavailable and says
// nothing about the space's own remote, which is fine.
func TestAFailedAttachmentPushLeavesTheSpaceRemoteAlone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		a.connectAssets()
		if err := os.RemoveAll(a.aRemote); err != nil {
			t.Fatal(err)
		}
		_, err := a.h.Space.PutAsset(space.Author{}, pageA, "gone.png", []byte("GONE"))
		var ue *space.UnavailableError
		if !errors.As(err, &ue) {
			t.Fatalf("want unavailable, got %v", err)
		}
		if rs, _ := a.h.Space.Remote(); rs.State != "ok" {
			t.Fatalf("the space's remote: %+v", rs)
		}
		if _, err := a.h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Still writable"}}}); err != nil {
			t.Fatalf("the space is not writable: %v", err)
		}
	})
}

// Whether assets/ at the root is the space's own is looked up once, not on
// every read: it is looked at again after a change of the space and a pull.
func TestWhetherAssetsIsOursIsKeptUntilTheSpaceChanges(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		a.connectAssets()
		as, err := a.h.Space.PutAsset(space.Author{}, pageA, "kept.png", []byte("KEPT"))
		if err != nil {
			t.Fatal(err)
		}
		if body, _, err := readAsset(t, a.h.Space, as.Rel); err != nil || body != "KEPT" {
			t.Fatalf("%q %v", body, err)
		}
		// A folder made behind the space's back is not looked for on each read.
		other := filepath.Join(a.root(a.in.ID), "ASSETS")
		if err := os.Mkdir(other, 0o755); err != nil {
			t.Fatal(err)
		}
		if body, _, err := readAsset(t, a.h.Space, as.Rel); err != nil || body != "KEPT" {
			t.Fatalf("the answer was looked up again: %q %v", body, err)
		}
		os.Remove(other)
		// A pull that brings a folder in the way is seen.
		push(t, a.remote, "ASSETS/readme.txt", "in the way\n")
		if err := a.h.Space.Pull(ctx, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readAsset(t, a.h.Space, as.Rel); !errors.Is(err, space.ErrNotFound) {
			t.Fatalf("a folder in the way after a pull: %v", err)
		}
	})
}

// A space whose attachment repository has files (taken when it was connected)
// may refer to them: the repository cannot be swapped for another address.
func TestAnAttachmentRepositoryWithFilesIsNotSwappedForAnother(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		push(t, a.aRemote, pageA+"/theirs.png", "THEIRS")
		a.connectAssets()
		if _, err := a.m.AssetsRemote(ctx, a.h); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, ok := a.h.Space.Assets().Lookup(pageA + "/theirs.png"); ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the table never had the file")
			}
			time.Sleep(10 * time.Millisecond)
		}
		other := filterBare(t)
		if _, err := a.m.ConnectAssets(ctx, a.h, fileTokenReq(other)); !errors.Is(err, ErrAssetsConflict) {
			t.Fatalf("swapped for another repository: %v", err)
		}
	})
}

// The switch was pushed but its answer was lost (and the check whether it
// landed could not be made): the move says failed for now, and once the space
// has the switch at its HEAD the move is done.
func TestASwitchWhoseAnswerWasLostEndsDone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		var r *runner
		a := newAssetFixture(t, st, func(o *Options) {
			o.AssetsHook = func(s string) error {
				if s == "switch-reset" {
					r.mu.Lock()
					r.loseAnswer = true
					r.mu.Unlock()
				}
				return nil
			}
		})
		r = a.r
		defer a.h.Release()
		want := a.uploadBeforeMove()
		a.connectAssets()
		if _, _, err := a.m.StartMove(ctx, a.h); err != nil {
			t.Fatal(err)
		}
		a.waitMove(store.MoveFailed)
		a.r.setDown(false)
		if runGit(t, a.remote, "log", "-n1", "--format=%s", "main") != space.SwitchMessage {
			t.Fatal("the switch did not land")
		}
		if err := a.h.Space.Pull(ctx, true); err != nil {
			t.Fatal(err)
		}
		v, started, err := a.m.StartMove(ctx, a.h)
		if err != nil || started || v.Move == nil || v.Move.State != store.MoveDone {
			t.Fatalf("%+v %v %v", v, started, err)
		}
		if v, err := a.m.AssetsRemote(ctx, a.h); err != nil || v.Move.State != store.MoveDone {
			t.Fatalf("%+v %v", v, err)
		}
		nothingLost(t, a, want)
	})
}

// The attachment bytes of a tenant on a cold map look the tenants of the cached
// spaces up together, not one query for each space.
func TestTheAttachmentBytesOfATenantLookUpTheSpacesTogether(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		var ids []string
		for i := 0; i < 5; i++ {
			in, _ := f.space(fmt.Sprintf("S%d", i))
			ids = append(ids, in.ID)
			if _, err := f.m.AssetCache().Put(in.ID, strings.Repeat("a", 40), strings.NewReader("1234")); err != nil {
				t.Fatal(err)
			}
		}
		// A space the database does not know (its files are dropped with it).
		if _, err := f.m.AssetCache().Put("GONE000001", strings.Repeat("a", 40), strings.NewReader("12")); err != nil {
			t.Fatal(err)
		}
		queries := 0
		testHookTenantLookup = func() { queries++ }
		defer func() { testHookTenantLookup = nil }()
		got, err := f.m.AssetBytesOfTenant(ctx, store.DefaultTenantID)
		if err != nil || got != 20 {
			t.Fatalf("%d %v, want 20", got, err)
		}
		if queries != 1 {
			t.Fatalf("%d queries for %d spaces", queries, len(ids)+1)
		}
		if other, err := f.m.AssetBytesOfTenant(ctx, "Other00000"); err != nil || other != 0 {
			t.Fatalf("%d %v", other, err)
		}
		if queries != 1 { // the space that is gone is remembered as gone, like the rest
			t.Fatalf("%d queries after two calls", queries)
		}

		// A space not seen before is looked up; the memory cleared meanwhile (by
		// another call reaching its bound) does not take the known ones out of the sum.
		in, _ := f.space("S5")
		if _, err := f.m.AssetCache().Put(in.ID, strings.Repeat("a", 40), strings.NewReader("1234")); err != nil {
			t.Fatal(err)
		}
		testHookTenantLookup = func() {
			queries++
			f.m.as.mu.Lock()
			f.m.as.tenants = map[string]string{}
			f.m.as.mu.Unlock()
		}
		if got, err := f.m.AssetBytesOfTenant(ctx, store.DefaultTenantID); err != nil || got != 24 {
			t.Fatalf("%d %v, want 24", got, err)
		}
		if queries != 2 {
			t.Fatalf("%d queries after three calls", queries)
		}
	})
}

// Connecting the attachment repository again (a new token, another repository)
// swaps its directory for the one tried out. Something that opens the
// repository meanwhile (the refresh after the last connect, the pull loop, a
// status read) waits for the swap and gets the repository of the new row; it
// used to make the directory again, and the swap failed.
func TestReconnectingTheAttachmentRepositoryWhileItIsOpened(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		type opened struct {
			r   any
			row *store.AssetRemote
			err error
		}
		var armed atomic.Bool
		got := make(chan opened, 1)
		a.m.testHook = func(point string) {
			if point != "assets-swap" || !armed.CompareAndSwap(true, false) {
				return
			}
			go func() {
				r, row, err := a.m.assetRepo(ctx, a.in.ID)
				got <- opened{r, row, err}
			}()
			// Given the time to open it, if it is let through.
			select {
			case o := <-got:
				got <- o
			case <-time.After(500 * time.Millisecond):
			}
		}
		a.connectAssets()
		other := filterBare(t)
		armed.Store(true)
		if _, err := a.m.ConnectAssets(ctx, a.h, fileTokenReq(other)); err != nil {
			t.Fatalf("connecting again while the repository is opened: %v", err)
		}
		o := <-got
		if o.err != nil || o.r == nil {
			t.Fatalf("the opener: %v %v", o.r, o.err)
		}
		row, err := store.GetAssetRemote(ctx, st.Q(), a.in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if o.row.URL != row.URL || row.URL == "" {
			t.Fatalf("the opener read %q; the row is %q", o.row.URL, row.URL)
		}
		r, _, err := a.m.assetRepo(ctx, a.in.ID)
		if err != nil || any(r) != o.r {
			t.Fatalf("the opener got another repository than the one kept: %v", err)
		}
		if r.Status().State != "ok" {
			t.Fatalf("%+v", r.Status())
		}
	})
}

// A refresh of the repository replaced by a new connect is cancelled; that is
// not the repository being offline, and must not overwrite the new state.
func TestARefreshCancelledByAReconnectLeavesTheStateAlone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAssetFixture(t, st)
		defer a.h.Release()
		release, in := make(chan struct{}), make(chan struct{})
		a.r.mu.Lock()
		a.r.holdDir, a.r.holdOnce, a.r.heldIn = a.m.assetsDir(a.in.ID), release, in
		a.r.mu.Unlock()
		a.connectAssets()
		select {
		case <-in: // the refresh after the connect is on its way
		case <-time.After(10 * time.Second):
			t.Fatal("no refresh after the connect")
		}
		a.connectAssets() // the same repository again (a new token)
		close(release)
		a.m.Close() // waits for the refresh
		row, err := store.GetAssetRemote(ctx, st.Q(), a.in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != store.RemoteOK {
			t.Fatalf("the state is %q (%s) after connecting again", row.State, row.LastError)
		}
	})
}
