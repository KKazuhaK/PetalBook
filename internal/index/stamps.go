package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// Stamp is what an index recorded about a file: its rev (the Git blob id of its
// content), its size and its modification time.
type Stamp struct {
	Rev     string
	Size    int64
	MtimeNs int64
}

// ReadStamps reads, without changing or checking anything, what the index at
// dbPath knows of each file: the pages, and the files that are not pages when
// the index noted their rev. A restored clone has its files' modification times
// put back from this (see space.RestoreStamps), so that the index sees them
// unchanged. A missing or unusable index gives no stamps and no error: it is
// only a cache.
func ReadStamps(dbPath string) (map[string]Stamp, error) {
	if strings.ContainsAny(dbPath, "?#") {
		return nil, fmt.Errorf("index path %q: '?' and '#' are not supported", dbPath)
	}
	if _, err := os.Stat(dbPath); err != nil {
		return map[string]Stamp{}, nil
	}
	db, err := sql.Open("sqlite", dbPath+"?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]Stamp{}
	for _, q := range []string{
		`SELECT path, rev, size, mtime_ns FROM pages`,
		`SELECT path, rev, size, mtime_ns FROM problems WHERE rev <> ''`,
	} {
		rows, err := db.QueryContext(context.Background(), q)
		if err != nil {
			return map[string]Stamp{}, nil // another version of the schema: nothing to put back
		}
		for rows.Next() {
			var p string
			var s Stamp
			if err := rows.Scan(&p, &s.Rev, &s.Size, &s.MtimeNs); err != nil {
				rows.Close()
				return nil, err
			}
			out[p] = s
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
