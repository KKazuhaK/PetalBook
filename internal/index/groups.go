package index

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxGroups is the most distinct values Groups returns.
const MaxGroups = 1000

// GroupQuery asks how the pages of a scope are spread over the values of one
// property. The scope is that of a listing: a folder (and below it), tags,
// filters.
type GroupQuery struct {
	Folder    *string
	Recursive bool
	Tags      []string
	Filters   []Filter
	// Key is the property to count by.
	Key string
}

// Group is a value of the property and the number of pages that have it (a
// list property counts a page once for each distinct value it holds).
type Group struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// GroupResult is the answer of Groups.
type GroupResult struct {
	// Groups are the values, most pages first, then by value.
	Groups []Group `json:"groups"`
	// Empty is the number of pages in the scope with no value for the property.
	Empty int `json:"empty"`
	// Truncated says there were more than MaxGroups values and the rarest are
	// left out.
	Truncated bool `json:"truncated"`
}

// Groups counts the pages of a scope by the values of a property. It reads the
// props table only (and the pages table for the scope), never a file. Cost: one
// pass over the rows of the property that belong to the scope.
func (x *Index) Groups(q GroupQuery) (GroupResult, error) {
	if q.Key == "" || len(q.Key) > maxFilterKey || !utf8.ValidString(q.Key) {
		return GroupResult{}, invalid("groups needs the key of a property")
	}
	if err := checkFilters(q.Filters); err != nil {
		return GroupResult{}, err
	}
	query, qargs, totalQuery, totalArgs := groupSQL(q)
	res := GroupResult{Groups: []Group{}}
	rows, err := x.db.Query(query, qargs...)
	if err != nil {
		return GroupResult{}, err
	}
	defer rows.Close()
	with := 0
	var all []Group
	for rows.Next() {
		var g Group
		var first int
		if err := rows.Scan(&g.Value, &g.Count, &first); err != nil {
			return GroupResult{}, err
		}
		with += first
		all = append(all, g)
	}
	if err := rows.Err(); err != nil {
		return GroupResult{}, err
	}
	rows.Close()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].Value < all[j].Value
	})
	if len(all) > MaxGroups {
		all, res.Truncated = all[:MaxGroups], true
	}
	if len(all) > 0 {
		res.Groups = all
	}

	var total int
	if err := x.db.QueryRow(totalQuery, totalArgs...).Scan(&total); err != nil {
		return GroupResult{}, err
	}
	res.Empty = total - with
	return res, nil
}

// groupSQL builds the two queries of Groups: the values with their page counts,
// and the number of pages in the scope. Where the scan starts follows the
// scope, so that the cost follows its size and not that of the space:
//
//   - a folder: its pages (the folder index), each with its row of the property
//     by primary key;
//   - tags and no folder: the pages of the first tag (the tags index);
//   - neither: the rows of the property.
func groupSQL(q GroupQuery) (query string, qargs []any, totalQuery string, totalArgs []any) {
	tags := q.Tags
	var lead string
	if q.Folder == nil && len(tags) > 0 {
		lead, tags = tags[0], tags[1:]
	}
	where, args := scopeConds(q.Folder, q.Recursive, tags)
	for _, f := range q.Filters {
		cond, cargs := f.sql()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	scope := ""
	if len(where) > 0 {
		scope = ` AND ` + strings.Join(where, ` AND `)
	}
	const cols = `SELECT s.val, count(DISTINCT s.page), sum(s.pos = 0) `
	switch {
	case q.Folder != nil:
		query = cols + `FROM pages p CROSS JOIN props s ON s.page = p.rowid AND s.key = ? WHERE 1 = 1` + scope + ` GROUP BY s.val`
		qargs = append([]any{q.Key}, args...)
		totalQuery = `SELECT count(*) FROM pages p WHERE 1 = 1` + scope
		totalArgs = args
	case lead != "":
		query = cols + `FROM tags t CROSS JOIN pages p ON p.rowid = t.page CROSS JOIN props s ON s.page = p.rowid AND s.key = ? WHERE t.tag = ?` + scope + ` GROUP BY s.val`
		qargs = append([]any{q.Key, lead}, args...)
		totalQuery = `SELECT count(*) FROM tags t CROSS JOIN pages p ON p.rowid = t.page WHERE t.tag = ?` + scope
		totalArgs = append([]any{lead}, args...)
	default:
		query = cols + `FROM props s CROSS JOIN pages p ON p.rowid = s.page WHERE s.key = ?` + scope + ` GROUP BY s.val`
		qargs = append([]any{q.Key}, args...)
		totalQuery = `SELECT count(*) FROM pages p WHERE 1 = 1` + scope
		totalArgs = args
	}
	return
}
