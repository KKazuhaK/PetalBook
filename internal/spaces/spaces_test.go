package spaces

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

var ctx = context.Background()

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func testManager(t *testing.T, st *store.Store) *Manager {
	t.Helper()
	needGit(t)
	dir, err := os.MkdirTemp("", "petalbook-spaces-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	m, err := NewAccounts(Options{Store: st, DataDir: filepath.Join(dir, "data"), Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func addUser(t *testing.T, st *store.Store, id, username string) store.User {
	t.Helper()
	u := store.User{ID: id, Username: username, Name: username, Role: "member", Created: time.Now(), TenantID: store.DefaultTenantID}
	if err := store.InsertUser(ctx, st.Q(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSingleMode(t *testing.T) {
	t.Parallel()
	needGit(t)
	st := storetest.Open(t, "sqlite")
	sp, err := space.Open(space.Options{Root: filepath.Join(t.TempDir(), "s"), Name: "Mine", CommitIdle: time.Hour, CommitMax: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	m, err := NewSingle(ctx, st, sp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Single() {
		t.Fatal()
	}
	l, _ := m.List(ctx, store.LocalUserID)
	if len(l) != 1 || l[0] != (Info{ID: "default", Name: "Mine", Role: "owner", DirectRole: "owner", Perms: perm.All, Personal: true, Inbox: "Inbox", Sync: "local"}) {
		t.Fatalf("%+v", l)
	}
	if h, err := m.Open(ctx, store.User{ID: store.LocalUserID, Role: "admin"}, "default"); err != nil || h.Space != sp {
		t.Fatalf("%v %v", h, err)
	}
	if _, err := m.Open(ctx, store.User{ID: store.LocalUserID, Role: "admin"}, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := m.Open(ctx, store.User{ID: "someone"}, "default"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, store.LocalUserID, "x"); err == nil {
		t.Fatal("a second space in single-user mode")
	}
	// The database knows the user and the space, so drafts can refer to them,
	// and doing it again (a restart) is harmless.
	if _, err := NewSingle(ctx, st, sp, nil); err != nil {
		t.Fatal(err)
	}
	if u, err := store.GetUser(ctx, st.Q(), store.AllTenants(), store.LocalUserID); err != nil || u.Role != "admin" {
		t.Fatalf("%v %v", u, err)
	}
}

func TestOpenIsLazyAndOnce(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		u := addUser(t, st, "U1", "kazuha")
		a, err := m.Create(ctx, u.ID, "A")
		if err != nil {
			t.Fatal(err)
		}
		b, err := m.Create(ctx, u.ID, "B")
		if err != nil {
			t.Fatal(err)
		}
		// Reopen the manager as after a restart: nothing is open until asked.
		m2, err := NewAccounts(Options{Store: st, DataDir: m.opt.DataDir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		defer m2.Close()
		if l, _ := m2.List(ctx, u.ID); len(l) != 2 || len(m2.open) != 0 {
			t.Fatalf("%d spaces listed, %d open", len(l), len(m2.open))
		}
		var wg sync.WaitGroup
		got := make([]*space.Space, 8)
		for i := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h, err := m2.Open(ctx, u, a.ID)
				if err != nil {
					t.Error(err)
					return
				}
				got[i] = h.Space
			}()
		}
		wg.Wait()
		for _, s := range got {
			if s != got[0] || s == nil {
				t.Fatal("one space was opened more than once")
			}
		}
		if len(m2.open) != 1 {
			t.Fatalf("%d open: the other space should not be", len(m2.open))
		}
		_ = b
	})
}

func TestMembershipDecidesAccessAndIsCached(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		u1, u2 := addUser(t, st, "U1", "one"), addUser(t, st, "U2", "two")
		a, _ := m.Create(ctx, u1.ID, "A")
		if _, err := m.Open(ctx, u2, a.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a non-member got in: %v", err)
		}
		if _, err := m.Open(ctx, u1, "0000000000"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		for _, bad := range []string{"", "../../etc", "a/b", "..", strings.Repeat("A", 11)} {
			if _, err := m.Open(ctx, u1, bad); !errors.Is(err, ErrNotFound) {
				t.Errorf("%q: %v", bad, err)
			}
			if _, _, err := m.Acquire(ctx, bad); !errors.Is(err, ErrNotFound) {
				t.Errorf("Acquire(%q): %v", bad, err)
			}
		}
		// The list is read once and then served from memory ...
		if _, err := m.List(ctx, u2.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), a.ID, u2.ID, "owner", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Open(ctx, u2, a.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("the cache was not used")
		}
		// ... until something about the user's spaces changes.
		m.MembershipChanged(u2.ID)
		if h, err := m.Open(ctx, u2, a.ID); err != nil || h.Info.Role != "owner" {
			t.Fatalf("%v %v", h, err)
		}
		// Creating a space shows up at once, without a Forget.
		l1, _ := m.List(ctx, u1.ID)
		if _, err := m.Create(ctx, u1.ID, "Another"); err != nil {
			t.Fatal(err)
		}
		if l2, _ := m.List(ctx, u1.ID); len(l2) != len(l1)+1 {
			t.Fatalf("%d -> %d", len(l1), len(l2))
		}
	})
}

func TestRenameIsForOwnersAndInvalidatesEveryMember(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		u1, u2 := addUser(t, st, "U1", "one"), addUser(t, st, "U2", "two")
		a, _ := m.Create(ctx, u1.ID, "A")
		store.AddMember(ctx, st.Q(), a.ID, u2.ID, "reader", time.Now())
		m.MembershipChanged(u2.ID)
		m.List(ctx, u2.ID)
		if _, err := m.Rename(ctx, u2, a.ID, "Mine"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("a reader renamed a space: %v", err)
		}
		if _, err := m.Rename(ctx, u1, a.ID, " "); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := m.Rename(ctx, u1, a.ID, "  Renamed  "); err != nil {
			t.Fatal(err)
		}
		for _, uid := range []string{u1.ID, u2.ID} {
			l, _ := m.List(ctx, uid)
			if l[0].Name != "Renamed" {
				t.Fatalf("%s sees %q", uid, l[0].Name)
			}
		}
		if _, err := m.Rename(ctx, store.User{ID: "stranger"}, a.ID, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestStagedSpaceIsInvisibleUntilCommitted(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		u := addUser(t, st, "U1", "one")
		stg, err := m.Stage("Pending", Identity{Name: "x", Email: "x@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		root, state := m.paths(stg.ID())
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			t.Fatal("no repository")
		}
		if l, _ := m.List(ctx, u.ID); len(l) != 0 {
			t.Fatal("a staged space is listed")
		}
		stg.Abort()
		stg.Abort() // harmless twice
		stg.Commit(u.ID)
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("an aborted space is still on disk")
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("an aborted space's index is still on disk")
		}
		if len(m.open) != 0 {
			t.Fatal("an aborted space was registered")
		}
	})
}

func TestMissingDirectoryIsAnErrorNotAnEmptyRepository(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		u := addUser(t, st, "U1", "one")
		a, _ := m.Create(ctx, u.ID, "A")
		m2, _ := NewAccounts(Options{Store: st, DataDir: m.opt.DataDir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond})
		defer m2.Close()
		root, _ := m2.paths(a.ID)
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if _, err := m2.Open(ctx, u, a.ID); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("the directory was quietly recreated")
		}
		if len(m2.open) != 0 {
			t.Fatal("a failed open is cached")
		}
	})
}

func TestCloseCommitsAndRefusesFurtherOpens(t *testing.T) {
	t.Parallel()
	needGit(t)
	st := storetest.Open(t, "sqlite")
	m := testManager(t, st)
	u := addUser(t, st, "U1", "kazuha")
	a, _ := m.Create(ctx, u.ID, "A")
	h, _ := m.Open(ctx, u, a.ID)
	if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Unsaved"}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	root, _ := m.paths(a.ID)
	out, err := exec.Command("git", "-C", root, "log", "--format=%an|%cn|%s").Output()
	if err != nil || !strings.Contains(string(out), "kazuha|Svc|Add Unsaved") {
		t.Fatalf("not committed at shutdown (author is the owner, committer the service): %q %v", out, err)
	}
	m2, _ := NewAccounts(Options{Store: st, DataDir: m.opt.DataDir, CloseWait: 200 * time.Millisecond})
	m2.Close()
	if _, _, err := m2.Acquire(ctx, a.ID); err == nil {
		t.Fatal("a closed manager opened a space")
	}
	if err := m.Close(); err != nil {
		t.Fatal("closing twice is an error:", err)
	}
}

func TestDataDirectoryWithUnsupportedCharacters(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, "sqlite")
	for _, p := range []string{"/tmp/a#b", "/tmp/a?b"} {
		if _, err := NewAccounts(Options{Store: st, DataDir: p}); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if _, err := NewAccounts(Options{Store: st}); err == nil {
		t.Error("no data directory accepted")
	}
}

func TestValidName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"A": "A", "  Tang  ": "Tang", "日本語のノート": "日本語のノート", strings.Repeat("x", 80): strings.Repeat("x", 80)} {
		if got, err := ValidName(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 81), "a\nb", "a\x00b", "bad\xff"} {
		if _, err := ValidName(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestIdentityOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		u    store.User
		want string
	}{
		{store.User{Username: "kazuha", Name: "Kazuha Z", Email: "k@example.test"}, "Kazuha Z <k@example.test>"},
		{store.User{Username: "kazuha", Name: "Kazuha"}, "Kazuha <kazuha@users.petalbook.invalid>"},
		{store.User{Username: "kazuha", Name: "A <b>\nC", Email: "x y@z"}, "A bC <kazuha@users.petalbook.invalid>"},
		{store.User{Username: "kazuha", Name: " <> "}, "kazuha <kazuha@users.petalbook.invalid>"},
	} {
		got := IdentityOf(c.u).String()
		if got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
		if _, _, err := space.ParseCommitter(got); err != nil {
			t.Errorf("%q does not parse as a Git identity: %v", got, err)
		}
	}
}

// --- limits and idle closing -------------------------------------------------

type tclock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tclock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *tclock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func limitedManager(t *testing.T, st *store.Store, mod func(*Options)) (*Manager, *tclock) {
	t.Helper()
	needGit(t)
	dir, err := os.MkdirTemp("", "petalbook-spaces-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	clk := &tclock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}
	opt := Options{Store: st, DataDir: filepath.Join(dir, "data"), Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour, Now: clk.now}
	if mod != nil {
		mod(&opt)
	}
	m, err := NewAccounts(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, clk
}

func TestAUserCannotOwnMoreSpacesThanTheLimit(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m, _ := limitedManager(t, st, func(o *Options) { o.MaxSpaces = 3 })
		u := addUser(t, st, "U1", "one")
		other := addUser(t, st, "U2", "two")
		// A personal space counts, as any space the user owns does.
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "PERSONAL1", "My notes", u.ID, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		store.AddMember(ctx, st.Q(), "PERSONAL1", u.ID, "owner", time.Now())
		for _, n := range []string{"A", "B"} {
			if _, err := m.Create(ctx, u.ID, n); err != nil {
				t.Fatal(err)
			}
		}
		_, err := m.Create(ctx, u.ID, "C")
		if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "3") {
			t.Fatalf("%v", err)
		}
		dirs, _ := os.ReadDir(filepath.Join(m.opt.DataDir, "spaces"))
		if len(dirs) != 2 {
			t.Fatalf("a refused space left a directory: %d", len(dirs))
		}
		// Other people are not affected, and a space someone else owns does not count.
		if _, err := m.Create(ctx, other.ID, "Theirs"); err != nil {
			t.Fatal(err)
		}
		// Racing creates cannot pass the limit.
		u3 := addUser(t, st, "U3", "three")
		var wg sync.WaitGroup
		var okMu sync.Mutex
		ok := 0
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := m.Create(ctx, u3.ID, "S"+string(rune('a'+i))); err == nil {
					okMu.Lock()
					ok++
					okMu.Unlock()
				}
			}()
		}
		wg.Wait()
		if ok != 3 {
			t.Fatalf("%d spaces made with a limit of 3", ok)
		}
		// 0 means no limit.
		m2, _ := limitedManager(t, st, func(o *Options) { o.MaxSpaces = 0 })
		u4 := addUser(t, st, "U4", "four")
		for range 5 {
			if _, err := m2.Create(ctx, u4.ID, "x"); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func fdCount(t *testing.T) int {
	t.Helper()
	for _, d := range []string{"/proc/self/fd", "/dev/fd"} {
		if es, err := os.ReadDir(d); err == nil {
			return len(es)
		}
	}
	t.Skip("cannot count open files on this system")
	return 0
}

func TestOpenSpacesAndFilesStayBounded(t *testing.T) {
	needGit(t)
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) { o.MaxOpen = 4; o.MaxSpaces = 0; o.IdleAfter = time.Hour })
	u := addUser(t, st, "U1", "one")
	var ids []string
	for i := range 4 {
		in, err := m.Create(ctx, u.ID, "S"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
		clk.add(time.Second)
	}
	m.Trim()
	base := fdCount(t)
	for i := range 40 {
		in, err := m.Create(ctx, u.ID, "T"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
		clk.add(time.Second)
		m.Trim()
		if n := m.OpenCount(); n > 4 {
			t.Fatalf("%d spaces open with a cap of 4", n)
		}
	}
	if grown := fdCount(t) - base; grown > 12 {
		t.Fatalf("40 more spaces grew the open files by %d", grown)
	}
	// Every one is still there: closed ones reopen on the next request.
	for _, id := range ids {
		h, err := m.Open(ctx, u, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if _, err := h.Space.List(index.Query{}); err != nil {
			t.Fatal(err)
		}
		h.Release()
		m.Trim()
	}
	if n := m.OpenCount(); n > 4 {
		t.Fatalf("%d open", n)
	}
}

func TestIdleSpacesAreClosedFlushedAndReopened(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m, clk := limitedManager(t, st, func(o *Options) { o.IdleAfter = 10 * time.Minute; o.MaxSpaces = 0 })
		u := addUser(t, st, "U1", "kazuha")
		a, _ := m.Create(ctx, u.ID, "A")
		h, _ := m.Open(ctx, u, a.ID)
		pg, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Written"}}, Body: "x\n"})
		if err != nil {
			t.Fatal(err)
		}
		// In use: never closed, however long.
		clk.add(time.Hour)
		m.Trim()
		if m.OpenCount() != 1 {
			t.Fatal("a space with a request in flight was closed")
		}
		h.Release()
		h.Release() // releasing twice does not give back a place that was never held
		// Used a moment ago: stays.
		m.Trim()
		if m.OpenCount() != 1 {
			t.Fatal("closed straight after use")
		}
		clk.add(11 * time.Minute)
		m.Trim()
		if m.OpenCount() != 0 {
			t.Fatal("an idle space was not closed")
		}
		// Its pending commit was flushed before it was closed.
		root, _ := m.paths(a.ID)
		out, err := exec.Command("git", "-C", root, "log", "--format=%s").Output()
		if err != nil || !strings.Contains(string(out), "Add Written") {
			t.Fatalf("not committed when closed: %q %v", out, err)
		}
		// The next request reopens it without anyone noticing.
		h, err = m.Open(ctx, u, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		got, err := h.Space.Get(pg.ID)
		if err != nil || got.Body != "x\n" {
			t.Fatalf("%v %v", got, err)
		}
		if res, _ := h.Space.List(index.Query{}); len(res.Items) != 1 {
			t.Fatal("the index of the reopened space is wrong")
		}
	})
}

func TestASpaceThatCannotFlushStaysOpenWithItsPendingChanges(t *testing.T) {
	t.Parallel()
	needGit(t)
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) { o.IdleAfter = time.Minute; o.MaxSpaces = 0 })
	u := addUser(t, st, "U1", "kazuha")
	a, _ := m.Create(ctx, u.ID, "A")
	root, _ := m.paths(a.ID)
	h, _ := m.Open(ctx, u, a.ID)
	if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Pending"}}}); err != nil {
		t.Fatal(err)
	}
	h.Release()
	// The repository is in the middle of the person's own merge: nothing can be committed.
	merge := filepath.Join(root, ".git", "MERGE_HEAD")
	if err := os.WriteFile(merge, []byte("0000000000000000000000000000000000000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clk.add(5 * time.Minute)
	m.Trim()
	if m.OpenCount() != 1 {
		t.Fatal("a space with changes it could not commit was closed")
	}
	// Once the repository is free again, the next trim commits and closes it.
	os.Remove(merge)
	m.Trim()
	if m.OpenCount() != 0 {
		t.Fatal("not closed once it could be flushed")
	}
	out, _ := exec.Command("git", "-C", root, "log", "--format=%s").Output()
	if !strings.Contains(string(out), "Add Pending") {
		t.Fatalf("the pending change was lost: %q", out)
	}
}

func TestACloseNeverRunsAlongsideAnOpenOfTheSameSpace(t *testing.T) {
	t.Parallel()
	needGit(t)
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) { o.IdleAfter = time.Minute; o.MaxSpaces = 0 })
	u := addUser(t, st, "U1", "kazuha")
	a, _ := m.Create(ctx, u.ID, "A")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // keeps trying to close it
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				clk.add(2 * time.Minute)
				m.Trim()
			}
		}
	}()
	var users sync.WaitGroup
	for range 4 {
		users.Add(1)
		go func() {
			defer users.Done()
			for range 25 {
				h, err := m.Open(ctx, u, a.ID)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "x"}}}); err != nil {
					t.Error(err)
				}
				h.Release()
			}
		}()
	}
	users.Wait()
	close(stop)
	wg.Wait()
	h, err := m.Open(ctx, u, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if res, _ := h.Space.List(index.Query{Limit: 200}); len(res.Items) != 100 {
		t.Fatalf("%d pages of 100 written", len(res.Items))
	}
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestCloseWaitsForSpacesThatRequestsStillHold(t *testing.T) {
	t.Parallel()
	needGit(t)
	st := storetest.Open(t, "sqlite")
	m, _ := limitedManager(t, st, func(o *Options) { o.CloseWait = 10 * time.Second; o.MaxSpaces = 0 })
	u := addUser(t, st, "U1", "one")
	a, _ := m.Create(ctx, u.ID, "A")
	h, err := m.Open(ctx, u, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond) // a request that is still working
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Finished"}}}); err != nil {
			t.Error(err) // the space must still be open while the request holds it
		}
		close(released)
		h.Release()
	}()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	default:
		t.Fatal("Close returned while a request still held the space")
	}
	root, _ := m.paths(a.ID)
	if out, _ := exec.Command("git", "-C", root, "log", "--format=%s").Output(); !strings.Contains(string(out), "Add Finished") {
		t.Fatalf("the request's write was not committed:\n%s", out)
	}
	// Nothing new is acquired once closing has begun.
	if _, _, err := m.Acquire(ctx, a.ID); err == nil {
		t.Fatal("acquired after close")
	}
}

