package space

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// A settings file (space.yml, tags.yml) that cannot be used (it is not YAML
// with keys at the top, its list of entries is not one, an alias is inside its
// own anchor, it is over a limit or not a regular file: a person broke it, or a
// pull brought it) is nobody's to fix before the space works again. The space
// goes on by itself:
//
// Reading. The settings are those of the newest version of the file in the
// history of the space that can be used, looked for among the last
// maxRestoreLook commits that changed the file; if there is none, there are no
// settings, as in a space that never had the file. What was found is
// remembered for the file as it is now, so the history is searched once for
// one unusable content, not once for every request.
//
// Repair. The file itself is put right by the next change that writes it, and
// by nothing else: a read never writes, so it never makes a commit, and in a
// space that follows a remote never a push. The change writes the restored
// settings with what it changes, and in the same commit keeps the file as it
// was next to it, as `<file>.unreadable-<UTC date and time>`. A content that
// is already kept under such a name (a pull brought the same file back) is not
// kept a second time. A change that needs nothing written (a folder without
// settings is moved) leaves the file as it is, and the space goes on reading
// the restored settings.
//
// Telling. SettingsNotices says what happened, for the people who look after
// the space: nobody is asked to do anything.
//
// Recovery after a crash goes the same way (recoverBindings).

// maxRestoreLook is how many commits that changed a settings file are looked
// at for a version that can be used.
const maxRestoreLook = 50

// unreadableMark is what comes between the name of a settings file and the
// time in the name of the copy kept of it.
const unreadableMark = ".unreadable-"

// SettingsVersion is a version of a settings file: the commit it is in, and
// when. Commit is "" for what is in the working tree and not in a commit.
type SettingsVersion struct {
	Commit string    `json:"commit,omitempty"`
	Time   time.Time `json:"time"`
}

// SettingsNotice says that a settings file could not be used and what the
// server did about it.
type SettingsNotice struct {
	// File is the settings file, as a path in the space.
	File string `json:"file"`
	// Unreadable is true while the file in the space is still the one that
	// cannot be used (the server reads the restored settings; the next change
	// to the settings writes them), and false once it was put right.
	Unreadable bool `json:"unreadable"`
	// Since is the oldest version in a row that could not be used.
	Since *SettingsVersion `json:"since,omitempty"`
	// RestoredFrom is the version the settings in use were taken from; nil when
	// the history has none that can be used, and the settings started empty.
	RestoredFrom *SettingsVersion `json:"restoredFrom,omitempty"`
	// Copy is where the file as it was is kept, once it was put right.
	Copy string `json:"copy,omitempty"`
	// RepairedAt is when it was put right.
	RepairedAt *time.Time `json:"repairedAt,omitempty"`
}

// restored is the settings that stand in for a file that cannot be used.
type restored struct {
	text  []byte // nil: no settings
	from  *SettingsVersion
	since *SettingsVersion
}

// doc is the restored settings as a document; nil for none.
func (r *restored) doc(f settingsFile) (*yaml.Node, error) {
	if r.text == nil {
		return nil, nil
	}
	return f.parse(r.text)
}

// healer restores the settings files of one space.
type healer struct {
	root, stateDir string
	repo           func() *gitstore.Repo
	log            *slog.Logger

	mu    sync.Mutex
	found map[string]*restored // by what the unusable file is (stateOf) and where the history stands
	using map[string]*restored // by file: what is read in its place now
	wrote map[string]lastWrite // by file: what this server last wrote there
}

// lastWrite is the text of a settings file as the server wrote it, and when. In
// a space that commits a while after a change, it is newer than anything in
// the history until that commit is made.
type lastWrite struct {
	text []byte
	at   time.Time
}

// wroteGood remembers what was written to f.
func (h *healer) wroteGood(f settingsFile, text []byte) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.wrote == nil {
		h.wrote = map[string]lastWrite{}
	}
	h.wrote[f.rel] = lastWrite{text, time.Now()}
	h.found = nil // what the history had to give may be older than this now
	h.mu.Unlock()
}

// historyMark changes whenever the history of the space gets a commit: what
// was found in it for an unusable file is good for as long as it stays.
func (h *healer) historyMark() string {
	if fi, err := os.Stat(filepath.Join(h.root, ".git", "logs", "HEAD")); err == nil {
		return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	if h.repo == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return h.repo().Head(ctx)
}

// stateOf identifies a settings file as it is now, for one that cannot be
// used: its content, or for what has no content to read, what it is.
func stateOf(root string, f settingsFile) (key string, mtime time.Time) {
	abs := filepath.Join(root, filepath.FromSlash(f.rel))
	fi, err := os.Lstat(abs)
	if err != nil {
		return f.rel + ":gone", time.Time{}
	}
	mtime = fi.ModTime()
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		to, _ := os.Readlink(abs)
		return f.rel + ":link:" + to, mtime
	case !fi.Mode().IsRegular():
		return f.rel + ":" + fi.Mode().Type().String(), mtime
	case fi.Size() > int64(f.max):
		return fmt.Sprintf("%s:large:%d:%d", f.rel, fi.Size(), mtime.UnixNano()), mtime
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("%s:unread:%d:%d", f.rel, fi.Size(), mtime.UnixNano()), mtime
	}
	sum := sha256.Sum256(data)
	return f.rel + ":" + hex.EncodeToString(sum[:]), mtime
}

