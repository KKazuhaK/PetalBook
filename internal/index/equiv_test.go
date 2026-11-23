package index

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// A listing and a count are read in one of several ways, chosen by the sizes
// of what is asked (plan.go). The tests here hold every way to the same
// answer: that of a model that knows nothing of indexes. It keeps the rows of
// the tables in memory, checks every page against every condition, and sorts
// what is left.

// modelPage is a page as the tables hold it.
type modelPage struct {
	rowid      int64
	id         string
	folder     string
	mtime      int64
	created    string
	createdKey int64
	untitled   int
	titleKey   string
	tags       map[string]bool
	props      map[string][]propRow // by key, in the order of pos
}

func loadModel(t *testing.T, x *Index) []*modelPage {
	t.Helper()
	byRow := map[int64]*modelPage{}
	var all []*modelPage
	rows, err := x.db.Query(`SELECT rowid, id, folder, mtime_ns, created, created_key, untitled, title_key FROM pages ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		p := &modelPage{tags: map[string]bool{}, props: map[string][]propRow{}}
		if err := rows.Scan(&p.rowid, &p.id, &p.folder, &p.mtime, &p.created, &p.createdKey, &p.untitled, &p.titleKey); err != nil {
			t.Fatal(err)
		}
		byRow[p.rowid] = p
		all = append(all, p)
	}
	rows.Close()
	if rows, err = x.db.Query(`SELECT page, tag FROM tags`); err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var page int64
		var tag string
		rows.Scan(&page, &tag)
		byRow[page].tags[tag] = true
	}
	rows.Close()
	if rows, err = x.db.Query(`SELECT page, key, pos, val, skey FROM props ORDER BY page, key, pos`); err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var page int64
		var r propRow
		rows.Scan(&page, &r.key, &r.pos, &r.val, &r.skey)
		byRow[page].props[r.key] = append(byRow[page].props[r.key], r)
	}
	rows.Close()
	return all
}

// keeps says whether the page meets the filter, as the doc of Filter has it.
func (p *modelPage) keeps(f Filter) bool {
	rows := p.props[f.Key]
	any := func(ok func(propRow) bool) bool {
		for _, r := range rows {
			if ok(r) {
				return true
			}
		}
		return false
	}
	listed := func(r propRow) bool {
		for _, v := range f.vals {
			if r.val == v {
				return true
			}
		}
		return false
	}
	switch f.Op {
	case "in":
		return any(listed)
	case "notIn":
		return len(rows) > 0 && !any(listed)
	case "range":
		return any(func(r propRow) bool { return (f.lo == nil || r.skey >= *f.lo) && (f.hi == nil || r.skey <= *f.hi) })
	case "empty":
		return len(rows) == 0
	case "notEmpty":
		return len(rows) > 0
	}
	return any(func(r propRow) bool { return strings.Contains(asciiLower(r.val), f.vals[0]) })
}

// inScope says whether the page is one the query is about, filters included.
func (p *modelPage) inScope(folder *string, recursive bool, tags []string, fs []Filter) bool {
	if folder != nil {
		f := *folder
		switch {
		case !recursive && p.folder != f:
			return false
		case recursive && f != "" && p.folder != f && !strings.HasPrefix(p.folder, f+"/"):
			return false
		}
	}
	for _, tag := range tags {
		if !p.tags[tag] {
			return false
		}
	}
	for _, f := range fs {
		if !p.keeps(f) {
			return false
		}
	}
	return true
}

// modelList is the whole listing of q, the ids in order.
func modelList(t *testing.T, pages []*modelPage, q Query) []string {
	t.Helper()
	fs := append([]Filter(nil), q.Filters...)
	if err := checkFilters(fs); err != nil {
		t.Fatal(err)
	}
	var in []*modelPage
	for _, p := range pages {
		switch {
		case !p.inScope(q.Folder, q.Recursive, q.Tags, fs):
		case q.UpdatedFrom != 0 && p.mtime < q.UpdatedFrom, q.UpdatedTo != 0 && p.mtime >= q.UpdatedTo:
		case q.CreatedFrom != "" && (p.created == "" || p.created < q.CreatedFrom):
		case q.CreatedTo != "" && (p.created == "" || p.created > q.CreatedTo):
		default:
			in = append(in, p)
		}
	}
	name := q.Sort
	if name == "" {
		name = "-updated"
	}
	desc := strings.HasPrefix(name, "-")
	name = strings.TrimPrefix(name, "-")
	// way turns "a before b ascending" into the direction of the sort.
	way := func(c int) bool {
		if desc {
			return c > 0
		}
		return c < 0
	}
	cmp := func(a, b int64) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	var less func(a, b *modelPage) bool
	switch {
	case name == "updated":
		less = func(a, b *modelPage) bool {
			if a.mtime != b.mtime {
				return way(cmp(a.mtime, b.mtime))
			}
			return way(cmp(a.rowid, b.rowid))
		}
	case name == "created":
		less = func(a, b *modelPage) bool {
			if a.createdKey != b.createdKey {
				return way(cmp(a.createdKey, b.createdKey))
			}
			return way(cmp(a.rowid, b.rowid))
		}
	case name == "title":
		less = func(a, b *modelPage) bool {
			switch {
			case a.untitled != b.untitled:
				return a.untitled < b.untitled
			case a.titleKey != b.titleKey:
				return way(strings.Compare(a.titleKey, b.titleKey))
			case a.createdKey != b.createdKey:
				return way(cmp(a.createdKey, b.createdKey))
			}
			return a.rowid < b.rowid
		}
	default:
		key := strings.TrimPrefix(name, "prop:")
		n := len(q.PropOptions)
		// The place of the group of a page: the options in turn, the other
		// values, the pages without; descending, the other values, the
		// options from the last, the pages without.
		group := func(p *modelPage) int {
			rows := p.props[key]
			if len(rows) == 0 {
				return n + 1
			}
			at := -1
			for i, o := range q.PropOptions {
				if o == rows[0].val {
					at = i
					break
				}
			}
			switch {
			case at < 0 && desc:
				return 0
			case at < 0:
				return n
			case desc:
				return n - at
			}
			return at
		}
		less = func(a, b *modelPage) bool {
			ga, gb := group(a), group(b)
			if ga != gb {
				return ga < gb
			}
			if ga == n+1 {
				return a.rowid < b.rowid
			}
			other := (desc && ga == 0) || (!desc && ga == n)
			if ka, kb := a.props[key][0].skey, b.props[key][0].skey; other && ka != kb {
				return way(strings.Compare(ka, kb))
			}
			return way(cmp(a.rowid, b.rowid))
		}
	}
	sort.SliceStable(in, func(i, j int) bool { return less(in[i], in[j]) })
	out := []string{}
	for _, p := range in {
		out = append(out, p.id)
	}
	return out
}

// modelGroups is the count of q.
func modelGroups(t *testing.T, pages []*modelPage, q GroupQuery) GroupResult {
	t.Helper()
	fs := append([]Filter(nil), q.Filters...)
	if err := checkFilters(fs); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	res := GroupResult{Groups: []Group{}}
	for _, p := range pages {
		if !p.inScope(q.Folder, q.Recursive, q.Tags, fs) {
			continue
		}
		rows := p.props[q.Key]
		if len(rows) == 0 {
			res.Empty++
		}
		seen := map[string]bool{}
		for _, r := range rows {
			if !seen[r.val] {
				counts[r.val]++
			}
			seen[r.val] = true
		}
	}
	for v, n := range counts {
		res.Groups = append(res.Groups, Group{v, n})
	}
	sort.Slice(res.Groups, func(i, j int) bool {
		a, b := res.Groups[i], res.Groups[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Value < b.Value
	})
	if len(res.Groups) > MaxGroups {
		res.Groups, res.Truncated = res.Groups[:MaxGroups], true
	}
	return res
}

var equivOptions = []string{"todo", "doing", "done", "never"}

// equivIndex is an index of pages with every kind of value in every kind of
// place: folders inside folders and beside them, tags, properties that are
// options, other text, numbers, booleans, lists (with an option first, last,
// twice), absent; titles and dates that tie.
func equivIndex(t *testing.T, r *rand.Rand, pages int) *Index {
	t.Helper()
	x := openIndex(t)
	folders := []string{"", "a", "a", "a", "a/b", "a/b", "a/b/c", "a-x", "ab", "z"}
	statuses := []string{"todo", "todo", "doing", "done", "done", "zeta", "Alpha", "alpha", "3", "true", "\"3\"",
		"[todo, zeta]", "[zeta, todo]", "[done, done]", "[beta, Beta, doing]", "", ""}
	labels := []string{"red", "green", "blue", "Red"}
	pick := func(from []string) string { return from[r.Intn(len(from))] }
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < pages; i++ {
		var h strings.Builder
		fmt.Fprintf(&h, "id: %010d\n", i)
		if r.Intn(8) != 0 {
			fmt.Fprintf(&h, "title: %s %d\n", pick([]string{"Note", "note", "页", "Zed"}), r.Intn(pages/3+1))
		}
		if r.Intn(3) != 0 {
			fmt.Fprintf(&h, "created: 2026-0%d-%02d\n", 1+r.Intn(3), 1+r.Intn(6))
		}
		var tags []string
		for _, tag := range []string{"x", "y", "z"} {
			if r.Intn(2) == 0 {
				tags = append(tags, tag)
			}
		}
		if len(tags) > 0 {
			fmt.Fprintf(&h, "tags: [%s]\n", strings.Join(tags, ", "))
		}
		if s := pick(statuses); s != "" {
			fmt.Fprintf(&h, "status: %s\n", s)
		}
		switch r.Intn(6) {
		case 0:
		case 1:
			fmt.Fprintf(&h, "points: %d.5\n", r.Intn(20)-10)
		default:
			fmt.Fprintf(&h, "points: %d\n", r.Intn(40)-10)
		}
		if r.Intn(4) != 0 {
			fmt.Fprintf(&h, "due: 2026-%02d-%02d\n", 1+r.Intn(12), 1+r.Intn(28))
		}
		if n := r.Intn(4); n > 0 {
			var ls []string
			for j := 0; j < n; j++ {
				ls = append(ls, pick(labels))
			}
			fmt.Fprintf(&h, "labels: [%s]\n", strings.Join(ls, ", "))
		}
		fmt.Fprintf(&h, "name: \"N%d-%s\"\n", i, pick([]string{"ab", "AB", "cd", "页", "50%", "a_b", "N1", "x\\\\y"}))
		rel := fmt.Sprintf("%05d.md", i)
		if f := pick(folders); f != "" {
			rel = f + "/" + rel
		}
		// Times of writing tie now and then, and are not in the order of the rows.
		putAt(t, x, rel, h.String(), fmt.Sprintf("body %d\n", i), base.Add(time.Duration(r.Intn(pages/2+1))*time.Hour))
	}
	return x
}

// randomFilter is a filter of any kind on one of the properties of equivIndex.
func randomFilter(r *rand.Rand) Filter {
	pick := func(from ...string) string { return from[r.Intn(len(from))] }
	some := func(from ...any) []any {
		var out []any
		for _, v := range from {
			if r.Intn(2) == 0 {
				out = append(out, v)
			}
		}
		if len(out) == 0 {
			out = append(out, from[r.Intn(len(from))])
		}
		return out
	}
	switch r.Intn(9) {
	case 0:
		return Filter{Key: "status", Op: "in", Values: some("todo", "doing", "done", "zeta", "alpha", 3, true, "nothing")}
	case 1:
		return Filter{Key: "status", Op: "notIn", Values: some("todo", "doing", "done", "never", "zeta", "Alpha", "alpha", "beta", "Beta", 3, true)}
	case 2:
		if r.Intn(4) == 0 { // more distinct values than are listed to find where to start
			return Filter{Key: "name", Op: "notIn", Values: some("N1-ab", "N2-ab", "N3-cd", "N4-AB")}
		}
		return Filter{Key: "labels", Op: pick("in", "notIn"), Values: some("red", "green", "blue", "Red")}
	case 3:
		lo, hi := r.Intn(40)-12, r.Intn(40)-12
		v := []any{lo, hi}
		if r.Intn(4) == 0 {
			v[r.Intn(2)] = nil
		}
		return Filter{Key: "points", Op: "range", Values: v}
	case 4:
		v := []any{fmt.Sprintf("2026-%02d-01", 1+r.Intn(12)), fmt.Sprintf("2026-%02d-20", 1+r.Intn(12))}
		if r.Intn(3) == 0 {
			v[r.Intn(2)] = ""
		}
		return Filter{Key: "due", Op: "range", Values: v}
	case 5:
		return Filter{Key: pick("status", "points", "due", "labels", "nothing"), Op: "empty"}
	case 6:
		return Filter{Key: pick("status", "points", "due", "labels", "nothing"), Op: "notEmpty"}
	case 7:
		return Filter{Key: "name", Op: "contains", Values: []any{pick("ab", "AB", "N1", "-c", "页", "zzz", "7", "%", "_", "N_", "\\")}}
	}
	return Filter{Key: pick("status", "labels"), Op: "contains", Values: []any{pick("o", "e", "ta", "3", "D")}}
}

// randomScope is a folder, tags and filters: any mix the API takes.
func randomScope(r *rand.Rand) (folder *string, recursive bool, tags []string, fs []Filter) {
	if r.Intn(4) != 0 {
		folder = strp([]string{"", "a", "a", "a/b", "a/b/c", "a-x", "ab", "z", "none"}[r.Intn(9)])
		recursive = r.Intn(2) == 0
	}
	for _, tag := range []string{"x", "y", "z", "none"} {
		if r.Intn(5) == 0 {
			tags = append(tags, tag)
		}
	}
	for n := []int{0, 0, 1, 1, 1, 2, 2, 3, 10}[r.Intn(9)]; n > 0; n-- {
		fs = append(fs, randomFilter(r))
	}
	return
}

// equivPlans are the ways of reading, by the bounds that choose them.
var equivPlans = []struct {
	name              string
	lead, set, budget int
}{
	{"as served", 2000, 4000, 1000},
	{"walk", 0, 0, 1 << 30},
	{"sets, scan of the order", 0, 0, 0},
	{"sets, sorted by lookup", 0, 1 << 30, 0},
	{"from the smallest condition", 1 << 30, 1 << 30, 0},
	{"from the smallest, scan of the order", 1 << 30, 0, 0},
	{"a short walk, then sets", 0, 3, 5},
	{"a short walk, small bounds", 4, 6, 9},
	{"walk, from a condition of one page", 1, 0, 1 << 30},
}

func TestEveryWayOfReadingAListingGivesWhatTheModelGives(t *testing.T) {
	r := rand.New(rand.NewSource(20261122))
	pages, queries := 160, 140
	if testing.Short() || raceDetector {
		pages, queries = 110, 20
	}
	x := equivIndex(t, r, pages)
	model := loadModel(t, x)
	sorts := []string{"", "updated", "-updated", "created", "-created", "title", "-title",
		"prop:status", "-prop:status", "prop:status", "-prop:status", "prop:points", "-prop:points", "prop:labels", "-prop:due", "prop:nothing"}
	for i := 0; i < queries; i++ {
		var q Query
		q.Folder, q.Recursive, q.Tags, q.Filters = randomScope(r)
		q.Sort = sorts[r.Intn(len(sorts))]
		if strings.HasSuffix(q.Sort, "prop:status") && r.Intn(3) != 0 {
			q.PropOptions = equivOptions
		}
		if r.Intn(12) == 0 {
			q.UpdatedFrom = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixNano()
		}
		if r.Intn(12) == 0 {
			q.UpdatedTo = time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC).UnixNano()
		}
		if r.Intn(12) == 0 {
			q.CreatedFrom = "2026-02-02"
		}
		if r.Intn(12) == 0 {
			q.CreatedTo = "2026-02-04"
		}
		q.Props = r.Intn(2) == 0
		want := modelList(t, model, q)
		limit := []int{1, 2, 3, 7, 20, 200}[r.Intn(6)]
		if len(want) > 40 && limit < 3 {
			limit = 7
		}
		describe := func() string {
			f := "any"
			if q.Folder != nil {
				f = fmt.Sprintf("%q recursive=%v", *q.Folder, q.Recursive)
			}
			return fmt.Sprintf("query %d: folder %s tags %v sort %q options %v limit %d dates %d %d %q %q filters %+v",
				i, f, q.Tags, q.Sort, q.PropOptions, limit, q.UpdatedFrom, q.UpdatedTo, q.CreatedFrom, q.CreatedTo, q.Filters)
		}
		// Each way alone, then a way picked anew for every page of the listing:
		// a cursor made by one is read by another.
		for p := 0; p <= len(equivPlans); p++ {
			name := "a way for each page"
			got := []string{}
			q.Limit, q.Cursor = limit, ""
			for n := 0; ; n++ {
				plan := equivPlans[r.Intn(len(equivPlans))]
				if p < len(equivPlans) {
					plan = equivPlans[p]
					name = plan.name
				}
				setPlan(t, plan.lead, plan.set, plan.budget)
				res, err := x.List(q)
				if err != nil {
					t.Fatalf("%s\n%s: %v", describe(), name, err)
				}
				got = append(got, ids(res)...)
				if res.Next == "" {
					break
				}
				if len(res.Items) != limit || n > len(want) {
					t.Fatalf("%s\n%s: a page of %d and more to come, after %d pages", describe(), name, len(res.Items), n)
				}
				q.Cursor = res.Next
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s\n%s:\n got  %v\n want %v", describe(), name, got, want)
			}
		}
	}
}

func TestEveryWayOfCountingGroupsGivesWhatTheModelGives(t *testing.T) {
	r := rand.New(rand.NewSource(20261123))
	pages, queries := 160, 200
	if testing.Short() || raceDetector {
		pages, queries = 110, 40
	}
	x := equivIndex(t, r, pages)
	model := loadModel(t, x)
	keys := []string{"status", "status", "labels", "points", "name", "due", "nothing"}
	for i := 0; i < queries; i++ {
		q := GroupQuery{Key: keys[r.Intn(len(keys))]}
		q.Folder, q.Recursive, q.Tags, q.Filters = randomScope(r)
		want := modelGroups(t, model, q)
		for _, plan := range equivPlans {
			setPlan(t, plan.lead, plan.set, plan.budget)
			got, err := x.countGroups(q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				f := "any"
				if q.Folder != nil {
					f = fmt.Sprintf("%q recursive=%v", *q.Folder, q.Recursive)
				}
				t.Fatalf("query %d, %s: key %q folder %s tags %v filters %+v\n got  %+v\n want %+v", i, plan.name, q.Key, f, q.Tags, q.Filters, got, want)
			}
		}
	}
}

// A search with a sort is a listing whose words are one more condition: every
// way of reading it finds the pages a search by relevance finds, in the order
// the model puts them.
func TestEveryWayOfReadingASearchAgrees(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	x := equivIndex(t, r, 120)
	model := loadModel(t, x)
	texts := []string{"body", "note", "页", "body 7", "nothing-at-all"}
	if testing.Short() || raceDetector {
		texts = texts[1:3]
	}
	for _, text := range texts {
		// What the words match, from the search by relevance.
		matched := map[string]bool{}
		q := Query{Text: text, Limit: 200}
		for {
			res, err := x.List(q)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids(res) {
				matched[id] = true
			}
			if q.Cursor = res.Next; res.Next == "" {
				break
			}
		}
		for _, sortName := range []string{"-updated", "title", "prop:status", "-prop:points"} {
			for _, fs := range [][]Filter{nil, {{Key: "status", Op: "notEmpty"}}, {{Key: "points", Op: "range", Values: []any{0, 9}}, {Key: "status", Op: "notIn", Values: []any{"todo"}}}} {
				sq := Query{Sort: sortName, Filters: fs, Folder: strp("a"), Recursive: true}
				if sortName == "prop:status" {
					sq.PropOptions = equivOptions
				}
				var want []string
				for _, id := range modelList(t, model, sq) {
					if matched[id] {
						want = append(want, id)
					}
				}
				sq.Text = text
				for i, plan := range equivPlans {
					if i >= 5 && (testing.Short() || raceDetector) {
						break
					}
					setPlan(t, plan.lead, plan.set, plan.budget)
					var got []string
					sq.Limit, sq.Cursor = 7, ""
					for n := 0; n < 100; n++ {
						res, err := x.List(sq)
						if err != nil {
							t.Fatal(err)
						}
						got = append(got, ids(res)...)
						if sq.Cursor = res.Next; res.Next == "" {
							break
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%q sort %s filters %+v, %s:\n got  %v\n want %v", text, sortName, fs, plan.name, got, want)
					}
				}
			}
		}
	}
}
