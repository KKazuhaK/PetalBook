// Package assetrepo is a space's attachment repository (milestone 3.1,
// docs/api.md "Attachment repository"): a second Git repository that holds the
// files of the space's assets/ folder, read through a cache of bounded size.
//
// The server keeps a clone with every commit and tree and no contents (the
// "meta" repository). From it comes the table of the repository's current
// files: path, content id and, where known, size. A read looks the path up in
// the table (a path that is not in it is not found, without asking the
// remote), then serves the content from the attachment cache, fetching it by
// its id first when it is not there. New files are pushed before a request is
// answered, from a scratch repository that borrows the meta one's objects
// (gitstore.Repo.Scratch), so contents never stay in the meta repository.
package assetrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/assetcache"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/space"
)

// MaxCommitBytes is how much one commit may add: GitHub refuses a push over
// 2 GB, so the files of a move go in commits of at most this much.
var MaxCommitBytes int64 = 500 << 20

// How many fetches by id run at once: for one space, and for the server.
const (
	fetchesPerSpace = 4
	fetchesOverall  = 16
)

// fetchSlots bounds the fetches by id of every space together.
var fetchSlots = make(chan struct{}, fetchesOverall)

// fetchBatch is how many files a pinned space fetches in one request: the
// scratch repository holds a batch at a time.
const fetchBatch = 1000

// Options configures Open.
type Options struct {
	Space  string // the id of the space: the cache's folder for it
	Dir    string // the meta repository
	URL    string
	Branch string
	Cred   gitstore.Credential
	Cache  *assetcache.Cache
	// Status is what was stored of the repository's state.
	Status space.RemoteStatus
	// OnStatus is told when the state changes; it persists it.
	OnStatus func(space.RemoteStatus)
	// OnUsed is told when the first file has gone to the repository; when it
	// fails it is told again at the next push.
	OnUsed func() error
	// Runner and AllowUnsafe are for tests.
	Runner      gitstore.Runner
	AllowUnsafe bool
	NetTimeout  time.Duration
	// FetchFresh: a refresh is skipped when one succeeded this recently (default 5 s).
	FetchFresh time.Duration
	Logger     *slog.Logger
}

type entry struct {
	id   string
	size int64 // -1 when not known
}

// Repo is safe for concurrent use.
type Repo struct {
	o    Options
	meta *gitstore.Repo
	log  *slog.Logger
	ctx  context.Context
	stop context.CancelFunc

	// opMu serialises the operations that change the repository or move the
	// table: a refresh and a push.
	opMu      sync.Mutex
	lastFresh time.Time

	mu    sync.RWMutex
	table map[string]entry
	tip   string // the commit the table is of
	st    space.RemoteStatus

	fmu     sync.Mutex
	flights map[string]*flight
	slots   chan struct{} // bounds the fetches by id of this space

	used atomic.Bool
	seq  atomic.Int64
}

type flight struct {
	done chan struct{}
	err  error
}

