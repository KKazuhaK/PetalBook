package index

import "testing"

// The writer keeps a large page cache; the readers a small one each, and they
// read the file through a memory map, so an index's pages are held once by the
// operating system instead of in each of its connections.
func TestReadConnectionsHaveASmallCacheAndAMap(t *testing.T) {
	x := openIndex(t)
	var w, r, a, mm int64
	if err := x.wdb.QueryRow(`PRAGMA cache_size`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if err := x.db.QueryRow(`PRAGMA cache_size`).Scan(&r); err != nil {
		t.Fatal(err)
	}
	if err := x.aux.QueryRow(`PRAGMA cache_size`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := x.db.QueryRow(`PRAGMA mmap_size`).Scan(&mm); err != nil {
		t.Fatal(err)
	}
	if w != writeCacheKiB || r != readCacheKiB || a != readCacheKiB || mm != readMmapBytes {
		t.Fatalf("writer cache %d, reader %d, aux %d, map %d", w, r, a, mm)
	}
}
