package space

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// Every read and write under .petalbook goes through the functions here. A
// pull's checkout, or a person, can put a link where a folder was at any
// moment, also between a look at the folder and the opening of a file in it.
// So nothing is looked up by its path twice: the space is opened as an
// os.Root (nothing reached through it can be outside the space), each folder
// on the way is looked at without following it, opened, and kept only when
// what was opened is the folder that was looked at; the file is then read,
// written or renamed in that open folder. A folder that turns out to be
// something else is reported as blocked, as one that was a link all along.

// errChanged: what was opened is not what was looked at a moment before.
var errChanged = errors.New("the file was replaced while it was being read")

// splitRel is the folder and the name of a slash path of the space.
func splitRel(rel string) (dir, name string) {
	dir, name = path.Split(rel)
	return strings.TrimSuffix(dir, "/"), name
}

// openDirIn opens the folder dir (a slash path of the space, "" for the top)
// of the space at root, never through something on the way that is not a
// folder: that is named by blocked, and nothing is opened. A folder on the way
// that is not there is fs.ErrNotExist.
func openDirIn(root, dir string) (d *os.Root, blocked string, err error) {
	d, err = os.OpenRoot(root)
	if err != nil || dir == "" {
		return d, "", err
	}
	parts := strings.Split(dir, "/")
	for i, p := range parts {
		at := strings.Join(parts[:i+1], "/")
		fi, err := d.Lstat(p)
		if err != nil {
			d.Close()
			return nil, "", err
		}
		if !fi.IsDir() {
			d.Close()
			return nil, at, nil
		}
		next, err := d.OpenRoot(p)
		d.Close()
		if err != nil {
			// A link that was put there since, out of the space.
			return nil, at, nil
		}
		st, err := next.Stat(".")
		if err != nil || !os.SameFile(fi, st) {
			next.Close()
			return nil, at, nil // replaced since it was looked at
		}
		d = next
	}
	return d, "", nil
}

// wayBlocked is the first folder on the way to rel (a slash path of the space;
// rel itself is not looked at) that is there and is not a folder: a link, a
// file. "" when every folder on the way is one, or is not there.
func wayBlocked(root, rel string) (string, error) {
	dir, _ := splitRel(rel)
	d, blocked, err := openDirIn(root, dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if d != nil {
		d.Close()
	}
	return blocked, nil
}

// lstatIn is the Lstat of rel in root, never through a link or a file on the
// way: when one is in the way, it is what is looked at, and blocked names it.
func lstatIn(root, rel string) (fi fs.FileInfo, blocked string, err error) {
	dir, name := splitRel(rel)
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return nil, "", err
	}
	if blocked != "" {
		bdir, bname := splitRel(blocked)
		pd, b2, err := openDirIn(root, bdir)
		if err != nil || b2 != "" {
			return nil, blocked, errors.Join(err, fmt.Errorf("%s is not a folder", b2))
		}
		defer pd.Close()
		fi, err = pd.Lstat(bname)
		return fi, blocked, err
	}
	defer d.Close()
	fi, err = d.Lstat(name)
	return fi, "", err
}

// isRealDir reports whether rel in root is a folder reached through folders.
func isRealDir(root, rel string) bool {
	d, blocked, err := openDirIn(root, rel)
	if d != nil {
		d.Close()
	}
	return err == nil && blocked == ""
}

// readIn reads the file rel of root when it is a regular file reached through
// folders and not larger than max: at most max+1 bytes are read, so a caller
// sees that it is larger. data is nil, and nothing is opened, when it is not
// such a file; fi says what it is (for blocked, what is in the way).
func readIn(root, rel string, max int64) (data []byte, fi fs.FileInfo, blocked string, err error) {
	dir, name := splitRel(rel)
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return nil, nil, "", err
	}
	if blocked != "" {
		fi, _, err = lstatIn(root, rel)
		return nil, fi, blocked, err
	}
	defer d.Close()
	if fi, err = d.Lstat(name); err != nil {
		return nil, nil, "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, fi, "", nil
	}
	f, err := d.Open(name)
	if err != nil {
		return nil, fi, "", err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
		return nil, fi, "", errors.Join(err, errChanged)
	}
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fi, "", err
	}
	if data == nil {
		data = []byte{}
	}
	return data, fi, "", nil
}