// Open prepares the meta repository and reads the table from what it has.
// No network is used: the first Refresh brings it up to date.
func Open(ctx context.Context, o Options) (*Repo, error) {
	if o.Branch == "" {
		o.Branch = "main"
	}
	if o.FetchFresh <= 0 {
		o.FetchFresh = 5 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	meta := gitstore.New(gitstore.Options{Dir: o.Dir, Runner: o.Runner, Cred: o.Cred, NetTimeout: o.NetTimeout, AllowUnsafe: o.AllowUnsafe})
	if !meta.IsRepo(ctx) || !isOwnRepo(o.Dir) {
		if err := os.MkdirAll(o.Dir, 0o755); err != nil {
			return nil, err
		}
		if err := meta.Init(ctx, o.Branch); err != nil {
			return nil, err
		}
	}
	if meta.RemoteURL(ctx) != o.URL {
		if err := meta.SetRemote(ctx, o.URL); err != nil {
			return nil, err
		}
	}
	r := &Repo{o: o, meta: meta, log: o.Logger, table: map[string]entry{}, flights: map[string]*flight{}, slots: make(chan struct{}, fetchesPerSpace), st: o.Status}
	if r.st.State == "" {
		r.st.State = space.RemoteOK
	}
	r.ctx, r.stop = context.WithCancel(context.Background())
	r.removeScratch()
	if tip := meta.RemoteTip(ctx, o.Branch); tip != "" {
		if err := r.loadTable(ctx, tip); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// isOwnRepo: the directory has its own .git (not one found in a parent).
func isOwnRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// Close ends fetches that are under way.
func (r *Repo) Close() { r.stop() }

// Dir is the meta repository's directory.
func (r *Repo) Dir() string { return r.o.Dir }

func (r *Repo) scratchDir() string { return filepath.Join(r.o.Dir, ".git", "petalbook-scratch") }

// removeScratch removes what an earlier run left of its scratch repositories:
// they hold nothing that is not on the remote (a push is made before anything
// is answered, and what a move still has to copy is read from the space again).
func (r *Repo) removeScratch() { os.RemoveAll(r.scratchDir()) }

func (r *Repo) newScratch(ctx context.Context) (*gitstore.Repo, string, error) {
	dir := filepath.Join(r.scratchDir(), strconv.FormatInt(r.seq.Add(1), 10)+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.MkdirAll(r.scratchDir(), 0o755); err != nil {
		return nil, "", err
	}
	s, err := r.meta.Scratch(ctx, dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, "", err
	}
	return s, dir, nil
}

// --- the table ----------------------------------------------------------------------

func (r *Repo) loadTable(ctx context.Context, tip string) error {
	es, err := r.meta.ListTree(ctx, tip)
	if err != nil {
		return err
	}
	t := make(map[string]entry, len(es))
	for _, e := range es {
		if e.Type == "blob" {
			t[e.Path] = entry{e.ID, -1}
		}
	}
	r.mu.Lock()
	r.table, r.tip = t, tip
	r.mu.Unlock()
	return nil
}

// Info is what the table says of a file.
func (r *Repo) lookup(rel string) (entry, bool) {
	r.mu.RLock()
	e, ok := r.table[rel]
	r.mu.RUnlock()
	return e, ok
}

// Lookup is the content id and size (-1 when not known) of the file at rel
// (below the root of the repository), from the table in memory.
func (r *Repo) Lookup(rel string) (space.AssetInfo, bool) {
	e, ok := r.lookup(rel)
	return space.AssetInfo{ID: e.id, Size: e.size}, ok
}

// Count is how many files the table has.
func (r *Repo) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.table)
}

// Tip is the commit the table is of.
func (r *Repo) Tip() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tip
}

// Files returns a copy of the table: path to content id.
func (r *Repo) Files() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.table))
	for p, e := range r.table {
		out[p] = e.id
	}
	return out
}

// Status is what the server knows of the repository right now.
func (r *Repo) Status() space.RemoteStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.st
}

func (r *Repo) setStatus(f func(*space.RemoteStatus)) {
	r.mu.Lock()
	before := r.st
	f(&r.st)
	st := r.st
	r.mu.Unlock()
	if r.o.OnStatus != nil && (st.State != before.State || st.Kind != before.Kind || st.Error != before.Error || before.LastSync.IsZero() || st.LastSync.Sub(before.LastSync) > 30*time.Second) {
		r.o.OnStatus(st)
	}
}

func (r *Repo) markOK() {
	r.setStatus(func(st *space.RemoteStatus) {
		st.State, st.Kind, st.Error, st.LastSync = space.RemoteOK, "", "", time.Now().UTC()
	})
}

// fail records what a failed network command says about the repository and
// returns the error a caller passes on.
func (r *Repo) fail(err error) error {
	var ne *gitstore.NetError
	msg := "could not reach the attachment repository"
	if errors.As(err, &ne) {
		msg = ne.Msg
	}
	msg = gitstore.Scrub(msg)
	var ce *gitstore.CredentialError
	switch {
	case errors.As(err, &ce) && ce.Code != "" && !ce.Offline:
		r.setStatus(func(st *space.RemoteStatus) { st.State, st.Kind, st.Error = space.RemoteError, ce.Code, msg })
	case gitstore.KindOf(err) == gitstore.KindAuth:
		r.setStatus(func(st *space.RemoteStatus) { st.State, st.Kind, st.Error = space.RemoteError, space.KindAuth, msg })
	case gitstore.KindOf(err) == gitstore.KindHostKey:
		r.setStatus(func(st *space.RemoteStatus) { st.State, st.Kind, st.Error = space.RemoteError, space.KindHostKey, msg })
	default:
		r.setStatus(func(st *space.RemoteStatus) { st.State, st.Kind, st.Error = space.RemoteOffline, "", msg })
	}
	return &space.UnavailableError{Msg: "the attachment repository: " + msg, Err: err}
}

func isNet(err error) bool {
	return gitstore.KindOf(err) != gitstore.KindOther || errors.Is(err, context.Canceled)
}

