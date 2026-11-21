// Package drafts keeps what people are typing apart from the page files. A
// draft is one user's unsaved version of one page, stored in the state
// database; it becomes the file, and a Git commit, when the user commits it or
// when a sweeper finds it has been left long enough.
package drafts

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/KazuhaHub/PetalBook/internal/page"
	"github.com/KazuhaHub/PetalBook/internal/space"
	"github.com/KazuhaHub/PetalBook/internal/spaces"
	"github.com/KazuhaHub/PetalBook/internal/store"
)

// Defaults of docs/api.md.
const (
	DefaultIdle = 2 * time.Minute
	DefaultMax  = 10 * time.Minute
	DefaultTick = 15 * time.Second

	// batch is how many due drafts one sweep takes; more wait for the next tick.
	batch = 50
	// retryAfter is how long a draft that failed for a reason that may pass
	// (disk, Git) waits before the sweeper tries again.
	retryAfter = time.Minute
)

// Config sets the timing.
type Config struct {
	Idle, Max, Tick time.Duration
	Now             func() time.Time // default time.Now
}

// Draft is a stored draft.
type Draft struct {
	Version string
	BaseRev string
	Meta    page.Meta
	Body    string
	Updated time.Time
	Stale   bool
}

// Conflict is the answer when the draft on the server is not the one the
// caller meant: Draft is what there is (nil when there is none), Current the
// page as the file has it.
type Conflict struct {
	Draft   *Draft
	Current *space.Page
}

func (*Conflict) Error() string { return "the draft on the server is not the one expected" }

// Service is safe for concurrent use.
type Service struct {
	st     *store.Store
	spaces *spaces.Manager
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	// locks serialise work on one page's drafts (a user's store, commit and
	// save against each other, the sweeper, and the page's deletion) without a
	// lock per page: a page hashes to one of these.
	locks [64]sync.Mutex
	// hook, if set, is called before the sweeper applies a draft (tests).
	hook func(spaceID, pageID, userID string)
	// afterSave, if set, is called when a file has been written and the
	// draft's bookkeeping is still to do (tests).
	afterSave func()
	// beforeLock, if set, is called by Put just before it takes the lock (tests).
	beforeLock func()
}

// New returns the service and brings the due times of waiting drafts in line
// with cfg, so changed flags apply to drafts an earlier run stored.
func New(ctx context.Context, st *store.Store, mgr *spaces.Manager, cfg Config, log *slog.Logger) (*Service, error) {
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
	}
	if cfg.Max <= 0 {
		cfg.Max = DefaultMax
	}
	if cfg.Tick <= 0 {
		cfg.Tick = DefaultTick
	}
	if cfg.Max < cfg.Idle {
		return nil, errors.New("the draft maximum must not be shorter than the idle time")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := store.RescheduleDrafts(ctx, st.Q(), cfg.Idle, cfg.Max); err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, spaces: mgr, cfg: cfg, log: log, now: now}, nil
}

// lock is the lock of a page. It is per page, not per draft, so that deleting
// a page can keep every user's stores out while it removes their drafts.
func (s *Service) lock(spaceID, pageID, _ string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(spaceID + "\x00" + pageID))
	return &s.locks[h.Sum32()%uint32(len(s.locks))]
}

