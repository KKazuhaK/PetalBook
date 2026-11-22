package gitstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// HeadWindow is how much of the start of a file is read to decide whether a
// version belongs to a page: the header is at the start.
const HeadWindow = 16 << 10

// Version is one commit that changed a file, as followed through renames.
type Version struct {
	Commit string
	Time   time.Time
	Author string
	// Path is the file's path in this commit (for a deletion, the path it had).
	Path       string
	Status     byte // A, M, D, R, T
	Similarity int  // for R: how much of the content is unchanged, in percent
	// Rev is the blob id of the file in this commit; "" for a deletion.
	Rev string
}

// HistoryQuery selects a page of versions.
type HistoryQuery struct {
	// Start is the commit to walk back from and Path the file's path there.
	Start, Path string
	Limit       int
	// Accept is given the path and the first bytes of the file as it was in a
	// version (for a deletion: as it was just before) and says whether the
	// file is the one wanted. Versions it refuses are left out, which is how a
	// path that another file was moved or created over is told apart.
	Accept func(path string, head []byte) bool
	// SkipFirst is a commit that is left out if the walk starts with it: the
	// version a cursor continues after.
	SkipFirst string
}

// Cursor says where the next page continues.
type Cursor struct{ Commit, Path string }

// batchCap is how many commits one `git log` call reads; more are read when
// versions were refused.
const (
	minBatch = 30
	maxBatch = 200
	// maxScanned bounds the work for one page when most versions are refused.
	maxScanned = 2000
)

// FileHistory returns up to q.Limit versions, newest first, and the cursor of
// the next page (nil when there is none). Each page costs one `git log` (more
// only when versions were refused) and one `git cat-file --batch`, whatever
// its size, and starts at the cursor's commit, not at the top of the history.
func (r *Repo) FileHistory(ctx context.Context, q HistoryQuery) ([]Version, *Cursor, error) {
	if q.Limit < 1 {
		q.Limit = 30
	}
	if q.Start != "HEAD" && !commitRe.MatchString(q.Start) {
		return nil, nil, errors.New("not a commit id")
	}
	// A path that starts with a dash is fine: it comes after "--" on the command line.
	if q.Path == "" || strings.ContainsAny(q.Path, "\n\x00") {
		return nil, nil, errors.New("unusable path")
	}
	var out []Version
	cur := Cursor{Commit: q.Start, Path: q.Path}
	skip := q.SkipFirst
	scanned := 0
	for {
		want := q.Limit + 1 - len(out)
		n := min(max(want+want/2+2, minBatch), maxBatch)
		raw, err := r.logFollow(ctx, cur.Commit, cur.Path, n+1)
		if err != nil {
			return nil, nil, err
		}
		exhausted := len(raw) < n+1
		if len(raw) > 0 && raw[0].Commit == skip {
			raw = raw[1:]
		}
		skip = ""
		if len(raw) > n {
			raw = raw[:n]
		}
		if len(raw) == 0 {
			break
		}
		specs := make([]string, len(raw))
		for i, v := range raw {
			if v.Status == 'D' {
				specs[i] = v.Commit + "^:" + v.Path
			} else {
				specs[i] = v.Commit + ":" + v.Path
			}
		}
		heads, err := r.readHeads(ctx, specs, HeadWindow)
		if err != nil {
			return nil, nil, err
		}
		for i, v := range raw {
			scanned++
			h := heads[i]
			if !h.found || (q.Accept != nil && !q.Accept(v.Path, h.head)) {
				continue
			}
			if v.Status != 'D' {
				v.Rev = h.oid
			}
			out = append(out, v)
			if len(out) == q.Limit+1 {
				last := out[q.Limit-1]
				return out[:q.Limit], &Cursor{Commit: last.Commit, Path: last.Path}, nil
			}
		}
		if exhausted || scanned >= maxScanned {
			break
		}
		last := raw[len(raw)-1]
		cur = Cursor{Commit: last.Commit, Path: last.Path}
		skip = last.Commit
	}
	return out, nil, nil
}

