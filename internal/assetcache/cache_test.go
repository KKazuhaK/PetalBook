package assetcache

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func id(c byte) string { return strings.Repeat(string(rune(c)), 40) }

// blob is n bytes of c and their content id, as Git names them.
func blob(c byte, n int) (string, []byte) {
	data := bytes.Repeat([]byte{c}, n)
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", n)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), data
}

func TestPutOpenTrimOrder(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	budget := int64(1000)
	c, err := Open(Options{Dir: dir, Budget: func() int64 { return budget }, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[byte]string{}
	for _, ch := range []byte("abc") {
		ids[ch], _ = blob(ch, 40)
	}
	id := func(c byte) string { return ids[c] }
	for i, ch := range []byte("abc") {
		_, data := blob(ch, 40)
		if _, err := c.Put("s1", id(ch), bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		// a is the oldest read, then b, then c
		old := now.Add(time.Duration(i-10) * time.Hour)
		os.Chtimes(filepath.Join(dir, "s1", id(ch)), old, old)
	}
	budget = 100 // three files of 40 bytes are over it now
	if got := c.Total(); got != 120 {
		t.Fatalf("total %d", got)
	}
	// Reading a moves it to the front.
	f, _, err := c.Open("s1", id('a'))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, f)
	f.Close()
	c.Trim()
	if c.Has("s1", id('b')) || !c.Has("s1", id('a')) || !c.Has("s1", id('c')) {
		t.Fatalf("trim kept the wrong files: a=%v b=%v c=%v", c.Has("s1", id('a')), c.Has("s1", id('b')), c.Has("s1", id('c')))
	}
	if c.Total() != 80 || c.SpaceBytes("s1") != 80 {
		t.Fatalf("total after trim %d", c.Total())
	}
	// A new instance counts the same from disk, and removes a cut-short write.
	os.WriteFile(filepath.Join(dir, "s1", ".tmp-x"), []byte("half"), 0o644)
	c2, err := Open(Options{Dir: dir, Budget: func() int64 { return budget }})
	if err != nil || c2.Total() != 80 {
		t.Fatalf("reopen: %v total %d", err, c2.Total())
	}
	if _, err := os.Stat(filepath.Join(dir, "s1", ".tmp-x")); err == nil {
		t.Fatal("a half written file survived the start")
	}
}

func TestPinnedSpaceIsKeptAndNotCounted(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(Options{Dir: dir, Budget: func() int64 { return 50 }, Pinned: func(s string) bool { return s == "keep" }})
	c.Put("keep", id('a'), bytes.NewReader(make([]byte, 80)))
	c.Put("other", id('b'), bytes.NewReader(make([]byte, 40)))
	if !c.Has("keep", id('a')) || !c.Has("other", id('b')) {
		t.Fatal("a pinned space's file counted against the budget")
	}
	c.Put("other", id('c'), bytes.NewReader(make([]byte, 40)))
	if !c.Has("keep", id('a')) {
		t.Fatal("a pinned space's file was removed")
	}
	if c.Counted() > 50 {
		t.Fatalf("counted %d", c.Counted())
	}
	if err := c.DropSpace("other"); err != nil || c.SpaceBytes("other") != 0 {
		t.Fatalf("drop: %v", err)
	}
}

// A file whose contents are not those its name says (a write the disk lost
// part of) is removed when the cache is opened, and not counted.
func TestOpenRemovesFilesWhoseContentsDoNotMatchTheirID(t *testing.T) {
	dir := t.TempDir()
	good, data := blob('g', 100)
	short, _ := blob('s', 100)
	other, _ := blob('o', 100)
	os.MkdirAll(filepath.Join(dir, "s1"), 0o755)
	os.WriteFile(filepath.Join(dir, "s1", good), data, 0o644)
	os.WriteFile(filepath.Join(dir, "s1", short), bytes.Repeat([]byte{'s'}, 60), 0o644)
	os.WriteFile(filepath.Join(dir, "s1", other), bytes.Repeat([]byte{'x'}, 100), 0o644)
	c, err := Open(Options{Dir: dir, Budget: func() int64 { return 1 << 20 }})
	if err != nil {
		t.Fatal(err)
	}
	if c.Total() != 100 || c.SpaceBytes("s1") != 100 {
		t.Fatalf("total %d", c.Total())
	}
	if !c.Has("s1", good) || c.Has("s1", short) || c.Has("s1", other) {
		t.Fatalf("good %v short %v other %v", c.Has("s1", good), c.Has("s1", short), c.Has("s1", other))
	}
}
