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
	id := fmt.Sprintf("%010s", title)
	id = strings.Map(func(r rune) rune {
		if r == ' ' {
			return '0'
		}
		return r
	}, id)
	putAt(t, x, folder+"/"+title+".md", "id: "+id+"\ntitle: "+title+"\n"+header, "正文 "+title+"\n", time.Now())
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
	x := openIndex(t)
	m := newMem()
	for i := 0; i < MaxGroups+5; i++ {
		m.files[fmt.Sprintf("t/%04d.md", i)] = memFile{[]byte(fmt.Sprintf("---\nid: %010d\nv: v%04d\n---\nx\n", i, i)), clock + int64(i)}
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
