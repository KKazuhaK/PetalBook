package spaces

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// rbFix is a manager whose clock and recycle hook the test controls, with a
// space that has one row of everything a space has.
type rbFix struct {
	*gfix
	dir  string
	now  time.Time
	mu   sync.Mutex
	stop string // the step the hook "crashes" at
}

func (f *rbFix) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *rbFix) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *rbFix) crashAt(step string) {
	f.mu.Lock()
	f.stop = step
	f.mu.Unlock()
}

var errRecycleCrash = errors.New("crash")

func newRbFix(t *testing.T, st *store.Store) *rbFix {
	t.Helper()
	needGit(t)
	f := &rbFix{now: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)}
	dir, err := os.MkdirTemp("", "petalbook-recycle-")
	if err != nil {
		t.Fatal(err)
	}
	f.dir = filepath.Join(dir, "data")
	m, err := NewAccounts(Options{Store: st, DataDir: f.dir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond,
		CommitIdle: time.Hour, CommitMax: time.Hour, Now: f.clock, PurgePause: -1, RecycleEvery: -1,
		RecycleHook: func(step string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.stop == step {
				return errRecycleCrash
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	// The manager's background work (the attachment repository of the fixture's
	// space is probed on a host that does not exist) may make a directory after
	// Close has returned: the directory is removed until it stays removed.
	t.Cleanup(func() {
		m.Close()
		for i := 0; i < 20; i++ {
			os.RemoveAll(dir)
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	f.gfix = &gfix{t: t, st: st, m: m}
	return f
}

// fullSpace makes a space with an owner, an editor, a group, a favourite, a
// draft, a remote, daily settings and a folder move, and a page in its clone.
func (f *rbFix) fullSpace(owner, editor, member string) Info {
	t := f.t
	t.Helper()
	f.user(owner)
	f.user(editor)
	f.user(member)
	info, err := f.m.Create(ctx, owner, "Team")
	if err != nil {
		t.Fatal(err)
	}
	q := f.st.Q()
	if err := store.AddMember(ctx, q, info.ID, editor, perm.Editor, f.clock()); err != nil {
		t.Fatal(err)
	}
	g := f.group("Readers-"+owner, "local")
	f.in(g, member)
	if err := store.AddSpaceGroup(ctx, q, info.ID, g, perm.Viewer, f.clock()); err != nil {
		t.Fatal(err)
	}
	sp, release, err := f.m.Acquire(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := sp.Create(space.Author{Name: "Olive", Email: "o@example.test"}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Hello"}}, Body: "hello world\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Flush(); err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := store.AddFavorite(ctx, q, editor, info.ID, store.Favorite{Kind: store.FavPage, Target: pg.ID}, f.clock()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDraft(ctx, q, store.Draft{SpaceID: info.ID, PageID: pg.ID, UserID: editor, BaseRev: pg.Rev, Meta: `{"title":"Hello"}`, Body: "unsaved words", Created: f.clock(), Updated: f.clock(), Due: f.clock().Add(time.Hour)}, "", time.Minute, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRemote(ctx, q, store.Remote{SpaceID: info.ID, URL: "https://git.example.test/team.git", Branch: "main", CredType: "token", Provider: "git",
		Username: "x", TokenEnc: "sealed-token", State: "ok", Updated: f.clock()}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAssetRemote(ctx, q, store.Remote{SpaceID: info.ID, URL: "https://git.example.test/team-assets.git", Branch: "main", CredType: "token", Provider: "git",
		Username: "x", TokenEnc: "sealed-assets-token", State: "ok", Updated: f.clock()}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Exec(ctx, `INSERT INTO space_daily(space_id, inherit, apis) VALUES (?, 0, '["a"]')`, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFolderMove(ctx, q, info.ID, "Old", "New", f.clock()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecordMeasurement(ctx, info.ID, store.Measurement{Bytes: 1234, Pages: 1, HavePages: true, At: f.clock()}); err != nil {
		t.Fatal(err)
	}
	return info
}

// rowsOf is every row of a table that belongs to a space, as sorted text, for
// comparing what was there with what came back.
func (f *rbFix) rowsOf(table, col, id string) []string {
	f.t.Helper()
	rows, err := f.st.Q().Query(ctx, `SELECT * FROM `+table+` WHERE `+col+` = ?`, id)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			f.t.Fatal(err)
		}
		var b strings.Builder
		for i, v := range vals {
			if bs, ok := v.([]byte); ok {
				v = string(bs)
			}
			fmt.Fprintf(&b, "%s=%v;", cols[i], v)
		}
		out = append(out, b.String())
	}
	sort.Strings(out)
	return out
}

func (f *rbFix) snapshot(id string) map[string][]string {
	snap := map[string][]string{"spaces": f.rowsOf("spaces", "id", id)}
	for _, t := range store.RecycledTables() {
		snap[t] = f.rowsOf(t, "space_id", id)
	}
	return snap
}

func (f *rbFix) tenantUsage() store.Usage {
	u, err := store.GetUsage(ctx, f.st.Q(), store.UsageTenant, store.DefaultTenantID)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *rbFix) userUsage(id string) store.Usage {
	u, err := store.GetUsage(ctx, f.st.Q(), store.UsageUser, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *rbFix) item(id string) *store.RecycleItem {
	it, err := store.GetRecycleItem(ctx, f.st.Q(), store.AllTenants(), id, false)
	if err != nil {
		f.t.Fatalf("item %s: %v", id, err)
	}
	return it
}

func (f *rbFix) itemOfSpace(spaceID string) *store.RecycleItem {
	f.t.Helper()
	l, _, err := store.ListRecycle(ctx, f.st.Q(), store.AllTenants(), store.RecycleFilter{})
	if err != nil {
		f.t.Fatal(err)
	}
	for i := range l {
		if l[i].SpaceID == spaceID {
			return &l[i]
		}
	}
	f.t.Fatalf("no item of %s", spaceID)
	return nil
}

func (f *rbFix) noItem(id string) {
	f.t.Helper()
	if _, err := store.GetRecycleItem(ctx, f.st.Q(), store.AllTenants(), id, false); !errors.Is(err, store.ErrNotFound) {
		f.t.Fatalf("item %s is still there: %v", id, err)
	}
}

func TestRecycleKeepsEverythingAndRestoreGivesItBack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		info := f.fullSpace("olive", "edith", "mia")
		root, state := f.m.paths(info.ID)
		before := f.snapshot(info.ID)
		for tbl, rows := range before {
			if len(rows) == 0 {
				t.Fatalf("the fixture has no row of %s", tbl)
			}
		}
		usage := f.tenantUsage()
		ownerUsage := f.userUsage("olive")
		if usage.Spaces != 1 || ownerUsage.Spaces != 1 {
			t.Fatalf("usage before: %+v %+v", usage, ownerUsage)
		}
		// Everyone's lists are warm, so that the deletion has to drop them.
		for _, u := range []string{"olive", "edith", "mia"} {
			if l, _ := f.m.List(ctx, u); len(l) == 0 {
				t.Fatalf("%s has no space before", u)
			}
		}
		gitLog := func(dir string) string {
			out, err := exec.Command("git", "-C", dir, "log", "--format=%s|%H").Output()
			if err != nil {
				t.Fatalf("git log %s: %v", dir, err)
			}
			return string(out)
		}
		historyBefore := gitLog(root)

		item, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{By: "root", ByName: "root", Reason: "tidy"})
		if err != nil {
			t.Fatal(err)
		}
		if item.Kind != "space" || item.SpaceID != info.ID || item.Name != "Team" || item.Reason != "tidy" || item.TenantID != store.DefaultTenantID {
			t.Fatalf("%+v", item)
		}
		it := f.item(item.ID)
		if it.State != store.RecycleReady || len(it.Dirs) != 2 {
			t.Fatalf("%+v", it)
		}
		// Gone from the live tables, all of them.
		for tbl, rows := range f.snapshot(info.ID) {
			if len(rows) != 0 {
				t.Errorf("%s still has %v", tbl, rows)
			}
		}
		// The directories are in the bin, whole: the history is the same.
		for _, p := range []string{root, state} {
			if pathExists(p) {
				t.Errorf("%s is still where it was", p)
			}
		}
		bin := f.m.RecycleDir(item.ID)
		if gitLog(filepath.Join(bin, "space")) != historyBefore {
			t.Fatal("the repository in the bin is not the one that was deleted")
		}
		if !pathExists(filepath.Join(bin, "cache")) {
			t.Fatal("the index did not move")
		}
		// Not counted, not visible, not openable.
		if u := f.tenantUsage(); u.Spaces != 0 {
			t.Errorf("the tenant still counts it: %+v", u)
		}
		if u := f.userUsage("olive"); u.Spaces != 0 {
			t.Errorf("the owner still counts it: %+v", u)
		}
		for _, u := range []string{"olive", "edith", "mia"} {
			if l, _ := f.m.List(ctx, u); len(l) != 0 {
				t.Errorf("%s still lists it: %+v", u, l)
			}
		}
		if _, err := f.m.Open(ctx, store.User{ID: "olive", Role: "member"}, info.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a member opened a recycled space: %v", err)
		}
		if _, err := f.m.Open(ctx, store.User{ID: "root", Role: "admin", SiteAdmin: true, TenantID: store.DefaultTenantID}, info.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("an administrator opened a recycled space: %v", err)
		}

		res, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.SpaceID != info.ID || len(res.Skipped) != 0 {
			t.Fatalf("%+v", res)
		}
		after := f.snapshot(info.ID)
		for tbl, rows := range before {
			if strings.Join(rows, "\n") != strings.Join(after[tbl], "\n") {
				t.Errorf("%s came back different:\nbefore %v\nafter  %v", tbl, rows, after[tbl])
			}
		}
		f.noItem(item.ID)
		if gitLog(root) != historyBefore || !pathExists(state) || pathExists(bin) {
			t.Fatal("the directories did not come back as they were")
		}
		if u := f.tenantUsage(); u.Spaces != usage.Spaces || u.Bytes != usage.Bytes {
			t.Errorf("tenant usage %+v, was %+v", u, usage)
		}
		if u := f.userUsage("olive"); u.Spaces != 1 {
			t.Errorf("owner usage %+v", u)
		}
		for _, u := range []string{"olive", "edith", "mia"} {
			if l, _ := f.m.List(ctx, u); len(l) != 1 || l[0].ID != info.ID {
				t.Errorf("%s: %+v", u, l)
			}
		}
		// And it opens, with its page. (Its attachment repository, on a host that does
		// not exist, is taken away first: opening would start probing it in the
		// background, after the test.)
		if _, err := st.Q().Exec(ctx, `DELETE FROM asset_remotes WHERE space_id = ?`, info.ID); err != nil {
			t.Fatal(err)
		}
		h, err := f.m.Open(ctx, store.User{ID: "olive", Role: "member"}, info.ID)
		if err != nil {
			t.Fatal(err)
		}
		h.Release()
		if m, _ := filepath.Glob(filepath.Join(root, "*.md")); len(m) != 1 {
			t.Errorf("the restored space has the files %v", m)
		}
	})
}

// Every table that has a space_id is among those a recycled space keeps, or is
// said not to need it: a table added later without being added there would lose
// its rows on a restore.
func TestEveryTableOfASpaceIsRecycled(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		var query string
		if st.Dialect() == store.Postgres {
			query = `SELECT table_name FROM information_schema.columns WHERE column_name = 'space_id' AND table_schema = current_schema()`
		} else {
			query = `SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND p.name = 'space_id'`
		}
		rows, err := st.Q().Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		kept := map[string]bool{}
		for _, n := range store.RecycledTables() {
			kept[n] = true
		}
		// These name a space without belonging to it: a token's or a grant's list of
		// spaces it may reach (a restore brings its reach back by itself), and the
		// bin's own row.
		not := map[string]string{"api_token_spaces": "names spaces a token reaches; no foreign key", "oauth_grant_spaces": "the same for a grant", "recycle_items": "the bin's own"}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			if !kept[n] && not[n] == "" {
				t.Errorf("%s has a space_id and is not kept by a recycled space: add it to recycledTables, or say why it need not be", n)
			}
		}
	})
}

func TestRecycledRestoreLeavesOutPeopleWhoAreGoneAndNeedsAnOwner(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		info := f.fullSpace("olive", "edith", "mia")
		item, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// The only owner is deleted while the space is in the bin.
		if _, err := st.Q().Exec(ctx, `DELETE FROM users WHERE id = 'olive'`); err != nil {
			t.Fatal(err)
		}
		_, err = f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, "")
		if !errors.Is(err, ErrOwnerRequired) {
			t.Fatalf("%v", err)
		}
		// Nothing changed: the item is at rest with its directories, no row is live.
		it := f.item(item.ID)
		if it.State != store.RecycleReady || len(f.rowsOf("spaces", "id", info.ID)) != 0 || len(f.m.Problems(it)) != 0 {
			t.Fatalf("a refused restore left %+v", it)
		}
		if root, _ := f.m.paths(info.ID); pathExists(root) {
			t.Fatal("a refused restore left the clone in place")
		}
		// A person who does not exist, or is of another tenant, cannot be named.
		if _, err := st.Q().Exec(ctx, `INSERT INTO tenants(id, slug, name, name_key, created, updated) VALUES ('T2', 't2', 'Two', 'two', 1, 1)`); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertUser(ctx, st.Q(), store.User{ID: "ivy", Username: "ivy", Name: "ivy", Role: "member", Created: f.clock(), TenantID: "T2"}); err != nil {
			t.Fatal(err)
		}
		for _, who := range []string{"nobody", "ivy"} {
			if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, who); !errors.Is(err, ErrUserNotFound) {
				t.Fatalf("owner %s: %v", who, err)
			}
		}
		var n int
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM space_members m JOIN spaces s ON s.id = m.space_id JOIN users u ON u.id = m.user_id WHERE s.tenant_id <> u.tenant_id`).Scan(&n)
		if n != 0 {
			t.Fatal("a restore linked two tenants")
		}
		res, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, "edith")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Skipped) != 1 || res.Skipped[0].User != "olive" || res.Skipped[0].Reason != "deleted" {
			t.Fatalf("%+v", res.Skipped)
		}
		if r, _ := store.MemberRole(ctx, st.Q(), info.ID, "edith"); r != "owner" {
			t.Fatalf("edith is %q", r)
		}
		// The space is counted for its new first owner.
		if u := f.userUsage("edith"); u.Spaces != 1 {
			t.Fatalf("%+v", u)
		}
	})
}

func TestRecycleCrashesAreFinishedAtTheNextStart(t *testing.T) {
	for _, step := range []string{"recycle:rows", "recycle:moved-space"} {
		t.Run(step, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				f := newRbFix(t, st)
				info := f.fullSpace("olive", "edith", "mia")
				before := f.snapshot(info.ID)
				f.crashAt(step)
				// The crash is the end of the process: what the caller is told does not
				// matter, the item and the directories are what a start finds.
				if _, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{}); err != nil && !errors.Is(err, errRecycleCrash) {
					t.Fatalf("%v", err)
				}
				item := f.itemOfSpace(info.ID)
				// The rows are in the item; the directories may be anywhere between.
				it := f.item(item.ID)
				if it.State != store.RecycleMoving {
					t.Fatalf("state %s", it.State)
				}
				if len(f.rowsOf("spaces", "id", info.ID)) != 0 {
					t.Fatal("the rows are still live")
				}
				// A restore of an item that is not at rest is refused.
				if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); err == nil {
					t.Fatal("restored an item that was being moved")
				}
				f.crashAt("")
				if err := f.m.recoverRecycle(ctx, 0); err != nil {
					t.Fatal(err)
				}
				it = f.item(item.ID)
				if it.State != store.RecycleReady || len(f.m.Problems(it)) != 0 {
					t.Fatalf("%+v %v", it, f.m.Problems(it))
				}
				root, state := f.m.paths(info.ID)
				if pathExists(root) || pathExists(state) {
					t.Fatal("a directory was left behind")
				}
				// And everything comes back.
				if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); err != nil {
					t.Fatal(err)
				}
				for tbl, rows := range before {
					if strings.Join(rows, "\n") != strings.Join(f.snapshot(info.ID)[tbl], "\n") {
						t.Errorf("%s came back different", tbl)
					}
				}
			})
		})
	}
}

func TestRestoreCrashesAreUndoneAtTheNextStart(t *testing.T) {
	for _, step := range []string{"restore:marked", "restore:moved-space", "restore:moved-cache"} {
		t.Run(step, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				f := newRbFix(t, st)
				info := f.fullSpace("olive", "edith", "mia")
				before := f.snapshot(info.ID)
				item, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{})
				if err != nil {
					t.Fatal(err)
				}
				f.crashAt(step)
				if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); !errors.Is(err, errRecycleCrash) {
					t.Fatalf("%v", err)
				}
				if it := f.item(item.ID); it.State != store.RecycleRestoring {
					t.Fatalf("%+v", it)
				}
				if len(f.rowsOf("spaces", "id", info.ID)) != 0 {
					t.Fatal("rows were restored before the directories were")
				}
				// A purge cannot start on an item that is being restored.
				if _, err := f.m.PurgeItem(ctx, store.AllTenants(), item.ID); !errors.Is(err, ErrBusyItem) {
					t.Fatalf("%v", err)
				}
				f.crashAt("")
				if err := f.m.recoverRecycle(ctx, 0); err != nil {
					t.Fatal(err)
				}
				it := f.item(item.ID)
				if it.State != store.RecycleReady || len(f.m.Problems(it)) != 0 {
					t.Fatalf("%+v %v", it, f.m.Problems(it))
				}
				root, state := f.m.paths(info.ID)
				if pathExists(root) || pathExists(state) {
					t.Fatal("a directory stayed at its live place")
				}
				if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); err != nil {
					t.Fatal(err)
				}
				for tbl, rows := range before {
					if strings.Join(rows, "\n") != strings.Join(f.snapshot(info.ID)[tbl], "\n") {
						t.Errorf("%s came back different", tbl)
					}
				}
			})
		})
	}
}

func TestPurgeDeletesOnlyAfterTheDaysAndFinishesAfterACrash(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		days := 30
		f.m.SetRecycleDays(func(context.Context) int { return days })
		a := f.fullSpace("olive", "edith", "mia")
		item, err := f.m.RecycleSpace(ctx, store.AllTenants(), a.ID, RecycleOptions{})
		if err != nil {
			t.Fatal(err)
		}
		bin := f.m.RecycleDir(item.ID)
		f.advance(29 * 24 * time.Hour)
		if n, err := f.m.PurgeExpired(ctx); err != nil || n != 0 || !pathExists(bin) {
			t.Fatalf("purged before its time: %d %v", n, err)
		}
		// Shortening recycle.days moves every item's date.
		days = 7
		f.crashAt("purge:dirs")
		if _, err := f.m.PurgeExpired(ctx); !errors.Is(err, errRecycleCrash) {
			t.Fatalf("%v", err)
		}
		if pathExists(bin) {
			t.Fatal("the directories were not deleted")
		}
		if it := f.item(item.ID); it.State != store.RecyclePurging {
			t.Fatalf("%+v", it)
		}
		// Not restorable meanwhile, and the start finishes the purge.
		if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); err == nil {
			t.Fatal("restored an item that was being purged")
		}
		f.crashAt("")
		if err := f.m.recoverRecycle(ctx, 0); err != nil {
			t.Fatal(err)
		}
		f.noItem(item.ID)
		// One at a time, oldest first, and only items at rest.
		b := f.fullSpaceNamed("p1", "p2", "p3", "Second")
		c := f.fullSpaceNamed("q1", "q2", "q3", "Third")
		ib, _ := f.m.RecycleSpace(ctx, store.AllTenants(), b.ID, RecycleOptions{})
		f.advance(time.Hour)
		ic, _ := f.m.RecycleSpace(ctx, store.AllTenants(), c.ID, RecycleOptions{})
		f.advance(8 * 24 * time.Hour)
		if n, err := f.m.PurgeExpired(ctx); err != nil || n != 2 {
			t.Fatalf("%d %v", n, err)
		}
		f.noItem(ib.ID)
		f.noItem(ic.ID)
		if pathExists(f.m.RecycleDir(ib.ID)) || pathExists(f.m.RecycleDir(ic.ID)) {
			t.Fatal("directories are left in the bin")
		}
	})
}

func (f *rbFix) fullSpaceNamed(owner, editor, member, name string) Info {
	info := f.fullSpace(owner, editor, member)
	if err := store.RenameSpace(ctx, f.st.Q(), info.ID, name); err != nil {
		f.t.Fatal(err)
	}
	return info
}

func TestRecycleOfASpaceInUseChangesNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		info := f.fullSpace("olive", "edith", "mia")
		before := f.snapshot(info.ID)
		h, err := f.m.Open(ctx, store.User{ID: "olive", Role: "member"}, info.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{})
		h.Release()
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("%v", err)
		}
		for tbl, rows := range before {
			if strings.Join(rows, "\n") != strings.Join(f.snapshot(info.ID)[tbl], "\n") {
				t.Errorf("a refused deletion changed %s", tbl)
			}
		}
		if root, _ := f.m.paths(info.ID); !pathExists(root) {
			t.Fatal("the clone moved")
		}
		// Not stuck: once nobody holds it, it can be deleted.
		if _, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAPersonalSpaceIsNotRecycledOnItsOwnNorRestoredWithoutItsPerson(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		f.user("olive")
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "0000000P01", "Olive", "olive", "", f.clock()); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), "0000000P01", "olive", perm.Owner, f.clock()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.RecycleSpace(ctx, store.AllTenants(), "0000000P01", RecycleOptions{}); !errors.Is(err, ErrPersonalSpace) {
			t.Fatalf("%v", err)
		}
		// Deleted with its person (what the deletion of a person does) and restored alone: refused.
		item, err := f.m.RecycleSpace(ctx, store.AllTenants(), "0000000P01", RecycleOptions{WithPerson: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, "olive"); !errors.Is(err, ErrNeedsPerson) {
			t.Fatalf("%v", err)
		}
		if it := f.item(item.ID); it.State != store.RecycleReady {
			t.Fatalf("%+v", it)
		}
	})
}

func TestRemovePacedRemovesTheTree(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a/b/c.txt", "a/d.txt", "e.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, "x", p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x", p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink to a directory outside is removed as a link, never followed.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "x", "a", "link")); err != nil {
		t.Skip("no symlinks")
	}
	if err := removeAllPaced(ctx, filepath.Join(dir, "x"), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if pathExists(filepath.Join(dir, "x")) {
		t.Fatal("not removed")
	}
	if !pathExists(filepath.Join(outside, "keep.txt")) {
		t.Fatal("a symbolic link was followed")
	}
}
