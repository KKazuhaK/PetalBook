package assetcache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func id(c byte) string { return strings.Repeat(string(rune(c)), 40) }

func TestPutOpenTrimOrder(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	budget := int64(1000)
	c, err := Open(Options{Dir: dir, Budget: func() int64 { return budget }, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for i, ch := range []byte("abc") {
		if _, err := c.Put("s1", id(ch), bytes.NewReader(make([]byte, 40))); err != nil {
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
