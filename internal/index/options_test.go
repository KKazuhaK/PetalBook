package index

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// putProps adds a page to the folder with the given header lines.
func putProps(t *testing.T, x *Index, folder, title string, header string) {
	t.Helper()
	putAt(t, x, folder+"/"+title+".md", "id: "+propID(title)+"\ntitle: "+title+"\n"+header, "正文 "+title+"\n", time.Now())
}

// propPage is a page for loadProps: what putProps takes.
type propPage struct{ folder, title, header string }

// loadProps puts many pages in an empty index in one read of a source, which
// is much quicker than a write of each (putProps) where a test needs hundreds.
func loadProps(t *testing.T, x *Index, pages []propPage) {
	t.Helper()
	m := newMem()
	for _, p := range pages {
		m.put(p.folder+"/"+p.title+".md", "---\nid: "+propID(p.title)+"\ntitle: "+p.title+"\n"+p.header+"---\n正文 "+p.title+"\n")
	}
	syncAll(t, x, m, false)
}

func propID(title string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' {
			return '0'
		}
		return r
	}, fmt.Sprintf("%010s", title))
}

// listAll pages through a query with the given limit and returns the labels
// in order, and fails when a page is returned twice.
func listAll(t *testing.T, x *Index, q Query, limit int) []string {
	t.Helper()
	q.Limit = limit
	var out []string
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		res, err := x.List(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			if seen[it.ID] {
				t.Fatalf("%s is listed twice: %v", it.Label, out)
			}
			seen[it.ID] = true
			out = append(out, it.Label)
		}
		if res.Next == "" {
			return out
		}
		q.Cursor = res.Next
	}
	t.Fatal("the listing does not end")
	return nil
}

func optionsFixture(t *testing.T) *Index {
	x := openIndex(t)
	for _, p := range []struct{ title, header string }{
		{"A", "status: done\n"},
		{"B", "status: todo\n"},
		{"C", "status: doing\n"},
		{"D", "status: zzz\n"},
		{"E", "status: aaa\n"},
		{"F", "points: 1\n"},
		{"G", "status: todo\n"},
		{"H", "status: \"  \"\n"},
	} {
		putProps(t, x, "t", p.title, p.header)
	}
	// Another folder, with pages in every group, that must not show up.
	putProps(t, x, "other", "X", "status: todo\n")
	putProps(t, x, "other", "Y", "points: 1\n")
	return x
}

var statusOptions = []string{"todo", "doing", "done"}

func TestSortByOptionsFollowsTheOrderOfTheOptions(t *testing.T) {
	x := optionsFixture(t)
	for _, c := range []struct {
		sort string
		want string
	}{
		// The options in turn (pages of one option by rowid), the other values
		// by text, the pages without a value last.
		{"prop:status", "B G C A E D F H"},
		// Reversed: the other values first, the options from the last, and the
		// pages without a value last still.
		{"-prop:status", "D E A C G B F H"},
	} {
		want := strings.Fields(c.want)
		for _, limit := range []int{1, 2, 3, 5, 8, 20} {
			q := Query{Folder: strp("t"), Sort: c.sort, PropOptions: statusOptions}
			if got := listAll(t, x, q, limit); !reflect.DeepEqual(got, want) {
				t.Errorf("%s, %d at a time: %v, want %v", c.sort, limit, got, want)
			}
		}
	}
	// Without the options it is the order of the text, as before.
	if got := listAll(t, x, Query{Folder: strp("t"), Sort: "prop:status"}, 3); !reflect.DeepEqual(got, strings.Fields("E C A B G D F H")) {
		t.Errorf("by text: %v", got)
	}
}

