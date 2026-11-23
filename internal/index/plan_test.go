package index

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// setPlan sets, until the test ends, the bounds that choose how a listing is
// read: the most pages of a condition the read starts from, the most matches
// put in order by lookup, and the most pages a walk of the order looks at.
//
//	setPlan(t, 0, 0, 1<<30)  the order is walked and every page looked up
//	setPlan(t, 0, 0, 0)      the conditions are read as sets, the order scanned
//	setPlan(t, 1<<30, 1<<30, 0)  the read starts from the smallest condition
func setPlan(t *testing.T, lead, set, budget int) {
	t.Helper()
	oldLead, oldSet, oldBudget := smallLead, smallSet, walkBudget
	smallLead, smallSet, walkBudget = lead, set, func(int) int { return budget }
	t.Cleanup(func() { smallLead, smallSet, walkBudget = oldLead, oldSet, oldBudget })
}

// queryPlan is a query that ran, with the lines of its EXPLAIN QUERY PLAN.
type queryPlan struct {
	query string
	lines []string
}

func (p queryPlan) String() string { return p.query + "\n    " + strings.Join(p.lines, "\n    ") }

// plansOf runs f and returns the plan of each query the listings and counts
// in it ran.
func plansOf(t *testing.T, x *Index, f func()) []queryPlan {
	t.Helper()
	return newPlansOf(t, x, map[string]bool{}, f)
}

// newPlansOf is plansOf for the queries that are not in seen, which it adds
// them to.
func newPlansOf(t *testing.T, x *Index, seen map[string]bool, f func()) []queryPlan {
	t.Helper()
	type ran struct {
		query string
		args  []any
	}
	var mu sync.Mutex
	var all []ran
	x.trace = func(query string, args []any) {
		mu.Lock()
		if !seen[query] {
			seen[query] = true
			all = append(all, ran{query, args})
		}
		mu.Unlock()
	}
	f()
	x.trace = nil
	var out []queryPlan
	for _, r := range all {
		rows, err := x.db.Query(`EXPLAIN QUERY PLAN `+r.query, r.args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, r.query)
		}
		p := queryPlan{query: r.query}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			p.lines = append(p.lines, detail)
		}
		rows.Close()
		out = append(out, p)
	}
	return out
}

