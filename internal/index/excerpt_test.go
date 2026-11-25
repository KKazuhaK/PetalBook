package index

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// The excerpt of a page is not in the table the index scans: it is read, for
// the pages a response shows, from a table of its own.

func longBody(i int) string { return fmt.Sprintf("页%d ", i) + strings.Repeat("花", 600) }

func seedExcerpts(t *testing.T, n int) (*Index, *memSource) {
	t.Helper()
	m := newMem()
	for i := 0; i < n; i++ {
		m.put(fmt.Sprintf("d/%03d.md", i), pageFile(fmt.Sprintf("EXC%07d", i), fmt.Sprintf("T%d", i), "", nil, "", longBody(i)))
	}
	x := openIndex(t)
	syncAll(t, x, m, false)
	return x, m
}

func TestExcerptsAreNotInThePagesTable(t *testing.T) {
	x, _ := seedExcerpts(t, 5)
	rows, err := x.wdb.Query(`PRAGMA table_info(pages)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "excerpt" {
			t.Fatal("pages still has an excerpt column")
		}
	}
	rows.Close()
	var n int
	if err := x.wdb.QueryRow(`SELECT count(*) FROM excerpts`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("excerpts holds %d rows, err %v", n, err)
	}
}

func TestExcerptsFollowThePage(t *testing.T) {
	x, m := seedExcerpts(t, 3)
	count := func() (n int) {
		if err := x.wdb.QueryRow(`SELECT count(*) FROM excerpts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	// Rewritten: the excerpt is the new one, not a second row.
	m.put("d/001.md", pageFile("EXC0000001", "T1", "", nil, "", "new text"))
	syncAll(t, x, m, false)
	s, ok, err := x.Summary("EXC0000001")
	if err != nil || !ok || s.Excerpt != "new text" || count() != 3 {
		t.Fatalf("after a rewrite: %q ok=%v err=%v rows=%d", s.Excerpt, ok, err, count())
	}
	// Deleted: its row goes.
	delete(m.files, "d/002.md")
	syncAll(t, x, m, false)
	if count() != 2 {
		t.Fatalf("after a delete the table holds %d rows", count())
	}
}

func TestEveryReadOfSummariesCarriesTheExcerpt(t *testing.T) {
	x, _ := seedExcerpts(t, 12)
	check := func(what string, s Summary) {
		t.Helper()
		var i int
		fmt.Sscanf(s.ID, "EXC%07d", &i)
		if s.Excerpt == "" || s.Excerpt != page.Excerpt(longBody(i)) {
			t.Fatalf("%s: excerpt of %s is %q", what, s.ID, s.Excerpt)
		}
	}
	res, err := x.List(Query{Limit: 50})
	if err != nil || len(res.Items) != 12 {
		t.Fatalf("list: %v %d", err, len(res.Items))
	}
	for _, s := range res.Items {
		check("list", s)
	}
	res, err = x.List(Query{Text: "花", Limit: 5})
	if err != nil || len(res.Items) != 5 {
		t.Fatalf("search: %v %d", err, len(res.Items))
	}
	for _, s := range res.Items {
		check("search", s)
	}
	s, ok, _ := x.Summary("EXC0000003")
	if !ok {
		t.Fatal("no summary")
	}
	check("summary", s)
	s, ok, _ = x.SummaryWithProps("EXC0000004")
	if !ok {
		t.Fatal("no summary")
	}
	check("summary with props", s)
	got, err := x.Summaries([]string{"EXC0000005", "EXC0000006", "nope"})
	if err != nil || len(got) != 2 {
		t.Fatalf("summaries: %v %d", err, len(got))
	}
	check("summaries", got["EXC0000006"])
	_, s, err = x.Pick(PickQuery{}, func(int) int { return 7 })
	if err != nil {
		t.Fatal(err)
	}
	check("pick", s)
}

func TestExcerptsAreReadOnlyForTheRowsShown(t *testing.T) {
	x, _ := seedExcerpts(t, 40)
	before := excerptRowsRead.Load()
	res, err := x.List(Query{Text: "花", Limit: 5})
	if err != nil || len(res.Items) != 5 || res.Next == "" {
		t.Fatalf("search: %v %d", err, len(res.Items))
	}
	if n := excerptRowsRead.Load() - before; n != 5 {
		t.Fatalf("a page of 5 read %d excerpts", n)
	}
	before = excerptRowsRead.Load()
	if _, err := x.List(Query{Limit: 7}); err != nil {
		t.Fatal(err)
	}
	if n := excerptRowsRead.Load() - before; n != 7 {
		t.Fatalf("a page of 7 read %d excerpts", n)
	}
	// What a listing reads of pages holds no excerpt.
	if strings.Contains(selectCols(true), "excerpt") {
		t.Fatal("the columns of a summary still name the excerpt")
	}
}

func TestLoadKnownUsesACoveringIndex(t *testing.T) {
	x, _ := seedExcerpts(t, 3)
	rows, err := x.wdb.Query(`EXPLAIN QUERY PLAN ` + knownQuery)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if got := strings.Join(plan, "; "); !strings.Contains(got, "COVERING INDEX") {
		t.Fatalf("loadKnown scans the table: %s", got)
	}
}

func TestTheVersionBeforeExcerptsWereMovedIsRebuilt(t *testing.T) {
	const before = 12
	if schemaVersion <= before {
		t.Fatalf("schemaVersion is %d: an index of version %d would be kept", schemaVersion, before)
	}
}
