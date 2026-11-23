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
// rows of a table it has just emptied).
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
		body := fmt.Sprintf("正文 %d [x](/assets/a/b.png) 更多的文字\n", i)
		switch {
		case i%20 == 1: // 2 500 pages with a field the type does not have
			extra = "type: poem\nstatus: done\ncolour: red\n"
		case i%25 == 2: // 2 000 with a value that does not fit
			extra = "type: poem\nrating: high\n"
		case i%50 == 3: // 1 000 with an attachment that is not there
			body = "![](/assets/gone/x.png)\n"
		case i%10 == 4: // 5 000 that share their title with one more
			title = fmt.Sprintf("同名 %d", (i%50)*1000+i/100)
		}
		fmt.Fprintf(&b, "---\nid: %010d\ntitle: %s\ntags: [a]\n%s---\n%s", i, title, extra, body)
		m.files[fmt.Sprintf("lib/f%02d/%05d.md", i%50, i)] = memFile{[]byte(b.String()), clock() + int64(i)}
	}
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
	for _, k := range []string{KindNoID, KindNotInType, KindTypeMismatch, KindMissingAsset, KindDuplicateTitle} {
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
}
