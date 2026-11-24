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
	if got := metaOf(t, a); !reflect.DeepEqual(got, want) {
		t.Fatalf("copy has meta %v, a fresh index has %v", got, want)
	}
	wantB := map[string]string{}
	for k, v := range want {
		wantB[k] = v
	}
	wantB["root"] = "/space/b"
	if got := metaOf(t, b); !reflect.DeepEqual(got, wantB) {
		t.Fatalf("second copy has meta %v, want %v", got, wantB)
	}
	// Changing one leaves the other alone.
	if _, err := a.wdb.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('types_fp', 'x')`); err != nil {
		t.Fatal(err)
	}
	if got := metaOf(t, b); !reflect.DeepEqual(got, wantB) {
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
