// Package space is one tree of pages on disk, backed by a Git repository and
// mirrored by an index. It owns every write to the files: atomic replacement,
// optimistic concurrency, naming, and handing changes to the committer.
package space

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/sentence"
)

// Defaults for the committer: a commit after a few quiet seconds, or ten
// minutes after the first uncommitted write. The quiet period is short because
// the long wait now happens in drafts (see the drafts package): by the time a
// write reaches a file the person has stopped typing or asked for it.
const (
	DefaultCommitIdle = 5 * time.Second
	DefaultCommitMax  = 10 * time.Minute
	DefaultName       = "My notes"
	DefaultCommitter  = "PetalBook <petalbook@localhost>"
)

// Options configures Open.
type Options struct {
	Root string // the space directory; created if missing
	// Name is the display name. When empty it is read from .petalbook/space.yml,
	// else DefaultName. It is written to space.yml only when that file is created.
	Name string
	// StateDir holds the index, the journal and the notices. Default:
	// DefaultStateDir(Root), in the Git directory.
	StateDir string
	// Committer is the Git committer of automatic commits, "Name <email>"; it
	// is their author too unless Author is given.
	Committer string
	// Author, "Name <email>", is who the commits are for (the space's owner).
	Author     string
	CommitIdle time.Duration
	CommitMax  time.Duration
	// ResyncInterval is the shortest time between two index re-syncs
	// triggered by a lookup that missed (default 2 s).
	ResyncInterval time.Duration
	Logger         *slog.Logger
	// Remote, when set, makes the space follow a remote repository (see
	// remote.go): changes are pushed before they are answered, the background
	// committer is not used for them, and what a crash left behind is dealt with.
	Remote *RemoteOptions
	// WriteGate, when set, is asked before every change to the files: an error
	// it returns refuses the change (the storage policy's deadline, see
	// internal/spaces). Drafts are not files of the space and never ask it.
	WriteGate func() error
	// OnRebuild, when set, is told the phases of a big sync of the index that
	// Open makes (index.PhaseWaiting while it waits for one of the server's
	// slots, then index.PhaseRebuilding), while Open runs.
	OnRebuild func(phase string)
	// SlotContext, when set, ends the wait of Open's index sync for one of the
	// server's rebuild slots: Open then fails with its error.
	SlotContext context.Context
}

// Space is safe for concurrent use.
type Space struct {
	// step, a test hook (nil otherwise), is told each step of a change that
	// a crash could come after; a test panics in it to stop the change
	// there, or returns an error to fail the step.
	step func(name string) error

	root     string // absolute, symlinks resolved
	stateDir string
	name     string
	idx      *index.Index
	cm       *committer
	log      *slog.Logger

	// gate is Options.WriteGate.
	gate func() error
	// evicted is set for a space opened by OpenIndexOnly: it has an index and
	// no files, so only what the index answers is available.
	evicted bool
	// settingsNote is the settings stamp the space had when it was last closed
	// with its files (SettingsStamp of an evicted space).
	settingsNote string

	mu     sync.Mutex // serialises every change to files and the index
	closed bool       // set by Close, under mu: later changes are refused before they write
	// rem is set while the space follows a remote repository. It is switched
	// under mu.
	rem atomic.Pointer[remote]
	// assets is the attachment repository (see attachments.go).
	assets atomic.Pointer[AssetBackend]
	// assetsOurs caches assetsDirIsOurs: a generation times 4, plus 0 not
	// known, 1 yes, 2 no. A change of the space, a pull, a walk and a switch of
	// the attachments forget it (the next generation), so that an answer looked
	// up before one of them is never kept after it.
	assetsOurs atomic.Int64
	// ctx ends when the space is closed; network commands run under it so that
	// a hung remote cannot keep Close waiting.
	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time
	ops    fileOps

	// cfg is what was last read of the bindings in space.yml (see config.go).
	cfgMu sync.Mutex
	cfg   configCache
	// heal restores a settings file that cannot be used (see restore.go).
	heal *healer
	// rb remembers the service's rewrites of links, so that a save or a draft
	// based on the rev before one is not a conflict (rebase.go).
	rb *rebaseLog
	// tagCfg is what was last read of the views in tags.yml (see views.go).
	tagCfg tagCache
	// daily is what was picked for the daily text (see daily.go).
	dailyMu sync.Mutex
	daily   dailyMemo
	// typeCache are the type files parsed, by name (see listing.go).
	typeCache map[string]typeCacheEntry
	// typesDirty is set when a change touched a type or a binding: the type
	// problems of the pages are looked at again before the change returns
	// (see checks.go).
	typesDirty atomic.Bool

	// src is the file tree the index mirrors. syncEvery throttles the
	// re-sync a missed lookup triggers, so ids that do not exist (a stale
	// bookmark) cost one directory walk per interval, not one per request.
	src       index.Source
	syncEvery time.Duration
	syncMu    sync.Mutex
	lastSync  time.Time
	walks     atomic.Int64 // re-syncs run, for tests
}

