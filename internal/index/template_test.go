package index

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func metaOf(t *testing.T, x *Index) map[string]string {
	t.Helper()
	rows, err := x.wdb.Query(`SELECT key, value FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		m[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return m
}

// withoutEpoch returns m without its epoch, after checking that the epoch is
// well formed and, when name is set, that no other index in seen has it.
func withoutEpoch(t *testing.T, m map[string]string, name string, seen map[string]string) map[string]string {
	t.Helper()
	e := m["epoch"]
	if !isEpoch(e) {
		t.Fatalf("meta %v has no epoch", m)
	}
	if name != "" {
		for other, oe := range seen {
			if oe == e {
				t.Fatalf("%s has the epoch of %s: %s", name, other, e)
			}
		}
		seen[name] = e
	}
	out := map[string]string{}
	for k, v := range m {
		if k != "epoch" {
			out[k] = v
		}
	}
	return out
}

func openAt(t *testing.T, dir, name, root string) *Index {
	t.Helper()
	x, err := Open(filepath.Join(dir, name), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func withTemplate(t *testing.T, on bool) {
	t.Helper()
	was := reuse.Load()
	reuse.Store(on)
	t.Cleanup(func() { reuse.Store(was) })
}

// The prepared index holds no meta rows, and an index made from it has exactly
// the meta of a fresh one, each with its own root.
func TestTemplateCopiesHaveTheirOwnMeta(t *testing.T) {
	dir := t.TempDir()

	withTemplate(t, false)
	fresh := openAt(t, dir, "fresh.db", "/space/a")
	if fresh.copied {
		t.Fatal("fresh index was copied")
	}
	want := metaOf(t, fresh)
	if len(want) == 0 {
		t.Fatal("a fresh index has no meta")
	}

	withTemplate(t, true)
	a := openAt(t, dir, "a.db", "/space/a")
	b := openAt(t, dir, "b.db", "/space/b")
	if !a.copied || !b.copied {
		t.Fatalf("copied: a=%v b=%v, want both", a.copied, b.copied)
	}
	// Each index has an epoch of its own; everything else of a copy is what
	// a fresh index has.
	epochs := map[string]string{"fresh": want["epoch"]}
	if got := withoutEpoch(t, metaOf(t, a), "a", epochs); !reflect.DeepEqual(got, withoutEpoch(t, want, "", nil)) {
		t.Fatalf("copy has meta %v, a fresh index has %v", got, want)
	}
	wantB := map[string]string{}
	for k, v := range want {
		wantB[k] = v
	}
	wantB["root"] = "/space/b"
	if got := withoutEpoch(t, metaOf(t, b), "b", epochs); !reflect.DeepEqual(got, withoutEpoch(t, wantB, "", nil)) {
		t.Fatalf("second copy has meta %v, want %v", got, wantB)
	}
	if a.Head() == b.Head() || a.Head() == fresh.Head() {
		t.Fatalf("heads are shared: fresh %q, a %q, b %q", fresh.Head(), a.Head(), b.Head())
	}
	// Changing one leaves the other alone.
	if _, err := a.wdb.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('types_fp', 'x')`); err != nil {
		t.Fatal(err)
	}
	if got := withoutEpoch(t, metaOf(t, b), "", nil); !reflect.DeepEqual(got, withoutEpoch(t, wantB, "", nil)) {
		t.Fatalf("meta of the second copy changed: %v", got)
	}
	// And the prepared bytes themselves carry no row.
	tpl := filepath.Join(dir, "tpl.db")
	if !place(tpl, template.data) {
		t.Fatal("place failed")
	}
	x := openAt(t, dir, "tpl.db", templateRoot)
	if got := metaOf(t, x); len(got) != 0 {
		t.Fatalf("template has meta %v", got)
	}
}

// Places of one new path at once: the file is put there whole by one of them,
// and no other file is left beside it. (The losers find the winner's file.)
func TestPlaceIsAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := []byte("0123456789")
	target := filepath.Join(dir, "x.db")
	var wg sync.WaitGroup
	won := make(chan bool, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won <- place(target, data)
		}()
	}
	wg.Wait()
	close(won)
	n := 0
	for w := range won {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d places won, want 1", n)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

// A copy of the prepared index runs its change feed as a fresh index does: the
// clean flag is written at Close, StoredHead reads the head of the closed copy,
// and the next open keeps the epoch; a copy that was not closed starts a new one.
func TestTemplateCopyFeedLifecycle(t *testing.T) {
	dir := t.TempDir()
	withTemplate(t, true)
	path := filepath.Join(dir, "c.db")
	x, err := Open(path, "/space/c")
	if err != nil {
		t.Fatal(err)
	}
	if !x.copied {
		t.Fatal("not copied")
	}
	head := x.Head()
	if head == "" {
		t.Fatal("copy has no head")
	}
	if got, err := StoredHead(path); err != nil || got != "" {
		t.Fatalf("StoredHead of an open copy = %q, %v; want empty", got, err)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := StoredHead(path); err != nil || got != head {
		t.Fatalf("StoredHead = %q, %v; want %q", got, err, head)
	}
	y, err := Open(path, "/space/c")
	if err != nil {
		t.Fatal(err)
	}
	if y.copied {
		t.Fatal("an existing index was replaced by a copy")
	}
	if y.Head() != head {
		t.Fatalf("head after a clean reopen = %q, want %q", y.Head(), head)
	}
	// Not closed: the next open starts a new epoch.
	z, err := Open(path, "/space/c")
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	defer y.Close()
	if z.Head() == head {
		t.Fatalf("head after an unclean reopen kept the epoch: %q", z.Head())
	}
}