func TestCloseGivesUpOnAStuckRequestAndItsLaterWritesFailCleanly(t *testing.T) {
	needGit(t)
	st := storetest.Open(t, "sqlite")
	var logs logBuf
	m, _ := limitedManager(t, st, func(o *Options) {
		o.CloseWait = 100 * time.Millisecond
		o.MaxSpaces = 0
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	u := addUser(t, st, "U1", "one")
	a, _ := m.Create(ctx, u.ID, "A")
	h, _ := m.Open(ctx, u, a.ID) // never released
	start := time.Now()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 90*time.Millisecond || d > 5*time.Second {
		t.Fatalf("waited %v", d)
	}
	if !strings.Contains(logs.String(), "still hold") {
		t.Fatalf("giving up was not logged:\n%s", logs.String())
	}
	// The request that arrives late is refused, and nothing is written.
	if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Late"}}}); !errors.Is(err, space.ErrClosed) {
		t.Fatalf("%v", err)
	}
	root, _ := m.paths(a.ID)
	if _, err := os.Stat(filepath.Join(root, "Late.md")); err == nil {
		t.Fatal("a half-written file after close")
	}
}

func TestOpenPermissions(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		owner := addUser(t, st, "U1", "kazuha")
		viewer := addUser(t, st, "U2", "tang")
		stranger := addUser(t, st, "U3", "kai")
		boss := addUser(t, st, "U4", "boss")
		boss.Role = "admin"
		a, err := m.Create(ctx, owner.ID, "A")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), a.ID, viewer.ID, perm.Viewer, time.Now()); err != nil {
			t.Fatal(err)
		}
		m.MembershipChanged(viewer.ID)

		open := func(u store.User, id string) *Handle {
			t.Helper()
			h, err := m.Open(ctx, u, id)
			if err != nil {
				t.Fatalf("%s: %v", u.Username, err)
			}
			t.Cleanup(h.Release)
			return h
		}
		if h := open(owner, a.ID); h.Info.Perms != perm.All || h.Info.Role != "owner" || h.Info.ViaAdmin {
			t.Fatalf("%+v", h.Info)
		}
		if h := open(viewer, a.ID); h.Info.Perms != perm.Of(perm.Read) || h.Info.Role != "viewer" || h.Info.ViaAdmin {
			t.Fatalf("%+v", h.Info)
		}
		h := open(boss, a.ID)
		// An administrator manages a space and does not read it (3.5): reading is a
		// view, opened with a reason.
		if h.Info.Perms != AdminPerms || h.Info.Perms.Has(perm.Read) || h.Info.Perms.Has(perm.Write) || h.Info.Role != "" || !h.Info.ViaAdmin || h.Info.Name != "A" || h.Info.Inbox != "Inbox" {
			t.Fatalf("%+v", h.Info)
		}
		if _, err := m.Open(ctx, stranger, a.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		// Not even an administrator opens what is not there, or what is no id.
		for _, id := range []string{"0000000000", "../x", ""} {
			if _, err := m.Open(ctx, boss, id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%q: %v", id, err)
			}
		}
		// The administrator is in no list.
		if l, _ := m.List(ctx, boss.ID); len(l) != 0 {
			t.Fatalf("%+v", l)
		}
		// Only whoever may manage renames, and an administrator may.
		if _, err := m.Rename(ctx, viewer, a.ID, "X"); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
		if in, err := m.Rename(ctx, boss, a.ID, "By admin"); err != nil || in.Name != "By admin" {
			t.Fatal(in, err)
		}
		if l, _ := m.List(ctx, viewer.ID); l[len(l)-1].Name != "By admin" && l[0].Name != "By admin" {
			t.Fatalf("%+v", l)
		}
	})
}

