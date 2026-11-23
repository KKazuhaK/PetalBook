// Package assetcache is the server's cache of the contents of attachments that
// live in an attachment repository (milestone 3.1, docs/api.md "The attachment
// cache"): DATA/assetcache/<space>/<content id>, one file each, named by the
// content id Git gave the file, so the same file is stored once however many
// times it is referred to.
//
// All spaces share one budget. When it is exceeded the files read least
// recently go first. A file's modification time is its last read (changed at
// most once an hour), so the order survives a restart without a database; the
// total is kept in memory and is exact, because the server writes every file
// in the cache itself.
package assetcache

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// TouchEvery is the shortest time between two changes of a file's modification
// time by reads.
const TouchEvery = time.Hour

// trimTo is the share of the budget a trim leaves in use, so that a cache that
// is full does not walk its files for every file it adds.
const trimTo = 0.9

var idRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ValidID reports whether id is a Git object id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Cache is safe for concurrent use.
type Cache struct {
	dir    string
	budget func() int64
	// Pinned says that a space keeps all its files: they are never removed and
	// do not count against the budget.
	pinned func(space string) bool
	now    func() time.Time

	mu    sync.Mutex
	total int64
	space map[string]int64
}

// Options configures Open.
type Options struct {
	Dir string
	// Budget is the most the files of the spaces that are not pinned may take
	// together; read at every trim, so that it can be changed while running.
	Budget func() int64
	Pinned func(space string) bool
	Now    func() time.Time
}

// Open prepares the directory and counts what is in it (once: the total is
// kept in memory from then on). Files a crash left half written are removed.
func Open(o Options) (*Cache, error) {
	if o.Dir == "" {
		return nil, errors.New("the directory of the attachment cache is not given")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	c := &Cache{dir: o.Dir, budget: o.Budget, pinned: o.Pinned, now: o.Now, space: map[string]int64{}}
	if c.now == nil {
		c.now = time.Now
	}
	spaces, err := os.ReadDir(o.Dir)
	if err != nil {
		return nil, err
	}
	for _, sp := range spaces {
		if !sp.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(o.Dir, sp.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			p := filepath.Join(o.Dir, sp.Name(), f.Name())
			if !ValidID(f.Name()) {
				os.RemoveAll(p) // a write that was cut short
				continue
			}
			if fi, err := f.Info(); err == nil && fi.Mode().IsRegular() {
				c.space[sp.Name()] += fi.Size()
				c.total += fi.Size()
			}
		}
	}
	return c, nil
}

func (c *Cache) path(space, id string) string { return filepath.Join(c.dir, space, id) }

func okSpace(space string) bool {
	return space != "" && !strings.ContainsAny(space, `/\`) && space != "." && space != ".."
}

// Open opens the cached file for reading. fs.ErrNotExist when it is not there.
// A read moves the file to the front of the queue (at most once an hour).
func (c *Cache) Open(space, id string) (*os.File, fs.FileInfo, error) {
	if !okSpace(space) || !ValidID(id) {
		return nil, nil, fs.ErrNotExist
	}
	p := c.path(space, id)
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = fs.ErrNotExist
		}
		return nil, nil, err
	}
	if now := c.now(); now.Sub(fi.ModTime()) > TouchEvery {
		_ = os.Chtimes(p, now, now)
	}
	return f, fi, nil
}

// Has reports whether the file is cached, without counting as a read.
func (c *Cache) Has(space, id string) bool {
	if !okSpace(space) || !ValidID(id) {
		return false
	}
	fi, err := os.Lstat(c.path(space, id))
	return err == nil && fi.Mode().IsRegular()
}

// Put stores what r reads as the file id of the space: written beside its
// place and renamed into it, so a reader sees all of it or none. It returns the
// size. A file that is there already is replaced by the same bytes.
func (c *Cache) Put(space, id string, r io.Reader) (int64, error) {
	if !okSpace(space) || !ValidID(id) {
		return 0, fmt.Errorf("not a cache entry: %q %q", space, id)
	}
	dir := filepath.Join(c.dir, space)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	p := c.path(space, id)
	c.mu.Lock()
	var old int64
	if fi, err := os.Lstat(p); err == nil {
		old = fi.Size()
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		c.mu.Unlock()
		os.Remove(tmp.Name())
		return 0, err
	}
	c.space[space] += n - old
	c.total += n - old
	c.mu.Unlock()
	c.Trim()
	return n, nil
}

// counted is what counts against the budget: the files of the spaces that are
// not pinned. Callers hold c.mu.
func (c *Cache) counted() int64 {
	n := c.total
	if c.pinned != nil {
		for sp, b := range c.space {
			if c.pinned(sp) {
				n -= b
			}
		}
	}
	return n
}

// Total is the bytes of all files; SpaceBytes those of one space.
func (c *Cache) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Counted is the bytes that count against the budget.
func (c *Cache) Counted() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counted()
}

// SpaceBytes is what the files of the space take.
func (c *Cache) SpaceBytes(space string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.space[space]
}

// Budget is the limit now.
func (c *Cache) Budget() int64 {
	if c.budget == nil {
		return 0
	}
	return c.budget()
}

// DropSpace removes every file of a space (it was evicted, or deleted).
func (c *Cache) DropSpace(space string) error {
	if !okSpace(space) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(c.dir, space)); err != nil {
		return err
	}
	c.total -= c.space[space]
	delete(c.space, space)
	return nil
}

type entry struct {
	space, id string
	size      int64
	mtime     time.Time
}

// Trim removes the files read least recently until what counts is within the
// budget (a little under it, see trimTo). Files of pinned spaces stay.
func (c *Cache) Trim() {
	if c.budget == nil {
		return
	}
	limit := c.budget()
	if limit <= 0 {
		return
	}
	c.mu.Lock()
	if c.counted() <= limit {
		c.mu.Unlock()
		return
	}
	defer c.mu.Unlock()
	var all []entry
	for sp := range c.space {
		if c.pinned != nil && c.pinned(sp) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(c.dir, sp))
		if err != nil {
			continue
		}
		for _, f := range files {
			if !ValidID(f.Name()) {
				continue
			}
			if fi, err := f.Info(); err == nil && fi.Mode().IsRegular() {
				all = append(all, entry{sp, f.Name(), fi.Size(), fi.ModTime()})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mtime.Before(all[j].mtime) })
	target := int64(float64(limit) * trimTo)
	for _, e := range all {
		if c.counted() <= target {
			break
		}
		if err := os.Remove(c.path(e.space, e.id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		c.space[e.space] -= e.size
		c.total -= e.size
	}
}