// logFollow runs `git log --follow` for one file and parses what it prints.
// Output of -z with a format is, per commit, the format fields separated by
// NUL, then the name-status entries (status, path[, path]) each separated by NUL.
func (r *Repo) logFollow(ctx context.Context, start, path string, n int) ([]Version, error) {
	out, err := r.run(ctx, Cmd{Args: []string{"log", "--follow", "--name-status", "-z", "--no-color", "-n", strconv.Itoa(n),
		"--format=%H%x00%ct%x00%an", start, "--", path}})
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

func parseLog(out string) []Version {
	tok := strings.Split(out, "\x00")
	var vs []Version
	for i := 0; i < len(tok); {
		h := strings.TrimLeft(tok[i], "\n")
		if !hexRe.MatchString(h) || i+3 > len(tok) {
			i++
			continue
		}
		ct, _ := strconv.ParseInt(tok[i+1], 10, 64)
		v := Version{Commit: h, Time: time.Unix(ct, 0).UTC(), Author: tok[i+2]}
		i += 3
		// The entries of this commit: --follow shows only the followed file.
		for i < len(tok) {
			st := strings.TrimLeft(tok[i], "\n")
			if st == "" || !isStatus(st) {
				break
			}
			switch st[0] {
			case 'R', 'C':
				if i+2 >= len(tok) {
					return vs
				}
				v.Status, v.Path = st[0], tok[i+2]
				v.Similarity, _ = strconv.Atoi(st[1:])
				i += 3
			default:
				if i+1 >= len(tok) {
					return vs
				}
				v.Status, v.Path = st[0], tok[i+1]
				i += 2
			}
		}
		if v.Status != 0 {
			vs = append(vs, v)
		}
	}
	return vs
}

func isStatus(s string) bool {
	if s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type head struct {
	found bool
	oid   string
	head  []byte
	full  []byte
}

// readHeads reads the objects named by specs ("<commit>:<path>") through one
// `git cat-file --batch`, keeping the first window bytes of each. A path in a
// commit that has no such file (or is not a file) is not found.
func (r *Repo) readHeads(ctx context.Context, specs []string, window int) ([]head, error) {
	return r.readBlobs(ctx, specs, window, false)
}

func (r *Repo) readBlobs(ctx context.Context, specs []string, window int, full bool) ([]head, error) {
	for _, s := range specs {
		if strings.ContainsAny(s, "\n\x00") {
			return nil, errors.New("unusable path")
		}
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := r.run(ctx, Cmd{Args: []string{"cat-file", "--batch"}, Stdin: []byte(strings.Join(specs, "\n") + "\n"), Stdout: pw})
		pw.CloseWithError(err)
		errc <- err
	}()
	br := bufio.NewReaderSize(pr, 64<<10)
	out := make([]head, len(specs))
	var perr error
	for i := range specs {
		line, err := br.ReadString('\n')
		if err != nil {
			perr = err
			break
		}
		f := strings.Fields(strings.TrimRight(line, "\n"))
		if len(f) != 3 || f[1] != "blob" {
			continue // "<spec> missing", or a tree or commit
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || size < 0 {
			perr = fmt.Errorf("unexpected cat-file output %q", line)
			break
		}
		keep := min(size, int64(window))
		if full {
			if size > MaxOutput {
				perr = errors.New("the file is too large")
				break
			}
			keep = size
		}
		buf := make([]byte, keep)
		if _, err := io.ReadFull(br, buf); err != nil {
			perr = err
			break
		}
		if size > keep {
			if _, err := io.CopyN(io.Discard, br, size-keep); err != nil {
				perr = err
				break
			}
		}
		if _, err := br.ReadByte(); err != nil { // the newline after the content
			perr = err
			break
		}
		out[i] = head{found: true, oid: f[0], head: buf, full: buf}
	}
	io.Copy(io.Discard, pr) // let the process finish
	err := <-errc
	if perr != nil {
		return nil, perr
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadBlob reads a whole file as it was in a commit, and its blob id. A
// missing file is fs.ErrNotExist-like: ErrNoSuchFile.
func (r *Repo) ReadBlob(ctx context.Context, commit, path string) ([]byte, string, error) {
	if !commitRe.MatchString(commit) {
		return nil, "", errors.New("not a commit id")
	}
	hs, err := r.readBlobs(ctx, []string{commit + ":" + path}, 0, true)
	if err != nil {
		return nil, "", err
	}
	if !hs[0].found {
		return nil, "", ErrNoSuchFile
	}
	return hs[0].full, hs[0].oid, nil
}

// ErrNoSuchFile: the commit has no such file.
var ErrNoSuchFile = errors.New("no such file in that commit")

// maxPredecessorCandidates bounds how many deleted files of one commit are
// looked at when searching for a file's earlier name.
const maxPredecessorCandidates = 500

// Predecessor looks, in the commit that added a file, for the file it was
// before: one that the same commit deleted and that accept recognises (by the
// start of its content as it was in the parent). Git's own rename detection
// gives up when most of the content changed in the same commit, which is what
// happens when a title is changed together with the text; an identity kept
// inside the file does not have that limit. It returns the parent commit and
// the earlier path, or "" when there is none.
func (r *Repo) Predecessor(ctx context.Context, commit string, candidate func(path string) bool, accept func(path string, head []byte) bool) (parent, path string, err error) {
	if !commitRe.MatchString(commit) {
		return "", "", errors.New("not a commit id")
	}
	parent = r.CommitOf(ctx, commit+"^")
	if parent == "" {
		return "", "", nil // the first commit: nothing came before
	}
	changes, err := r.DiffNames(ctx, parent, commit)
	if err != nil {
		return "", "", err
	}
	var paths, specs []string
	for _, c := range changes {
		if c.Status != 'D' || (candidate != nil && !candidate(c.New)) {
			continue
		}
		paths = append(paths, c.New)
		specs = append(specs, parent+":"+c.New)
		if len(paths) == maxPredecessorCandidates {
			break
		}
	}
	if len(paths) == 0 {
		return "", "", nil
	}
	heads, err := r.readHeads(ctx, specs, HeadWindow)
	if err != nil {
		return "", "", err
	}
	for i, h := range heads {
		if h.found && accept(paths[i], h.head) {
			return parent, paths[i], nil
		}
	}
	return "", "", nil
}