// A change drops the cache of the people it touches and of no one else, and it
// moves the generation that keeps a list read before the change out of the cache.
func TestMembershipChangedDropsOnlyThoseUsers(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, "sqlite")
	m := testManager(t, st)
	u1, u2 := addUser(t, st, "U1", "kazuha"), addUser(t, st, "U2", "tang")
	for _, u := range []store.User{u1, u2} {
		if _, err := m.List(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	cached := func(id string) bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.cache.users[id]
		return ok
	}
	if !cached("U1") || !cached("U2") {
		t.Fatal("lists are not cached")
	}
	m.MembershipChanged("U1")
	if cached("U1") || !cached("U2") {
		t.Fatalf("U1 cached %v, U2 cached %v", cached("U1"), cached("U2"))
	}
	// A read that began before a change must not store what it read.
	m.mu.Lock()
	gen := m.listGen
	m.mu.Unlock()
	m.MembershipChanged("U2")
	m.mu.Lock()
	moved := m.listGen != gen
	m.mu.Unlock()
	if !moved {
		t.Fatal("a change does not move the generation")
	}
}

// A crash between staging a space and recording it leaves its directories on
// disk with no row. The next start removes those, once they are old enough
// that no request can still be making them, and nothing else: not a space
// that has its row, not one still being made, not a directory it did not stage.
func TestLeftoverStagedSpacesAreRemovedAtStart(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		needGit(t)
		dir := filepath.Join(t.TempDir(), "data")
		opt := Options{Store: st, DataDir: dir, Serve: true, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour}
		m, err := NewAccounts(opt)
		if err != nil {
			t.Fatal(err)
		}
		u := addUser(t, st, "U1", "one")
		real, err := m.Create(ctx, u.ID, "Real")
		if err != nil {
			t.Fatal(err)
		}
		crashed, err := m.Stage("Crashed", Identity{Name: "x", Email: "x@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := m.Stage("Being made", Identity{Name: "x", Email: "x@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		// Crashed after its row was written but before Commit: it stays.
		recorded, err := m.Stage("Recorded", Identity{Name: "x", Email: "x@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, recorded.ID(), "Recorded", "", "", time.Now()); err != nil {
			t.Fatal(err)
		}
		// A directory this code did not stage, with no row: not ours to remove.
		foreign := filepath.Join(dir, "spaces", "01FOREIGN0000000000000000")
		if err := os.MkdirAll(foreign, 0o755); err != nil {
			t.Fatal(err)
		}
		// A space with an attachment repository has its clone made before the row:
		// the crashed one's is removed with it; the others' and a stranger's stay.
		for _, id := range []string{crashed.ID(), fresh.ID(), recorded.ID(), "01FOREIGN0000000000000000"} {
			if err := os.MkdirAll(filepath.Join(dir, "assetrepos", id), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// The process "crashes": the staged spaces are closed but neither committed nor aborted.
		crashed.sp.Close()
		fresh.sp.Close()
		recorded.sp.Close()
		m.Close()
		old := time.Now().Add(-time.Hour)
		for _, id := range []string{crashed.ID(), recorded.ID()} {
			if err := os.Chtimes(filepath.Join(dir, "staging", id), old, old); err != nil {
				t.Fatalf("no staging mark for %s: %v", id, err)
			}
		}
		realRoot, _ := m.paths(real.ID)
		if err := os.Chtimes(realRoot, old, old); err != nil {
			t.Fatal(err)
		}

		m2, err := NewAccounts(opt)
		if err != nil {
			t.Fatal(err)
		}
		defer m2.Close()
		exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
		root, state := m2.paths(crashed.ID())
		if exists(root) || exists(state) || exists(filepath.Join(dir, "staging", crashed.ID())) {
			t.Fatal("the crashed staged space is still on disk")
		}
		for name, id := range map[string]string{"the real space": real.ID, "the space still being made": fresh.ID(), "the recorded space": recorded.ID()} {
			if root, _ := m2.paths(id); !exists(filepath.Join(root, ".git")) {
				t.Fatalf("%s was removed", name)
			}
		}
		if exists(filepath.Join(dir, "assetrepos", crashed.ID())) {
			t.Fatal("the attachment repository of the crashed staged space is still on disk")
		}
		for _, id := range []string{fresh.ID(), recorded.ID(), "01FOREIGN0000000000000000"} {
			if !exists(filepath.Join(dir, "assetrepos", id)) {
				t.Fatalf("the attachment repository of %s was removed", id)
			}
		}
		if exists(filepath.Join(dir, "staging", recorded.ID())) {
			t.Fatal("the mark of a recorded space was kept")
		}
		if !exists(filepath.Join(dir, "staging", fresh.ID())) {
			t.Fatal("the mark of a space still being made was removed")
		}
		if !exists(foreign) {
			t.Fatal("a directory that was not staged was removed")
		}
		if l, _ := m2.List(ctx, u.ID); len(l) != 1 {
			t.Fatalf("%v", l)
		}
	})
}

// A tenant's administrator never reaches a person's own space, in any way; a
// site administrator reaches it as they reach any space (to manage it).
func TestAPersonalSpaceIsNotOpenedByATenantAdministrator(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		person := addUser(t, st, "P1", "person")
		personal, err := m.Create(ctx, person.ID, "Mine")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `UPDATE spaces SET personal_of = ? WHERE id = ?`, person.ID, personal.ID); err != nil {
			t.Fatal(err)
		}
		tenantAdmin := store.User{ID: "A1", Username: "tadmin", Role: "admin", TenantID: store.DefaultTenantID}
		siteAdmin := store.User{ID: "A2", Username: "sadmin", Role: "admin", SiteAdmin: true, TenantID: store.DefaultTenantID}
		if _, err := m.Open(ctx, tenantAdmin, personal.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a tenant administrator opened a personal space: %v", err)
		}
		h, err := m.Open(ctx, siteAdmin, personal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if h.Info.Perms.Has(perm.Read) {
			t.Fatal("a site administrator reads without a view")
		}
		h.Release()
	})
}
