package index

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCheckPageInALargeSpace measures the check page with 50 000 pages and a
// few thousand problems of every kind: the first page with the counts, a page
// in the middle of a long kind, and the cost of a full rebuild, which records
// the problems as it goes. It runs only when asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestCheckPageInALargeSpace -v ./internal/index/
//
// The listing is a few index queries (a page and the counts, whatever the
// number of pages), so it must stay in the tens of milliseconds; a rebuild
// must not be noticeably slower than it was without the checks (the same pages
// took about 5.0 seconds before they were added, 3.7 after: the statements
// that look a page up are prepared once, and a rebuild no longer deletes the
// rows of a table it has just emptied). With the links of 2.4, every page
// links to another: the broken_link rows of a rebuild are one query over about
// 101 000 links (40 to 46 ms of the rebuild), and a page that 1 000 others link
// to by path costs about 25 ms to remove or add (their rows are looked at
// again), a save of a page alone well under a millisecond.
func TestCheckPageInALargeSpace(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	const pages = 50_000
	x := openIndex(t)
	x.UseTypes(poemRules("timing"))
	x.SetAssetExists(func(rel string) bool { return rel != "assets/gone/x.png" })
	m := newMem()
	for i := 0; i < pages; i++ {
		var b strings.Builder
		extra := "status: done\n"
		title := fmt.Sprintf("页 %d", i)
		// Every page links to the one before it, by id and path.
		body := fmt.Sprintf("正文 %d [x](/assets/a/b.png) 更多的文字 [前](/lib/f%02d/%05d.md \"%010d\")\n", i, (i+49)%50, (i+pages-1)%pages, (i+pages-1)%pages)
		switch {
		case i%20 == 1: // 2 500 pages with a field the type does not have
			extra = "type: poem\nstatus: done\ncolour: red\n"
		case i%25 == 2: // 2 000 with a value that does not fit
			extra = "type: poem\nrating: high\n"
		case i%50 == 3: // 1 000 with an attachment that is not there
			body = "![](/assets/gone/x.png)\n"
		case i%10 == 4: // 5 000 that share their title with one more
			title = fmt.Sprintf("同名 %d", (i%50)*1000+i/100)
		case i%40 == 5: // 1 250 with a link to a page that does not exist
			body += fmt.Sprintf("[无](/lib/none/%05d.md)\n", i)
		case i%50 == 6: // 1 000 that link to the hub page by path
			body += "[中](/hub.md)\n"
		}
		fmt.Fprintf(&b, "---\nid: %010d\ntitle: %s\ntags: [a]\n%s---\n%s", i, title, extra, body)
		m.files[fmt.Sprintf("lib/f%02d/%05d.md", i%50, i)] = memFile{[]byte(b.String()), clock() + int64(i)}
	}
	hub := []byte("---\nid: 9999999999\ntitle: 中\n---\nhub\n")
	m.files["hub.md"] = memFile{hub, clock()}
	for i := 0; i < 3000; i++ {
		m.files[fmt.Sprintf("bad/%04d.md", i)] = memFile{[]byte("---\ntitle: x\n---\nno id\n"), clock() + int64(pages+i)}
	}
	for r := 0; r < 3; r++ {
		start := time.Now()
		if _, err := x.Sync(m, true); err != nil {
			t.Fatal(err)
		}
		t.Logf("full rebuild of %d pages and %d files that are not pages: %v", pages, 3000, time.Since(start))
	}
	v := listChecks(t, x, "", "")
	t.Logf("counts %v", v.counts)
	var total int
	for _, n := range v.counts {
		total += n
	}
	if total < 5000 {
		t.Fatalf("only %d problems", total)
	}

	measure := func(name string, f func() int) {
		t.Helper()
		var ds []time.Duration
		for i := 0; i < 20; i++ {
			start := time.Now()
			f()
			ds = append(ds, time.Since(start))
		}
		sortDurations(ds)
		t.Logf("%-52s median %8v  worst %8v", name, ds[len(ds)/2], ds[len(ds)-1])
		if ds[len(ds)/2] > 50*time.Millisecond {
			t.Errorf("%s: median %v is more than 50 ms", name, ds[len(ds)/2])
		}
	}
	measure("first page and the counts, all kinds", func() int { return len(listChecks(t, x, "", "").items) })
	for _, k := range []string{KindNoID, KindNotInType, KindTypeMismatch, KindBrokenLink, KindMissingAsset, KindDuplicateTitle} {
		first := listChecks(t, x, k, "")
		if len(first.items) != CheckPage || first.next == "" {
			t.Fatalf("%s: %d items", k, len(first.items))
		}
		measure("first page of "+k, func() int { return len(listChecks(t, x, k, "").items) })
		measure("page 2 of "+k, func() int { return len(listChecks(t, x, k, first.next).items) })
	}
	deep := listChecks(t, x, KindDuplicateTitle, "")
	for i := 0; i < 30 && deep.next != ""; i++ {
		deep = listChecks(t, x, KindDuplicateTitle, deep.next)
	}
	measure("page 31 of duplicate_title", func() int { return len(listChecks(t, x, KindDuplicateTitle, deep.next).items) })

	// One save that changes a page of a big group: the group is looked at again.
	var ds []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		if _, err := x.Put("lib/f04/00004.md", []byte(fmt.Sprintf("---\nid: %010d\ntitle: 同名 %d\n---\nx %d\n", 4, 0, i)), clock()+int64(i)); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, time.Since(start))
	}
	sortDurations(ds)
	t.Logf("%-52s median %8v  worst %8v", "one save into a title group", ds[len(ds)/2], ds[len(ds)-1])

	// A page that 1 000 others link to by path goes away and comes back: each
	// time their broken_link rows are looked at again.
	if n := listChecks(t, x, "", "").counts[KindBrokenLink]; n != 1250 {
		t.Fatalf("broken links before: %d", n)
	}
	var gone, back []time.Duration
	for i := 0; i < 10; i++ {
		start := time.Now()
		if err := x.Remove("9999999999"); err != nil {
			t.Fatal(err)
		}
		gone = append(gone, time.Since(start))
		if n := listChecks(t, x, "", "").counts[KindBrokenLink]; n != 2250 {
			t.Fatalf("broken links without the hub: %d", n)
		}
		start = time.Now()
		if _, err := x.Put("hub.md", hub, clock()+int64(i)); err != nil {
			t.Fatal(err)
		}
		back = append(back, time.Since(start))
	}
	sortDurations(gone)
	sortDurations(back)
	t.Logf("%-52s median %8v  worst %8v", "remove a page 1 000 pages link to", gone[len(gone)/2], gone[len(gone)-1])
	t.Logf("%-52s median %8v  worst %8v", "add it back", back[len(back)/2], back[len(back)-1])
	// One save of a page with a broken link (its own links only).
	ds = ds[:0]
	for i := 0; i < 20; i++ {
		start := time.Now()
		if _, err := x.Put("lib/f05/00005.md", []byte(fmt.Sprintf("---\nid: %010d\ntitle: 页 5\n---\n[无](/lib/none/x%d.md)\n", 5, i)), clock()+int64(i)); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, time.Since(start))
	}
	sortDurations(ds)
	t.Logf("%-52s median %8v  worst %8v", "one save of a page with a broken link", ds[len(ds)/2], ds[len(ds)-1])
}