// Errors the API maps to status codes.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	// ErrNoFolder: the path is not a folder of the space.
	ErrNoFolder = errors.New("no such folder")
	// ErrNotEmpty: the folder holds pages and was not to be removed with them.
	ErrNotEmpty = errors.New("the folder holds pages")
	// ErrClosed: the space has been closed (the server is shutting down) and
	// takes no more changes; nothing was written.
	ErrClosed = errors.New("the space is closed")
)

// InvalidError is a request the space refuses because of its content.
type InvalidError struct {
	Msg string
	// Code is a stable error code (docs/api.md) when the refusal has one, else "".
	Code string
}

func (e *InvalidError) Error() string { return e.Msg }

func invalidf(format string, a ...any) error {
	return &InvalidError{Msg: sentence.End(fmt.Sprintf(format, a...))}
}

// CodeBadLanguage is the error code of a language tag that is not one.
const CodeBadLanguage = "bad_language"

// BadLanguage is the refusal of a language tag that is not well formed or
// whose language is not in the IANA registry.
func BadLanguage(tag string) error {
	return &InvalidError{Msg: fmt.Sprintf("%q is not a language tag like zh-Hans, en or pt-BR.", tag), Code: CodeBadLanguage}
}

// ConflictError means the page changed since the client read it.
type ConflictError struct{ Current *Page }

func (e *ConflictError) Error() string { return "the page was changed by someone else" }

// indexFile is the name of a space's index in its state directory.
const indexFile = "index.db"

// StoredHead is the change cursor the index of the closed space with this state
// directory was left at, read without opening the space (see index.StoredHead):
// "" when there is none to give.
func StoredHead(stateDir string) (string, error) {
	return index.StoredHead(filepath.Join(stateDir, indexFile))
}

// NoteRestored notes in the closed index of the space with this state directory
// that its clone was made again from the remote (see index.NoteRestored): the
// change cursors handed out before stay good.
func NoteRestored(stateDir string) error {
	return index.NoteRestored(filepath.Join(stateDir, indexFile))
}

