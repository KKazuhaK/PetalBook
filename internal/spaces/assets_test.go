package spaces

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
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