// restore finds the settings to use in place of the file f, which cannot be
// used as it is. An error means the history could not be asked, which passes.
func (h *healer) restore(f settingsFile) (*restored, error) {
	key, mtime := stateOf(h.root, f)
	key += "@" + h.historyMark()
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.found[key]; r != nil {
		return r, nil
	}
	r, err := h.search(f)
	if err != nil {
		return nil, unavailable(err, "%s cannot be used as it is, and the history of the space could not be read for an earlier version; try again", f.rel)
	}
	if r.since == nil {
		r.since = &SettingsVersion{Time: mtime.UTC()}
	}
	// What this server wrote after the newest usable commit, and has not
	// committed yet, is newer than that commit.
	if w, ok := h.wrote[f.rel]; ok && (r.from == nil || w.at.Truncate(time.Second).After(r.from.Time)) {
		if _, perr := f.parse(w.text); perr == nil {
			r.text, r.from = w.text, &SettingsVersion{Time: w.at.UTC()}
		}
	}
	if len(h.found) >= 16 {
		h.found = nil
	}
	if h.found == nil {
		h.found = map[string]*restored{}
	}
	h.found[key] = r
	if h.log != nil {
		from := "none; the settings start empty"
		if r.from != nil {
			from = r.from.Commit
		}
		h.log.Warn("a settings file cannot be used; the space goes on with the newest version that can", "file", f.rel, "restored_from", from)
	}
	return r, nil
}

// search looks through the history of f, newest first.
func (h *healer) search(f settingsFile) (*restored, error) {
	r := &restored{}
	if h.repo == nil {
		return r, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := h.repo()
	if repo.Head(ctx) == "" {
		return r, nil // no commit yet
	}
	versions, _, err := repo.FileHistory(ctx, gitstore.HistoryQuery{Start: "HEAD", Path: f.rel, Limit: maxRestoreLook})
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		at := &SettingsVersion{Commit: v.Commit, Time: v.Time.UTC()}
		if v.Status == 'D' {
			// The file was deleted here: no settings, and that is a version too.
			r.from = at
			return r, nil
		}
		data, _, err := repo.ReadBlob(ctx, v.Commit, v.Path)
		if err != nil && !errors.Is(err, gitstore.ErrNoSuchFile) {
			return nil, err
		}
		if err == nil && len(data) <= f.max {
			if _, perr := f.parse(data); perr == nil {
				if data == nil {
					data = []byte{}
				}
				r.text, r.from = data, at
				return r, nil
			}
		}
		r.since = at
	}
	return r, nil
}

// fine says the file f is read as it is.
func (h *healer) fine(f settingsFile) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.using, f.rel)
	h.mu.Unlock()
}

func (h *healer) inUse(f settingsFile, r *restored) {
	h.mu.Lock()
	if h.using == nil {
		h.using = map[string]*restored{}
	}
	h.using[f.rel] = r
	h.mu.Unlock()
}

// loadSettings reads a settings file of the space: nil when there is none or
// nothing in it, and for a file that cannot be used, the settings restored for
// it. f.bad only when not even those can be had (the history cannot be read
// just now), or in a Space that has nothing to restore with.
func (s *Space) loadSettings(f settingsFile) (*yaml.Node, error) {
	doc, err := loadSettingsAt(s.root, f)
	if !errors.Is(err, f.bad) {
		if err == nil {
			s.heal.fine(f)
		}
		return doc, err
	}
	if s.heal == nil {
		return nil, err
	}
	r, rerr := s.heal.restore(f)
	if rerr != nil {
		return nil, err
	}
	s.heal.inUse(f, r)
	return r.doc(f)
}

// copiesOf lists the copies kept of f, as paths in the space, oldest first.
func copiesOf(root string, f settingsFile) []string {
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(f.rel)))
	entries, _ := os.ReadDir(dir)
	prefix := filepath.Base(f.rel) + unreadableMark
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.ToSlash(filepath.Join(filepath.Dir(f.rel), e.Name())))
		}
	}
	sort.Strings(out)
	return out
}

