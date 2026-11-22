package index

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestPropQueriesInALargeFolder measures what the table view asks of the
// index in a folder of 50 000 pages that carry typed properties: the first
// page of a sort by a property, a later page by cursor, and a filter with a
// sort. It takes a while, so it runs only when asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestPropQueriesInALargeFolder -v ./internal/index/
//
// The numbers are logged, and each must stay in the tens of milliseconds: a
// page of a listing must not cost more as the folder grows.
func TestPropQueriesInALargeFolder(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	const pages = 50_000
	x := openIndex(t)
	m := newMem()
	statuses := []string{"idea", "draft", "review", "done", "dropped"}
	labels := []string{"urgent", "home", "work", "reading", "travel", "money", "health", "garden"}
	for i := 0; i < pages; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "---\nid: %010d\ntitle: 页 %d\ncreated: 2026-01-%02d\nstatus: %s\n", i, i, i%28+1, statuses[i*7%5])
		if i%9 != 0 { // some pages have no date and no points
			fmt.Fprintf(&b, "due: 20%02d-%02d-%02d\npoints: %d\n", 24+i%3, i%12+1, i%28+1, i%1000)
		}
		fmt.Fprintf(&b, "labels: [%s, %s]\n---\n正文 %d\n", labels[i%8], labels[(i*3+1)%8], i)
		m.files[fmt.Sprintf("tasks/%05d.md", i)] = memFile{[]byte(b.String()), clock + int64(i)}
	}
	// A small folder among the big one, its pages written last, so they have
	// the highest rowids and the worst place for a scan of the whole space.
	for i := 0; i < 100; i++ {
		m.files[fmt.Sprintf("small/%03d.md", i)] = memFile{[]byte(fmt.Sprintf("---\nid: S%09d\nstatus: review\npoints: %d\n---\nx\n", i, i)), clock + int64(pages+i)}
	}
	start := time.Now()
	if _, err := x.Sync(m, false); err != nil {
		t.Fatal(err)
	}
	t.Logf("index of %d pages built in %v", pages, time.Since(start))

	folder := "tasks"
	// measure runs q a few times and logs the median.
	measure := func(name string, q Query) Result {
		t.Helper()
		q.Folder = &folder
		var ds []time.Duration
		var res Result
		for i := 0; i < 15; i++ {
			start := time.Now()
			var err error
			if res, err = x.List(q); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(start))
		}
		sortDurations(ds)
		t.Logf("%-52s median %8v  worst %8v  (%d items)", name, ds[len(ds)/2], ds[len(ds)-1], len(res.Items))
		return res
	}

	measure("first 50, sort prop:status", Query{Sort: "prop:status", Limit: 50, Props: true})
	measure("first 50, sort -prop:due", Query{Sort: "-prop:due", Limit: 50, Props: true})
	measure("first 50, sort prop:points", Query{Sort: "prop:points", Limit: 50, Props: true})
	measure("first 50, sort prop:labels (a list)", Query{Sort: "prop:labels", Limit: 50, Props: true})
	// A page deep into the listing, by cursor.
	r := measure("first 50, sort prop:status", Query{Sort: "prop:status", Limit: 50})
	for i := 0; i < 400 && r.Next != ""; i++ { // 20 000 pages in
		var err error
		if r, err = x.List(Query{Folder: &folder, Sort: "prop:status", Limit: 50, Cursor: r.Next}); err != nil {
			t.Fatal(err)
		}
	}
	measure("page 400 by cursor, sort prop:status", Query{Sort: "prop:status", Limit: 50, Cursor: r.Next})
	r = measure("first 50, sort -prop:due", Query{Sort: "-prop:due", Limit: 50})
	for i := 0; i < 400 && r.Next != ""; i++ {
		var err error
		if r, err = x.List(Query{Folder: &folder, Sort: "-prop:due", Limit: 50, Cursor: r.Next}); err != nil {
			t.Fatal(err)
		}
	}
	measure("page 400 by cursor, sort -prop:due", Query{Sort: "-prop:due", Limit: 50, Cursor: r.Next})
	// The pages without a due date come last, by rowid.
	for r.Next != "" {
		var err error
		if r, err = x.List(Query{Folder: &folder, Sort: "-prop:due", Limit: 200, Cursor: r.Next}); err != nil {
			t.Fatal(err)
		}
		if r.Items[len(r.Items)-1].Path > "tasks/44444.md" {
			break
		}
	}
	measure("a page among those without a due date", Query{Sort: "-prop:due", Limit: 50, Cursor: r.Next})

	in := []Filter{{Key: "status", Op: "in", Values: []any{"draft", "review"}}}
	measure("filter status in (2 of 5), sort -prop:due", Query{Filters: in, Sort: "-prop:due", Limit: 50, Props: true})
	measure("filter status in (2 of 5), sort prop:status", Query{Filters: in, Sort: "prop:status", Limit: 50, Props: true})
	measure("filter status in + labels in, sort prop:points", Query{Filters: []Filter{in[0], {Key: "labels", Op: "in", Values: []any{"urgent"}}}, Sort: "prop:points", Limit: 50})
	measure("filter points range 100..200, sort -prop:due", Query{Filters: []Filter{{Key: "points", Op: "range", Values: []any{100, 200}}}, Sort: "-prop:due", Limit: 50})
	measure("filter title-ish contains on status, sort prop:due", Query{Filters: []Filter{{Key: "status", Op: "contains", Values: []any{"rev"}}}, Sort: "prop:due", Limit: 50})
	measure("filter points range 998..999 (rare), sort -prop:due", Query{Filters: []Filter{{Key: "points", Op: "range", Values: []any{998, 999}}}, Sort: "-prop:due", Limit: 50})
	measure("filter due empty, sort prop:status", Query{Filters: []Filter{{Key: "due", Op: "empty"}}, Sort: "prop:status", Limit: 50})
	small := "small"
	for _, by := range []string{"prop:status", "-prop:points", "prop:nothing"} {
		q := Query{Folder: &small, Sort: by, Limit: 50}
		var ds []time.Duration
		for i := 0; i < 15; i++ {
			start := time.Now()
			if _, err := x.List(q); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(start))
		}
		sortDurations(ds)
		t.Logf("%-52s %8v  worst %8v", "a folder of 100 among 50 000, sort "+by, ds[len(ds)/2], ds[len(ds)-1])
	}
	measure("(for comparison) plain list, sort -updated", Query{Limit: 50})
	measure("(for comparison) plain list, sort title", Query{Sort: "title", Limit: 50})
}