// Refresh brings the table up to date with the remote: it fetches the commits
// and trees (not the contents), and applies the difference. It returns the
// paths that are new, or whose contents changed, and those that are gone.
// force fetches even when a fetch succeeded a moment ago.
func (r *Repo) Refresh(ctx context.Context, force bool) (added, removed []string, err error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	return r.refreshLocked(ctx, force)
}

func (r *Repo) refreshLocked(ctx context.Context, force bool) (added, removed []string, err error) {
	if !force && !r.lastFresh.IsZero() && time.Since(r.lastFresh) < r.o.FetchFresh {
		return nil, nil, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	has, err := r.meta.HasBranch(ctx, r.o.Branch)
	if err != nil {
		if isNet(err) {
			return nil, nil, r.fail(err)
		}
		return nil, nil, err
	}
	if !has {
		// A new repository with nothing in it, or one whose branch is gone
		// (deleted, or the repository made again, empty): it has no files, so
		// the table has none either, whatever it had before.
		r.mu.Lock()
		for p := range r.table {
			removed = append(removed, p)
		}
		r.table, r.tip = map[string]entry{}, ""
		r.mu.Unlock()
		sort.Strings(removed)
		r.lastFresh = time.Now()
		r.markOK()
		return nil, removed, nil
	}
	old := r.Tip()
	if err := r.meta.FetchFiltered(ctx, r.o.Branch, old == ""); err != nil {
		if isNet(err) {
			return nil, nil, r.fail(err)
		}
		return nil, nil, err
	}
	tip := r.meta.RemoteTip(ctx, r.o.Branch)
	r.lastFresh = time.Now()
	r.markOK()
	if tip == "" || tip == old {
		return nil, nil, nil
	}
	if old == "" {
		if err := r.loadTable(ctx, tip); err != nil {
			return nil, nil, err
		}
		for p := range r.Files() {
			added = append(added, p)
		}
		sort.Strings(added)
		return added, nil, nil
	}
	changes, err := r.meta.DiffRaw(ctx, old, tip)
	if err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	for _, c := range changes {
		if c.Status == 'D' {
			delete(r.table, c.Old)
			removed = append(removed, c.Old)
			continue
		}
		if c.Status == 'A' || c.Status == 'M' || c.Status == 'T' {
			r.table[c.New] = entry{c.NewID, -1}
			added = append(added, c.New)
		}
	}
	r.tip = tip
	r.mu.Unlock()
	return added, removed, nil
}

// --- reading ------------------------------------------------------------------------

// Open opens the cached contents of the file at rel, fetching them by their id
// first when they are not in the cache. A path the table does not have is
// space.ErrNotFound without asking the remote. When the contents cannot be had
// the answer is a *space.NotCachedError.
func (r *Repo) Open(ctx context.Context, rel string) (*os.File, space.AssetInfo, error) {
	e, ok := r.lookup(rel)
	if !ok {
		return nil, space.AssetInfo{}, space.ErrNotFound
	}
	info := space.AssetInfo{ID: e.id, Size: e.size}
	if f, fi, err := r.o.Cache.Open(r.o.Space, e.id); err == nil {
		info.Size = fi.Size()
		return f, info, nil
	}
	if err := r.ensure(ctx, e.id); err != nil {
		return nil, info, err
	}
	f, fi, err := r.o.Cache.Open(r.o.Space, e.id)
	if err != nil {
		// Fetched and trimmed away at once: the budget is smaller than the file.
		return nil, info, &space.NotCachedError{Err: err}
	}
	info.Size = fi.Size()
	return f, info, nil
}

// Cached reports whether the contents of the file at rel are in the cache.
func (r *Repo) Cached(rel string) bool {
	e, ok := r.lookup(rel)
	return ok && r.o.Cache.Has(r.o.Space, e.id)
}

// ensure fetches the contents by id and waits for them, up to space.FetchWait;
// later readers of the same file share the fetch.
func (r *Repo) ensure(ctx context.Context, id string) error {
	r.fmu.Lock()
	f := r.flights[id]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		r.flights[id] = f
		go func() {
			err := r.fetchOne(id)
			f.err = err
			r.fmu.Lock()
			delete(r.flights, id)
			r.fmu.Unlock()
			close(f.done)
		}()
	}
	r.fmu.Unlock()
	timer := time.NewTimer(space.FetchWait)
	defer timer.Stop()
	select {
	case <-f.done:
		if f.err != nil {
			return &space.NotCachedError{Err: f.err}
		}
		return nil
	case <-timer.C:
		return &space.NotCachedError{Fetching: true}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fetchOne fetches one file's contents into a scratch repository and copies
// them into the cache.
func (r *Repo) fetchOne(id string) error {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Minute)
	defer cancel()
	return r.fetchIDs(ctx, []string{id})
}

// slot waits for a fetch to be allowed: one of the space's, then one of the
// server's. The answer gives them back.
func (r *Repo) slot(ctx context.Context) (func(), error) {
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case fetchSlots <- struct{}{}:
	case <-ctx.Done():
		<-r.slots
		return nil, ctx.Err()
	}
	return func() { <-fetchSlots; <-r.slots }, nil
}

