package index

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func putAt(t *testing.T, x *Index, rel, header, body string, mtime time.Time) {
	t.Helper()
	if _, err := x.Put(rel, []byte("---\n"+header+"---\n"+body), mtime.UnixNano()); err != nil {
		t.Fatal(err)
	}
}

func titles(t *testing.T, x *Index, q Query) string {
	t.Helper()
	res, err := x.List(q)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range res.Items {
		out = append(out, it.Label)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestListFilters(t *testing.T) {
	x := openIndex(t)
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	putAt(t, x, "a/one.md", "id: AAAAAAAAA1\ntitle: 玉兰\ncreated: 2025-12-03\ntags: [花]\n", "栀子 在正文里\n", day("2026-11-01T00:00"))
	putAt(t, x, "a/two.md", "id: AAAAAAAAA2\ntitle: 栀子\ncreated: 2026-01-10\ntags: [花]\n", "玉兰 在正文里\n", day("2026-11-01T23:59"))
	putAt(t, x, "b/three.md", "id: AAAAAAAAA3\ntitle: 黄昏\n", "栀子 没有创建日期\n", day("2026-11-02T00:00"))
	putAt(t, x, "b/four.md", "id: AAAAAAAAA4\ntitle: 栀子 二\ncreated: 2026-02-01\n", "x\n", day("2026-10-31T12:00"))

	ns := func(s string) int64 { return day(s).UnixNano() }
	for _, c := range []struct {
		name string
		q    Query
		want string
	}{
		{"text anywhere", Query{Text: "栀子"}, "栀子,栀子 二,玉兰,黄昏"},
		{"text in titles", Query{Text: "栀子", TitleOnly: true}, "栀子,栀子 二"},
		{"titles, sorted", Query{Text: "栀子", TitleOnly: true, Sort: "title"}, "栀子,栀子 二"},
		{"updated from", Query{UpdatedFrom: ns("2026-11-01T00:00")}, "栀子,玉兰,黄昏"},
		{"updated to (end of the day)", Query{UpdatedTo: ns("2026-11-02T00:00")}, "栀子,栀子 二,玉兰"},
		{"updated on one day", Query{UpdatedFrom: ns("2026-11-01T00:00"), UpdatedTo: ns("2026-11-02T00:00")}, "栀子,玉兰"},
		{"created from", Query{CreatedFrom: "2026-01-10"}, "栀子,栀子 二"},
		{"created to", Query{CreatedTo: "2026-01-10"}, "栀子,玉兰"},
		{"created range, none without a date", Query{CreatedFrom: "2000-01-01", CreatedTo: "2100-01-01"}, "栀子,栀子 二,玉兰"},
		{"with a tag and text", Query{Tags: []string{"花"}, Text: "栀子", TitleOnly: true}, "栀子"},
		{"with a folder", Query{Folder: strp("b"), CreatedFrom: "2026-01-01"}, "栀子 二"},
		{"all together", Query{Folder: strp("a"), Tags: []string{"花"}, Text: "栀子", CreatedTo: "2025-12-31", UpdatedFrom: ns("2026-11-01T00:00")}, "玉兰"},
	} {
		if got := titles(t, x, c.q); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func strp(s string) *string { return &s }

// A filter stays in force across pages of a listing.
func TestListFiltersWithACursor(t *testing.T) {
	x := openIndex(t)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 9; i++ {
		created := "2026-01-0" + fmt.Sprint(i+1)
		putAt(t, x, fmt.Sprintf("p%d.md", i), fmt.Sprintf("id: BBBBBBBBB%d\ntitle: 页 %d\ncreated: %s\n", i, i, created), "x\n", base.Add(time.Duration(i)*time.Hour))
	}
	q := Query{CreatedFrom: "2026-01-03", CreatedTo: "2026-01-07", UpdatedTo: base.Add(6 * time.Hour).UnixNano(), Limit: 2, Sort: "created"}
	var got []string
	for {
		res, err := x.List(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			got = append(got, it.Created)
		}
		if res.Next == "" {
			break
		}
		q.Cursor = res.Next
	}
	if strings.Join(got, ",") != "2026-01-03,2026-01-04,2026-01-05,2026-01-06" {
		t.Fatalf("%v", got)
	}
	// Relevance order pages by offset.
	q = Query{Text: "页", TitleOnly: true, CreatedFrom: "2026-01-08", Limit: 1}
	n := 0
	for {
		res, err := x.List(q)
		if err != nil {
			t.Fatal(err)
		}
		n += len(res.Items)
		if res.Next == "" {
			break
		}
		q.Cursor = res.Next
	}
	if n != 2 {
		t.Fatalf("%d", n)
	}
}