// Every query a listing or a count runs, in every way of reading, reads a
// range of an index: nothing scans the props table or the tags table, nothing
// sorts or groups more than the few pages it was handed, every condition
// looked up by page is a lookup by primary key, and every order that is walked
// or scanned comes out of its index already in order.
func TestEveryQueryOfAListingOrACountReadsAnIndexRange(t *testing.T) {
	x := equivIndex(t, rand.New(rand.NewSource(3)), 220)
	f := func(key, op string, v ...any) Filter { return Filter{Key: key, Op: op, Values: v} }
	many := make([]any, 100)
	for i := range many {
		many[i] = fmt.Sprintf("x%d", i)
	}
	filters := []Filter{
		f("status", "in", "todo", "zeta"), f("status", "in", many...), f("status", "notIn", "done", "never"), f("name", "notIn", many...),
		f("points", "range", 0, 9), f("points", "range", nil, 9), f("due", "range", "2026-03-01", ""),
		f("status", "empty"), f("labels", "notEmpty"), f("name", "contains", "ab"),
	}
	type scope struct {
		folder    *string
		recursive bool
		tags      []string
	}
	scopes := []scope{{}, {folder: strp("a")}, {folder: strp("a"), recursive: true}, {tags: []string{"x"}}, {folder: strp("a"), recursive: true, tags: []string{"x", "y", "z"}}}
	sorts := []string{"", "updated", "created", "-created", "title", "-title", "prop:points", "-prop:points", "prop:status", "-prop:status"}
	seen := map[string]bool{}
	check := func(what string, run func()) {
		t.Helper()
		for _, p := range newPlansOf(t, x, seen, run) {
			few := strings.Contains(p.query, "json_each(?)") // the pages are handed over, at most a few thousand
			for _, line := range p.lines {
				bad := ""
				switch {
				case regexp.MustCompile(`^SCAN (props|tags|s|s0|f|o|t)\b`).MatchString(line):
					bad = "scans the props or the tags table"
				case regexp.MustCompile(`^SCAN (p|pages)$`).MatchString(line) && !strings.Contains(p.query, "ORDER BY p.rowid LIMIT ?"):
					bad = "scans the page table" // but for a walk of it by rowid, which the limit ends
				case strings.Contains(line, "AUTOMATIC") || strings.Contains(line, "MATERIALIZE"):
					bad = "builds an index or a table of its own"
				case strings.Contains(line, "TEMP B-TREE") && !few && !strings.Contains(p.query, "s.val IN ("):
					bad = "sorts or groups rows no bound was put on" // but the few rows of the other values
				case regexp.MustCompile(`^SEARCH (f|o|s0) `).MatchString(line) && !strings.Contains(line, "USING PRIMARY KEY (page=? AND key=?"):
					bad = "looks a property up by something other than its page"
				case strings.HasPrefix(line, "SEARCH t ") && !(strings.Contains(line, "page=?") && strings.Contains(line, "tag=?")):
					bad = "looks a tag up by something other than its page"
				case strings.HasPrefix(line, "SEARCH p ") && few && !strings.Contains(line, "INTEGER PRIMARY KEY (rowid=?)"):
					bad = "does not read the pages it was handed by rowid"
				}
				if bad != "" {
					t.Errorf("%s: a query %s (%s)\n%s", what, bad, line, p)
				}
			}
		}
	}
	plans := equivPlans[:6]
	if raceDetector { // as served, walked, as sets: the queries of the others are among theirs
		plans, sorts = plans[:3], []string{"", "-title", "-prop:points", "prop:status"}
	}
	for _, plan := range plans {
		setPlan(t, plan.lead, plan.set, plan.budget)
		for si, sc := range scopes {
			for fi := -1; fi < len(filters); fi++ {
				var fs []Filter
				if fi >= 0 {
					fs = []Filter{filters[fi], filters[(fi+3)%len(filters)]}
				}
				if fi >= 0 && fi%len(scopes) != si && si > 0 {
					continue // every filter with no scope, and with one of the others
				}
				what := fmt.Sprintf("%s, scope %d, filters %d", plan.name, si, fi)
				for _, s := range sorts {
					q := Query{Folder: sc.folder, Recursive: sc.recursive, Tags: sc.tags, Filters: fs, Sort: s, Limit: 3}
					if strings.HasSuffix(s, "prop:status") {
						q.PropOptions = equivOptions
					}
					check(what+", sort "+s, func() {
						// The first page, and one from a cursor.
						res, err := x.List(q)
						if err != nil {
							t.Fatal(err)
						}
						if q.Cursor = res.Next; q.Cursor != "" {
							if _, err := x.List(q); err != nil {
								t.Fatal(err)
							}
						}
					})
				}
				for _, key := range []string{"status", "name"} { // few distinct values, and many
					check(what+", groups of "+key, func() {
						if _, err := x.countGroups(GroupQuery{Folder: sc.folder, Recursive: sc.recursive, Tags: sc.tags, Filters: fs, Key: key}); err != nil {
							t.Fatal(err)
						}
					})
				}
			}
		}
	}
	// Ten filters and three tags at once: more ranges than are read at a time.
	setPlan(t, 0, 0, 0)
	check("many conditions", func() {
		if _, err := x.List(Query{Folder: strp("a"), Recursive: true, Tags: []string{"x", "y", "z"}, Filters: filters, Limit: 3}); err != nil {
			t.Fatal(err)
		}
	})
	if len(seen) < 60 && !raceDetector {
		t.Errorf("only %d different queries were looked at", len(seen))
	}
}
