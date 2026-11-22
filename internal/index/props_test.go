package index

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

func rowsOf(t *testing.T, header string) ([]propRow, string) {
	t.Helper()
	doc := page.Parse([]byte("---\nid: AAAAAAAAAA\n" + header + "---\nbody\n"))
	rows, col, _ := propsOf(doc.Meta())
	return rows, col
}

func partialOf(t *testing.T, header string) []string {
	t.Helper()
	doc := page.Parse([]byte("---\nid: AAAAAAAAAA\n" + header + "---\nbody\n"))
	_, _, partial := propsOf(doc.Meta())
	return partial
}

// What the index holds of a key that is not the whole of it is said, so that a
// table does not write the shortened copy back over the file's value.
func TestPropsThatTheIndexOnlyPartlyHoldAreListed(t *testing.T) {
	long := strings.Repeat("字", MaxPropText+1)
	exact := strings.Repeat("字", MaxPropText)
	var many strings.Builder
	for i := 0; i <= maxPropElements; i++ {
		fmt.Fprintf(&many, "%d, ", i)
	}
	var keys strings.Builder
	for i := 0; i < maxPropKeys+2; i++ {
		fmt.Fprintf(&keys, "k%03d: 1\n", i)
	}
	for _, c := range []struct {
		name, header string
		want         []string
	}{
		{"nothing is lost", "status: draft\npoints: 3\nlist: [a, 1, true]\ndone: false\n", nil},
		{"text of exactly the limit", "t: " + exact + "\n", nil},
		{"cut text", "t: " + long + "\nok: fine\n", []string{"t"}},
		{"cut text in a list", "l: [a, " + long + "]\n", []string{"l"}},
		{"a list that was cut", "l: [" + many.String() + "]\n", []string{"l"}},
		{"a list that lost an object", "l: [a, {x: 1}, b]\n", []string{"l"}},
		{"a list that lost a null", "l: [a, null, b]\n", []string{"l"}},
		{"a list of nothing that is a value", "l: [{x: 1}]\nm: [[1], [2]]\n", []string{"l", "m"}},
		{"an object", "o: {a: 1}\nfine: 2\nsecond: {b: [1]}\n", []string{"o", "second"}},
		{"not a number", "n: .nan\ni: .inf\n", []string{"n", "i"}},
		{"no value to lose", "a:\nb: null\nc: \"  \"\nd: []\n", nil},
		{"keys past the limit", keys.String(), []string{"k200", "k201"}},
	} {
		if got := partialOf(t, c.header); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPropsExtraction(t *testing.T) {
	rows, col := rowsOf(t, `title: 题
status: draft
points: 3
ratio: 2.5
done: true
due: 2026-11-22
labels: [b, a, 7]
nothing:
blank: "  "
empty: []
nested: {a: 1}
mixed: [x, {a: 1}, [1, 2], null, y]
`)
	want := []propRow{
		{"status", 0, "draft", "draft"},
		{"points", 0, "3", numberKey(3)},
		{"ratio", 0, "2.5", numberKey(2.5)},
		{"done", 0, "true", "1"},
		{"due", 0, "2026-11-22", "2026-11-22"},
		{"labels", 0, "b", "b"},
		{"labels", 1, "a", "a"},
		{"labels", 2, "7", numberKey(7)},
		{"mixed", 0, "x", "x"},
		{"mixed", 1, "y", "y"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows:\n%v\nwant\n%v", rows, want)
	}
	// Built-in fields are not properties; nothing, blanks, empty lists and
	// objects are not there; the order of the header is kept.
	const wantCol = `{"status":"draft","points":3,"ratio":2.5,"done":true,"due":"2026-11-22","labels":["b","a",7],"mixed":["x","y"]}`
	if col != wantCol {
		t.Errorf("column:\n%s\nwant\n%s", col, wantCol)
	}
	if rows, col := rowsOf(t, "title: 只有标题\ntags: [a]\ncreated: 2026-01-01\ntype: t\n"); rows != nil || col != "" {
		t.Errorf("a header of built-in fields only has properties: %v %q", rows, col)
	}
}

func TestPropsLimits(t *testing.T) {
	long := strings.Repeat("长", MaxPropText+50)
	rows, col := rowsOf(t, "text: "+long+"\n")
	if n := len([]rune(rows[0].val)); n != MaxPropText {
		t.Errorf("a long text is cut to %d characters, it has %d", MaxPropText, n)
	}
	if !strings.Contains(col, strings.Repeat("长", MaxPropText)+`"`) || strings.Contains(col, strings.Repeat("长", MaxPropText+1)) {
		t.Errorf("the column holds the cut text: %.60s", col)
	}
	var b strings.Builder
	b.WriteString("many: [")
	for i := 0; i < maxPropElements+50; i++ {
		fmt.Fprintf(&b, "v%d, ", i)
	}
	b.WriteString("]\n")
	if rows, _ := rowsOf(t, b.String()); len(rows) != maxPropElements {
		t.Errorf("a list is indexed up to %d elements, got %d", maxPropElements, len(rows))
	}
}

func TestNumberKeysSortLikeNumbers(t *testing.T) {
	nums := []float64{-1e9, -100.5, -2, -0.001, 0, 0.001, 1, 2, 2.5, 10, 100, 1e9}
	for i := 1; i < len(nums); i++ {
		if !(numberKey(nums[i-1]) < numberKey(nums[i])) {
			t.Errorf("%v does not sort before %v: %s %s", nums[i-1], nums[i], numberKey(nums[i-1]), numberKey(nums[i]))
		}
	}
	if numberKey(0) != numberKey(-0.0*1) {
		t.Error("-0 and 0 must be one key")
	}
}

// clockTime is a modification time later than the last one handed out.
func clockTime(string) time.Time {
	clock += int64(time.Second)
	return time.Unix(0, clock)
}

// putProp indexes a page in folder f with the header lines given.
func putProp(t *testing.T, x *Index, folder, id, header string) {
	t.Helper()
	rel := pid(id) + ".md"
	if folder != "" {
		rel = folder + "/" + rel
	}
	putAt(t, x, rel, "id: "+pid(id)+"\n"+header, "x\n", clockTime(id))
}

// pid is a page id made of a short name: the name, then zeros.
func pid(name string) string { return name + strings.Repeat("0", 10-len(name)) }

// shortIDs is ids with the zeros of pid taken off again.
func shortIDs(r Result) []string {
	out := ids(r)
	for i, id := range out {
		out[i] = strings.TrimRight(id, "0")
	}
	return out
}

func listIDs(t *testing.T, x *Index, q Query) []string {
	t.Helper()
	var all []string
	for i := 0; ; i++ {
		res, err := x.List(q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		all = append(all, shortIDs(res)...)
		if res.Next == "" {
			return all
		}
		if i > 100 {
			t.Fatal("the cursor never ends")
		}
		q.Cursor = res.Next
	}
}

func TestSortByPropertyPutsPagesWithoutItLast(t *testing.T) {
	x := openIndex(t)
	// rowid order is the order of the puts: A1 A2 A3 A4 A5 A6 A7
	putProp(t, x, "f", "A1", "status: review\npoints: 10\n")
	putProp(t, x, "f", "A2", "title: no status\n")
	putProp(t, x, "f", "A3", "status: draft\npoints: 9\n")
	putProp(t, x, "f", "A4", "status: Review\npoints: -3\n")
	putProp(t, x, "f", "A5", "status: \"\"\n")
	putProp(t, x, "f", "A6", "status: draft\npoints: 100\nlabels: [z, a]\n")
	putProp(t, x, "g", "A7", "status: aaa\n")
	f := "f"
	for _, c := range []struct {
		sort string
		want string
	}{
		// Text sorts as the title does (lower case); ties by rowid. Pages
		// without a value, or with a blank one, come last, by rowid.
		{"prop:status", "A3 A6 A1 A4 A2 A5"},
		{"-prop:status", "A4 A1 A6 A3 A2 A5"},
		{"prop:points", "A4 A3 A1 A6 A2 A5"},
		{"-prop:points", "A6 A1 A3 A4 A2 A5"},
		// A list sorts by its first value.
		{"prop:labels", "A6 A1 A2 A3 A4 A5"},
		{"prop:nothing", "A1 A2 A3 A4 A5 A6"},
		{"-prop:nothing", "A1 A2 A3 A4 A5 A6"},
	} {
		for _, limit := range []int{200, 1, 2, 4} {
			got := strings.Join(listIDs(t, x, Query{Folder: &f, Sort: c.sort, Limit: limit}), " ")
			if got != c.want {
				t.Errorf("sort %s, limit %d: %s, want %s", c.sort, limit, got, c.want)
			}
		}
	}
}

func TestCursorOfAPropertySortIsStableWhilePagesChange(t *testing.T) {
	x := openIndex(t)
	for i := 1; i <= 6; i++ {
		putProp(t, x, "", fmt.Sprintf("B%d", i), fmt.Sprintf("n: %d\n", i))
	}
	putProp(t, x, "", "B7", "title: none\n")
	res, err := x.List(Query{Sort: "prop:n", Limit: 3})
	if err != nil || strings.Join(shortIDs(res), " ") != "B1 B2 B3" || res.Next == "" {
		t.Fatalf("%v %v", shortIDs(res), err)
	}
	// Between the pages: one is deleted, one changes value to sort earlier
	// than the cursor, one is added after it.
	if err := x.Remove(pid("B2")); err != nil {
		t.Fatal(err)
	}
	putProp(t, x, "", "B4", "n: -5\n")
	putProp(t, x, "", "B8", "n: 5.5\n")
	res, err = x.List(Query{Sort: "prop:n", Limit: 10, Cursor: res.Next})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(shortIDs(res), " "); got != "B5 B8 B6 B7" {
		t.Errorf("after the cursor: %s", got)
	}
	// A cursor belongs to its sort.
	first, _ := x.List(Query{Sort: "prop:n", Limit: 2})
	if _, err := x.List(Query{Sort: "prop:m", Limit: 2, Cursor: first.Next}); err == nil || !strings.Contains(err.Error(), "another sort") {
		t.Errorf("a cursor of another sort must be refused: %v", err)
	}
	if _, err := x.List(Query{Sort: "prop:", Limit: 2}); err == nil {
		t.Error("prop: without a key must be refused")
	}
}

func TestPropertyFilters(t *testing.T) {
	x := openIndex(t)
	putProp(t, x, "f", "C1", "status: draft\npoints: 5\ndue: 2026-03-01\nlabels: [a, b]\nname: Gardenia 栀子\n")
	putProp(t, x, "f", "C2", "status: done\npoints: 12\ndue: 2026-05-10\nlabels: [b]\nname: rose\n")
	putProp(t, x, "f", "C3", "status: review\npoints: -1.5\nlabels: [c]\nname: ROSE garden\n")
	putProp(t, x, "f", "C4", "title: bare\n")
	putProp(t, x, "f", "C5", "status: draft\ndone: false\npoints: 5.0\n")
	putProp(t, x, "g", "C6", "status: draft\ndone: true\n")
	list := func(fs ...Filter) string {
		t.Helper()
		res, err := x.List(Query{Filters: fs, Sort: "prop:nothing", Limit: 200})
		if err != nil {
			t.Fatalf("%+v: %v", fs, err)
		}
		return strings.Join(shortIDs(res), " ")
	}
	f := func(key, op string, v ...any) Filter { return Filter{Key: key, Op: op, Values: v} }
	for _, c := range []struct {
		name string
		fs   []Filter
		want string
	}{
		{"in, text", []Filter{f("status", "in", "draft", "done")}, "C1 C2 C5 C6"},
		{"in, a list holds one of them", []Filter{f("labels", "in", "a", "c")}, "C1 C3"},
		{"in, a number as number or text", []Filter{f("points", "in", json.Number("5"))}, "C1 C5"},
		{"in, a number as 5.0", []Filter{f("points", "in", 5.0)}, "C1 C5"},
		{"in, a bool", []Filter{f("done", "in", true)}, "C6"},
		{"range, numbers", []Filter{f("points", "range", 0, 6)}, "C1 C5"},
		{"range, negative to open", []Filter{f("points", "range", -2, nil)}, "C1 C2 C3 C5"},
		{"range, open to number", []Filter{f("points", "range", "", 5)}, "C1 C3 C5"},
		{"range, dates", []Filter{f("due", "range", "2026-03-01", "2026-04-30")}, "C1"},
		{"range, dates, from only", []Filter{f("due", "range", "2026-04-01", nil)}, "C2"},
		{"empty", []Filter{f("due", "empty")}, "C3 C4 C5 C6"},
		{"notEmpty", []Filter{f("due", "notEmpty")}, "C1 C2"},
		{"notEmpty, false is a value", []Filter{f("done", "notEmpty")}, "C5 C6"},
		{"contains, ignores case in ASCII", []Filter{f("name", "contains", "ROSE")}, "C2 C3"},
		{"contains, other letters as written", []Filter{f("name", "contains", "栀子")}, "C1"},
		{"contains looks only at its own key", []Filter{f("status", "contains", "rose")}, ""},
		{"and", []Filter{f("status", "in", "draft"), f("points", "range", 1, nil), f("due", "empty")}, "C5"},
		{"empty on a key nobody has", []Filter{f("zzz", "empty")}, "C1 C2 C3 C4 C5 C6"},
	} {
		if got := list(c.fs...); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// With a folder, a tag-less query and a sort by another property.
	fo := "f"
	res, err := x.List(Query{Folder: &fo, Filters: []Filter{f("status", "in", "draft")}, Sort: "-prop:points", Limit: 10})
	if err != nil || strings.Join(shortIDs(res), " ") != "C5 C1" {
		t.Errorf("folder, filter and sort: %v %v", shortIDs(res), err)
	}
	// A filter stays in force across the pages of the listing, in both groups.
	got := listIDs(t, x, Query{Filters: []Filter{f("due", "empty")}, Sort: "prop:status", Limit: 1})
	if strings.Join(got, " ") != "C5 C6 C3 C4" {
		t.Errorf("paged filter: %v", got)
	}
}

func TestBadPropertyFilters(t *testing.T) {
	x := openIndex(t)
	f := func(key, op string, v ...any) Filter { return Filter{Key: key, Op: op, Values: v} }
	for name, fs := range map[string][]Filter{
		"no key":          {f("", "empty")},
		"unknown op":      {f("a", "like", "x")},
		"in, no values":   {f("a", "in")},
		"in, an object":   {f("a", "in", map[string]any{})},
		"range, one":      {f("a", "range", 1)},
		"range, none":     {f("a", "range", nil, nil)},
		"range, mixed":    {f("a", "range", 1, "x")},
		"range, a bool":   {f("a", "range", true, 1)},
		"empty, a value":  {f("a", "empty", "x")},
		"contains, two":   {f("a", "contains", "x", "y")},
		"contains, blank": {f("a", "contains", "")},
		"contains, a num": {f("a", "contains", 3)},
	} {
		if _, err := x.List(Query{Filters: fs}); err == nil || !strings.Contains(err.Error(), "invalid query") {
			t.Errorf("%s: %v", name, err)
		}
	}
	many := make([]Filter, maxFilters+1)
	for i := range many {
		many[i] = f("a", "empty")
	}
	if _, err := x.List(Query{Filters: many}); err == nil {
		t.Error("too many filters")
	}
}

func TestListWithPropsAsksForThemAndTheRev(t *testing.T) {
	x := openIndex(t)
	putProp(t, x, "", "D1", "status: draft\nlabels: [a, b]\n")
	putProp(t, x, "", "D2", "title: bare\n")
	plain, err := x.List(Query{Sort: "prop:status"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range plain.Items {
		if it.Props != nil || it.Rev != "" {
			t.Errorf("a listing without props=1 carries neither: %+v", it)
		}
	}
	for _, sort := range []string{"", "prop:status", "title"} {
		res, err := x.List(Query{Sort: sort, Props: true})
		if err != nil || len(res.Items) != 2 {
			t.Fatal(sort, err)
		}
		for _, it := range res.Items {
			want := `{}`
			if it.ID == pid("D1") {
				want = `{"status":"draft","labels":["a","b"]}`
			}
			if string(it.Props) != want || len(it.Rev) == 0 {
				t.Errorf("sort %q: %s props %s rev %q, want %s", sort, it.ID, it.Props, it.Rev, want)
			}
		}
	}
}

func TestPropsFollowThePage(t *testing.T) {
	x := openIndex(t)
	putProp(t, x, "", "E1", "status: draft\n")
	putProp(t, x, "", "E1", "points: 4\n") // the same page, rewritten
	res, _ := x.List(Query{Filters: []Filter{{Key: "status", Op: "notEmpty"}}})
	if len(res.Items) != 0 {
		t.Error("the old properties of a rewritten page are gone")
	}
	res, _ = x.List(Query{Filters: []Filter{{Key: "points", Op: "in", Values: []any{4}}}})
	if len(res.Items) != 1 {
		t.Error("the new ones are there")
	}
	if err := x.Remove(pid("E1")); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := x.db.QueryRow(`SELECT count(*) FROM props`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a deleted page leaves no property rows: %d %v", n, err)
	}
	// A full sync empties the table before it fills it.
	m := newMem()
	m.put("a.md", pageFile("FFFFFFFFFF", "t", "", nil, "", "x\n"))
	m.files["a.md"] = memFile{[]byte("---\nid: FFFFFFFFFF\nk: v\n---\nx\n"), m.files["a.md"].mtime}
	syncAll(t, x, m, true)
	syncAll(t, x, m, true)
	if err := x.db.QueryRow(`SELECT count(*) FROM props`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows after two full syncs: %d %v", n, err)
	}
}

func TestListSaysWhichPropsAreOnlyPartlyThere(t *testing.T) {
	x := openIndex(t)
	putProp(t, x, "", "F1", "status: draft\nnote: "+strings.Repeat("a", MaxPropText+5)+"\nmeta: {a: 1}\nl: [x, null]\n")
	putProp(t, x, "", "F2", "status: done\n")
	for _, sort := range []string{"", "prop:status"} {
		res, err := x.List(Query{Sort: sort, Props: true})
		if err != nil || len(res.Items) != 2 {
			t.Fatal(sort, err)
		}
		for _, it := range res.Items {
			want := []string(nil)
			if it.ID == pid("F1") {
				want = []string{"note", "meta", "l"}
			}
			if !reflect.DeepEqual(it.PartialProps, want) {
				t.Errorf("sort %q: %s partial %v, want %v", sort, it.ID, it.PartialProps, want)
			}
			b, _ := json.Marshal(it)
			if want == nil && strings.Contains(string(b), "partialProps") {
				t.Errorf("an empty partialProps is left out: %s", b)
			}
		}
	}
	plain, _ := x.List(Query{})
	for _, it := range plain.Items {
		if it.PartialProps != nil {
			t.Errorf("a listing without props=1 has none: %+v", it)
		}
	}
	// It follows the page: a rewrite that makes the value whole clears it.
	putProp(t, x, "", "F1", "status: draft\nnote: short\n")
	res, _ := x.List(Query{Props: true, Sort: "prop:status"})
	for _, it := range res.Items {
		if it.PartialProps != nil {
			t.Errorf("%s: %v", it.ID, it.PartialProps)
		}
	}
}
