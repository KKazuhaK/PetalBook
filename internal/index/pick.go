package index

import (
	"encoding/json"
	"errors"
)

// ErrNoPick is Pick's answer when no page meets the query.
var ErrNoPick = errors.New("no page meets the query")

// PickQuery selects the pages one is picked from: the scope of a listing and
// its filters, and optionally an effective type.
type PickQuery struct {
	Folder    *string
	Recursive bool
	Tags      []string
	Filters   []Filter
	// Type keeps the pages whose header says this type, and those whose header
	// says none and that lie in one of TypeFolders (the folders the type is
	// bound to, which the index knows nothing of: the caller works them out).
	Type        string
	TypeFolders []string
}

// Pick returns the page the query selects that is at the position at(total)
// among those that meet it, in the order of the index's own numbering of the
// pages (stable while the pages are), and how many meet it. ErrNoPick when
// none does. Cost: the pages that meet the query are counted as a set, one scan
// of the range of each condition at most (see plan.go), then one page is read
// by its number; no file is read.
func (x *Index) Pick(q PickQuery, at func(total int) int) (total int, s Summary, err error) {
	if err := checkFilters(q.Filters); err != nil {
		return 0, s, err
	}
	conds := conditions(q.Folder, q.Recursive, q.Tags, q.Filters)
	if q.Type != "" {
		conds = append(conds, typeCond(q.Type, q.TypeFolders))
	}
	if len(conds) == 0 {
		conds = []cond{{sets: []pageSet{allPages}}}
	}
	r, err := x.begin()
	if err != nil {
		return 0, s, err
	}
	defer r.end()
	m, _, err := r.match(conds, false)
	if err != nil {
		return 0, s, err
	}
	total = m.count()
	if total == 0 {
		return 0, s, ErrNoPick
	}
	n := at(total)
	if n < 0 || n >= total {
		return 0, s, errors.New("index: the position is out of range")
	}
	var rowid int64 = -1
	m.each(-1, func(id int64) bool {
		if n == 0 {
			rowid = id
			return false
		}
		n--
		return true
	})
	rows, err := r.db.Query(`SELECT `+selectCols(false)+` FROM pages p WHERE p.rowid = ?`, rowid)
	if err != nil {
		return 0, s, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, s, ErrNoPick
	}
	s, _, _, _, _, _, err = scanSummary(rows, false)
	return total, s, err
}

// Summary returns the summary of a page by its id, false when it is not indexed.
func (x *Index) Summary(id string) (s Summary, ok bool, err error) {
	err = x.one(func(db querier) error {
		rows, err := db.Query(`SELECT `+selectCols(false)+` FROM pages p WHERE p.id = ?`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		ok = true
		s, _, _, _, _, _, err = scanSummary(rows, false)
		return err
	})
	return
}

// typeCond is the condition of PickQuery.Type.
func typeCond(typ string, folders []string) cond {
	if len(folders) == 0 {
		return cond{where: `p.type = ?`, args: []any{typ},
			sets: []pageSet{{sql: `SELECT rowid AS id FROM pages WHERE type = ?`, args: []any{typ}}}}
	}
	list, _ := json.Marshal(folders)
	return cond{
		where: `(p.type = ? OR (p.type = '' AND p.folder IN (SELECT value FROM json_each(?))))`,
		args:  []any{typ, string(list)},
		sets: []pageSet{{sql: `SELECT rowid AS id FROM pages WHERE type = ? OR (type = '' AND folder IN (SELECT value FROM json_each(?)))`,
			args: []any{typ, string(list)}}},
	}
}

// Summaries returns the summaries of the pages with these ids (at most 500),
// by id; an id that is not indexed is not in the answer. One query.
func (x *Index) Summaries(ids []string) (map[string]Summary, error) {
	out := make(map[string]Summary, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 500 {
		return nil, invalid("at most 500 ids")
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	err := x.one(func(db querier) error {
		rows, err := db.Query(`SELECT `+selectCols(false)+` FROM pages p WHERE p.id IN (`+marks(len(ids))+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, _, _, _, _, _, err := scanSummary(rows, false)
			if err != nil {
				return err
			}
			out[s.ID] = s
		}
		return rows.Err()
	})
	return out, err
}

// FoldersKnown says which of the paths are folders of the space (empty ones
// included); the root is always one. One query.
func (x *Index) FoldersKnown(paths []string) (map[string]bool, error) {
	out := make(map[string]bool, len(paths))
	var args []any
	for _, p := range paths {
		if p == "" {
			out[p] = true
			continue
		}
		args = append(args, p)
	}
	if len(args) == 0 {
		return out, nil
	}
	if len(args) > 500 {
		return nil, invalid("at most 500 paths")
	}
	err := x.one(func(db querier) error {
		rows, err := db.Query(`SELECT path FROM folders WHERE path IN (`+marks(len(args))+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			out[p] = true
		}
		return rows.Err()
	})
	return out, err
}