// bookkeeping returns a context for what must be finished once a file has been
// written: not cancelled when the client goes away or the server is stopping,
// but bounded. Without it a dropped request leaves a draft that is already in
// the file, which later shows up as a false conflict.
func bookkeeping(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

func toDraft(d *store.Draft, currentRev string) (*Draft, error) {
	var meta page.Meta
	if err := meta.UnmarshalJSON([]byte(d.Meta)); err != nil {
		return nil, err
	}
	return &Draft{Version: d.Version, BaseRev: d.BaseRev, Meta: meta, Body: d.Body, Updated: d.Updated, Stale: d.BaseRev != currentRev}, nil
}

// Stored is the answer to storing a draft.
type Stored struct {
	Version string
	BaseRev string
	Updated time.Time
	Stale   bool
}

// Put stores or replaces the user's draft of a page. prev is the version it
// replaces, "" when the caller knows of none; if the draft on the server is not
// that one the answer is a *Conflict and nothing is stored. The checks are
// those of saving the page; the file is not touched. It answers
// space.ErrNotFound for a page that does not exist and *space.InvalidError for
// content that a save would refuse.
func (s *Service) Put(ctx context.Context, sp *space.Space, spaceID, userID, pageID, prev, baseRev string, meta page.Meta, body string) (*Stored, error) {
	// PostgreSQL cannot store a NUL in text; refuse it on every database so a
	// draft behaves the same wherever the server runs.
	if strings.IndexByte(body, 0) >= 0 {
		return nil, &space.InvalidError{Msg: "a draft cannot contain a NUL character"}
	}
	mj, err := meta.MarshalJSON()
	if err != nil {
		return nil, err
	}
	if s.beforeLock != nil {
		s.beforeLock()
	}
	l := s.lock(spaceID, pageID, userID)
	l.Lock()
	defer l.Unlock()
	// Checked under the lock: a page deleted, or a file saved, while this store
	// waited is seen as it is now (no draft for a deleted page; a true "stale").
	cur, err := sp.CheckDraft(pageID, meta, body)
	if err != nil {
		return nil, err
	}
	d, err := store.PutDraft(ctx, s.st.Q(), store.Draft{
		SpaceID: spaceID, PageID: pageID, UserID: userID, BaseRev: baseRev, Meta: string(mj), Body: body, Updated: s.now().UTC().Truncate(time.Millisecond),
	}, prev, s.cfg.Idle, s.cfg.Max)
	var dc *store.DraftConflict
	if errors.As(err, &dc) {
		return nil, s.conflict(dc.Current, cur)
	}
	if err != nil {
		return nil, err
	}
	return &Stored{Version: d.Version, BaseRev: d.BaseRev, Updated: d.Updated, Stale: d.BaseRev != cur.Rev}, nil
}

func (s *Service) conflict(d *store.Draft, cur *space.Page) error {
	c := &Conflict{Current: cur}
	if d != nil {
		var err error
		if c.Draft, err = toDraft(d, cur.Rev); err != nil {
			return err
		}
	}
	return c
}

// Get returns the user's draft of a page, or nil. currentRev is the file's rev
// now, to tell whether the draft is stale.
func (s *Service) Get(ctx context.Context, spaceID, pageID, userID, currentRev string) (*Draft, error) {
	d, err := store.GetDraft(ctx, s.st.Q(), spaceID, pageID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toDraft(d, currentRev)
}

// Delete discards the user's draft of a page, if any. With a version it
// discards only that one, and answers a *Conflict (Draft nil when there is
// none any more) when the draft is another; a page that is gone is ErrNotFound.
// With a rev as well, the file must be at that rev, or the answer is the same
// *Conflict and nothing is deleted: a client that gives up its draft because
// its text "equals the file" learns that the file is no longer the one it means.
func (s *Service) Delete(ctx context.Context, sp *space.Space, spaceID, pageID, userID, version, rev string) error {
	l := s.lock(spaceID, pageID, userID)
	l.Lock()
	defer l.Unlock()
	if version == "" {
		return store.DeleteDraft(ctx, s.st.Q(), spaceID, pageID, userID)
	}
	if rev != "" {
		cur, err := sp.Get(pageID)
		if err != nil {
			return err
		}
		if cur.Rev != rev {
			d, err := store.GetDraft(ctx, s.st.Q(), spaceID, pageID, userID)
			if errors.Is(err, store.ErrNotFound) {
				d, err = nil, nil
			}
			if err != nil {
				return err
			}
			return s.conflict(d, cur)
		}
	}
	ok, err := store.DeleteDraftVersion(ctx, s.st.Q(), spaceID, pageID, userID, version)
	if err != nil || ok {
		return err
	}
	// The draft is not that version, or there is none: the server may have
	// committed it on its own, so the caller is told, with the page as it is.
	d, err := store.GetDraft(ctx, s.st.Q(), spaceID, pageID, userID)
	if errors.Is(err, store.ErrNotFound) {
		d, err = nil, nil
	}
	if err != nil {
		return err
	}
	cur, err := sp.Get(pageID)
	if err != nil {
		return err
	}
	return s.conflict(d, cur)
}

// DeletePage discards every user's draft of a page that no longer exists.
func (s *Service) DeletePage(ctx context.Context, spaceID, pageID string) error {
	l := s.lock(spaceID, pageID, "")
	l.Lock()
	defer l.Unlock()
	return store.DeletePageDrafts(ctx, s.st.Q(), spaceID, pageID)
}

// DeletePageFile deletes a page and, in the same exclusion, every user's draft
// of it. A draft store racing the delete either lands before it and is removed
// with the rest, or finds the page gone and is refused: it can never leave a
// draft behind for a page that no longer exists.
func (s *Service) DeletePageFile(ctx context.Context, sp *space.Space, spaceID, pageID string) error {
	l := s.lock(spaceID, pageID, "")
	l.Lock()
	defer l.Unlock()
	if err := sp.Delete(pageID); err != nil {
		return err
	}
	bk, cancel := bookkeeping(ctx)
	defer cancel()
	if err := store.DeletePageDrafts(bk, s.st.Q(), spaceID, pageID); err != nil {
		// The page is deleted; failing the request would tell the person it was not.
		s.log.Warn("could not discard the drafts of a deleted page", "space", spaceID, "page", pageID, "error", err)
	}
	return nil
}

// SaveFile saves a page directly, as PUT page does. The user's draft is left
// alone: a draft holds text that may exist nowhere else, so nothing but a
// commit, a store that names it, an explicit delete or the deletion of the page
// ever removes one. After the save it is usually stale, and whoever opens the
// page is shown both. The one exception is a draft that says exactly what was
// just saved: it has nothing left to add and goes (only the version seen before
// the save, under the draft's lock).
func (s *Service) SaveFile(ctx context.Context, sp *space.Space, spaceID, userID, pageID, baseRev string, meta page.Meta, body string) (*space.Page, error) {
	l := s.lock(spaceID, pageID, userID)
	l.Lock()
	defer l.Unlock()
	seen, err := store.GetDraft(ctx, s.st.Q(), spaceID, pageID, userID)
	if errors.Is(err, store.ErrNotFound) {
		seen, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := sp.Save(pageID, baseRev, meta, body)
	if err != nil {
		return nil, err
	}
	if s.afterSave != nil {
		s.afterSave()
	}
	if seen != nil {
		var dm page.Meta
		if err := dm.UnmarshalJSON([]byte(seen.Meta)); err != nil {
			return p, nil
		}
		if _, same, err := sp.Equivalent(pageID, dm, seen.Body); err == nil && same {
			bk, cancel := bookkeeping(ctx)
			defer cancel()
			if _, err := store.DeleteDraftVersion(bk, s.st.Q(), spaceID, pageID, userID, seen.Version); err != nil {
				s.log.Warn("could not remove a draft that equals the saved page", "error", err)
			}
		}
	}
	return p, nil
}

// Commit writes the user's draft, which must be that version, to the file
// exactly as a save with the draft's base would, and removes the draft. It
// answers a *Conflict, writing nothing, when the draft is another version, is
// gone, or is stale (a stale draft is kept and held back from the sweeper); a
// draft that is stale but says what the file says is simply removed. A page
// that is not there is space.ErrNotFound and the draft is kept: its text
// exists nowhere else.
func (s *Service) Commit(ctx context.Context, sp *space.Space, spaceID, userID, pageID, version string) (*space.Page, error) {
	l := s.lock(spaceID, pageID, userID)
	l.Lock()
	defer l.Unlock()
	cur, err := sp.Get(pageID)
	if err != nil {
		return nil, err
	}
	d, err := store.GetDraft(ctx, s.st.Q(), spaceID, pageID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &Conflict{Current: cur}
	}
	if err != nil {
		return nil, err
	}
	if d.Version != version {
		return nil, s.conflict(d, cur)
	}
	p, err := s.apply(ctx, sp, d)
	var ce *space.ConflictError
	if errors.As(err, &ce) {
		bk, cancel := bookkeeping(ctx)
		defer cancel()
		if herr := store.HoldDraft(bk, s.st.Q(), spaceID, pageID, userID, d.Version); herr != nil {
			s.log.Warn("could not hold a stale draft", "error", herr)
		}
		return nil, s.conflict(d, ce.Current)
	}
	return p, err
}

// apply saves a draft's content and removes the draft if it has not been
// stored again meanwhile. A draft that makes no difference to the file writes
// nothing (Save sees identical bytes) and still counts as applied; so does one
// that is stale but equals what the file holds now.
func (s *Service) apply(ctx context.Context, sp *space.Space, d *store.Draft) (*space.Page, error) {
	var meta page.Meta
	if err := meta.UnmarshalJSON([]byte(d.Meta)); err != nil {
		return nil, err
	}
	p, err := sp.Save(d.PageID, d.BaseRev, meta, d.Body)
	var ce *space.ConflictError
	if errors.As(err, &ce) {
		if cur, same, eerr := sp.Equivalent(d.PageID, meta, d.Body); eerr == nil && same {
			p, err = cur, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if s.afterSave != nil {
		s.afterSave()
	}
	bk, cancel := bookkeeping(ctx)
	defer cancel()
	if _, err := store.DeleteDraftVersion(bk, s.st.Q(), d.SpaceID, d.PageID, d.UserID, d.Version); err != nil {
		return nil, err
	}
	return p, nil
}

// Run sweeps every Tick until ctx ends. It returns after any draft it was
// applying is finished. Drafts not yet due stay in the database for the next
// run: stopping never forces them into the files.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// SweepResult counts what one sweep did.
type SweepResult struct {
	Applied, Held, Failed int
}

// Sweep applies the drafts that are due: one indexed query. A draft that turns
// out stale, whose page is gone, or that the page refuses, is held until it is
// stored again (never deleted: its text may be all there is); one that failed
// for another reason is tried again later.
func (s *Service) Sweep(ctx context.Context) SweepResult {
	var r SweepResult
	now := s.now()
	due, err := store.DueDrafts(ctx, s.st.Q(), now, batch)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("could not look for due drafts", "error", err)
		}
		return r
	}
	for i := range due {
		if ctx.Err() != nil {
			break
		}
		d := due[i]
		if s.hook != nil {
			s.hook(d.SpaceID, d.PageID, d.UserID)
		}
		s.sweepOne(ctx, d, now, &r)
	}
	return r
}

// Lock order, everywhere: a space is acquired (a request's middleware, or the
// sweeper here) before a page's draft lock is taken, and nothing that can be
// slow (opening a space, which may rebuild an index) is done while holding a
// draft lock for something else. So the sweeper opens the space first and only
// then locks, and re-reads the draft under the lock.
func (s *Service) sweepOne(ctx context.Context, d store.Draft, now time.Time, r *SweepResult) {
	sp, release, err := s.spaces.Acquire(ctx, d.SpaceID)
	if err != nil {
		s.log.Warn("could not open the space of a draft", "space", d.SpaceID, "error", err)
		s.delay(ctx, &d, now, r)
		return
	}
	defer release()
	l := s.lock(d.SpaceID, d.PageID, d.UserID)
	l.Lock()
	defer l.Unlock()
	// The user may have committed or replaced it since the query.
	cur, err := store.GetDraft(ctx, s.st.Q(), d.SpaceID, d.PageID, d.UserID)
	if err != nil || cur.Held || cur.Due.After(now) {
		return
	}
	_, err = s.apply(ctx, sp, cur)
	var (
		ce  *space.ConflictError
		inv *space.InvalidError
	)
	bk, cancel := bookkeeping(ctx)
	defer cancel()
	switch {
	case err == nil:
		r.Applied++
	case errors.Is(err, space.ErrNotFound), errors.As(err, &ce), errors.As(err, &inv):
		store.HoldDraft(bk, s.st.Q(), cur.SpaceID, cur.PageID, cur.UserID, cur.Version)
		r.Held++
		if errors.Is(err, space.ErrNotFound) {
			// The page file is gone, so the app can no longer show this draft.
			// It is kept; this line (once, as a held draft is not swept again)
			// says where it is. "petalbook draft export" prints it.
			s.log.Warn("a draft is kept although its page file is gone: its text can be recovered with \"petalbook draft export\"",
				"space", cur.SpaceID, "page", cur.PageID)
		}
	default:
		s.log.Warn("could not apply a draft; will try again", "space", cur.SpaceID, "page", cur.PageID, "error", err)
		s.delay(ctx, cur, now, r)
	}
}

func (s *Service) delay(ctx context.Context, d *store.Draft, now time.Time, r *SweepResult) {
	r.Failed++
	store.DelayDraft(ctx, s.st.Q(), d.SpaceID, d.PageID, d.UserID, d.Version, now.Add(retryAfter))
}