// fetchIDs fetches these contents in one request into a scratch repository
// and copies them into the cache.
func (r *Repo) fetchIDs(ctx context.Context, ids []string) error {
	done, err := r.slot(ctx)
	if err != nil {
		return err
	}
	defer done()
	s, dir, err := r.newScratch(ctx)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := s.FetchBlobs(ctx, ids); err != nil {
		if isNet(err) {
			return r.fail(err)
		}
		return err
	}
	for _, id := range ids {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(s.CatBlob(ctx, id, pw)) }()
		_, err = r.o.Cache.Put(r.o.Space, id, pr)
		pr.CloseWithError(err)
		if err != nil {
			return err
		}
	}
	r.markOK()
	return nil
}

// --- writing ------------------------------------------------------------------------

// ErrNameTaken: the repository has a file at the path with other contents.
var ErrNameTaken = space.ErrExists

// File is a file to put in the repository: Data is its contents, or Path the
// file they are in (the files of a move are not read into memory).
type File struct {
	Rel  string
	Data []byte
	Path string
	// ID is the content id when the caller knows it (Path files); filled in.
	ID   string
	Size int64
}

// Put commits the files to the repository and pushes them: one commit, on the
// latest state of the remote, tried again on top of a newer one when the push
// was not accepted. A file already there with the same contents is left as it
// is; with other contents the answer is ErrNameTaken and nothing is pushed.
// The answer is an *space.UnavailableError when the remote cannot be reached;
// nothing is then kept.
func (r *Repo) Put(ctx context.Context, files []File, author, committer gitstore.Identity, message string) error {
	if len(files) == 0 {
		return nil
	}
	r.opMu.Lock()
	defer r.opMu.Unlock()
	for attempt := 0; attempt < 4; attempt++ {
		if _, _, err := r.refreshLocked(ctx, true); err != nil {
			return err
		}
		pushed, err := r.putOnce(ctx, files, author, committer, message)
		if err == nil {
			if !pushed {
				return nil
			}
			if _, _, err := r.refreshLocked(ctx, true); err != nil {
				// Pushed: the files are safe. The table catches up at the next refresh.
				r.log.Warn("could not refresh the attachment table after a push", "error", err)
			}
			return nil
		}
		if gitstore.KindOf(err) == gitstore.KindRejected {
			continue // the remote moved: build it again on the new tip
		}
		return err
	}
	return &space.UnavailableError{Msg: "the attachment repository keeps changing; try again in a moment"}
}

func (r *Repo) putOnce(ctx context.Context, files []File, author, committer gitstore.Identity, message string) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	tip := r.Tip()
	s, dir, err := r.newScratch(ctx)
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	// What has to go: not the files that are there with the same contents.
	var paths []string
	var todo []File
	for _, f := range files {
		if !gitstore.AssetPath(f.Rel) {
			return false, fmt.Errorf("%w: %q is not a path an attachment can have", space.ErrNotFound, f.Rel)
		}
		id := f.ID
		if id == "" && f.Data != nil {
			id = gitstore.BlobID(f.Data)
		}
		if id == "" {
			return false, fmt.Errorf("the content id of %s is not known", f.Rel)
		}
		if e, ok := r.lookup(f.Rel); ok {
			if e.id == id {
				continue
			}
			return false, ErrNameTaken
		}
		todo = append(todo, f)
		paths = append(paths, "")
	}
	if len(todo) == 0 {
		return false, nil
	}
	for i := range todo {
		f := &todo[i]
		if f.Data != nil {
			p := filepath.Join(dir, ".data-"+strconv.Itoa(i))
			if err := os.WriteFile(p, f.Data, 0o600); err != nil {
				return false, err
			}
			paths[i] = p
		} else {
			fi, err := os.Lstat(f.Path)
			if err != nil || !fi.Mode().IsRegular() {
				return false, fmt.Errorf("%s is not a regular file", f.Rel)
			}
			paths[i] = f.Path
		}
	}
	ids, err := s.HashFiles(ctx, paths)
	if err != nil {
		return false, err
	}
	put := map[string]string{}
	for i, f := range todo {
		put[f.Rel] = ids[i]
	}
	if len(put) == 0 {
		return false, nil
	}
	if tip != "" {
		if err := s.SetHead(ctx, tip); err != nil {
			return false, err
		}
	}
	tree, _, err := s.EditTree(ctx, s.TreeOf(ctx, tip), gitstore.TreeEdit{Put: put})
	if err != nil {
		return false, err
	}
	if _, err := s.CommitTree(ctx, tree, tip, message, author, committer); err != nil {
		return false, err
	}
	if err := s.Push(ctx, r.o.Branch); err != nil {
		if isNet(err) && gitstore.KindOf(err) != gitstore.KindRejected {
			return false, r.fail(err)
		}
		return false, err
	}
	// Under opMu: one push at a time records it.
	if !r.used.Load() {
		if r.o.OnUsed == nil {
			r.used.Store(true)
		} else if err := r.o.OnUsed(); err != nil {
			r.log.Warn("could not record that a file went to the attachment repository; it is tried again at the next push", "error", err)
		} else {
			r.used.Store(true)
		}
	}
	// What was uploaded from memory is in the cache from the start.
	for i, f := range todo {
		if f.Data != nil {
			if _, err := r.o.Cache.Put(r.o.Space, ids[i], bytesReader(f.Data)); err != nil {
				r.log.Warn("could not cache an uploaded attachment", "error", err)
			}
		}
	}
	return true, nil
}

