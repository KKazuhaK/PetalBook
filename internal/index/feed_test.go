package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func putPage(t *testing.T, x *Index, rel, id, title string) {
	t.Helper()
	if _, err := x.Put(rel, []byte(pageFile(id, title, "", nil, "", "body "+title+"\n")), clockNs.Add(int64(time.Second))); err != nil {
		t.Fatal(err)
	}
}

func changesAll(t *testing.T, x *Index, since string, limit int) (items []CatalogEntry, next string) {
	t.Helper()
	for {
		cl, err := x.Changes(since, limit)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, cl.Items...)
		since = cl.Next
		if !cl.More {
			return items, since
		}
	}
}

func pageID(n int) string { return fmt.Sprintf("01JD3K8%03d", n) }

func TestFeedSeqIsIncreasingUnderConcurrentWrites(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	start := x.Head()
	const writers, each = 8, 25
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				n := g*each + i
				putPage(t, x, fmt.Sprintf("p%d.md", n), pageID(n), fmt.Sprintf("T%d", n))
				if i%5 == 4 { // a change of a page already there
					putPage(t, x, fmt.Sprintf("p%d.md", n), pageID(n), fmt.Sprintf("T%d again", n))
				}
			}
		}()
	}
	wg.Wait()
	// Every write took a number higher than the one before: read them back in
	// the order of the sequence, in small steps, and see each page once.
	items, next := changesAll(t, x, start, 7)
	if len(items) != writers*each {
		t.Fatalf("%d items, want %d", len(items), writers*each)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Fatalf("%s twice", it.ID)
		}
		seen[it.ID] = true
	}
	if next != x.Head() {
		t.Fatalf("next %s, head %s", next, x.Head())
	}
	rows, err := x.wdb.Query(`SELECT seq FROM pages ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prev, n int64
	for rows.Next() {
		var s int64
		rows.Scan(&s)
		if s <= prev {
			t.Fatalf("seq %d after %d", s, prev)
		}
		prev, n = s, n+1
	}
	// 200 pages and 40 rewrites: 240 numbers were taken.
	if _, seq, _ := parseCursor(x.Head()); seq != uint64(writers*each+writers*each/5) {
		t.Fatalf("head seq %d", seq)
	}
}

func TestFeedChangesAreOnceAndInOrder(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	putPage(t, x, "a.md", pageID(1), "A")
	putPage(t, x, "b.md", pageID(2), "B")
	base := x.Head()

	for i := 0; i < 5; i++ {
		putPage(t, x, "a.md", pageID(1), fmt.Sprintf("A%d", i))
	}
	putPage(t, x, "c.md", pageID(3), "C")
	items, next := changesAll(t, x, base, 1000)
	if len(items) != 2 || items[0].ID != pageID(1) || items[1].ID != pageID(3) || items[0].Title != "A4" {
		t.Fatalf("items %+v", items)
	}
	if cl, _ := x.Changes(next, 10); len(cl.Items) != 0 || cl.More || cl.Next != next {
		t.Fatalf("nothing new: %+v", cl)
	}

	// A move keeps the id and gets a new number; a removal leaves a record.
	putPage(t, x, "sub/a.md", pageID(1), "A4")
	if err := x.Forget([]string{"a.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := x.Remove(pageID(2)); err != nil {
		t.Fatal(err)
	}
	items, next = changesAll(t, x, next, 1000)
	if len(items) != 2 || items[0].ID != pageID(1) || items[0].Path != "sub/a.md" || items[1].ID != pageID(2) || !items[1].Deleted {
		t.Fatalf("items %+v", items)
	}
	// The removal of a page made again with the same id is not a removal.
	putPage(t, x, "b2.md", pageID(2), "B")
	items, _ = changesAll(t, x, next, 1000)
	if len(items) != 1 || items[0].Deleted || items[0].Path != "b2.md" {
		t.Fatalf("items %+v", items)
	}
	var n int
	x.wdb.QueryRow(`SELECT count(*) FROM gone`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d records of removals", n)
	}
}

func TestFeedMoreAndNext(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	base := x.Head()
	for i := 0; i < 5; i++ {
		putPage(t, x, fmt.Sprintf("p%d.md", i), pageID(i), "T")
	}
	cl, err := x.Changes(base, 2)
	if err != nil || len(cl.Items) != 2 || !cl.More || cl.Next == x.Head() {
		t.Fatalf("%+v %v", cl, err)
	}
	cl2, _ := x.Changes(cl.Next, 3)
	if len(cl2.Items) != 3 || cl2.More || cl2.Next != x.Head() || cl2.Items[0].ID != pageID(2) {
		t.Fatalf("%+v", cl2)
	}
	for _, bad := range []string{"", "x", "zzzzzzzz-1", "00000000-", "00000000-1a", x.Head() + "0000000000000000000"} {
		if _, err := x.Changes(bad, 10); err != ErrBadFeedCursor {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	// A cursor ahead of the head, or of another epoch, is expired.
	if _, err := x.Changes("00000000-1", 10); err != ErrExpired {
		t.Fatal(err)
	}
	if _, err := x.Changes(cursorOf(x.head.Load().epoch, 9999), 10); err != ErrExpired {
		t.Fatal(err)
	}
}

func TestFeedGoneIsBoundedByCount(t *testing.T) {
	old := goneMax
	goneMax = 3
	t.Cleanup(func() { goneMax = old })
	x := openIndex(t)
	for i := 0; i < 8; i++ {
		putPage(t, x, fmt.Sprintf("p%d.md", i), pageID(i), "T")
	}
	early := x.Head()
	for i := 0; i < 8; i++ {
		if err := x.Remove(pageID(i)); err != nil {
			t.Fatal(err)
		}
		var n int
		x.wdb.QueryRow(`SELECT count(*) FROM gone`).Scan(&n)
		if n > 3 {
			t.Fatalf("%d records after %d removals", n, i+1)
		}
	}
	if _, err := x.Changes(early, 100); err != ErrExpired {
		t.Fatalf("a cursor from before the dropped records: %v", err)
	}
	cur := x.Head()
	putPage(t, x, "new.md", pageID(50), "N")
	if items, _ := changesAll(t, x, cur, 10); len(items) != 1 {
		t.Fatalf("%+v", items)
	}
	// The three removals that are kept can be asked for by a cursor at the floor.
	var floor uint64
	x.wdb.QueryRow(`SELECT value FROM meta WHERE key = 'gone_floor'`).Scan(&floor)
	items, _ := changesAll(t, x, cursorOf(x.head.Load().epoch, floor), 100)
	removed := 0
	for _, it := range items {
		if it.Deleted {
			removed++
		}
	}
	if removed != 3 {
		t.Fatalf("%d removals at the floor, want 3", removed)
	}
}

func TestFeedGoneIsBoundedByAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	x := openIndex(t)
	x.now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		putPage(t, x, fmt.Sprintf("p%d.md", i), pageID(i), "T")
	}
	early := x.Head()
	x.Remove(pageID(0))
	mid := x.Head()
	now = now.Add(179 * 24 * time.Hour)
	x.Remove(pageID(1))
	if _, err := x.Changes(early, 10); err != nil {
		t.Fatalf("within 180 days: %v", err)
	}
	now = now.Add(2 * 24 * time.Hour) // the first removal is 181 days old
	x.Remove(pageID(2))
	if _, err := x.Changes(early, 10); err != ErrExpired {
		t.Fatalf("older than 180 days: %v", err)
	}
	if _, err := x.Changes(mid, 10); err != nil {
		t.Fatalf("at the first removal: %v", err)
	}
	// A restart prunes by age too.
	now = now.Add(200 * 24 * time.Hour)
	if _, err := x.Sync(newMem(), false); err != nil { // removes the page left: one record, the others are old
		t.Fatal(err)
	}
	var n int
	x.wdb.QueryRow(`SELECT count(*) FROM gone`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d records", n)
	}
}

func TestFeedNewEpochAfterRebuildAndRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "state", "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	src := newMem()
	src.put("a.md", pageFile(pageID(1), "A", "", nil, "", "x"))
	syncAll(t, x, src, false)
	head := x.Head()
	if _, err := x.Changes(head, 10); err != nil {
		t.Fatal(err)
	}
	x.Close()
	// The head survives a restart.
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if x.Head() != head {
		t.Fatalf("head %s, was %s", x.Head(), head)
	}
	syncAll(t, x, src, false)
	if x.Head() != head {
		t.Fatalf("a sync with nothing to do moved the head: %s", x.Head())
	}
	// A rebuild from scratch is a new epoch.
	syncAll(t, x, src, true)
	if _, err := x.Changes(head, 10); err != ErrExpired {
		t.Fatalf("after a rebuild: %v", err)
	}
	if x.Head() == head {
		t.Fatal("same head after a rebuild")
	}
}

func TestFeedSchemaVersionRebuildsOldIndex(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "state", "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.wdb.Exec(`PRAGMA user_version = 9`); err != nil {
		t.Fatal(err)
	}
	old := x.Head()
	x.Close()
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if x.Head() == old {
		t.Fatal("an index of the older version was kept")
	}
}

func TestFeedCatalog(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	for i := 0; i < 30; i++ {
		folder := []string{"", "Tang", "Tang/Li", "Song", "Tangible"}[i%5]
		rel := fmt.Sprintf("p%d.md", i)
		if folder != "" {
			rel = folder + "/" + rel
		}
		putPage(t, x, rel, pageID(i), fmt.Sprintf("T%d", i))
	}
	walk := func(folders []string, limit int) (ids []string, first Batch) {
		cursor := ""
		for {
			b, err := x.Catalog(folders, cursor, limit)
			if err != nil {
				t.Fatal(err)
			}
			if cursor == "" {
				first = b
			} else if b.Changes != "" {
				t.Fatal("changes in a later batch")
			}
			for _, e := range b.Items {
				ids = append(ids, e.ID)
			}
			if b.Next == "" {
				return
			}
			cursor = b.Next
		}
	}
	all, first := walk(nil, 7)
	if len(all) != 30 || first.Changes != x.Head() || first.Tree == "" {
		t.Fatalf("%d pages, changes %q", len(all), first.Changes)
	}
	if len(first.Items) != 7 || first.Next == "" {
		t.Fatalf("first batch %d next %q", len(first.Items), first.Next)
	}
	if ids, _ := walk([]string{"Tang"}, 4); len(ids) != 12 { // Tang and Tang/Li, not Tangible
		t.Fatalf("Tang: %d", len(ids))
	}
	if ids, _ := walk([]string{"Tang/Li", "Song", "/"}, 4); len(ids) != 30 { // the root names all
		t.Fatalf("all: %d", len(ids))
	}
	if ids, _ := walk([]string{"Tang/Li", "Song"}, 100); len(ids) != 12 {
		t.Fatalf("two folders: %d", len(ids))
	}
	// A cursor of another set of folders, or not one of ours, is refused.
	if _, err := x.Catalog([]string{"Song"}, first.Next, 7); err != ErrBadFeedCursor {
		t.Fatalf("%v", err)
	}
	if _, err := x.Catalog(nil, "nope", 7); err != ErrBadFeedCursor {
		t.Fatalf("%v", err)
	}
	// A page written while the walk is on is in the changes after its start.
	b, _ := x.Catalog(nil, "", 10)
	putPage(t, x, "late.md", pageID(99), "Late")
	putPage(t, x, "p0.md", pageID(0), "Changed")
	items, _ := changesAll(t, x, b.Changes, 100)
	if len(items) != 2 {
		t.Fatalf("%+v", items)
	}
	// A rebuild ends the walk.
	syncAll(t, x, newMem(), true)
	if _, err := x.Catalog(nil, b.Next, 10); err != ErrExpired {
		t.Fatalf("%v", err)
	}
}

func TestFeedTreeStamp(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	t0 := x.TreeStamp()
	putPage(t, x, "a.md", pageID(1), "A")
	t1 := x.TreeStamp()
	if t1 == t0 {
		t.Fatal("a page was added")
	}
	putPage(t, x, "a.md", pageID(1), "A, edited")
	if x.TreeStamp() != t1 {
		t.Fatal("an edit is not a change of the tree")
	}
	x.AddFolder("F/G")
	t2 := x.TreeStamp()
	if t2 == t1 {
		t.Fatal("a folder was made")
	}
	x.AddFolder("F/G")
	if x.TreeStamp() != t2 {
		t.Fatal("a folder that was there")
	}
	putPage(t, x, "F/a.md", pageID(1), "A, moved")
	t3 := x.TreeStamp()
	if t3 == t2 {
		t.Fatal("a page moved to another folder")
	}
	x.Remove(pageID(1))
	if x.TreeStamp() == t3 {
		t.Fatal("a page was removed")
	}
	b, _ := x.Catalog(nil, "", 10)
	if b.Tree != x.TreeStamp() {
		t.Fatalf("%s %s", b.Tree, x.TreeStamp())
	}
}

func TestFeedAPageThatIsThereIsNeverAlsoARemoval(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	base := x.Head()
	putPage(t, x, "a.md", pageID(1), "A")
	// A record of removal that outlived the page's coming back (not made by the index itself).
	if _, err := x.wdb.Exec(`INSERT INTO gone(id, seq, at) VALUES (?, 1, 1)`, pageID(1)); err != nil {
		t.Fatal(err)
	}
	items, _ := changesAll(t, x, base, 10)
	if len(items) != 1 || items[0].Deleted {
		t.Fatalf("%+v", items)
	}
	// Writing the page again clears the record, whatever the counter says.
	putPage(t, x, "a.md", pageID(1), "A again")
	var n int
	x.wdb.QueryRow(`SELECT count(*) FROM gone`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d records", n)
	}
}

// A count of removals that drifted from the table (too high, or too low so that
// taking one away would go below zero) never fails a write or a sync: the prune
// counts the table again.
func TestFeedGoneCountThatDriftedNeverFailsAWrite(t *testing.T) {
	old := goneMax
	goneMax = 3
	t.Cleanup(func() { goneMax = old })
	x := openIndex(t)
	for i := 0; i < 8; i++ {
		putPage(t, x, fmt.Sprintf("p%d.md", i), pageID(i), "T")
	}
	gone := func() (n int) {
		x.wdb.QueryRow(`SELECT count(*) FROM gone`).Scan(&n)
		return n
	}
	// Too high: the prune would look for a row past the end of the table.
	if _, err := x.wdb.Exec(`UPDATE meta SET value = '1000' WHERE key = 'gone_n'`); err != nil {
		t.Fatal(err)
	}
	if err := x.Remove(pageID(0)); err != nil {
		t.Fatalf("a removal with the count too high: %v", err)
	}
	if err := x.Remove(pageID(1)); err != nil {
		t.Fatal(err)
	}
	var n uint64
	x.wdb.QueryRow(`SELECT value FROM meta WHERE key = 'gone_n'`).Scan(&n)
	if int(n) != gone() {
		t.Fatalf("gone_n %d, table %d", n, gone())
	}
	// Too low: a page that comes back takes its record away from a count of zero.
	if _, err := x.wdb.Exec(`UPDATE meta SET value = '0' WHERE key = 'gone_n'`); err != nil {
		t.Fatal(err)
	}
	putPage(t, x, "p0.md", pageID(0), "T")
	for i := 2; i < 8; i++ {
		if err := x.Remove(pageID(i)); err != nil {
			t.Fatalf("removal %d after the count went below zero: %v", i, err)
		}
		if gone() > 3 {
			t.Fatalf("%d records", gone())
		}
	}
	// A sync (as at a start) with the count too high again.
	if _, err := x.wdb.Exec(`UPDATE meta SET value = '99999' WHERE key = 'gone_n'`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Sync(newMem(), false); err != nil {
		t.Fatalf("a sync with the count too high: %v", err)
	}
	if gone() > 3 {
		t.Fatalf("%d records after the sync", gone())
	}
}

// copyFiles copies the database files as they are on disk now, as a power cut
// would leave them (the shared-memory file is rebuilt from the log).
func copyFiles(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(from + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to+suffix, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// An index that was not closed (a crash, a power cut that may have taken its
// last commits) starts a new epoch when it is opened again: a sequence number
// that was handed out before can be handed out again, so no old cursor may be
// answered.
func TestFeedNewEpochAfterAnUncleanShutdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	putPage(t, x, "a.md", pageID(1), "A")
	head := x.Head()
	// The files as the machine stopped, with the index still open.
	crashed := filepath.Join(dir, "b", "index.db")
	copyFiles(t, p, crashed)
	y, err := Open(crashed, "/space")
	if err != nil {
		t.Fatal(err)
	}
	if y.Head() == head || strings.SplitN(y.Head(), "-", 2)[0] == strings.SplitN(head, "-", 2)[0] {
		t.Fatalf("same epoch after an unclean shutdown: %s, was %s", y.Head(), head)
	}
	if _, err := y.Changes(head, 10); err != ErrExpired {
		t.Fatalf("an old cursor after an unclean shutdown: %v", err)
	}
	// Closed cleanly, it keeps its head.
	after := y.Head()
	y.Close()
	y, err = Open(crashed, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer y.Close()
	if y.Head() != after {
		t.Fatalf("head %s after a clean close, was %s", y.Head(), after)
	}
}

// The first sync after an open that finds files that differ from the index
// starts a new epoch: what the index held may have been rolled back.
func TestFeedNewEpochWhenTheStartupSyncFindsChanges(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "state", "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	src := newMem()
	src.put("a.md", pageFile(pageID(1), "A", "", nil, "", "x"))
	syncAll(t, x, src, false)
	head := x.Head()
	x.Close()
	src.put("b.md", pageFile(pageID(2), "B", "", nil, "", "y"))
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	syncAll(t, x, src, false)
	if _, err := x.Changes(head, 10); err != ErrExpired {
		t.Fatalf("an old cursor after a startup sync that found changes: %v", err)
	}
	// A later sync that finds changes is an ordinary write.
	cur := x.Head()
	src.put("c.md", pageFile(pageID(3), "C", "", nil, "", "z"))
	syncAll(t, x, src, false)
	if items, _ := changesAll(t, x, cur, 10); len(items) != 1 {
		t.Fatalf("%+v", items)
	}
}

// The head a closed index was left at is read without opening it: nothing is
// created where there is no index, an index that was not closed cleanly has no
// head to give, and the read leaves nothing that a later open would trip on.
func TestStoredHeadOfAClosedIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "none", "index.db")
	if h, err := StoredHead(missing); err != nil || h != "" {
		t.Fatalf("no index: %q %v", h, err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("the read made something: %v", err)
	}

	p := filepath.Join(dir, "state", "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	putPage(t, x, "a.md", pageID(1), "A")
	// Open: it may change at any moment, and its next open after a crash is a new epoch.
	if h, err := StoredHead(p); err != nil || h != "" {
		t.Fatalf("an open index: %q %v", h, err)
	}
	head := x.Head()
	x.Close()
	for range 2 {
		h, err := StoredHead(p)
		if err != nil || h != head {
			t.Fatalf("closed: %q %v, want %s", h, err, head)
		}
	}
	// A reader of a WAL database may leave the log (empty) and the shared memory, as any reader does; never a change.
	if fi, err := os.Stat(p + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("the read wrote %d bytes to the log", fi.Size())
	}
	// A later open is not disturbed: same head, writes go on.
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if x.Head() != head {
		t.Fatalf("head %s after the read, was %s", x.Head(), head)
	}
	putPage(t, x, "b.md", pageID(2), "B")
	if items, _ := changesAll(t, x, head, 10); len(items) != 1 {
		t.Fatalf("%+v", items)
	}
}

// A space restored from its remote (3.1) keeps its index: the files that
// changed on the remote while it was evicted come in as changes, under the same
// epoch, so a cursor from before the eviction stays good. The note is used once
// and only on an index that was closed cleanly.
func TestFeedRestoreKeepsTheEpoch(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "state", "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	src := newMem()
	src.put("a.md", pageFile(pageID(1), "A", "", nil, "", "x"))
	syncAll(t, x, src, false)
	head := x.Head()
	x.Close()

	// While it was evicted the remote got a new page.
	src.put("b.md", pageFile(pageID(2), "B", "", nil, "", "y"))
	if err := NoteRestored(p); err != nil {
		t.Fatal(err)
	}
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(t, x, src, false)
	items, next := changesAll(t, x, head, 10)
	if len(items) != 1 || items[0].ID != pageID(2) {
		t.Fatalf("changes after a restore: %+v", items)
	}
	if next != x.Head() || !strings.HasPrefix(next, strings.SplitN(head, "-", 2)[0]+"-") {
		t.Fatalf("the epoch moved: %s, was %s", next, head)
	}
	head = x.Head()
	x.Close()

	// The note was used up: the next open whose startup sync finds changes is
	// a new epoch, as without a restore.
	src.put("c.md", pageFile(pageID(3), "C", "", nil, "", "z"))
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(t, x, src, false)
	if _, err := x.Changes(head, 10); err != ErrExpired {
		t.Fatalf("the restore note was used twice: %v", err)
	}
	head = x.Head()
	// An index that was not closed cleanly gets no note: its next open is a
	// new epoch whatever the restore says.
	if err := NoteRestored(p); err != nil {
		t.Fatal(err)
	}
	y, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer y.Close()
	defer x.Close()
	if y.Head() == head {
		t.Fatal("an unclean index kept its epoch after a restore note")
	}
	// No index at all: nothing to note, nothing made.
	none := filepath.Join(t.TempDir(), "none", "index.db")
	if err := NoteRestored(none); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(none)); !os.IsNotExist(err) {
		t.Fatalf("NoteRestored made something: %v", err)
	}
}

// Notes stored at Close are read by the next open, and Close keeps them.
func TestCloseWithStoresNotes(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "index.db")
	x, err := Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.CloseWith(map[string]string{"settings": "abc", "epoch": "nope"}); err != nil {
		t.Fatal(err)
	}
	if h, err := StoredHead(p); err != nil || h == "" {
		t.Fatalf("CloseWith did not close cleanly: %q %v", h, err)
	}
	x, err = Open(p, "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if v, err := x.StoredNote("settings"); err != nil || v != "abc" {
		t.Fatalf("note %q %v", v, err)
	}
	if v, _ := x.StoredNote("epoch"); v != "" {
		t.Fatalf("a key that is not a note was stored: %q", v)
	}
}
