package space

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestChangeFeedInALargeSpace measures what the offline sync asks of a space of
// 50 000 pages: the first build of the index, the head, a poll of the changes
// with nothing new and with a few changes, a batch of the catalogue, 100
// bodies, and what the feed adds to the size of the index. It runs only when
// asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestChangeFeedInALargeSpace -v ./internal/space/
//
// Not parallel: it measures timings, which tests running alongside would distort.
func TestChangeFeedInALargeSpace(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	const pages = 50_000
	root := filepath.Join(t.TempDir(), "space")
	ids := make([]string, pages)
	for i := 0; i < pages; i++ {
		folder := filepath.Join(root, fmt.Sprintf("lib/f%02d", i%50))
		if i < 50 {
			os.MkdirAll(folder, 0o755)
		}
		ids[i] = fmt.Sprintf("%010d", i)
		var b strings.Builder
		fmt.Fprintf(&b, "---\nid: %s\ntitle: 页 %d\ntags: [诗, 词]\ncreated: 2026-01-%02d\n---\n", ids[i], i, i%28+1)
		for l := 0; l < 12; l++ {
			fmt.Fprintf(&b, "正文第 %d 行，页 %d，一些用来凑够长度的文字 lorem ipsum dolor sit amet。\n", l, i)
		}
		if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("p%d.md", i)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	s, err := Open(Options{Root: root, Name: "big", CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("first build of the index: %v", time.Since(start).Round(time.Millisecond))

	median := func(n int, f func()) time.Duration {
		d := make([]time.Duration, n)
		for i := range d {
			t0 := time.Now()
			f()
			d[i] = time.Since(t0)
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d[n/2]
	}
	head := s.Head()
	t.Logf("head, from memory: %v per call", median(1001, func() { s.Head() }))
	t.Logf("changes with nothing new: %v (median of 301)", median(301, func() {
		if _, err := s.Changes(head, 1000); err != nil {
			t.Fatal(err)
		}
	}))
	t.Logf("tree and settings stamps: %v, %v", median(301, func() { s.TreeStamp() }), median(301, func() { s.SettingsStamp() }))
	var cursor string
	t.Logf("a catalogue batch of 2000: %v (median of 25)", median(25, func() {
		b, err := s.Catalog(nil, cursor, 2000)
		if err != nil || len(b.Items) != 2000 {
			t.Fatal(err, len(b.Items))
		}
		cursor = b.Next
	}))
	t.Logf("a catalogue batch of 2000 from a folder: %v", median(25, func() {
		if _, err := s.Catalog([]string{"lib/f07"}, "", 2000); err != nil {
			t.Fatal(err)
		}
	}))
	round := 0
	t.Logf("100 bodies, one after the other: %v (median of 25)", median(25, func() {
		for i := 0; i < 100; i++ {
			if _, err := s.Get(ids[(round*100+i*37)%pages]); err != nil {
				t.Fatal(err)
			}
		}
		round++
	}))
	// A poll that has 20 changes to report, and a write.
	for i := 0; i < 20; i++ {
		p, _ := s.Get(ids[i])
		if _, err := s.Save(Author{Name: "t", Email: "t@example.test"}, ids[i], p.Rev, p.Meta, p.Body+"x\n"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("changes with 20 changes: %v", median(101, func() {
		cl, err := s.Changes(head, 1000)
		if err != nil || len(cl.Items) != 20 {
			t.Fatal(err, len(cl.Items))
		}
	}))

	// The size of the index with and without the feed, on a copy of the file
	// once the space is closed (which folds the log into it).
	idxPath := s.idx.Path()
	s.Close()
	with := fileSize(idxPath)
	data, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(cp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`VACUUM`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	withVacuumed := fileSize(cp)
	for _, q := range []string{`DROP INDEX pages_seq`, `DROP TABLE gone`, `ALTER TABLE pages DROP COLUMN seq`, `VACUUM`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	without := fileSize(cp)
	t.Logf("index size: %d bytes as it is, %d vacuumed with the feed, %d without: +%.2f%%", with, withVacuumed, without, 100*float64(withVacuumed-without)/float64(without))
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}