// Contains reports which of the files (path to content id) the table has with
// exactly those contents, after a refresh from the remote: what a move checks
// before the space's own copy is taken away.
// The check reads the tree of the commit the remote's branch is at, fetched
// just now, not the table: the table is kept up to date by differences and
// must not be what lets the space's own copy go.
func (r *Repo) Missing(ctx context.Context, want map[string]string) ([]string, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if _, _, err := r.refreshLocked(ctx, true); err != nil {
		return nil, err
	}
	have := map[string]string{}
	if tip := r.Tip(); tip != "" {
		if remote := r.meta.RemoteTip(ctx, r.o.Branch); remote != tip {
			return nil, fmt.Errorf("the attachment repository's branch is at %s, the table at %s", remote, tip)
		}
		es, err := r.meta.ListTree(ctx, tip)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if e.Type == "blob" {
				have[e.Path] = e.ID
			}
		}
	}
	var miss []string
	for p, id := range want {
		if have[p] != id {
			miss = append(miss, p)
		}
	}
	sort.Strings(miss)
	return miss, nil
}

// Remove deletes the meta repository and the cached contents of the space: it
// is not the repository's remote that is touched, never.
func (r *Repo) Remove() error {
	r.Close()
	err := os.RemoveAll(r.o.Dir)
	if cerr := r.o.Cache.DropSpace(r.o.Space); err == nil {
		err = cerr
	}
	return err
}

// Rel converts the path of a file of the space (assets/<rest>) to the path in
// the repository; ok is false for a path outside assets/.
func Rel(p string) (string, bool) {
	rest, ok := strings.CutPrefix(path.Clean(p), space.AssetsDir+"/")
	return rest, ok && rest != ""
}

var _ = fs.ErrNotExist

// FetchAll fetches the contents of every file of the table that is not in the
// cache: a pinned space keeps all its attachments.
// They are fetched in batches of fetchBatch, one request each.
func (r *Repo) FetchAll(ctx context.Context) error {
	seen := map[string]bool{}
	var todo []string
	for _, id := range r.Files() {
		if !seen[id] && !r.o.Cache.Has(r.o.Space, id) {
			seen[id] = true
			todo = append(todo, id)
		}
	}
	sort.Strings(todo)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()
	for len(todo) > 0 {
		n := min(len(todo), fetchBatch)
		if err := r.fetchIDs(ctx, todo[:n]); err != nil {
			return err
		}
		todo = todo[n:]
	}
	return nil
}

// CheckClean says why the repository cannot be said to hold nothing that is
// not on its remote ("" when it can): an upload or a move is pushing right now,
// or a scratch repository of an earlier one is left. An error means the remote
// cannot be reached, so what is there cannot be checked. What is pushed is
// pushed before anything is answered, so nothing else can be waiting.
func (r *Repo) CheckClean(ctx context.Context) (string, error) {
	if !r.opMu.TryLock() {
		return "attachments are being pushed to its attachment repository", nil
	}
	defer r.opMu.Unlock()
	if es, err := os.ReadDir(r.scratchDir()); err == nil && len(es) > 0 {
		return "an upload to its attachment repository is not finished", nil
	}
	if _, _, err := r.refreshLocked(ctx, true); err != nil {
		return "", err
	}
	return "", nil
}
