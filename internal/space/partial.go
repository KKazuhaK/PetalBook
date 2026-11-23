package space

// A space whose clone is partial (milestone 3.1, docs/api.md "Partial clones")
// has every commit and tree, and the contents of the files of the version that
// is checked out. What it lacks is the contents of older versions: a read of
// history fetches them by their ids, from the remote, in one request per page.
// The working tree and the index are always complete, so reading the current
// text is never affected.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/index"
)

// FetchWait is how long a read that needs contents from the remote waits for
// them (15 seconds); a fetch that takes longer goes on in the background and
// the read answers NotCachedError with Fetching true. Tests make it shorter.
var FetchWait = 15 * time.Second

// NotCachedError: contents of an old version are not on the server and could
// not be had now. Fetching says whether a fetch is still going on, so that
// trying again soon is likely to find them.
type NotCachedError struct {
	Fetching bool
	Err      error
}

func (e *NotCachedError) Error() string {
	if e.Fetching {
		return "This version is not on the server yet; it is being fetched from the remote repository."
	}
	return "This version is not on the server and the remote repository cannot be reached."
}
func (e *NotCachedError) Unwrap() error { return e.Err }

type blobFlight struct {
	done chan struct{}
	err  error
}

// partialState is what a partial clone needs beyond a full one.
type partialState struct {
	mu      sync.Mutex
	flights map[string]*blobFlight // blob id -> the fetch that is bringing it
	failAt  time.Time              // when a fetch last failed
	failErr error
}

// Partial reports whether the clone is partial: the remote's old versions are
// fetched when asked for.
func (s *Space) Partial() bool {
	r := s.rem.Load()
	return r != nil && r.partial.Load()
}

// enterPartial puts the space in partial mode if its clone is one.
func (s *Space) enterPartial(ctx context.Context, r *remote) {
	if !r.repo.IsPartial(ctx) {
		return
	}
	r.partial.Store(true)
	r.repo.SetEnsure(s.blobFetcher(r))
	s.cm.git.r().SetEnsure(s.blobFetcher(r))
}

