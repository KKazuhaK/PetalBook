package spaces

// Sizes of spaces. An administrator wants to see how much room each space
// takes, but a space can hold tens of thousands of pages and a server
// thousands of spaces: walking a directory per row of every list is out of the
// question. So sizes are measured in the background, one space at a time, and
// stored with the time they were measured; the list only reads the stored
// value, and a single space can be measured on demand (Resize).
//
// The cost, at the defaults: when a space has never been measured, or was
// measured more than SizeAge (6 hours) ago, the sizer walks
// DataDir/spaces/<id> including its .git: one lstat per file, a few hundred
// milliseconds for tens of thousands of files on a warm disk, with a pause
// after each directory so a walk does not hold the disk. It takes the stale
// spaces one after another, the one measured longest ago first, starting the
// next as soon as a walk is done, so a first round over thousands of spaces
// takes as long as the walks do, not days. At most one walk runs at any time
// (the sizer's and Resize's share a turn, and a waiting Resize gets the next
// one), so the load does not grow with the number of spaces; only how long a
// round lasts does. Once nothing is stale it looks again every SizeEvery (one
// minute): a lookup of two index queries. The size is the sum of the sizes of
// the files (hard links counted each time); the cache directory with the index
// is not counted, it can be rebuilt.
//
// A space whose copy on the server is partial or evicted (milestone 3.1) is
// measured for the listing only (SetSpaceSize): the quotas keep its last full
// measurement, so a partial clone or an eviction never shrinks what a person
// uses.
//
// The same turn also counts the pages of the space (one COUNT on its index: from
// the open space, else from the index file without opening the space) and
// stores both, with the difference added to the usage rows of the space's first
// owner and its tenant in one transaction (see store.RecordMeasurement). That
// is the quotas' only source of bytes and pages: nothing on a request path
// walks files or counts pages.

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Defaults of the sizer.
const (
	DefaultSizeEvery = time.Minute
	DefaultSizeAge   = 6 * time.Hour
	// walkPause is how long a walk rests after each directory, so it does not
	// compete with requests for the disk.
	walkPause = time.Millisecond
)

// runSizer measures the stale spaces, one at a time and without waiting
// between them, then looks again every SizeEvery, until ctx ends.
func (m *Manager) runSizer(ctx context.Context) {
	every := m.opt.SizeEvery
	if every == 0 {
		every = DefaultSizeEvery
	}
	if every < 0 {
		return
	}
	age := m.opt.SizeAge
	if age == 0 {
		age = DefaultSizeAge
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for ctx.Err() == nil && m.measureNext(ctx, age) {
			}
		}
	}
}

// measureNext measures the stalest space if it is stale, and reports whether
// it made progress (measured it, or put a failing one at the back of the
// queue), so that the caller goes on to the next.
func (m *Manager) measureNext(ctx context.Context, age time.Duration) bool {
	id, ok, err := store.NextSpaceToMeasure(ctx, m.opt.Store.Q(), m.opt.Now().Add(-age))
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("could not choose a space to measure", "error", err)
		}
		return false
	}
	if !ok {
		return false
	}
	_, _, err = m.Resize(ctx, store.AllTenants(), id)
	switch {
	case err == nil:
		return true
	case ctx.Err() != nil:
		return false
	case errors.Is(err, ErrNotFound):
		return true // deleted meanwhile; it is no longer in the queue
	}
	m.log.Warn("could not measure a space", "space", id, "error", err)
	// Back of the queue, so one broken space does not take every turn.
	if err := store.SkipSpaceSize(context.WithoutCancel(ctx), m.opt.Store.Q(), id, m.opt.Now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true // deleted meanwhile: nothing to queue, go on to the next
		}
		m.log.Warn("could not note a failed measurement", "space", id, "error", err)
		return false
	}
	return true
}

// Resize measures the space's directory now and stores the result. It waits for
// a walk that is already running (one at a time), and stops if ctx ends.
func (m *Manager) Resize(ctx context.Context, sc store.Scope, id string) (int64, time.Time, error) {
	if _, err := m.adminSpace(ctx, sc, id); err != nil {
		return 0, time.Time{}, err
	}
	select {
	case m.sizeSem <- struct{}{}:
		defer func() { <-m.sizeSem }()
	case <-ctx.Done():
		return 0, time.Time{}, ctx.Err()
	}
	root, _ := m.paths(id)
	n, err := dirSize(ctx, root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A space deleted while it was being measured has had its directory
			// moved away: that is not a failure to measure.
			if _, aerr := m.adminSpace(ctx, sc, id); errors.Is(aerr, ErrNotFound) {
				return 0, time.Time{}, ErrNotFound
			}
		}
		return 0, time.Time{}, err
	}
	at := m.opt.Now()
	if !m.holdsWhole(id) {
		// A partial clone or an evicted space (milestone 3.1): what the server
		// holds of it is not what the space is, so only the listing's size is
		// written and the quotas keep its last full measurement (see
		// store.RecordMeasurement). Not the request's context, as below.
		if err := store.SetSpaceSize(context.WithoutCancel(ctx), m.opt.Store.Q(), id, n, at); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return 0, time.Time{}, ErrNotFound
			}
			return 0, time.Time{}, err
		}
		return n, at, nil
	}
	pages, havePages := m.pageCount(ctx, id)
	// Not the request's context: a measurement that was made is kept. One
	// transaction stores the size and adds the difference to the usage rows.
	touched, err := m.opt.Store.RecordMeasurement(context.WithoutCancel(ctx), id, store.Measurement{Bytes: n, Pages: pages, HavePages: havePages, At: at})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, time.Time{}, ErrNotFound
		}
		return 0, time.Time{}, err
	}
	if m.quota.usage != nil {
		m.quota.usage(context.WithoutCancel(ctx), UsageEvent{Touched: []store.Touched{touched}})
	}
	return n, at, nil
}

// pageCount is the number of pages of a space: a COUNT on its index, from the
// open space when it is open, else straight from the index file (opening the
// space for it would pull its remote and push another space out). ok is false
// when it cannot be known (no index yet): the space keeps its earlier count.
func (m *Manager) pageCount(ctx context.Context, id string) (n int64, ok bool) {
	m.mu.Lock()
	e := m.open[id]
	var sp *space.Space
	if e != nil && e.closing == nil {
		// e.sp and e.err are written by the opener without m.mu and are only
		// settled once done is closed.
		select {
		case <-e.done:
			if e.err == nil && e.sp != nil {
				sp = e.sp
				e.refs++
			}
		default:
		}
	}
	m.mu.Unlock()
	if sp != nil {
		defer func() { m.mu.Lock(); e.refs--; m.mu.Unlock() }()
		c, err := sp.PageCount()
		if err != nil {
			m.log.Warn("could not count the pages of a space", "space", id, "error", err)
			return 0, false
		}
		return int64(c), true
	}
	_, state := m.paths(id)
	c, found, err := index.CountFile(filepath.Join(state, "index.db"))
	if err != nil {
		m.log.Warn("could not count the pages of a space's index", "space", id, "error", err)
		return 0, false
	}
	return int64(c), found
}

// dirSize adds up the sizes of the files under root. A file that disappears
// during the walk (a commit rewrites Git's files all the time) is skipped.
func dirSize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p != root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case <-time.After(walkPause):
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
