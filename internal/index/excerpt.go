package index

import (
	"encoding/json"
	"sync/atomic"
)

// excerptRowsRead counts the excerpts read for responses; a test checks that
// a page of a listing reads the excerpts of the pages it shows and no more.
var excerptRowsRead atomic.Int64

// fillExcerpts sets the Excerpt of the summaries, in one query on the table of
// excerpts (see the schema). Summaries read from pages carry none: the text is
// about a kilobyte per page, and a listing sorts, skips and counts far more
// rows than it shows, so it is read here, for the rows that are shown. A page
// that is gone since the listing was read keeps an empty excerpt.
func (x *Index) fillExcerpts(items []Summary) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	excerptRowsRead.Add(int64(len(items)))
	text := make(map[string]string, len(items))
	err = x.one(func(db querier) error {
		rows, err := db.Query(`SELECT p.id, e.text FROM json_each(?) j CROSS JOIN pages p ON p.id = j.value CROSS JOIN excerpts e ON e.rowid = p.rowid`, string(list))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, s string
			if err := rows.Scan(&id, &s); err != nil {
				return err
			}
			text[id] = s
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Excerpt = text[items[i].ID]
	}
	return nil
}