// seen is what a read found in the place of a file: the file it read, or
// what was in the way of it (blocked, as lstatIn names it). fi is nil when the
// read found nothing it could go by: nothing there, or something replaced
// while it was read.
type seen struct {
	fi      fs.FileInfo
	blocked string
}

// is reports whether the read found what a look made before it (fi, blocked)
// found. What was read is kept by that look only when it is: a folder on the
// way can be swapped for a link, or be gone, and come back between the look
// and the read, and the file then looks as it did while what was read is
// something else (nothing, or the settings restored for it).
func (w seen) is(fi fs.FileInfo, blocked string) bool {
	return w.fi != nil && fi != nil && w.blocked == blocked && os.SameFile(w.fi, fi) &&
		w.fi.Mode() == fi.Mode() && w.fi.Size() == fi.Size() && w.fi.ModTime().Equal(fi.ModTime())
}

// lookedAt, set by the tests only, runs between the look at a file that a
// cache is kept by and the read of it.
var lookedAt atomic.Pointer[func(root, rel string)]

func afterLook(root, rel string) {
	if f := lookedAt.Load(); f != nil {
		(*f)(root, rel)
	}
}

// readlinkIn is where the link rel of root leads, read in its folder.
func readlinkIn(root, rel string) (string, error) {
	dir, name := splitRel(rel)
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return "", err
	}
	if blocked != "" {
		return "", fmt.Errorf("%s is not a folder", blocked)
	}
	defer d.Close()
	return d.Readlink(name)
}

// readDirIn lists the folder dir of root, reached through folders; nothing
// when it is not one.
func readDirIn(root, dir string) ([]fs.DirEntry, error) {
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return nil, err
	}
	if blocked != "" {
		return nil, fmt.Errorf("%s is not a folder", blocked)
	}
	defer d.Close()
	f, err := d.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// mkdirIn opens the folder dir of root, making what of it is not there; never
// through something on the way that is not a folder.
func mkdirIn(root, dir string) (*os.Root, error) {
	d, err := os.OpenRoot(root)
	if err != nil || dir == "" {
		return d, err
	}
	parts := strings.Split(dir, "/")
	for i, p := range parts {
		fi, err := d.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if err = d.Mkdir(p, 0o755); err == nil || errors.Is(err, fs.ErrExist) {
				fi, err = d.Lstat(p)
			}
		}
		if err == nil && !fi.IsDir() {
			err = fmt.Errorf("%s is not a folder", strings.Join(parts[:i+1], "/"))
		}
		if err != nil {
			d.Close()
			return nil, err
		}
		next, err := d.OpenRoot(p)
		d.Close()
		if err != nil {
			return nil, fmt.Errorf("%s is not a folder", strings.Join(parts[:i+1], "/"))
		}
		if st, err := next.Stat("."); err != nil || !os.SameFile(fi, st) {
			next.Close()
			return nil, fmt.Errorf("%s is not a folder", strings.Join(parts[:i+1], "/"))
		}
		d = next
	}
	return d, nil
}

// tempMark is in the name of a file written next to a settings file before
// it takes the place of that file; one left by a crash is removed at the start.
const tempMark = ".petalbook-w-"

// writeIn replaces the file rel of root with data, the folders on the way made
// when they are not there: a reader or a crash sees the old file or the new
// one. The new file is written in the folder itself, opened as above, and
// renamed there, so no link put on the way meanwhile is ever written through.
func writeIn(root, rel string, data []byte) error {
	dir, name := splitRel(rel)
	d, err := mkdirIn(root, dir)
	if err != nil {
		return err
	}
	defer d.Close()
	mode := os.FileMode(0o644)
	if fi, err := d.Lstat(name); err == nil && fi.Mode().IsRegular() {
		mode = fi.Mode().Perm()
	}
	var rnd [8]byte
	rand.Read(rnd[:])
	tmp := "." + name + tempMark + hex.EncodeToString(rnd[:])
	f, err := d.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		f.Close()
		d.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		d.Remove(tmp)
		return err
	}
	if err := d.Rename(tmp, name); err != nil {
		d.Remove(tmp)
		return err
	}
	return nil
}

