package space

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// A journal record says what the server is about to do to a file (or did),
// durably, so a crash between a write and its commit cannot leave the change
// outside Git for good.
//
//	w  Path was (or is being) written with content whose blob id is Rev
//	d  Path is being deleted
//	m  From is being moved to To (hard link, then remove the old name)
//	t  Path changed; no further check
//	r  From is being renamed to To, another spelling of its name, through Path
//	b  the folder From is being moved to To: the bindings in space.yml of it
//	   and of the folders in it have to follow (done at recovery if the folder
//	   is at To, not at From, and the old keys are still there)
//	u  the folder Path is being deleted: its bindings have to go with it
//	bd, ud  the bindings of the "b" or "u" record with the same fields have
//	   been dealt with: that record is not made again
//	f  the field From of the type whose file is Path is being renamed To: the
//	   type's file is written first (with the content whose blob id is Rev),
//	   then the Pages, then the views in space.yml. At recovery, a type file
//	   with that content has the rest finished; another has nothing done
//	fd the rename of the "f" record with the same fields is finished
//	fp some of the pages of the "f" record with the same fields that follows
//	   it: a rename can rewrite more pages than fit on one line, so its page
//	   list is written as parts before the record (in the same append, so a
//	   readable "f" record has every part before it), and the record says how
//	   many pages there are in all
//
// A "w" record only counts at recovery if the file really has that content,
// so a name the server tried to take but another file held is never committed.
type jrec struct {
	Op   string `json:"op"`
	Path string `json:"p,omitempty"`
	From string `json:"f,omitempty"`
	To   string `json:"to,omitempty"`
	Rev  string `json:"rev,omitempty"`
	// Dir marks an "r" record of a folder: its files are journaled one by
	// one, and the folder's own names are not paths to commit.
	Dir bool `json:"dir,omitempty"`
	// AuthorName and AuthorEmail say who made the change, so a commit after a
	// crash is still theirs. A record without them gets the space's default
	// author.
	AuthorName  string `json:"an,omitempty"`
	AuthorEmail string `json:"ae,omitempty"`
	// Via is the means of the change (Author.Via).
	Via string `json:"av,omitempty"`
	// Pages are the pages an "f" record rewrites (in memory: on disk they are
	// in its "fp" parts), or the part of them an "fp" record carries.
	Pages []string `json:"ps,omitempty"`
	// N is how many pages an "f" record rewrites, all its parts together.
	N int `json:"pn,omitempty"`
}

// journalPart bounds the bytes of the page paths (as JSON) in one "fp" record,
// so that no line this server writes is longer than maxJournalLine (a path is
// at most a few kilobytes). Lines are read whatever their length.
const (
	journalPart    = 64 << 10
	maxJournalLine = journalPart + 64<<10
)