func sortDurations(ds []time.Duration) {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
}

// TestOptionOrderAndBoardInALargeFolder measures what saved views and the
// board ask of the index in a folder of 50 000 pages: a sort by a select
// field in the order of its options (read in groups, one per option), the
// three kinds of column of a board (in, notIn, empty), and the counts of the
// column heads. It runs only when asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestOptionOrderAndBoardInALargeFolder -v ./internal/index/
func TestOptionOrderAndBoardInALargeFolder(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	const pages = 50_000
	x := openIndex(t)
	m := newMem()
	options := []string{"idea", "draft", "review", "done", "dropped", "later"} // "later" nobody has
	for i := 0; i < pages; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "---\nid: %010d\ntitle: 页 %d\n", i, i)
		switch {
		case i%40 == 0: // no status
		case i%97 == 0:
			b.WriteString("status: legacy\n") // not an option
		default:
			fmt.Fprintf(&b, "status: %s\n", options[i*7%5])
		}
		if i%9 != 0 {
			fmt.Fprintf(&b, "points: %d\n", i%1000)
		}
		fmt.Fprintf(&b, "labels: [%s, %s]\n---\n正文 %d\n", options[i%5], options[(i*3+1)%5], i)
		m.files[fmt.Sprintf("tasks/%05d.md", i)] = memFile{[]byte(b.String()), clock + int64(i)}
	}
	for i := 0; i < 100; i++ { // a small folder written last, in the groups of the big one
		m.files[fmt.Sprintf("small/%03d.md", i)] = memFile{[]byte(fmt.Sprintf("---\nid: S%09d\nstatus: %s\ntags: [rare]\n---\nx\n", i, options[i%5])), clock + int64(pages+i)}
	}
	start := time.Now()
	if _, err := x.Sync(m, false); err != nil {
		t.Fatal(err)
	}
	t.Logf("index of %d pages built in %v", pages, time.Since(start))

	folder, small := "tasks", "small"
	measure := func(name string, f func() (int, error)) {
		t.Helper()
		var ds []time.Duration
		n := 0
		for i := 0; i < 15; i++ {
			start := time.Now()
			var err error
			if n, err = f(); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(start))
		}
		sortDurations(ds)
		t.Logf("%-58s median %8v  worst %8v  (%d)", name, ds[len(ds)/2], ds[len(ds)-1], n)
	}
	list := func(q Query) func() (int, error) {
		return func() (int, error) {
			r, err := x.List(q)
			return len(r.Items), err
		}
	}
	for _, sort := range []string{"prop:status", "-prop:status"} {
		q := Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 20, Props: true}
		measure("first 20, sort "+sort+" in option order", list(q))
		// A page deep into the listing: 20 000 pages in, by cursor.
		r, err := x.List(Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 200})
		for i := 0; i < 100 && err == nil && r.Next != ""; i++ {
			r, err = x.List(Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 200, Cursor: r.Next})
		}
		if err != nil {
			t.Fatal(err)
		}
		q.Cursor = r.Next
		measure("page 1000 by cursor (20 000 in), sort "+sort, list(q))
		// Pages through the whole listing to the other values and the missing ones.
		for r.Next != "" && err == nil {
			last := r.Items[len(r.Items)-1]
			if strings.Contains(string(last.Props), "legacy") || !strings.Contains(string(last.Props), "status") {
				break
			}
			r, err = x.List(Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 200, Props: true, Cursor: r.Next})
		}
		if err != nil {
			t.Fatal(err)
		}
		measure("a page among the other values / without one, "+sort, list(Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 20, Cursor: r.Next}))
		measure("a folder of 100 among 50 000, sort "+sort, list(Query{Folder: &small, Sort: sort, PropOptions: options, Limit: 20}))
		// A selective filter: about 25 to 50 of the 50 000 pages match.
		measure("points in range [5,5] (selective), sort "+sort, list(Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 20, Filters: []Filter{{Key: "points", Op: "range", Values: []any{5, 5}}}}))
		measure("labels in [later] (none), sort "+sort, list(Query{Folder: &folder, Sort: sort, PropOptions: options, Limit: 20, Filters: []Filter{{Key: "labels", Op: "in", Values: []any{"later"}}}}))
	}
	measure("(for comparison) sort prop:status by text", list(Query{Folder: &folder, Sort: "prop:status", Limit: 20, Props: true}))
	measure("filter points range, sort prop:status in option order", list(Query{Folder: &folder, Sort: "prop:status", PropOptions: options, Limit: 20,
		Filters: []Filter{{Key: "points", Op: "range", Values: []any{100, 200}}}}))

	// The columns of a board: 20 cards each, in the default order.
	for _, o := range options[:5] {
		measure("board column in ["+o+"]", list(Query{Folder: &folder, Limit: 20, Props: true, Filters: []Filter{{Key: "status", Op: "in", Values: []any{o}}}}))
	}
	measure("board column in [later] (no page has it)", list(Query{Folder: &folder, Limit: 20, Props: true, Filters: []Filter{{Key: "status", Op: "in", Values: []any{"later"}}}}))
	other := make([]any, len(options))
	for i, o := range options {
		other[i] = o
	}
	measure("board column notIn [all options] (1 page in 100)", list(Query{Folder: &folder, Limit: 20, Props: true, Filters: []Filter{{Key: "status", Op: "notIn", Values: other}}}))
	measure("board column empty (1 page in 40)", list(Query{Folder: &folder, Limit: 20, Props: true, Filters: []Filter{{Key: "status", Op: "empty"}}}))
	measure("board column empty, sort -prop:points", list(Query{Folder: &folder, Limit: 20, Props: true, Sort: "-prop:points", Filters: []Filter{{Key: "status", Op: "empty"}}}))
	measure("board column in [review], sort -prop:points", list(Query{Folder: &folder, Limit: 20, Props: true, Sort: "-prop:points", Filters: []Filter{{Key: "status", Op: "in", Values: []any{"review"}}}}))
	measure("board of the small folder, column in [review]", list(Query{Folder: &small, Limit: 20, Props: true, Filters: []Filter{{Key: "status", Op: "in", Values: []any{"review"}}}}))

	groups := func(q GroupQuery) func() (int, error) {
		return func() (int, error) {
			r, err := x.Groups(q)
			return len(r.Groups) + r.Empty, err
		}
	}
	measure("groups of status (count + empty)", groups(GroupQuery{Folder: &folder, Key: "status"}))
	measure("groups of labels (a list)", groups(GroupQuery{Folder: &folder, Key: "labels"}))
	measure("groups of status, filter points range", groups(GroupQuery{Folder: &folder, Key: "status", Filters: []Filter{{Key: "points", Op: "range", Values: []any{100, 200}}}}))
	measure("groups of status in the small folder", groups(GroupQuery{Folder: &small, Key: "status"}))
	// A rare tag (100 pages) in the space of 50 000: its cost follows the tag.
	measure("groups of status of a rare tag (100 pages)", groups(GroupQuery{Tags: []string{"rare"}, Key: "status"}))
}