func TestSortByOptionsOfAListUsesTheFirstValue(t *testing.T) {
	x := openIndex(t)
	putProps(t, x, "t", "P", "labels: [b, a]\n")
	putProps(t, x, "t", "Q", "labels: [a]\n")
	putProps(t, x, "t", "R", "labels: [c, a]\n")
	putProps(t, x, "t", "S", "labels: []\n")
	got := listAll(t, x, Query{Folder: strp("t"), Sort: "prop:labels", PropOptions: []string{"a", "b"}}, 2)
	if want := strings.Fields("Q P R S"); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	got = listAll(t, x, Query{Folder: strp("t"), Sort: "-prop:labels", PropOptions: []string{"a", "b"}}, 2)
	if want := strings.Fields("R P Q S"); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestSortByOptionsWorksWithFiltersAndRecursion(t *testing.T) {
	x := optionsFixture(t)
	putProps(t, x, "t/sub", "S", "status: doing\npoints: 5\n")
	q := Query{Folder: strp("t"), Recursive: true, Sort: "prop:status", PropOptions: statusOptions,
		Filters: []Filter{{Key: "status", Op: "notEmpty"}}}
	if got, want := listAll(t, x, q, 2), strings.Fields("B G C S A E D"); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	q.Filters = []Filter{{Key: "status", Op: "in", Values: []any{"done", "todo", "zzz"}}}
	if got, want := listAll(t, x, q, 1), strings.Fields("B G A D"); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	// A key with no pages in a group at all: every group is empty.
	q = Query{Folder: strp("t"), Sort: "prop:nothing", PropOptions: []string{"x", "y"}}
	if got := listAll(t, x, q, 3); len(got) != 8 {
		t.Errorf("%v", got)
	}
}

func TestACursorOfAnOptionOrderIsStableWhilePagesChange(t *testing.T) {
	x := optionsFixture(t)
	q := Query{Folder: strp("t"), Sort: "prop:status", PropOptions: statusOptions, Limit: 2}
	res, err := x.List(q)
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("%v %v", res, err)
	}
	var got []string
	for _, it := range res.Items {
		got = append(got, it.Label)
	}
	// Between two pages: a page is added to a group already read, one to a
	// group still to come, and one that has been read is removed.
	putProps(t, x, "t", "N1", "status: todo\n")
	putProps(t, x, "t", "N2", "status: done\n")
	if err := x.Remove(res.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	q.Cursor = res.Next
	for res.Next != "" {
		if res, err = x.List(q); err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			got = append(got, it.Label)
		}
		q.Cursor = res.Next
	}
	// Nothing is listed twice, nothing still to come is missed, and the page
	// added to a group already passed is not there (it comes before the cursor).
	want := strings.Fields("B G C A N2 E D F H")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestACursorOfAnotherOrderIsRefused(t *testing.T) {
	x := optionsFixture(t)
	opt := Query{Folder: strp("t"), Sort: "prop:status", PropOptions: statusOptions, Limit: 2}
	plain := Query{Folder: strp("t"), Sort: "prop:status", Limit: 2}
	a, err := x.List(opt)
	if err != nil || a.Next == "" {
		t.Fatal(err)
	}
	b, err := x.List(plain)
	if err != nil || b.Next == "" {
		t.Fatal(err)
	}
	for name, q := range map[string]Query{
		"a cursor of the options used for the text order": {Folder: strp("t"), Sort: "prop:status", Limit: 2, Cursor: a.Next},
		"a cursor of the text order used for the options": {Folder: strp("t"), Sort: "prop:status", PropOptions: statusOptions, Limit: 2, Cursor: b.Next},
		"a cursor of other options":                       {Folder: strp("t"), Sort: "prop:status", PropOptions: []string{"todo", "done"}, Limit: 2, Cursor: a.Next},
		"a cursor of another sort":                        {Folder: strp("t"), Sort: "-prop:status", PropOptions: statusOptions, Limit: 2, Cursor: a.Next},
	} {
		if _, err := x.List(q); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A cursor made up, with a group that is not there.
	bad := cursor{Sort: "prop:status", N: len(statusOptions) + 1, G: 99}.encode()
	if _, err := x.List(Query{Folder: strp("t"), Sort: "prop:status", PropOptions: statusOptions, Cursor: bad}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("%v", err)
	}
}

func TestNotInKeepsPagesWithAValueOutsideTheList(t *testing.T) {
	x := optionsFixture(t)
	putProps(t, x, "t", "P", "labels: [b, a]\n")
	putProps(t, x, "t", "Q", "labels: [a]\n")
	putProps(t, x, "t", "R", "labels: [b]\n")
	for _, c := range []struct {
		name   string
		filter Filter
		want   string
	}{
		{"the other values of a board", Filter{Key: "status", Op: "notIn", Values: []any{"todo", "doing", "done"}}, "D E"},
		{"one value", Filter{Key: "status", Op: "notIn", Values: []any{"todo"}}, "A C D E"},
		{"a page without the key is not in the list and has no value", Filter{Key: "points", Op: "notIn", Values: []any{5}}, "F"},
		{"a list that holds one of them is out", Filter{Key: "labels", Op: "notIn", Values: []any{"a"}}, "R"},
		{"a list that holds none", Filter{Key: "labels", Op: "notIn", Values: []any{"c"}}, "P Q R"},
		{"numbers and booleans", Filter{Key: "points", Op: "notIn", Values: []any{1, true}}, ""},
	} {
		if got := titles(t, x, Query{Folder: strp("t"), Filters: []Filter{c.filter}}); got != strings.ReplaceAll(c.want, " ", ",") {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// With the other two filters a board makes: the three columns partition the pages.
	cols := [][]Filter{
		{{Key: "status", Op: "in", Values: []any{"todo"}}},
		{{Key: "status", Op: "in", Values: []any{"doing"}}},
		{{Key: "status", Op: "in", Values: []any{"done"}}},
		{{Key: "status", Op: "notIn", Values: []any{"todo", "doing", "done"}}},
		{{Key: "status", Op: "empty"}},
	}
	total := 0
	for _, f := range cols {
		res, err := x.List(Query{Folder: strp("t"), Filters: f, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		total += len(res.Items)
	}
	if n, _ := x.List(Query{Folder: strp("t"), Limit: 200}); total != len(n.Items) {
		t.Errorf("the columns hold %d pages of %d", total, len(n.Items))
	}
	for _, bad := range [][]any{nil, {}, {nil}, {[]any{"x"}}} {
		_, err := x.List(Query{Filters: []Filter{{Key: "status", Op: "notIn", Values: bad}}})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("notIn %v: %v", bad, err)
		}
	}
}

func TestGroupsCountsValuesAndTheEmpty(t *testing.T) {
	x := optionsFixture(t)
	putProps(t, x, "t", "P", "labels: [b, a]\nstatus: todo\n")
	putProps(t, x, "t", "Q", "labels: [a, a]\nstatus: done\n")
	res, err := x.Groups(GroupQuery{Folder: strp("t"), Key: "status"})
	if err != nil {
		t.Fatal(err)
	}
	// Most pages first, then by value.
	want := []Group{{"todo", 3}, {"done", 2}, {"aaa", 1}, {"doing", 1}, {"zzz", 1}}
	if !reflect.DeepEqual(res.Groups, want) || res.Empty != 2 || res.Truncated {
		t.Errorf("%+v, want %v and 2 empty", res, want)
	}
	// A list counts a page once for each distinct value.
	res, _ = x.Groups(GroupQuery{Folder: strp("t"), Key: "labels"})
	if !reflect.DeepEqual(res.Groups, []Group{{"a", 2}, {"b", 1}}) || res.Empty != 8 {
		t.Errorf("%+v", res)
	}
	// With filters, recursion and tags the scope is that of a listing.
	res, _ = x.Groups(GroupQuery{Folder: strp("t"), Key: "status", Filters: []Filter{{Key: "labels", Op: "in", Values: []any{"a"}}}})
	if !reflect.DeepEqual(res.Groups, []Group{{"done", 1}, {"todo", 1}}) || res.Empty != 0 {
		t.Errorf("%+v", res)
	}
	putProps(t, x, "t/sub", "S", "status: todo\n")
	if res, _ = x.Groups(GroupQuery{Folder: strp("t"), Key: "status"}); res.Groups[0].Count != 3 {
		t.Errorf("not recursive: %+v", res)
	}
	if res, _ = x.Groups(GroupQuery{Folder: strp("t"), Recursive: true, Key: "status"}); res.Groups[0].Count != 4 {
		t.Errorf("recursive: %+v", res)
	}
	putAt(t, x, "tagged/one.md", "id: AAAAAAAAT1\ntags: [花]\nstatus: todo\n", "x\n", time.Now())
	putAt(t, x, "elsewhere/two.md", "id: AAAAAAAAT2\ntags: [花]\n", "x\n", time.Now())
	if res, _ = x.Groups(GroupQuery{Tags: []string{"花"}, Key: "status"}); !reflect.DeepEqual(res.Groups, []Group{{"todo", 1}}) || res.Empty != 1 {
		t.Errorf("a tag: %+v", res)
	}
	// A key nobody has: everything is empty.
	if res, _ = x.Groups(GroupQuery{Folder: strp("other"), Key: "nothing"}); len(res.Groups) != 0 || res.Empty != 2 {
		t.Errorf("%+v", res)
	}
	if _, err = x.Groups(GroupQuery{Folder: strp("t")}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("no key: %v", err)
	}
	if _, err = x.Groups(GroupQuery{Folder: strp("t"), Key: "status", Filters: []Filter{{Key: "a", Op: "nope"}}}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("a bad filter: %v", err)
	}
}

func TestGroupsAreLimited(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	m := newMem()
	for i := 0; i < MaxGroups+5; i++ {
		m.files[fmt.Sprintf("t/%04d.md", i)] = memFile{[]byte(fmt.Sprintf("---\nid: %010d\nv: v%04d\n---\nx\n", i, i)), clock() + int64(i)}
	}
	if _, err := x.Sync(m, false); err != nil {
		t.Fatal(err)
	}
	res, err := x.Groups(GroupQuery{Folder: strp("t"), Key: "v"})
	if err != nil || len(res.Groups) != MaxGroups || !res.Truncated || res.Empty != 0 {
		t.Errorf("%v %d %v %d", err, len(res.Groups), res.Truncated, res.Empty)
	}
}

func TestValidSortAndCheckFilters(t *testing.T) {
	for _, ok := range []string{"", "updated", "-updated", "created", "title", "-title", "prop:status", "-prop:状态"} {
		if err := ValidSort(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"name", "prop:", "-prop:", "relevance", "--title"} {
		if err := ValidSort(bad); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	fs := []Filter{{Key: "a", Op: "notIn", Values: []any{"x"}}}
	if err := CheckFilters(fs); err != nil {
		t.Error(err)
	}
	if err := CheckFilters([]Filter{{Key: "a", Op: "in"}}); !errors.Is(err, ErrInvalidQuery) {
		t.Error(err)
	}
}

func TestFiltersOfAListingAndOfAViewHaveTheirOwnLimits(t *testing.T) {
	x := optionsFixture(t)
	n := func(k int) []Filter {
		fs := make([]Filter, k)
		for i := range fs {
			fs[i] = Filter{Key: "status", Op: "notEmpty"}
		}
		return fs
	}
	// A board's column adds its condition to the ten a view may hold.
	if _, err := x.List(Query{Folder: strp("t"), Filters: n(MaxViewFilters + 1)}); err != nil {
		t.Errorf("a view's filters and a column's: %v", err)
	}
	if err := CheckFilters(n(MaxViewFilters)); err != nil {
		t.Error(err)
	}
	if err := CheckFilters(n(MaxViewFilters + 1)); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("a view with too many filters: %v", err)
	}
	// An "other values" column excludes every option of a select, up to 500.
	many := make([]any, 500)
	for i := range many {
		many[i] = fmt.Sprintf("option %d", i)
	}
	res, err := x.List(Query{Folder: strp("t"), Limit: 50, Filters: []Filter{{Key: "status", Op: "notIn", Values: many}}})
	if err != nil || len(res.Items) == 0 {
		t.Errorf("notIn of 500 values: %v, %d items", err, len(res.Items))
	}
	if _, err := x.List(Query{Filters: []Filter{{Key: "status", Op: "notIn", Values: append(many, "one more")}}}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("notIn of 501 values: %v", err)
	}
}

func TestGroupsOfATagStartFromTheTagsIndex(t *testing.T) {
	x := optionsFixture(t)
	putProps(t, x, "t", "T1", "status: todo\ntags: [rare]\n")
	putProps(t, x, "other", "T2", "status: done\ntags: [rare, more]\n")
	putProps(t, x, "other", "T3", "tags: [rare]\n")
	for _, q := range []GroupQuery{
		{Tags: []string{"rare"}, Key: "status"},
		{Tags: []string{"rare", "more"}, Key: "status"},
		{Tags: []string{"rare"}, Key: "status", Filters: []Filter{{Key: "status", Op: "notEmpty"}}},
	} {
		for _, plan := range plansOf(t, x, func() {
			if _, err := x.countGroups(q); err != nil {
				t.Fatal(err)
			}
		}) {
			for _, detail := range plan.lines {
				// The pages handed over (j), a subquery's rows and the list of a
				// property's values (d) are what is read from end to end.
				if strings.HasPrefix(detail, "SCAN") && !strings.HasPrefix(detail, "SCAN j") && !strings.HasPrefix(detail, "SCAN (subquery") && detail != "SCAN d" {
					t.Errorf("%v reads a whole table: %s\n%s", q.Tags, detail, plan.query)
				}
				if strings.Contains(detail, "pages_created") {
					t.Errorf("%v scans the pages: %s\n%s", q.Tags, detail, plan.query)
				}
				// The rows of the property are looked up for the pages of the tag;
				// its index ranges are only counted, up to a bound.
				if strings.Contains(detail, "SEARCH props USING") && !strings.Contains(plan.query, "LIMIT ?") {
					t.Errorf("%v reads an index range of the property: %s\n%s", q.Tags, detail, plan.query)
				}
			}
		}
	}
	got, err := x.Groups(GroupQuery{Tags: []string{"rare"}, Key: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Groups, []Group{{"done", 1}, {"todo", 1}}) || got.Empty != 1 {
		t.Errorf("%+v", got)
	}
	both, _ := x.Groups(GroupQuery{Tags: []string{"rare", "more"}, Key: "status"})
	if !reflect.DeepEqual(both.Groups, []Group{{"done", 1}}) || both.Empty != 0 {
		t.Errorf("%+v", both)
	}
}

func TestOptionOrderOfASmallFolderAsksOnlyForTheGroupsItHas(t *testing.T) {
	x := openIndex(t)
	opts := make([]string, 300)
	for i := range opts {
		opts[i] = fmt.Sprintf("o%03d", i)
	}
	putProps(t, x, "mine", "A", "status: o007\n")
	putProps(t, x, "mine", "B", "status: o250\n")
	putProps(t, x, "mine", "C", "status: legacy\n")
	putProps(t, x, "mine", "D", "points: 1\n")
	for i := 0; i < 50; i++ { // other folders hold every option
		putProps(t, x, "big", fmt.Sprintf("P%d", i), "status: "+opts[i*6]+"\n")
	}
	for _, c := range []struct{ sort, want string }{
		{"prop:status", "A,B,C,D"},
		{"-prop:status", "C,B,A,D"},
	} {
		before := x.queries.Load()
		got := strings.Join(listAll(t, x, Query{Folder: strp("mine"), Sort: c.sort, PropOptions: opts}, 20), ",")
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.sort, got, c.want)
		}
		if n := x.queries.Load() - before; n > 6 {
			t.Errorf("%s: %d group queries for a folder of 4 pages with 3 groups", c.sort, n)
		}
	}
}

// What is read from its smallest condition (a small folder, or the few pages a
// selective filter keeps of a big one) comes out in the same order, with the
// same cursors, as what is read by walking the order group by group, and as
// what is read as sets.
func TestSmallListingsFollowTheOrderOfTheGroupedRead(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	opts := []string{"todo", "doing", "done", "review"}
	vals := []string{"todo", "doing", "done", "review", "zeta", "alpha", "beta", ""}
	var pages []propPage
	for i := 0; i < 120; i++ {
		h := fmt.Sprintf("points: %d\n", i%7)
		if v := vals[i*5%len(vals)]; v != "" {
			h += "status: " + v + "\n"
		}
		if i%3 == 0 {
			h += "flag: x\n"
		}
		pages = append(pages, propPage{"t", fmt.Sprintf("P%03d", i), h})
	}
	for i := 0; i < 30; i++ {
		pages = append(pages, propPage{"other", fmt.Sprintf("Q%03d", i), "status: todo\nflag: x\n"})
	}
	loadProps(t, x, pages)
	filters := map[string][]Filter{
		"none":        nil,
		"in":          {{Key: "flag", Op: "in", Values: []any{"x"}}},
		"range":       {{Key: "points", Op: "range", Values: []any{2, 3}}},
		"in and rng":  {{Key: "flag", Op: "in", Values: []any{"x"}}, {Key: "points", Op: "range", Values: []any{nil, 3}}},
		"notEmpty":    {{Key: "status", Op: "notEmpty"}},
		"in on lists": {{Key: "status", Op: "in", Values: []any{"todo", "doing", "zeta"}}},
	}
	for name, fs := range filters {
		for _, sort := range []string{"prop:status", "-prop:status"} {
			for _, limit := range []int{1, 7, 200} {
				if limit == 1 && raceDetector && name != "in" {
					continue // one query a page, each slow under the detector: the 40 pages of "in" are enough
				}
				q := Query{Folder: strp("t"), Sort: sort, PropOptions: opts, Filters: fs}
				setPlan(t, x, 2000, 4000, 1000)
				before := x.queries.Load()
				small := listAll(t, x, q, limit)
				if n := x.queries.Load() - before; limit == 200 && n > 8 {
					t.Errorf("%s %s: %d queries for one page", name, sort, n)
				}
				setPlan(t, x, 0, 0, 1<<30) // the order walked, each page looked up
				walked := listAll(t, x, q, limit)
				setPlan(t, x, 0, 0, 0) // the conditions as sets, picked out of the order
				sets := listAll(t, x, q, limit)
				if !reflect.DeepEqual(small, walked) || !reflect.DeepEqual(small, sets) {
					t.Errorf("%s %s limit %d:\n small  %v\n walked %v\n sets   %v", name, sort, limit, small, walked, sets)
				}
			}
		}
	}
}

// A selective filter in a big folder costs the pages it keeps: a handful of
// queries (the sizes of the conditions, the pages of the smallest, their
// values, the summaries), not one per group.
func TestASelectiveFilterIsReadOnceNotPerGroup(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	opts := make([]string, 40)
	for i := range opts {
		opts[i] = fmt.Sprintf("o%02d", i)
	}
	pages := make([]propPage, 2500)
	for i := range pages {
		h := "status: " + opts[i%40] + "\n"
		if i%250 == 0 {
			h += "flag: x\n"
		}
		pages[i] = propPage{"big", fmt.Sprintf("B%04d", i), h}
	}
	loadProps(t, x, pages)
	for _, f := range []Filter{
		{Key: "flag", Op: "in", Values: []any{"x"}},
		{Key: "flag", Op: "notEmpty"},
	} {
		before := x.queries.Load()
		got := listAll(t, x, Query{Folder: strp("big"), Sort: "prop:status", PropOptions: opts, Filters: []Filter{f}}, 50)
		if len(got) != 10 {
			t.Errorf("%s: %d pages, want 10", f.Op, len(got))
		}
		if n := x.queries.Load() - before; n > 8 {
			t.Errorf("%s: %d queries for 10 pages in 10 groups of 40", f.Op, n)
		}
	}
}

// The counts of a board's column heads are made once per change of the index:
// asked again they come from memory, and after any write they are counted anew.
func TestGroupCountsAreRememberedUntilTheIndexChanges(t *testing.T) {
	x := optionsFixture(t)
	q := GroupQuery{Folder: strp("t"), Key: "status", Filters: []Filter{{Key: "status", Op: "notEmpty"}}}
	first, err := x.Groups(q)
	if err != nil {
		t.Fatal(err)
	}
	before := x.queries.Load()
	for i := 0; i < 5; i++ {
		again, err := x.Groups(q)
		if err != nil || !reflect.DeepEqual(again, first) {
			t.Fatalf("%v %+v", err, again)
		}
		again.Groups[0].Count = 99 // what a caller does with its copy is its own
	}
	if n := x.queries.Load() - before; n != 0 {
		t.Errorf("%d queries for counts that were known", n)
	}
	other := q
	other.Filters = nil
	if _, err := x.Groups(other); err != nil {
		t.Fatal(err)
	}
	if n := x.queries.Load() - before; n == 0 {
		t.Errorf("another question: %d queries", n)
	}
	putProps(t, x, "t", "NEW", "status: done\n")
	got, err := x.Groups(q)
	if err != nil {
		t.Fatal(err)
	}
	total := func(r GroupResult) (n int) {
		for _, g := range r.Groups {
			n += g.Count
		}
		return
	}
	if total(got) != total(first)+1 {
		t.Errorf("after a write: %d, before %d", total(got), total(first))
	}
}

// A folder too large to start from, whose filters say nothing of how few pages
// they keep (contains, empty, ...): the walk of the groups stops at its bound,
// and the matches are found from the index ranges of the conditions, not by a
// read of every group of the options.
func TestOptionOrderWithAFilterNoIndexServesWalksTheFolderOnce(t *testing.T) {
	x := openIndex(t)
	setPlan(t, x, 15, 15, 10)
	opts := make([]string, 40)
	for i := range opts {
		opts[i] = fmt.Sprintf("o%02d", i)
	}
	for i := 0; i < 60; i++ {
		h := fmt.Sprintf("status: %s\nname: n%d\n", opts[i%40], i)
		if i%7 == 0 {
			h = fmt.Sprintf("name: n%d\n", i) // no status
		}
		putProps(t, x, "big", fmt.Sprintf("P%03d", i), h)
	}
	for _, c := range []struct {
		name  string
		f     Filter
		sort  string
		paths string
	}{
		{"nothing", Filter{Key: "name", Op: "contains", Values: []any{"zzz"}}, "prop:status", ""},
		{"few", Filter{Key: "name", Op: "contains", Values: []any{"n5"}}, "prop:status", "n5"},
		{"few, reversed", Filter{Key: "name", Op: "contains", Values: []any{"n5"}}, "-prop:status", "n5"},
		{"empty", Filter{Key: "nothing", Op: "empty"}, "prop:status", ""}, // every page: more than the small scope, read by groups
	} {
		q := Query{Folder: strp("big"), Sort: c.sort, PropOptions: opts, Filters: []Filter{c.f}}
		before := x.queries.Load()
		got := listAll(t, x, q, 100)
		n := x.queries.Load() - before
		switch c.name {
		case "empty":
			if len(got) != 60 {
				t.Errorf("%s: %d pages", c.name, len(got))
			}
		case "nothing":
			if len(got) != 0 || n > 30 {
				t.Errorf("%s: %v in %d queries", c.name, got, n)
			}
		default:
			if len(got) != 11 || n > 30 { // n5, n50..n59: the walk stops at its bound, far short of the 41 groups
				t.Errorf("%s: %d pages in %d queries", c.name, len(got), n)
			}
		}
	}
}