// Open prepares the directory (git init, .petalbook/, space.yml), opens the
// index, brings it up to date with the files and starts the committer. On a
// large space with no usable index this takes as long as a full rebuild.
func Open(opt Options) (*Space, error) {
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if opt.Root == "" {
		return nil, errors.New("space directory not given")
	}
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	author, email, err := ParseCommitter(opt.Committer)
	if err != nil {
		return nil, err
	}
	g := &gitRepo{dir: root, name: author, email: email}
	if opt.Author != "" {
		an, ae, err := ParseCommitter(opt.Author)
		if err != nil {
			return nil, err
		}
		g.setAuthor(an, ae)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); errors.Is(err, fs.ErrNotExist) {
		if opt.Remote != nil {
			// Making an empty repository here would hide pages that were never
			// pushed. Putting the clone back is for an administrator.
			return nil, fmt.Errorf("the repository of this space is missing (%s has no .git): restore it, or disconnect the remote", root)
		}
		if err := g.init(); err != nil {
			return nil, err
		}
		log.Info("initialised a Git repository", "dir", root)
	} else if err != nil {
		return nil, err
	}
	// .git is a folder, or a file or a link that names one (a checkout made
	// with `git worktree add`, a submodule, --separate-git-dir): git says
	// where it is.
	dirs, err := resolveGitDirs(g.r(), root)
	if err != nil {
		return nil, err
	}
	g.dirs = dirs

	heal := &healer{root: root, repo: g.r, log: log, gitDir: dirs.git}
	// A local space writes space.yml at the start when there is none: a link
	// or a file where .petalbook belongs is put right before (see
	// settingspath.go): what is in its place is moved aside and that is
	// committed, and the folder is made again with what it had. What is put
	// back is committed with the first commit. A space that follows a remote
	// writes nothing there by itself; the first change that does puts it
	// right, and pushes that first.
	var refilled []string
	if opt.Remote == nil && firstBlockedOwn(root) != "" {
		b := firstBlockedOwn(root)
		w := wayWriter{
			record: func(rel string, deleted bool, _ []byte) (func(), error) {
				if !deleted {
					refilled = append(refilled, rel)
				}
				return func() {}, nil
			},
			commitNow: func(paths []string) error {
				id, _ := g.identities(Author{})
				_, err := g.commitPaths(id, "", paths, nil)
				var skipped *gitstore.SkippedError
				if errors.As(err, &skipped) {
					err = nil
				}
				for _, p := range paths {
					for j, r := range refilled {
						if r == p {
							refilled = append(refilled[:j], refilled[j+1:]...)
							break
						}
					}
				}
				return err
			},
		}
		if _, err := heal.clearWay(b+"/x", "", w); err != nil {
			return nil, fmt.Errorf("%s is not a folder and could not be put right: %w", b, err)
		}
	}
	ours := refilled // files under .petalbook the server made, for the first commit
	var name string
	if opt.Remote != nil {
		// A space that follows a remote never makes tracked files of its own
		// here: they would be commits nobody pushes. Its name is read, not
		// written.
		name = readSpaceName(root, opt.Name)
	} else {
		d, err := mkdirIn(root, ".petalbook")
		if err != nil {
			return nil, err
		}
		d.Close()
		var madeConfig bool
		if name, madeConfig, err = ensureSpaceConfig(root, opt.Name); err != nil {
			return nil, err
		}
		if madeConfig {
			ours = append(ours, ".petalbook/space.yml")
		}
	}

	stateDir := opt.StateDir
	if stateDir == "" {
		stateDir = stateDirIn(dirs.git)
	}
	if stateDir, err = filepath.Abs(stateDir); err != nil {
		return nil, err
	}
	// Resolve links (macOS temp directories are one) so "inside the space" is
	// decided on real paths.
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	if stateDir, err = filepath.EvalSymlinks(stateDir); err != nil {
		return nil, err
	}
	inTree := func(dir string) (string, bool) {
		rel, err := filepath.Rel(root, dir)
		return filepath.ToSlash(rel), err == nil && filepath.IsLocal(rel) && !within(dirs.git, dir)
	}
	if rel, ok := inTree(stateDir); ok {
		// A state directory given inside the work tree must not be committed.
		if err := g.exclude(rel + "/"); err != nil {
			return nil, err
		}
	}
	// A file of the space is written next to where it goes, under a hidden
	// name of the server's (newTempIn, writeIn), which git is told to leave
	// out. Nothing goes through a folder of temporary files that a pull can
	// replace, or that can be on another file system than the file.
	if err := g.excludeLine(tempPattern); err != nil {
		return nil, err
	}
	// What an earlier run changed but did not get to commit, and any move it
	// left half done, before the index looks at the files.
	jrn, recs, err := openJournal(filepath.Join(stateDir, "pending.journal"), log)
	if err != nil {
		return nil, err
	}
	if rel, ok := inTree(stateDir); ok {
		// A state directory inside the space is never written through
		// something that is not a folder there.
		jrn.guard = func() error {
			if b, err := wayBlocked(root, rel+"/x"); err != nil || b != "" {
				return errors.Join(err, fmt.Errorf("%s is not a folder", b))
			}
			return nil
		}
	}
	heal.setStateDir(stateDir)
	var recovered map[string]Author
	var recoveredRevs map[string]gitstore.Blob
	var giveUp []uint64
	rolledBack := "" // what stops the clone from being put back, if anything
	if opt.Remote != nil {
		// A space that follows a remote answers a change only after its
		// push: one an earlier run did not get to push is taken back, unless
		// its commit is on the remote after all (owner decision 2).
		rolledBack = rollbackRemote(context.Background(), g.r(), opt.Remote.Branch, root, recs, log)
	} else {
		recovered, recoveredRevs, giveUp = recoverJournal(heal, root, recs)
	}
	if len(giveUp) > 0 {
		ks := make([]jrec, len(giveUp))
		for i, tx := range giveUp {
			ks[i] = jrec{Op: "k", Tx: tx}
		}
		if err := jrn.append(ks); err != nil {
			return nil, err
		}
	}
	if len(recovered) > 0 {
		log.Info("recovered uncommitted changes from the journal", "paths", len(recovered))
	}

	idx, err := index.Open(filepath.Join(stateDir, indexFile), root)
	if err != nil {
		return nil, err
	}
	s := &Space{root: root, stateDir: stateDir, name: name, idx: idx, log: log, now: time.Now, ops: osOps(), src: dirSource{root: root}, syncEvery: opt.ResyncInterval, heal: heal, rb: openRebaseLog(rebaseLogPath(stateDir)), gate: opt.WriteGate}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	// A panic from here on leaves nothing open: the caller may recover it and
	// open the space again, which must not find this index and committer running.
	defer func() {
		if r := recover(); r != nil {
			s.cancel()
			if s.cm != nil {
				s.cm.Close()
			}
			idx.Close()
			panic(r)
		}
	}()
	if s.syncEvery <= 0 {
		s.syncEvery = 2 * time.Second
	}
	// The first sync walks every folder of pages, and the attachments, anyway:
	// it notes what a crash left of a write there (isTempName) on the way.
	temps := append(tempsIn(root, ".petalbook"), tempsIn(root, typesDir)...)
	s.watchChecks()
	idx.SetSyncNotify(opt.OnRebuild)
	slotCtx := opt.SlotContext
	if slotCtx == nil {
		slotCtx = context.Background()
	}
	st, err := idx.SyncContext(slotCtx, dirSource{root: root, temps: &temps}, false)
	idx.SetSyncNotify(nil)
	if err != nil {
		idx.Close()
		return nil, err
	}
	removeLeftTemps(root, g, temps, log)
	s.syncTypes()
	log.Info("index ready", "pages", st.Pages, "added", st.Added, "updated", st.Updated, "removed", st.Removed,
		"problems", st.Problems, "ms", st.Elapsed.Milliseconds())
	if st.Problems > 0 {
		if ps, err := idx.Problems(); err == nil {
			for i, p := range ps {
				if i == 10 {
					log.Warn("more files are not usable as pages", "more", len(ps)-10)
					break
				}
				log.Warn("file is not a usable page", "path", p.Path, "code", p.Code, "problem", p.Message)
			}
		}
	}

	idle, max := opt.CommitIdle, opt.CommitMax
	if idle <= 0 {
		idle = DefaultCommitIdle
	}
	if max <= 0 {
		max = DefaultCommitMax
	}
	s.lastSync = time.Now()
	if opt.Remote != nil {
		// What the journal says was written but not committed is handled by
		// initRemote, not by the background committer.
		for _, p := range refilled {
			if recovered == nil {
				recovered = map[string]Author{}
			}
			if _, ok := recovered[p]; !ok {
				recovered[p] = Author{}
			}
		}
		s.cm = newCommitter(g, idle, max, log, jrn, nil, nil)
		s.cm.setOnCommit(heal.committed)
		if err := s.initRemote(opt.Remote, recovered, rolledBack); err != nil {
			s.cm.Close()
			idx.Close()
			return nil, err
		}
		return s, nil
	}
	s.cm = newCommitter(g, idle, max, log, jrn, recovered, recoveredRevs)
	s.cm.setOnCommit(heal.committed)
	// Pages renamed or moved outside PetalBook since it last ran: the links
	// that carry their ids follow them.
	if n, err := s.RepairLinks(); err != nil {
		log.Warn("could not repair links after changes made outside", "error", err)
	} else if n > 0 {
		log.Info("repaired the paths of links to pages that were moved outside", "files", n)
	}
	if len(ours) > 0 {
		// The files we just made, and nothing else, are committed: one
		// transaction with what they hold.
		files := make([]txFile, 0, len(ours))
		for _, p := range ours {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				continue
			}
			files = append(files, txFile{path: p, rev: page.Rev(data), data: data})
		}
		end := s.cm.begin()
		id, err := s.cm.Prepare(Author{}, files)
		if err == nil {
			s.cm.Applied(id)
		}
		end()
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Name is the display name of the space.
func (s *Space) Name() string { return s.name }

// Root is the absolute directory of the space.
func (s *Space) Root() string { return s.root }

// StateDir is where the index and the commit journal are kept.
func (s *Space) StateDir() string { return s.stateDir }

// SetAuthor changes the default author: whom later commits are for when the
// change names nobody (the zero Author). The committer stays the service.
func (s *Space) SetAuthor(name, email string) { s.cm.git.setAuthor(name, email) }

// Close commits what is still uncommitted, stops the committer and closes the
// index. It returns the commit error, if any.
func (s *Space) Close() error {
	s.cancel() // a change waiting on the network ends, so the lock below is not held for long
	// Under the lock: a change in progress finishes first, and none starts after.
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	var err error
	if s.cm != nil {
		err = s.cm.Close()
	}
	// The settings stamp is kept with the index, for the offline sync of the
	// space when it is evicted and has no files to stamp (SettingsStamp).
	var notes map[string]string
	if !s.evicted {
		notes = map[string]string{"settings": s.SettingsStamp()}
	}
	if cerr := s.idx.CloseWith(notes); err == nil {
		err = cerr
	}
	return err
}

// Flush commits pending changes now.
func (s *Space) Flush() error {
	if s.cm == nil {
		return nil
	}
	return s.cm.Flush()
}

// spaceBytes is what an open space holds in memory beside its index: the
// committer, the watchers, the caches of settings and types (measured: a
// 200-page space costs about 2 MB in all).
const spaceBytes = 1 << 20

// MemoryCost estimates the memory the open space holds now, in bytes (see
// index.MemoryCost): cheap, it runs no query. The spaces manager keeps the
// sum over the open spaces under its budget.
func (s *Space) MemoryCost() int64 { return spaceBytes + s.idx.MemoryCost() }

// Evicted reports a space opened by OpenIndexOnly.
func (s *Space) Evicted() bool { return s.evicted }

// ErrIndexUnusable: the index of an evicted space is missing, of another
// schema version or otherwise cannot answer as it is (OpenIndexOnly). The space
// is to be restored, which builds it again from the files.
var ErrIndexUnusable = errors.New("the index of the evicted space cannot be read as it is")

// OpenIndexOnly opens what an evicted space still has: its index. There are no
// files (opt.Root does not exist), so lists, the tree, tags, search, links and
// checks are answered from the index and nothing else is: reading a page,
// writing, history and every other file operation need the space restored (the
// spaces manager does that before it opens the space for them). Nothing is
// created on disk but what the index database needs.
func OpenIndexOnly(opt Options) (*Space, error) {
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if opt.Root == "" || opt.StateDir == "" {
		return nil, errors.New("space directory and state directory not given")
	}
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}
	// The index names the space's directory as Open resolved it; this one does
	// not exist, so its parent is resolved.
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	root = filepath.Join(parent, filepath.Base(root))
	if _, err := os.Lstat(root); err == nil {
		return nil, errors.New("the space has files: it is not evicted")
	}
	stateDir, err := filepath.Abs(opt.StateDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	if stateDir, err = filepath.EvalSymlinks(stateDir); err != nil {
		return nil, err
	}
	// The index must be one that answers as it is: Open would build one of
	// another version (or a missing one) again, empty, with no files to fill
	// it. Such an index is left as it is, for the restore.
	idx, err := index.OpenExisting(filepath.Join(stateDir, "index.db"), root)
	if err != nil {
		if errors.Is(err, index.ErrUnusable) {
			return nil, fmt.Errorf("%w: %v", ErrIndexUnusable, err)
		}
		return nil, err
	}
	// Once the space's directory is there, the full space owns the index
	// (a restore moved it into place): this handle does not say, on closing,
	// that the index was closed cleanly.
	idx.SetCloseGuard(func() bool {
		_, err := os.Lstat(root)
		return errors.Is(err, fs.ErrNotExist)
	})
	s := &Space{root: root, stateDir: stateDir, name: opt.Name, idx: idx, log: log, now: time.Now, evicted: true}
	if s.settingsNote, err = idx.StoredNote("settings"); err != nil {
		idx.Close()
		return nil, err
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// Problems lists files that are not usable pages (no header, no id, duplicate id).
func (s *Space) Problems() ([]index.Problem, error) { return s.idx.Problems() }

// ensureLines appends the lines the file lacks and reports whether it changed.
func ensureLines(file string, lines ...string) (bool, error) {
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	out := string(data)
	changed := false
	for _, l := range lines {
		if !have[l] {
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			out += l + "\n"
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	return true, os.WriteFile(file, []byte(out), 0o644)
}

// readSpaceName is ensureSpaceConfig that never writes.
func readSpaceName(root, name string) string {
	if name != "" {
		return name
	}
	data, _, _, err := readIn(root, spaceFile, maxSpaceFile)
	if err != nil || data == nil {
		return DefaultName
	}
	var cfg struct {
		Name string `yaml:"name"`
	}
	_ = yaml.Unmarshal(data, &cfg)
	if cfg.Name == "" {
		return DefaultName
	}
	return cfg.Name
}

// ensureSpaceConfig creates space.yml with the name if it is missing, and
// returns the name in effect.
func ensureSpaceConfig(root, name string) (string, bool, error) {
	data, _, _, err := readIn(root, spaceFile, maxSpaceFile)
	if errors.Is(err, fs.ErrNotExist) {
		if name == "" {
			name = DefaultName
		}
		out, err := yaml.Marshal(map[string]string{"name": name})
		if err != nil {
			return "", false, err
		}
		return name, true, writeIn(root, spaceFile, out)
	}
	if err != nil {
		return "", false, err
	}
	if name != "" {
		return name, false, nil
	}
	var cfg struct {
		Name string `yaml:"name"`
	}
	// A space.yml we cannot read is the user's to fix; do not refuse to start.
	_ = yaml.Unmarshal(data, &cfg)
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	return cfg.Name, false, nil
}

// --- paths ------------------------------------------------------------------

// checkRel validates a slash-separated path inside the space: no empty,
// "." or ".." segments, no leading or trailing slash, nothing hidden, nothing
// a file system on the three big platforms would refuse. "" is the root.
func checkRel(rel string) error {
	if rel == "" {
		return nil
	}
	if !utf8.ValidString(rel) || len(rel) > 1024 {
		return invalidf("Invalid path.")
	}
	if strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return invalidf("Path %q must not start or end with a slash.", rel)
	}
	for i, seg := range strings.Split(rel, "/") {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return invalidf("Path %q has an empty, . or .. segment.", rel)
		case strings.HasPrefix(seg, "."):
			return invalidf("Path %q: names starting with a dot are reserved.", rel)
		case len(seg) > 255:
			return invalidf("Path %q: a name is too long.", rel)
		case strings.TrimRight(seg, ". ") != seg:
			return invalidf("Path %q: a name ends with a dot or space.", rel)
		case strings.IndexFunc(seg, func(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"\|?*`, r) }) >= 0:
			return invalidf("Path %q: a name has a character that file systems do not allow.", rel)
		case i == 0 && strings.EqualFold(seg, "assets"):
			// In any spelling: on a file system that ignores case, Assets is assets.
			return invalidf("The folder name %q is reserved for attachments.", seg)
		}
	}
	return nil
}

// ValidFolder reports whether rel is an acceptable folder path ("" is the root).
func ValidFolder(rel string) error { return checkRel(rel) }

// abs maps a checked relative path to a file system path, refusing any path
// that goes through a symbolic link (which could lead out of the space).
func (s *Space) abs(rel string) (string, error) {
	if err := checkRel(rel); err != nil {
		return "", err
	}
	p := s.root
	for _, seg := range strings.Split(rel, "/") {
		if rel == "" {
			break
		}
		p = filepath.Join(p, seg)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing below here exists yet, so nothing can be a link.
			return filepath.Join(s.root, filepath.FromSlash(rel)), nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", invalidf("Path %q goes through a symbolic link.", rel)
		}
	}
	return p, nil
}

// --- file tree as the index's source ----------------------------------------

type dirSource struct {
	root string
	// temps, when set, collects the names of files a crash left of a write
	// (isTempName), in the folders of pages and in the attachments, which
	// are then walked too, for that only.
	temps *[]string
}

// ignoredDir reports folders that never hold pages: Git's and our own
// directories, the attachments folder at the root, and any dot-directory.
func ignoredDir(rel, name string) bool {
	return strings.HasPrefix(name, ".") || (rel == name && name == "assets")
}

func (d dirSource) Walk(file func(index.FileInfo) error, dir func(string) error) error {
	return filepath.WalkDir(d.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished while walking
			}
			return err
		}
		rel, _ := filepath.Rel(d.root, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		name := e.Name()
		if e.Type()&fs.ModeSymlink != 0 {
			return nil // never follow links out of the space
		}
		inAssets := d.temps != nil && strings.HasPrefix(rel, "assets/")
		if e.IsDir() {
			switch {
			case d.temps != nil && rel == "assets":
				return nil // looked into for what a crash left only
			case inAssets && strings.HasPrefix(name, "."):
				return filepath.SkipDir
			case inAssets:
				return nil
			case ignoredDir(rel, name):
				return filepath.SkipDir
			}
			return dir(rel)
		}
		if d.temps != nil && isTempName(name) && e.Type().IsRegular() {
			*d.temps = append(*d.temps, rel)
			return nil
		}
		if inAssets || strings.HasPrefix(name, ".") || !strings.EqualFold(path.Ext(name), ".md") || !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		return file(index.FileInfo{Rel: rel, Size: info.Size(), MtimeNs: info.ModTime().UnixNano()})
	})
}

func (d dirSource) Stat(rel string) (index.FileInfo, error) {
	fi, err := os.Lstat(filepath.Join(d.root, filepath.FromSlash(rel)))
	if err != nil {
		return index.FileInfo{}, err
	}
	if !fi.Mode().IsRegular() {
		return index.FileInfo{}, fs.ErrNotExist
	}
	return index.FileInfo{Rel: rel, Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano()}, nil
}

func (d dirSource) DirExists(rel string) bool {
	fi, err := os.Lstat(filepath.Join(d.root, filepath.FromSlash(rel)))
	return err == nil && fi.IsDir()
}

func (d dirSource) Read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.root, filepath.FromSlash(rel)))
}

// PageCount is the number of pages in the space's index: one COUNT of its pages
// table, milliseconds whatever the size (the quotas count pages with it).
func (s *Space) PageCount() (int, error) { return s.idx.Count() }
