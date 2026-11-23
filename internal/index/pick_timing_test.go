package index

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDailyPickInALargeSpace measures what the daily text of a space asks of
// the index with 50 000 pages: the count of the pages that meet the source and
// the read of the one at a position, for a folder, a tag, a folder with a
// filter, a type bound to folders, a rare tag, and the whole space. It runs
// only when asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestDailyPickInALargeSpace -v ./internal/index/
//
// Each must stay in the tens of milliseconds: the pick is made once a day for
// each source, and again only when the page it chose is gone.
func TestDailyPickInALargeSpace(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	const pages = 50_000
	x := openIndex(t)
	m := newMem()
	statuses := []string{"idea", "draft", "review", "done", "dropped"}
	tags := []string{"诗", "词", "文", "赋", "曲", "联", "铭", "颂"}
	for i := 0; i < pages; i++ {
		var b strings.Builder
		folder := fmt.Sprintf("lib/f%02d", i%50)
		fmt.Fprintf(&b, "---\nid: %010d\ntitle: 页 %d\nstatus: %s\ntags: [%s]\n", i, i, statuses[i*7%5], tags[i%8])
		if i%10 == 0 {
			b.WriteString("type: poem\n")
		}
		if i == 123 {
			b.WriteString("rare: yes\n")
		}
		fmt.Fprintf(&b, "---\n正文 %d\n", i)
		m.files[fmt.Sprintf("%s/%05d.md", folder, i)] = memFile{[]byte(b.String()), clock() + int64(i)}
	}
	for i := 0; i < 100; i++ {
		m.files[fmt.Sprintf("small/%03d.md", i)] = memFile{[]byte(fmt.Sprintf("---\nid: S%09d\nstatus: done\ntags: [rare-tag]\n---\nx\n", i)), clock() + int64(pages+i)}
	}
	start := time.Now()
	if _, err := x.Sync(m, false); err != nil {
		t.Fatal(err)
	}
	t.Logf("index of %d pages built in %v", pages, time.Since(start))

	lib, small, root := "lib", "small", ""
	var boundFolders []string // the folders a type is bound to: half of them
	for i := 0; i < 50; i += 2 {
		boundFolders = append(boundFolders, fmt.Sprintf("lib/f%02d", i))
	}
	measure := func(name string, q PickQuery, wantTotal int) {
		t.Helper()
		var ds []time.Duration
		for i := 0; i < 15; i++ {
			start := time.Now()
			total, s, err := x.Pick(q, func(total int) int { return (i*7919 + 13) % total })
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(start))
			if total != wantTotal || s.ID == "" {
				t.Fatalf("%s: %d pages (want %d), %q", name, total, wantTotal, s.ID)
			}
		}
		sortDurations(ds)
		t.Logf("%-58s median %8v  worst %8v  (%d pages)", name, ds[len(ds)/2], ds[len(ds)-1], wantTotal)
		if ds[len(ds)/2] > 100*time.Millisecond {
			t.Errorf("%s: median %v is more than 100 ms", name, ds[len(ds)/2])
		}
	}
	measure("folder lib, recursive", PickQuery{Folder: &lib, Recursive: true}, pages)
	measure("whole space (root, recursive)", PickQuery{Folder: &root, Recursive: true}, pages+100)
	measure("tag 诗 (one page in eight)", PickQuery{Tags: []string{"诗"}}, pages/8)
	measure("tag rare-tag (100 pages)", PickQuery{Tags: []string{"rare-tag"}}, 100)
	measure("folder lib + status in (2 of 5)", PickQuery{Folder: &lib, Recursive: true, Filters: []Filter{{Key: "status", Op: "in", Values: []any{"draft", "review"}}}}, pages*2/5)
	measure("folder small + status in done", PickQuery{Folder: &small, Recursive: true, Filters: []Filter{{Key: "status", Op: "in", Values: []any{"done"}}}}, 100)
	measure("tag 赋 (6250 pages) + a property one page has", PickQuery{Tags: []string{"赋"}, Filters: []Filter{{Key: "rare", Op: "notEmpty"}}}, 1)
	measure("type poem, header only (one page in ten)", PickQuery{Type: "poem"}, pages/10)
	// A type bound to 25 of the 50 folders: the pages with no type of their own
	// there (nine in ten) and those with the header type (one in ten) elsewhere.
	want := 0
	for i := 0; i < pages; i++ {
		inBound := (i%50)%2 == 0
		switch {
		case i%10 == 0:
			want++ // the header says poem
		case inBound:
			want++
		}
	}
	measure("type poem bound to 25 of 50 folders", PickQuery{Type: "poem", TypeFolders: boundFolders}, want)
	// The pick when nothing meets the source.
	var ds []time.Duration
	for i := 0; i < 15; i++ {
		start := time.Now()
		if _, _, err := x.Pick(PickQuery{Tags: []string{"nothing"}}, func(int) int { return 0 }); err != ErrNoPick {
			t.Fatal(err)
		}
		ds = append(ds, time.Since(start))
	}
	sortDurations(ds)
	t.Logf("%-58s median %8v  worst %8v", "no page meets the source", ds[len(ds)/2], ds[len(ds)-1])
	// The page picked is read by id (the memory of a day): one row.
	ds = nil
	for i := 0; i < 15; i++ {
		start := time.Now()
		if _, ok, err := x.Summary(fmt.Sprintf("%010d", 4242)); err != nil || !ok {
			t.Fatal(ok, err)
		}
		ds = append(ds, time.Since(start))
	}
	sortDurations(ds)
	t.Logf("%-58s median %8v  worst %8v", "the summary of the remembered page, by id", ds[len(ds)/2], ds[len(ds)-1])
}