// blobFetcher is the function the history reads call for the blobs they lack.
func (s *Space) blobFetcher(r *remote) gitstore.EnsureFunc {
	return func(ctx context.Context, ids []string) error {
		fls, err := s.startFetch(r, ids)
		if err != nil {
			return err
		}
		timer := time.NewTimer(FetchWait)
		defer timer.Stop()
		for _, f := range fls {
			select {
			case <-f.done:
				if f.err != nil {
					return &NotCachedError{Fetching: false, Err: f.err}
				}
			case <-timer.C:
				return &NotCachedError{Fetching: true}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
}

// startFetch makes sure every id is being fetched, joining the fetches that
// are under way, and returns them. A fetch that failed a moment ago answers
// again with its failure at once (as FailFast does for a pull).
func (s *Space) startFetch(r *remote, ids []string) ([]*blobFlight, error) {
	p := &r.pstate
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.flights == nil {
		p.flights = map[string]*blobFlight{}
	}
	if p.failErr != nil && time.Since(p.failAt) < r.failFast {
		return nil, &NotCachedError{Fetching: false, Err: p.failErr}
	}
	var fresh []string
	seen := map[*blobFlight]bool{}
	var fls []*blobFlight
	for _, id := range ids {
		if f := p.flights[id]; f != nil {
			if !seen[f] {
				seen[f] = true
				fls = append(fls, f)
			}
			continue
		}
		fresh = append(fresh, id)
	}
	if len(fresh) > 0 {
		f := &blobFlight{done: make(chan struct{})}
		for _, id := range fresh {
			p.flights[id] = f
		}
		fls = append(fls, f)
		go func() {
			// The space's own context: closing the space ends the fetch.
			err := r.repo.FetchBlobs(s.ctx, fresh)
			p.mu.Lock()
			for _, id := range fresh {
				delete(p.flights, id)
			}
			if err != nil {
				p.failAt, p.failErr = time.Now(), err
			} else {
				p.failErr = nil
			}
			p.mu.Unlock()
			if err != nil {
				if s.ctx.Err() == nil {
					s.netFailure(r, err)
				}
			} else {
				r.markOK()
			}
			f.err = err
			close(f.done)
		}()
	}
	return fls, nil
}

// prefetchMove fetches the contents a move of the work tree from one commit to
// another needs, so that the move itself touches no network (a fast-forward, a
// reset to the remote's commit). It is a no-op for a full clone. Callers map
// the error as they map a failed fetch.
func (s *Space) prefetchMove(ctx context.Context, r *remote, from, to string) error {
	if !r.partial.Load() {
		return nil
	}
	return r.repo.PrefetchMove(ctx, from, to)
}

// RestoreStamps puts back the modification times of the files of a freshly
// checked out clone: a file whose content is the one the index recorded (by its
// Git blob id) gets the time the index recorded, so that the index sees it as
// unchanged and "by update" is the order it was. Only files that changed
// meanwhile are read again at the next open. It returns how many files it put
// back out of how many the index knew.
func RestoreStamps(root, dbPath string, tree []gitstore.TreeEntry) (restored, known int, err error) {
	stamps, err := index.ReadStamps(dbPath)
	if err != nil {
		return 0, 0, err
	}
	known = len(stamps)
	for _, e := range tree {
		st, ok := stamps[e.Path]
		if !ok || e.Type != "blob" || st.Rev != e.ID {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(e.Path))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != st.Size {
			continue
		}
		t := time.Unix(0, st.MtimeNs)
		if err := os.Chtimes(p, t, t); err != nil {
			return restored, known, fmt.Errorf("could not put back the time of %s: %w", e.Path, err)
		}
		restored++
	}
	return restored, known, nil
}

// StateClean reports why the state of a space that is not open holds something
// that exists nowhere else: the journal of changes written but not committed,
// including the moves whose links were not all made right. It is "" when there
// is nothing. A state directory that cannot be read is not clean.
func StateClean(stateDir string) string {
	data, err := os.ReadFile(filepath.Join(stateDir, "pending.journal"))
	switch {
	case err == nil && len(bytesTrim(data)) > 0:
		return "the journal of changes not yet committed is not empty"
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "the journal of changes could not be read"
	}
	return ""
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[0] == '\n' || b[0] == ' ' || b[0] == '\r' || b[0] == '\t') {
		b = b[1:]
	}
	return b
}

// MakeFull fetches every version of every file the partial clone lacks, and
// makes the clone a full one (a pinned space keeps everything on the server):
// the pulls after it bring every version too. It runs in the background and
// can be repeated: what is there is not fetched again.
//
// The order keeps the clone usable at every step: first the fetches stop
// being filtered (under fetchMu, so no pull is half way), so that nothing
// fetched from then on lacks contents; then what is missing is fetched,
// without holding up the pulls; last, under fetchMu again, nothing may be
// missing from any ref before the clone stops being partial.
func (s *Space) MakeFull(ctx context.Context) error {
	r := s.rem.Load()
	if r == nil || !r.partial.Load() {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	r.fetchMu.Lock()
	err := r.repo.StopFilter(ctx)
	r.fetchMu.Unlock()
	if err != nil {
		return err
	}
	ids, err := r.repo.MissingAll(ctx)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if err := r.repo.FetchBlobs(ctx, ids); err != nil {
			return s.netFailure(r, err)
		}
		r.markOK()
	}
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	if ids, err = r.repo.MissingAll(ctx); err != nil {
		return err
	}
	if len(ids) > 0 {
		if err := r.repo.FetchBlobs(ctx, ids); err != nil {
			return s.netFailure(r, err)
		}
		if ids, err = r.repo.MissingAll(ctx); err != nil {
			return err
		}
		if len(ids) > 0 {
			return fmt.Errorf("%d objects of the clone could not be fetched", len(ids))
		}
	}
	if err := r.repo.LeavePartial(ctx); err != nil {
		return err
	}
	r.partial.Store(false)
	r.repo.SetEnsure(nil)
	s.cm.git.r().SetEnsure(nil)
	return nil
}