// removeIn removes the file (or link) rel of root, in its folder.
func removeIn(root, rel string) error {
	dir, name := splitRel(rel)
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return err
	}
	if blocked != "" {
		return fmt.Errorf("%s is not a folder", blocked)
	}
	defer d.Close()
	return d.Remove(name)
}

// renameIn renames from to to, both in the same folder of root.
func renameIn(root, from, to string) error {
	dir, a := splitRel(from)
	dir2, b := splitRel(to)
	if dir != dir2 {
		return fmt.Errorf("%s and %s are not in one folder", from, to)
	}
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return err
	}
	if blocked != "" {
		return fmt.Errorf("%s is not a folder", blocked)
	}
	defer d.Close()
	return d.Rename(a, b)
}

// linkIn makes to a second name for the regular file from, in the same
// folder of root, and fails with fs.ErrExist if to is there. It falls back to
// a copy where the file system has no hard links.
func linkIn(root, from, to string) error {
	dir, a := splitRel(from)
	dir2, b := splitRel(to)
	if dir != dir2 {
		return fmt.Errorf("%s and %s are not in one folder", from, to)
	}
	d, blocked, err := openDirIn(root, dir)
	if err != nil {
		return err
	}
	if blocked != "" {
		return fmt.Errorf("%s is not a folder", blocked)
	}
	defer d.Close()
	err = d.Link(a, b)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, serr := d.Lstat(b); serr == nil {
		return fs.ErrExist
	}
	fi, lerr := d.Lstat(a)
	if lerr != nil || !fi.Mode().IsRegular() {
		return err
	}
	src, oerr := d.Open(a)
	if oerr != nil {
		return err
	}
	defer src.Close()
	if st, serr := src.Stat(); serr != nil || !os.SameFile(fi, st) {
		return errChanged
	}
	out, oerr := d.OpenFile(b, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if oerr != nil {
		return oerr
	}
	if _, werr := io.Copy(out, src); werr != nil {
		out.Close()
		d.Remove(b)
		return werr
	}
	return out.Close()
}

// tempsIn lists what a crash may have left of writeIn and newTempIn in the
// folder dir of root (isTempName, regular files), as slash paths of the space.
func tempsIn(root, dir string) []string {
	entries, err := readDirIn(root, dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if isTempName(e.Name()) && e.Type().IsRegular() {
			out = append(out, path.Join(dir, e.Name()))
		}
	}
	return out
}

// removeLeftTemps removes the files in rels (tempsIn, or found by the walk
// of the first index sync) that the repository does not have: a file of the
// repository is never removed, whatever its name. Each is removed in its
// folder, never through a link. The repository is asked only when there is
// something to remove, which is after a crash.
func removeLeftTemps(root string, g *gitRepo, rels []string, log *slog.Logger) {
	if len(rels) == 0 {
		return
	}
	tracked, err := g.trackedLower()
	if err != nil {
		log.Warn("files a crash left could not be told from the repository's; they are left", "files", len(rels), "error", err)
		return
	}
	n := 0
	for _, rel := range rels {
		if tracked[strings.ToLower(rel)] {
			continue
		}
		if fi, blocked, err := lstatIn(root, rel); err != nil || blocked != "" || !fi.Mode().IsRegular() {
			continue
		}
		if removeIn(root, rel) == nil {
			n++
		}
	}
	if n > 0 {
		log.Info("removed half-written files a crash left", "files", n)
	}
}

// trackedLower is every path the repository has, in the index or in HEAD,
// lower-cased (a file system that folds case takes one for the other).
func (g *gitRepo) trackedLower() (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := hookRunner{g}
	out := map[string]bool{}
	add := func(s string) {
		for _, p := range strings.Split(s, "\x00") {
			if p != "" {
				out[strings.ToLower(p)] = true
			}
		}
	}
	s, errOut, err := run.Run(ctx, gitstore.Cmd{Dir: g.dir, Args: []string{"ls-files", "-z"}})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, gitstore.Scrub(errOut))
	}
	add(s)
	if g.r().Head(ctx) != "" {
		s, errOut, err = run.Run(ctx, gitstore.Cmd{Dir: g.dir, Args: []string{"ls-tree", "-r", "-z", "--name-only", "HEAD"}})
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, gitstore.Scrub(errOut))
		}
		add(s)
	}
	return out, nil
}
