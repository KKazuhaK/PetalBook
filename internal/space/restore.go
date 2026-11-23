package space

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
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
	// File is the settings file or type file, as a path in the space.
	File string `json:"file"`
	// Entry is set when it is not the file but one entry of it that could not
	// be used (`Tang`), or one setting of an entry (`Tang: views`): it read as
	// not there, and was written over by the change that put settings there.
	Entry string `json:"entry,omitempty"`
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
	// Lost are the entries of the file as it was that could not be read even
	// one by one, so they are not in the settings in use: they are only in the
	// file (and, once it was put right, in the copy). "*" stands for all of
	// it, when not even that much of the file could be read.
	Lost []string `json:"lost,omitempty"`
}

// restored is the settings that stand in for a file that cannot be used.
type restored struct {
	text  []byte // nil: no settings
	from  *SettingsVersion
	since *SettingsVersion
	// lost are the entries of the file as it is that could not be read on
	// their own, so they are not in text (see salvage.go); "*" when nothing of
	// it could be read.
	lost []string
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

	mu sync.Mutex
	// found is what was found for an unusable file, by what the file is
	// (stateOf) and where the history stands (historyMark): the cheap first
	// look. bySrc is the same by what the file is and the newest commit that
	// changed it, which a commit of anything else leaves as it is.
	found    map[string]*restored
	bySrc    map[string]*restored
	using    map[string]*restored // by file: what is read in its place now
	odd      map[string][]string  // by file: the entries of it that cannot be used
	wrote    map[string]lastWrite // by file: what this server wrote there and has not committed yet
	names    *namesCache          // the types of a folder of the types that is not one now
	pending  []SettingsNotice     // repairs made before the state directory was known
	searches int                  // how many times the history was searched, for the tests
}

// lastWrite is the text of a settings file as the server wrote it, and when.
// It is kept only until the committer has committed that file: until then it
// is newer than anything in the history, and after that the history has it,
// in its place among the commits. Times of different machines are never
// compared.
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
	h.found, h.bySrc = nil, nil // what the history had to give may be older than this now
	h.mu.Unlock()
}

// committed is told the paths of a change once they are committed, or once
// the change was taken back: what this server wrote to them is no longer
// newer than the history.
func (h *healer) committed(paths []string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range paths {
		if _, ok := h.wrote[p]; ok {
			delete(h.wrote, p)
			h.found, h.bySrc = nil, nil
		}
	}
}

// historyMark changes whenever the history of the space gets a commit.
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
// used: its content, or for what has no content to read, what it is. Nothing
// is followed: a link or a file in the place of a folder on the way is what
// is looked at.
func stateOf(root string, f settingsFile) (key string, mtime time.Time) {
	fi, blocked, err := lstatIn(root, f.rel)
	if err != nil {
		return f.rel + ":gone", time.Time{}
	}
	mtime = fi.ModTime()
	at := f.rel
	if blocked != "" {
		key = f.rel + ":behind:" + blocked
		at = blocked
	} else {
		key = f.rel
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		to, _ := readlinkIn(root, at)
		return key + ":link:" + to, mtime
	case !fi.Mode().IsRegular():
		return key + ":" + fi.Mode().Type().String(), mtime
	case blocked != "":
		return fmt.Sprintf("%s:file:%d:%d", key, fi.Size(), mtime.UnixNano()), mtime
	case fi.Size() > int64(maxSalvage(f)):
		return fmt.Sprintf("%s:large:%d:%d", key, fi.Size(), mtime.UnixNano()), mtime
	}
	data, _, _, err := readIn(root, at, int64(maxSalvage(f)))
	if err != nil || data == nil {
		return fmt.Sprintf("%s:unread:%d:%d", key, fi.Size(), mtime.UnixNano()), mtime
	}
	sum := sha256.Sum256(data)
	return key + ":" + hex.EncodeToString(sum[:]), mtime
}

