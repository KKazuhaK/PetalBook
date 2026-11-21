package space

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/KazuhaHub/PetalBook/internal/page"
)

// A journal record says what the server is about to do to a file (or did),
// durably, so a crash between a write and its commit cannot leave the change
// outside Git for good.
//
//	w  Path was (or is being) written with content whose blob id is Rev
//	d  Path is being deleted
//	m  From is being moved to To (hard link, then remove the old name)
//	t  Path changed; no further check
//
// A "w" record only counts at recovery if the file really has that content,
// so a name the server tried to take but another file held is never committed.
type jrec struct {
	Op   string `json:"op"`
	Path string `json:"p,omitempty"`
	From string `json:"f,omitempty"`
	To   string `json:"to,omitempty"`
	Rev  string `json:"rev,omitempty"`
}

// journal is an append-only file in the state directory. Every append is
// fsynced before the write it announces. After a successful commit it is
// replaced by the paths still pending.
type journal struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// openJournal reads the records an earlier run left and opens the file for
// appending. Damaged lines are skipped.
func openJournal(path string) (*journal, []jrec, error) {
	var recs []jrec
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r jrec
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Op != "" {
			recs = append(recs, r)
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
		b, _ := json.Marshal(r)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(buf.Bytes()); err != nil {
		return err
	}
	return j.f.Sync()
}

// rewrite replaces the journal by a list of pending paths, atomically.
func (j *journal) rewrite(paths []string) error {
	sort.Strings(paths)
	var buf bytes.Buffer
	for _, p := range paths {
		b, _ := json.Marshal(jrec{Op: "t", Path: p})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	j.mu.Lock()
	defer j.mu.Unlock()
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
// commit must cover, and finishes a move the crash interrupted. Paths that no
// longer exist are fine: they become deletions. A record naming a path outside
// the space (the journal is a file on disk) is ignored.
func recoverPending(root string, recs []jrec) []string {
	set := map[string]bool{}
	ok := func(p string) bool {
		return p != "" && fs.ValidPath(p) && !strings.HasPrefix(p, ".git/") && p != ".git"
	}
	for _, r := range recs {
		switch r.Op {
		case "t", "d":
			if ok(r.Path) {
				set[r.Path] = true
			}
		case "w":
			if !ok(r.Path) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Path)))
			if err == nil && page.Rev(data) == r.Rev {
				set[r.Path] = true
			}
		case "m":
			if !ok(r.From) || !ok(r.To) {
				continue
			}
			finishMove(root, r.From, r.To)
			set[r.From], set[r.To] = true, true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
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
		os.Remove(a)
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
