package index

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A folder missing from the table between a page's folder and the root does not
// stop the time: it goes on to the nearest folder that is there.
func TestTreeUpdatedPastAMissingFolder(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	m := newMem()
	m.put("d/sub/deep/c.md", pageFile("CCCCCCCCCC", "C", "", nil, "", ""))
	syncAll(t, x, m, false)
	if _, err := x.wdb.Exec(`DELETE FROM folders WHERE path IN ('d/sub', 'd/sub/deep')`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.wdb.Exec(`INSERT INTO folders(path) VALUES ('d/sub/deep')`); err != nil {
		t.Fatal(err)
	}
	tree, err := x.Tree()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range tree {
		got[f.Path] = f.Updated != nil
	}
	if !reflect.DeepEqual(got, map[string]bool{"": true, "d": true, "d/sub/deep": true}) {
		t.Fatalf("%v", got)
	}
}

// A folder's Updated is the newest mtime of any page in it or below it; an
// empty subtree has none, and a save deep down moves every folder above it.
func TestTreeUpdated(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	m := newMem()
	m.put("a.md", pageFile("AAAAAAAAAA", "A", "", nil, "", ""))
	m.put("d/b.md", pageFile("BBBBBBBBBB", "B", "", nil, "", ""))
	m.put("d/sub/deep/c.md", pageFile("CCCCCCCCCC", "C", "", nil, "", ""))
	for rel, sec := range map[string]int64{"a.md": 3, "d/b.md": 2, "d/sub/deep/c.md": 9} {
		m.files[rel] = memFile{m.files[rel].data, (1_800_000_000 + sec) * 1_000_000_000}
	}
	syncAll(t, x, &withDirs{m, []string{"empty", "d/none"}}, false)
	at := func(sec int64) string { return time.Unix(1_800_000_000+sec, 0).UTC().Format(time.RFC3339) }
	got := func() map[string]string {
		tree, err := x.Tree()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range tree {
			out[f.Path] = ""
			if f.Updated != nil {
				out[f.Path] = f.Updated.UTC().Format(time.RFC3339)
			}
		}
		return out
	}
	want := map[string]string{"": at(9), "d": at(9), "d/sub": at(9), "d/sub/deep": at(9), "d/none": "", "empty": ""}
	if g := got(); !reflect.DeepEqual(g, want) {
		t.Fatalf("%v", g)
	}
	// A deep page saved later moves every folder above it, and no sibling.
	if _, err := x.Put("d/sub/deep/c.md", m.files["d/sub/deep/c.md"].data, (1_800_000_000+20)*1_000_000_000); err != nil {
		t.Fatal(err)
	}
	want["d/sub"], want["d/sub/deep"], want["d"], want[""] = at(20), at(20), at(20), at(20)
	if g := got(); !reflect.DeepEqual(g, want) {
		t.Fatalf("%v", g)
	}
	// The newest mtime of a folder is one probe of the (folder, mtime) index, not a scan.
	rows, err := x.db.Query(`EXPLAIN QUERY PLAN SELECT max(p.mtime_ns) FROM pages p WHERE p.folder = 'd'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, "pages_folder_mtime") || strings.Contains(detail, "SCAN") {
			t.Fatalf("plan: %s", detail)
		}
	}
	b, _ := json.Marshal(FolderCount{Path: "empty"})
	if string(b) != `{"path":"empty","pages":0}` {
		t.Fatal(string(b))
	}
}
