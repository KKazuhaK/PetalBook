// Package space is one tree of pages on disk, backed by a Git repository and
// mirrored by an index. It owns every write to the files: atomic replacement,
// optimistic concurrency, naming, and handing changes to the committer.
package space

import (
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

	"github.com/KazuhaHub/PetalBook/internal/index"
)

// Defaults for the committer: a commit after two quiet minutes, or ten
// minutes after the first uncommitted write.
const (
	DefaultCommitIdle = 2 * time.Minute
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
	// StateDir holds the index. Default: <Root>/.petalbook/cache.
	StateDir string
	// Committer is the Git author of automatic commits, "Name <email>".
	Committer  string
	CommitIdle time.Duration
	CommitMax  time.Duration
	// ResyncInterval is the shortest time between two index re-syncs
	// triggered by a lookup that missed (default 2 s).
	ResyncInterval time.Duration
	Logger         *slog.Logger
}

// Space is safe for concurrent use.
type Space struct {
	root string // absolute, symlinks resolved
	name string
	idx  *index.Index
	cm   *committer
	log  *slog.Logger

	mu  sync.Mutex // serialises every change to files and the index
	now func() time.Time
	ops fileOps

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
)

// InvalidError is a request the space refuses because of its content.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalidf(format string, a ...any) error { return &InvalidError{fmt.Sprintf(format, a...)} }

// ConflictError means the page changed since the client read it.
type ConflictError struct{ Current *Page }

func (e *ConflictError) Error() string { return "the page was changed by someone else" }

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
	if _, err := os.Stat(filepath.Join(root, ".git")); errors.Is(err, fs.ErrNotExist) {
		if err := g.init(); err != nil {
			return nil, err
		}
		log.Info("initialised a Git repository", "dir", root)
	} else if err != nil {
		return nil, err
	}

	pb := filepath.Join(root, ".petalbook")
	if err := os.MkdirAll(pb, 0o755); err != nil {
		return nil, err
	}
	var ours []string // files under .petalbook the server made, for the first commit
	if changed, err := ensureLines(filepath.Join(pb, ".gitignore"), "cache/", "tmp/"); err != nil {
		return nil, err
	} else if changed {
		ours = append(ours, ".petalbook/.gitignore")
	}
	name, madeConfig, err := ensureSpaceConfig(filepath.Join(pb, "space.yml"), opt.Name)
	if err != nil {
		return nil, err
	}
	if madeConfig {
		ours = append(ours, ".petalbook/space.yml")
	}

	stateDir := opt.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(pb, "cache")
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
	if rel, err := filepath.Rel(root, stateDir); err == nil && !strings.HasPrefix(rel, "..") && !strings.HasPrefix(filepath.ToSlash(rel), ".petalbook/") {
		// A state directory elsewhere inside the repository must not be committed.
		if err := g.exclude(filepath.ToSlash(rel) + "/"); err != nil {
			return nil, err
		}
	}
	// Half-written files from a crash.
	os.RemoveAll(filepath.Join(pb, "tmp"))

	// What an earlier run changed but did not get to commit, and any move it
	// left half done, before the index looks at the files.
	jrn, recs, err := openJournal(filepath.Join(stateDir, "pending.journal"))
	if err != nil {
		return nil, err
	}
	recovered := recoverPending(root, recs)
	if len(recovered) > 0 {
		log.Info("recovered uncommitted changes from the journal", "paths", len(recovered))
	}

	idx, err := index.Open(filepath.Join(stateDir, "index.db"), root)
	if err != nil {
		return nil, err
	}
	s := &Space{root: root, name: name, idx: idx, log: log, now: time.Now, ops: osOps(), src: dirSource{root}, syncEvery: opt.ResyncInterval}
	if s.syncEvery <= 0 {
		s.syncEvery = 2 * time.Second
	}
	st, err := idx.Sync(s.src, false)
	if err != nil {
		idx.Close()
		return nil, err
	}
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
	s.cm = newCommitter(g, idle, max, log, jrn, recovered)
	if len(ours) > 0 {
		recs := make([]jrec, len(ours))
		for i, p := range ours {
			recs[i] = jrec{Op: "t", Path: p}
		}
		if err := s.cm.Record(recs...); err != nil {
			return nil, err
		}
		s.cm.Touch(ours...) // commit the files we just made, and nothing else
	}
	return s, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Name is the display name of the space.
func (s *Space) Name() string { return s.name }

// Root is the absolute directory of the space.
func (s *Space) Root() string { return s.root }

// Close commits what is still uncommitted, stops the committer and closes the
// index. It returns the commit error, if any.
func (s *Space) Close() error {
	err := s.cm.Close()
	if cerr := s.idx.Close(); err == nil {
		err = cerr
	}
	return err
}

// Flush commits pending changes now.
func (s *Space) Flush() error { return s.cm.Flush() }

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

// ensureSpaceConfig creates space.yml with the name if it is missing, and
// returns the name in effect.
func ensureSpaceConfig(file, name string) (string, bool, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		if name == "" {
			name = DefaultName
		}
		out, err := yaml.Marshal(map[string]string{"name": name})
		if err != nil {
			return "", false, err
		}
		return name, true, os.WriteFile(file, out, 0o644)
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
		return invalidf("invalid path")
	}
	if strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return invalidf("path %q must not start or end with a slash", rel)
	}
	for i, seg := range strings.Split(rel, "/") {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return invalidf("path %q has an empty, . or .. segment", rel)
		case strings.HasPrefix(seg, "."):
			return invalidf("path %q: names starting with a dot are reserved", rel)
		case len(seg) > 255:
			return invalidf("path %q: a name is too long", rel)
		case strings.TrimRight(seg, ". ") != seg:
			return invalidf("path %q: a name ends with a dot or space", rel)
		case strings.IndexFunc(seg, func(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"\|?*`, r) }) >= 0:
			return invalidf("path %q: a name has a character that file systems do not allow", rel)
		case i == 0 && seg == "assets":
			return invalidf("the folder name %q is reserved for attachments", seg)
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
			return "", invalidf("path %q goes through a symbolic link", rel)
		}
	}
	return p, nil
}

// --- file tree as the index's source ----------------------------------------

type dirSource struct{ root string }

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
		if e.IsDir() {
			if ignoredDir(rel, name) {
				return filepath.SkipDir
			}
			return dir(rel)
		}
		if strings.HasPrefix(name, ".") || !strings.EqualFold(path.Ext(name), ".md") || !e.Type().IsRegular() {
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