// encodeRec writes r as the lines of the journal: one, or for an "f" record
// with pages, its "fp" parts and then the record.
func encodeRec(buf *bytes.Buffer, r jrec) {
	line := func(r jrec) {
		b, _ := json.Marshal(r)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if r.Op != "f" || len(r.Pages) == 0 {
		line(r)
		return
	}
	part := jrec{Op: "fp", Path: r.Path, From: r.From, To: r.To, Rev: r.Rev}
	size := 0
	for _, p := range r.Pages {
		q, _ := json.Marshal(p)
		if size > 0 && size+len(q)+1 > journalPart {
			line(part)
			part.Pages, size = nil, 0
		}
		part.Pages = append(part.Pages, p)
		size += len(q) + 1
	}
	line(part)
	r.N, r.Pages = len(r.Pages), nil
	line(r)
}

// journalDamage is a line of the journal that could not be read.
type journalDamage struct {
	Line int
	Len  int
	Why  string
}

// readJournal reads the records of a journal. A line that cannot be read is
// damage, reported and passed over: the records after it still count. A last
// line with no end is not damage: the append it began never finished, so the
// write it announced was never made; torn is its offset, or -1.
func readJournal(data []byte) (recs []jrec, damage []journalDamage, torn int) {
	torn = -1
	type key struct{ path, from, to, rev string }
	parts := map[key][]string{}
	for n, at := 1, 0; at < len(data); n++ {
		end := bytes.IndexByte(data[at:], '\n')
		if end < 0 {
			torn = at
			break
		}
		line := data[at : at+end]
		at += end + 1
		if len(line) == 0 {
			continue
		}
		var r jrec
		switch err := json.Unmarshal(line, &r); {
		case err != nil:
			damage = append(damage, journalDamage{n, len(line), err.Error()})
			continue
		case r.Op == "":
			damage = append(damage, journalDamage{n, len(line), "no op"})
			continue
		}
		k := key{r.Path, r.From, r.To, r.Rev}
		switch r.Op {
		case "fp":
			parts[k] = append(parts[k], r.Pages...)
			continue
		case "f":
			if r.N > 0 {
				r.Pages = append(parts[k], r.Pages...)
				if len(r.Pages) != r.N {
					damage = append(damage, journalDamage{n, len(line), fmt.Sprintf("the rename names %d pages, %d were found", r.N, len(r.Pages))})
				}
			}
			r.N = 0
			delete(parts, k)
		}
		recs = append(recs, r)
	}
	return recs, damage, torn
}

func (r jrec) author() Author { return Author{Name: r.AuthorName, Email: r.AuthorEmail, Via: r.Via} }

// journal is an append-only file in the state directory. Every append is
// fsynced before the write it announces. After a successful commit it is
// replaced by the paths still pending.
type journal struct {
	path string
	mu   sync.Mutex
	f    *os.File
	// guard, when set, says whether the folder of the journal can be written
	// in: it is not, through something that is not a folder (settingspath.go).
	guard func() error
}

// openJournal reads the records an earlier run left and opens the file for
// appending. A damaged line is logged as an error and passed over (see
// readJournal); a torn last line is cut off, so that the next record appended
// starts a line of its own.
func openJournal(path string, log *slog.Logger) (*journal, []jrec, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	recs, damage, torn := readJournal(data)
	for _, d := range damage {
		log.Error("a line of the journal cannot be read; what it recorded may not be committed or finished",
			"journal", path, "line", d.Line, "bytes", d.Len, "problem", d.Why)
	}
	if torn >= 0 {
		log.Warn("the journal ends in a record cut short by a stop; it is dropped", "journal", path, "bytes", len(data)-torn)
		if err := os.Truncate(path, int64(torn)); err != nil {
			return nil, nil, err
		}
	}
	j := &journal{path: path}
	if j.f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); err != nil {
		return nil, nil, err
	}
	return j, recs, nil
}