// restore finds the settings to use in place of the file f, which cannot be
// used as it is: the newest version of it in the history that can be, or
// what this server wrote there and has not committed yet, with what can still
// be read of the file as it is put onto that (salvage.go). An error means the
// history could not be asked, which passes.
//
// The history is searched once for one unusable content and one newest commit
// of the file: commits of other files do not search it again.
func (h *healer) restore(f settingsFile) (*restored, error) {
	state, mtime := stateOf(h.root, f)
	fast := state + "@" + h.historyMark()
	h.mu.Lock()
	if r := h.found[fast]; r != nil {
		h.mu.Unlock()
		return r, nil
	}
	h.mu.Unlock()
	versions, err := h.versions(f)
	if err != nil {
		return nil, unavailable(err, "%s cannot be used as it is, and the history of the space could not be read for an earlier version; try again", f.rel)
	}
	src := state + "#"
	if len(versions) > 0 {
		src += versions[0].Commit
	}
	h.mu.Lock()
	r := h.bySrc[src]
	w, written := h.wrote[f.rel]
	h.mu.Unlock()
	if r == nil {
		if r, err = h.search(f, versions); err != nil {
			return nil, unavailable(err, "%s cannot be used as it is, and the history of the space could not be read for an earlier version; try again", f.rel)
		}
		if r.since == nil {
			r.since = &SettingsVersion{Time: mtime.UTC()}
		}
		// What this server wrote and has not committed yet is newer than any
		// commit.
		if written && f.usable(w.text) {
			r.text, r.from = w.text, &SettingsVersion{Time: w.at.UTC()}
		}
		h.salvageInto(f, r)
		if h.log != nil {
			from := "none; the settings start empty"
			if r.from != nil {
				from = r.from.Commit
			}
			h.log.Warn("a settings file cannot be used; the space goes on with the newest version that can", "file", f.rel, "restored_from", from, "lost", r.lost)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if w2, ok := h.wrote[f.rel]; ok != written || (ok && !bytes.Equal(w2.text, w.text)) {
		return r, nil // written meanwhile: not remembered
	}
	if len(h.found) >= 16 {
		h.found = nil
	}
	if len(h.bySrc) >= 16 {
		h.bySrc = nil
	}
	if h.found == nil {
		h.found = map[string]*restored{}
	}
	if h.bySrc == nil {
		h.bySrc = map[string]*restored{}
	}
	h.found[fast], h.bySrc[src] = r, r
	return r, nil
}

// versions are the last maxRestoreLook commits that changed f, newest first.
func (h *healer) versions(f settingsFile) ([]gitstore.Version, error) {
	if h.repo == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := h.repo()
	if repo.Head(ctx) == "" {
		return nil, nil // no commit yet
	}
	versions, _, err := repo.FileHistory(ctx, gitstore.HistoryQuery{Start: "HEAD", Path: f.rel, Limit: maxRestoreLook})
	return versions, err
}

// search looks through versions of f, newest first, for one that can be used.
// A version where the file is gone because something that is not a folder
// took the place of a folder above it is not one: the file was not deleted,
// it could not be read.
func (h *healer) search(f settingsFile, versions []gitstore.Version) (*restored, error) {
	r := &restored{}
	if len(versions) == 0 {
		return r, nil
	}
	h.mu.Lock()
	h.searches++
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := h.repo()
	for _, v := range versions {
		at := &SettingsVersion{Commit: v.Commit, Time: v.Time.UTC()}
		if v.Status == 'D' {
			blocked, err := blockedIn(ctx, repo, v.Commit, v.Path)
			if err != nil {
				return nil, err
			}
			if blocked {
				r.since = at
				continue
			}
			// The file was deleted here: no settings, and that is a version too.
			r.from = at
			return r, nil
		}
		data, _, err := repo.ReadBlob(ctx, v.Commit, v.Path)
		if err != nil && !errors.Is(err, gitstore.ErrNoSuchFile) {
			return nil, err
		}
		if err == nil && len(data) <= f.max && f.usable(data) {
			if data == nil {
				data = []byte{}
			}
			r.text, r.from = data, at
			return r, nil
		}
		r.since = at
	}
	return r, nil
}

// blockedIn reports whether a folder above rel is a file or a link in commit.
func blockedIn(ctx context.Context, repo *gitstore.Repo, commit, rel string) (bool, error) {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		_, _, err := repo.ReadBlob(ctx, commit, strings.Join(parts[:i], "/"))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, gitstore.ErrNoSuchFile) {
			return false, err
		}
	}
	return false, nil
}

// setOdd says which entries of f cannot be used, as it was just read.
func (h *healer) setOdd(f settingsFile, entries []string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if len(entries) == 0 {
		delete(h.odd, f.rel)
	} else {
		if h.odd == nil {
			h.odd = map[string][]string{}
		}
		h.odd[f.rel] = entries
	}
	h.mu.Unlock()
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
	var out []string
	for _, c := range copiesIn(root, filepath.ToSlash(filepath.Dir(f.rel))) {
		if c.file == f.rel {
			out = append(out, c.rel)
		}
	}
	return out
}