// sameFile reports whether two paths hold the same thing: two regular files
// of the same content, or two links to the same place.
func sameFile(a, b string) bool {
	fa, errA := os.Lstat(a)
	fb, errB := os.Lstat(b)
	if errA != nil || errB != nil || fa.Mode().Type() != fb.Mode().Type() {
		return false
	}
	switch {
	case fa.Mode()&fs.ModeSymlink != 0:
		ta, errA := os.Readlink(a)
		tb, errB := os.Readlink(b)
		return errA == nil && errB == nil && ta == tb
	case !fa.Mode().IsRegular() || fa.Size() != fb.Size():
		return false
	}
	x, errA := os.Open(a)
	if errA != nil {
		return false
	}
	defer x.Close()
	y, errB := os.Open(b)
	if errB != nil {
		return false
	}
	defer y.Close()
	bx, by := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		n, errA := io.ReadFull(x, bx)
		m, errB := io.ReadFull(y, by)
		if n != m || !bytes.Equal(bx[:n], by[:m]) {
			return false
		}
		if errA != nil || errB != nil {
			return errA != nil && errB != nil
		}
	}
}

// keep puts the file f, which cannot be used, aside under the name of a copy,
// and returns that name as a path in the space. record is told the path before
// anything is made there (it journals it; the copy is part of the change). A
// content that is already kept is not kept again: its copy is the one named.
//
// A regular file gets a second name, so the file is whole under one of them
// whenever this stops; whoever writes f next replaces the first. What is not
// a regular file (a link, a folder) is moved away under the new name.
func (h *healer) keep(f settingsFile, record func(rel string) error) (string, error) {
	abs := filepath.Join(h.root, filepath.FromSlash(f.rel))
	for _, c := range copiesOf(h.root, f) {
		if sameFile(abs, filepath.Join(h.root, filepath.FromSlash(c))) {
			return c, record(c)
		}
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	name := f.rel + unreadableMark + time.Now().UTC().Format("20060102T150405Z")
	rel := name
	for n := 2; ; n++ {
		if _, err := os.Lstat(filepath.Join(h.root, filepath.FromSlash(rel))); errors.Is(err, fs.ErrNotExist) {
			break
		}
		rel = fmt.Sprintf("%s-%d", name, n)
	}
	if err := record(rel); err != nil {
		return "", err
	}
	target := filepath.Join(h.root, filepath.FromSlash(rel))
	if !fi.Mode().IsRegular() {
		return rel, os.Rename(abs, target)
	}
	return rel, linkNoClobber(abs, target)
}

// noticesFile is where the repairs made are written down, in the state
// directory of the space: they are this server's to tell, not the repository's.
func (h *healer) noticesFile() string { return filepath.Join(h.stateDir, "settings-restored.json") }

func (h *healer) readRepairs() []SettingsNotice {
	data, err := os.ReadFile(h.noticesFile())
	if err != nil {
		return nil
	}
	var out []SettingsNotice
	_ = json.Unmarshal(data, &out)
	return out
}

// repaired writes down that f was put right from r, its old content kept at copy.
func (h *healer) repaired(f settingsFile, r *restored, copy string) {
	now := time.Now().UTC()
	n := SettingsNotice{File: f.rel, Since: r.since, RestoredFrom: r.from, Copy: copy, RepairedAt: &now}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.using, f.rel)
	var list []SettingsNotice
	for _, o := range h.readRepairs() {
		if o.Copy != copy {
			list = append(list, o)
		}
	}
	list = append(list, n)
	if len(list) > 50 {
		list = list[len(list)-50:]
	}
	if h.log != nil {
		h.log.Warn("a settings file that could not be used was put right; the file as it was is kept", "file", f.rel, "copy", copy)
	}
	data, err := json.Marshal(list)
	if err != nil || h.stateDir == "" {
		return
	}
	tmp := h.noticesFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		os.Rename(tmp, h.noticesFile())
	}
}

// SettingsNotices says which settings files of the space could not be used:
// the ones the server reads restored settings for now, and the ones it has put
// right, for as long as the copy of what they were is kept in the space
// (removing the copy is how the notice is put away). It is for the people who
// look after the space; nothing is asked of anyone.
func (s *Space) SettingsNotices() []SettingsNotice {
	h := s.heal
	if h == nil {
		return nil
	}
	// Look at the files as they are now.
	s.config()
	s.tagViews()
	var out []SettingsNotice
	h.mu.Lock()
	defer h.mu.Unlock()
	var repairs map[string]SettingsNotice // read when there is a copy to tell of
	for _, f := range []settingsFile{spaceSettings, tagsSettings} {
		if r := h.using[f.rel]; r != nil {
			out = append(out, SettingsNotice{File: f.rel, Unreadable: true, Since: r.since, RestoredFrom: r.from})
		}
		for _, c := range copiesOf(s.root, f) {
			if repairs == nil {
				repairs = map[string]SettingsNotice{}
				for _, n := range h.readRepairs() {
					repairs[n.Copy] = n
				}
			}
			n, ok := repairs[c]
			if !ok {
				// Put right by another server, or before this one lost its notes.
				n = SettingsNotice{File: f.rel, Copy: c}
			}
			out = append(out, n)
		}
	}
	return out
}