func (j *journal) append(recs ...jrec) error {
	var buf bytes.Buffer
	for _, r := range recs {
		encodeRec(&buf, r)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(buf.Bytes()); err != nil {
		return err
	}
	return j.f.Sync()
}

// rewrite replaces the journal by the pending paths, each with its author, and
// the records in keep, atomically.
func (j *journal) rewrite(pending map[string]Author, keep []jrec) error {
	paths := make([]string, 0, len(pending))
	for p := range pending {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	for _, p := range paths {
		by := pending[p]
		b, _ := json.Marshal(jrec{Op: "t", Path: p, AuthorName: by.Name, AuthorEmail: by.Email, Via: by.Via})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	// Records of bindings still to be changed are not "done" by a commit.
	for _, r := range keep {
		encodeRec(&buf, r)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.guard != nil {
		if err := j.guard(); err != nil {
			return err
		}
	}
	tmp := j.path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	j.f.Close()
	j.f, err = os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	return err
}

func (j *journal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

// recoverPending turns the records of an earlier run into the paths the next
// commit must cover, each with the author of the last record that names it
// (as in the committer: the latest change to a path is the one that counts),
// and finishes a move the crash interrupted. Paths that no longer exist are
// fine: they become deletions. A record naming a path outside the space (the
// journal is a file on disk) is ignored.
func recoverPending(root string, recs []jrec) map[string]Author {
	return recoverPendingWith(nil, root, recs)
}

// recoverPendingWith is recoverPending; h restores a space.yml that cannot be
// used when the bindings of a folder are to be finished in it.
func recoverPendingWith(h *healer, root string, recs []jrec) map[string]Author {
	set := map[string]Author{}
	ok := func(p string) bool {
		return p != "" && fs.ValidPath(p) && !strings.HasPrefix(p, ".git/") && p != ".git"
	}
	var bindRecs, fieldRecs []jrec
	for _, r := range recs {
		by := r.author()
		switch r.Op {
		case "t", "d":
			if ok(r.Path) {
				set[r.Path] = by
			}
		case "w":
			if !ok(r.Path) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Path)))
			if err == nil && page.Rev(data) == r.Rev {
				set[r.Path] = by
			}
		case "m":
			if !ok(r.From) || !ok(r.To) {
				continue
			}
			finishMove(root, r.From, r.To)
			set[r.From], set[r.To] = by, by
		case "bd", "ud":
			for i, o := range bindRecs {
				if o.Op+"d" == r.Op && o.From == r.From && o.To == r.To && o.Path == r.Path {
					bindRecs = append(bindRecs[:i:i], bindRecs[i+1:]...)
					break
				}
			}
		case "f":
			if ok(r.Path) {
				fieldRecs = append(fieldRecs, r)
			}
		case "fd":
			for i, o := range fieldRecs {
				if o.From == r.From && o.To == r.To && o.Path == r.Path && o.Rev == r.Rev {
					fieldRecs = append(fieldRecs[:i:i], fieldRecs[i+1:]...)
					break
				}
			}
		case "b", "u":
			// Looked at after the files are in their final places.
			if (r.Op == "b" && (!ok(r.From) || !ok(r.To))) || (r.Op == "u" && !ok(r.Path)) {
				continue
			}
			bindRecs = append(bindRecs, r)
		case "r":
			if !ok(r.From) || !ok(r.To) || !ok(r.Path) {
				continue
			}
			finishRespell(root, r.Path, r.To)
			if !r.Dir {
				set[r.From], set[r.To], set[r.Path] = by, by, by
			}
		}
	}
	for _, r := range fieldRecs {
		for _, p := range recoverFieldRename(h, root, r) {
			if ok(p) {
				set[p] = r.author()
			}
		}
	}
	if len(bindRecs) > 0 {
		by := bindRecs[len(bindRecs)-1].author()
		for _, p := range recoverBindings(h, root, bindRecs) {
			set[p] = by
		}
	}
	return set
}

// listed reports whether dir has an entry named exactly name.
func listed(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// finishRespell completes a rename through a third name that stopped
// between its two steps: what is at mid goes to to, if neither to nor (on a
// file system that folds names) the old spelling is there. A folder that
// stays at mid has its files journaled under mid (Space.rename), so they are
// committed there, not as deleted.
func finishRespell(root, mid, to string) {
	m, t := filepath.Join(root, filepath.FromSlash(mid)), filepath.Join(root, filepath.FromSlash(to))
	if _, err := os.Lstat(m); err != nil {
		return
	}
	if _, err := os.Lstat(t); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	os.Rename(m, t)
}

// finishMove completes a move that stopped between the link and the removal of
// the old name: if both names exist and are the same file (or the same page,
// byte for byte), the old name goes.
func finishMove(root, from, to string) {
	a, b := filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))
	fa, errA := os.Lstat(a)
	fb, errB := os.Lstat(b)
	if errA != nil || errB != nil || !fa.Mode().IsRegular() || !fb.Mode().IsRegular() {
		return
	}
	if os.SameFile(fa, fb) {
		// Two names of one file: the link was made and the old name is still
		// there. But on a file system that folds names, a rename to another
		// spelling of the file's own name answers to both names with one
		// entry, and removing "the old name" would remove the only one. Only
		// two entries in the listing are two names.
		if a != b && listed(filepath.Dir(a), filepath.Base(a)) && listed(filepath.Dir(b), filepath.Base(b)) {
			os.Remove(a)
		}
		return
	}
	da, errA := os.ReadFile(a)
	db, errB := os.ReadFile(b)
	if errA != nil || errB != nil || !bytes.Equal(da, db) {
		return
	}
	if id := page.Parse(da).Meta().ID(); id != "" && id == page.Parse(db).Meta().ID() {
		os.Remove(a)
	}
}