// kept is a copy of a file that could not be used: where it is, and of what.
type kept struct{ rel, file string }

// copiesIn lists the copies kept in the folder dir of the space, by name;
// none when dir is not a folder reached through folders. dir "" is the top of
// the space, where only copies of .petalbook are looked for.
func copiesIn(root, dir string) []kept {
	if dir != "" && !isRealDir(root, dir) {
		return nil
	}
	entries, _ := readDirIn(root, dir)
	var out []kept
	for _, e := range entries {
		i := strings.Index(e.Name(), unreadableMark)
		if i <= 0 || (dir == "" && e.Name()[:i] != ".petalbook") {
			continue
		}
		rel := path.Join(dir, e.Name())
		out = append(out, kept{rel, path.Join(dir, e.Name()[:i])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// sameFile reports whether two paths of the space hold the same thing: two
// regular files of the same content, or two links to the same place. Neither
// is followed, nor anything on the way to them.
func sameFile(root, a, b string) bool {
	fa, blA, errA := lstatIn(root, a)
	fb, blB, errB := lstatIn(root, b)
	if errA != nil || errB != nil || blA != "" || blB != "" || fa.Mode().Type() != fb.Mode().Type() {
		return false
	}
	switch {
	case fa.Mode()&fs.ModeSymlink != 0:
		ta, errA := readlinkIn(root, a)
		tb, errB := readlinkIn(root, b)
		return errA == nil && errB == nil && ta == tb
	case !fa.Mode().IsRegular() || fa.Size() != fb.Size():
		return false
	}
	x, _, _, errA := readIn(root, a, fa.Size())
	y, _, _, errB := readIn(root, b, fb.Size())
	return errA == nil && errB == nil && x != nil && y != nil && bytes.Equal(x, y)
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
	if b, err := wayBlocked(h.root, f.rel); err != nil || b != "" {
		// clearWay is for that; nothing is moved through it.
		return "", errors.Join(err, fmt.Errorf("%s is not a folder", b))
	}
	for _, c := range copiesOf(h.root, f) {
		if sameFile(h.root, f.rel, c) {
			return c, record(c)
		}
	}
	fi, _, err := lstatIn(h.root, f.rel)
	if err != nil {
		return "", err
	}
	name := f.rel + unreadableMark + time.Now().UTC().Format("20060102T150405Z")
	rel := name
	for n := 2; ; n++ {
		if _, _, err := lstatIn(h.root, rel); errors.Is(err, fs.ErrNotExist) {
			break
		}
		rel = fmt.Sprintf("%s-%d", name, n)
	}
	if err := record(rel); err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return rel, renameIn(h.root, f.rel, rel)
	}
	return rel, linkIn(h.root, f.rel, rel)
}

// noticesFile is where the repairs made are written down, in the state
// directory of the space: they are this server's to tell, not the repository's.
func (h *healer) noticesFile() string { return filepath.Join(h.stateDir, "settings-restored.json") }

// stateOK reports whether the state directory can be used: one inside the
// space (under .petalbook, say) is not, through something on its way that is
// not a folder.
func (h *healer) stateOK() bool {
	if rel, err := filepath.Rel(h.root, h.stateDir); err == nil && filepath.IsLocal(rel) {
		if b, err := wayBlocked(h.root, filepath.ToSlash(rel)+"/x"); err != nil || b != "" {
			return false
		}
	}
	return true
}

func (h *healer) readRepairs() []SettingsNotice {
	if h.stateDir == "" || !h.stateOK() {
		return nil
	}
	data, err := os.ReadFile(h.noticesFile())
	if err != nil {
		return nil
	}
	var out []SettingsNotice
	_ = json.Unmarshal(data, &out)
	return out
}

// repaired writes down that f was put right (rep says of what), its old
// content kept at copy.
func (h *healer) repaired(f settingsFile, rep *repair, copy string) {
	now := time.Now().UTC()
	var made []SettingsNotice
	if rep.from != nil {
		made = append(made, SettingsNotice{File: f.rel, Since: rep.from.since, RestoredFrom: rep.from.from, Lost: rep.from.lost, Copy: copy, RepairedAt: &now})
	}
	for _, e := range rep.entries {
		made = append(made, SettingsNotice{File: f.rel, Entry: e, Copy: copy, RepairedAt: &now})
	}
	h.mu.Lock()
	delete(h.using, f.rel)
	h.mu.Unlock()
	if h.log != nil {
		h.log.Warn("a file that could not be used, or entries of it, were put right; the file as it was is kept", "file", f.rel, "entries", rep.entries, "copy", copy)
	}
	h.noteRepairs(made, copy)
}

// noteRepairs writes down repairs whose copy is copy. Before the state
// directory is known (a repair at the start) they wait for it.
func (h *healer) noteRepairs(made []SettingsNotice, copy string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stateDir == "" {
		h.pending = append(h.pending, made...)
		return
	}
	list := h.readRepairs()
	if len(h.pending) > 0 {
		list, h.pending = append(list, h.pending...), nil
	}
	// What is said again of the same file and copy replaces what was said.
	again := map[[3]string]bool{}
	for _, m := range made {
		again[[3]string{m.File, m.Entry, m.Copy}] = true
	}
	var out []SettingsNotice
	for _, o := range list {
		if !again[[3]string{o.File, o.Entry, o.Copy}] {
			out = append(out, o)
		}
	}
	out = append(out, made...)
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	// The state directory may be under .petalbook: never written through
	// something that is not a folder there.
	if !h.stateOK() {
		return
	}
	tmp := h.noticesFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		os.Rename(tmp, h.noticesFile())
	}
}

// forgetRepairs takes back what was written down of repairs whose copy is
// copy: the repair was undone (it could not be pushed).
func (h *healer) forgetRepairs(copy string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.found, h.bySrc, h.names = nil, nil, nil
	keep := h.pending[:0]
	for _, n := range h.pending {
		if n.Copy != copy {
			keep = append(keep, n)
		}
	}
	h.pending = keep
	if h.stateDir == "" || !h.stateOK() {
		return
	}
	list := h.readRepairs()
	out := list[:0]
	for _, n := range list {
		if n.Copy != copy {
			out = append(out, n)
		}
	}
	if len(out) == len(list) {
		return
	}
	if data, err := json.Marshal(out); err == nil {
		tmp := h.noticesFile() + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err == nil {
			os.Rename(tmp, h.noticesFile())
		}
	}
}

// setStateDir is where the notices are kept, known once the space has opened;
// repairs made before are written down now.
func (h *healer) setStateDir(dir string) {
	h.mu.Lock()
	h.stateDir = dir
	pending := h.pending
	h.pending = nil
	h.mu.Unlock()
	byCopy := map[string][]SettingsNotice{}
	var order []string
	for _, n := range pending {
		if _, ok := byCopy[n.Copy]; !ok {
			order = append(order, n.Copy)
		}
		byCopy[n.Copy] = append(byCopy[n.Copy], n)
	}
	for _, c := range order {
		h.noteRepairs(byCopy[c], c)
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
	// What is read restored now, and what is read as not there.
	var files []string
	for rel := range h.using {
		files = append(files, rel)
	}
	sort.Strings(files)
	for _, rel := range files {
		if _, _, err := lstatIn(s.root, rel); err != nil {
			delete(h.using, rel) // gone since it was read
			continue
		}
		r := h.using[rel]
		out = append(out, SettingsNotice{File: rel, Unreadable: true, Since: r.since, RestoredFrom: r.from, Lost: r.lost})
	}
	for _, f := range []settingsFile{spaceSettings, tagsSettings} {
		for _, e := range h.odd[f.rel] {
			out = append(out, SettingsNotice{File: f.rel, Entry: e, Unreadable: true})
		}
	}
	// What was put right, for as long as the copy is there.
	var repairs map[string][]SettingsNotice // read when there is a copy to tell of
	for _, dir := range []string{"", ".petalbook", typesDir} {
		for _, c := range copiesIn(s.root, dir) {
			if repairs == nil {
				repairs = map[string][]SettingsNotice{}
				for _, n := range h.readRepairs() {
					repairs[n.Copy] = append(repairs[n.Copy], n)
				}
			}
			if ns := repairs[c.rel]; len(ns) > 0 {
				out = append(out, ns...)
			} else {
				// Put right by another server, or before this one lost its notes.
				out = append(out, SettingsNotice{File: c.file, Copy: c.rel})
			}
		}
	}
	return out
}
