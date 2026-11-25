package gitstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A commit built from the folders it changes (commitByFolders): the trees of
// HEAD on the way to each changed path are read (one cat-file), changed in
// memory, written back as new trees (one mktree), and the commit made of the
// new root; the real index learns the new entries of these paths only (one
// update-index). Nothing reads or writes the whole index or the whole tree:
// a commit costs what its folders hold, not what the space holds.
//
// Git commands: rev-parse (HEAD and its tree), hash-object (the contents of
// paths that have none recorded, when there are any), cat-file, update-index
// (first: it takes the index's lock, so a commit is not made while someone
// else writes the index), mktree, commit-tree, and the move of the branch. Before, a commit took ten, of which
// read-tree, write-tree and diff --cached each went through the whole index.

// treeEntry is an entry of a tree: its mode as git writes it ("100644",
// "100755", "120000", "160000", "40000") and its object id (hex).
type treeEntry struct {
	mode string
	id   string
}

func (e treeEntry) isTree() bool { return e.mode == "40000" }

// pathChange is what a commit does to one path: the blob it gets (with its
// mode), or gone.
type pathChange struct {
	id   string // "" for gone
	mode string // "" for the mode it has, or 100644 for a new file
}

// errNotHere is commitByFolders saying it cannot make this commit (a path
// that is no regular file, a repository with another object format, no
// commit yet): the caller makes it the other way.
var errNotHere = errors.New("not a commit built from its folders")

// commitByFolders makes the commit of changes on top of HEAD as described
// above, or returns errNotHere before it changed anything.
func (r *Repo) commitByFolders(ctx context.Context, changes map[string]pathChange, message func([]Change) string, env []string) (bool, error) {
	head, headTree, err := r.headAndTree(ctx)
	if err != nil {
		return false, err
	}
	if head != "" && len(headTree) != 40 {
		return false, errNotHere // not SHA-1: the index way
	}
	if gd, err := r.absGitDir(ctx); err == nil {
		clearOldIndexes(gd) // what the index way left after a crash
	}
	// The folders on the way to every changed path, the root first.
	dirs := map[string]bool{"": true}
	for p := range changes {
		for d := path.Dir(p); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	trees := map[string]map[string]treeEntry{}
	if head != "" {
		if trees, err = r.readTrees(ctx, headTree, dirs); err != nil {
			return false, err
		}
	}
	// What each path was: for the modes kept and for the commit message.
	before := map[string]treeEntry{}
	for p := range changes {
		if e, ok := trees[parentDir(p)][path.Base(p)]; ok && !e.isTree() {
			before[p] = e
		}
	}
	// Apply, removals first: a name that goes as a file may come back as a
	// folder of the changes, and the other way round.
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if changes[p].id == "" {
			delete(dirTree(trees, parentDir(p)), path.Base(p))
		}
	}
	for _, p := range paths {
		c := changes[p]
		if c.id == "" {
			continue
		}
		mode := c.mode
		if mode == "" {
			mode = "100644"
			if b, ok := before[p]; ok && (b.mode == "100755" || b.mode == "100644") {
				mode = b.mode // an executable page stays one
			}
		}
		dir := parentDir(p)
		// The folders on the way are folders, whatever was there.
		for d := dir; d != ""; d = parentDir(d) {
			delete(dirTree(trees, parentDir(d)), path.Base(d))
			dirTree(trees, d)
		}
		dirTree(trees, dir)[path.Base(p)] = treeEntry{mode: mode, id: c.id}
	}
	// New trees, deepest first; an empty folder is no tree.
	order := make([]string, 0, len(trees))
	for d := range trees {
		order = append(order, d)
	}
	sort.Slice(order, func(i, j int) bool {
		if a, b := depth(order[i]), depth(order[j]); a != b {
			return a > b
		}
		return order[i] < order[j]
	})
	var batch bytes.Buffer
	var want []string
	root := ""
	for _, d := range order {
		entries := trees[d]
		if len(entries) == 0 && d != "" {
			// An empty folder is no tree; a file of the change that took its
			// name stays.
			if p := dirTree(trees, parentDir(d)); p[path.Base(d)].isTree() {
				delete(p, path.Base(d))
			}
			continue
		}
		if len(entries) == 0 {
			return false, errNotHere // nothing left at all
		}
		id := treeObject(entries)
		if d == "" {
			root = id
		} else {
			dirTree(trees, parentDir(d))[path.Base(d)] = treeEntry{mode: "40000", id: id}
		}
		if len(want) > 0 {
			batch.WriteByte(0) // the end of the tree before
		}
		names := make([]string, 0, len(entries))
		for n := range entries {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			e := entries[n]
			typ := "blob"
			switch e.mode {
			case "40000":
				typ = "tree"
			case "160000":
				typ = "commit"
			}
			fmt.Fprintf(&batch, "%s %s %s\t%s\x00", modeForMktree(e.mode), typ, e.id, n)
		}
		want = append(want, id)
	}
	// The changes, for the message: renames are a page gone from one place
	// and come to another with the same content, or the only page gone and
	// the only page come.
	cs := describe(paths, changes, before)
	// The index first, as a commit staged in it would be: it fails while
	// someone else holds the index, and then nothing is committed.
	if err := r.syncIndex(ctx, paths, changes, before); err != nil {
		return false, err
	}
	if root == headTree {
		return false, nil // the change netted out to nothing
	}
	if len(want) > 0 {
		out, err := r.run(ctx, Cmd{Args: []string{"mktree", "-z", "--missing", "--batch"}, Stdin: batch.Bytes()})
		if err != nil {
			return false, err
		}
		if got := strings.Fields(out); strings.Join(got, " ") != strings.Join(want, " ") {
			return false, fmt.Errorf("git made other trees than were built: %v, %v", got, want)
		}
	}
	args, err := commitTreeArgs(root, head)
	if err != nil {
		return false, err
	}
	commit, err := r.gitIn(ctx, env, []byte(message(cs)+"\n"), args...)
	if err != nil {
		return false, err
	}
	if args, err = advanceHeadArgs(commit, head); err != nil {
		return false, err
	}
	if _, err := r.git(ctx, args...); err != nil {
		return false, err
	}
	return true, nil
}

func depth(d string) int {
	if d == "" {
		return -1
	}
	return strings.Count(d, "/")
}

// parentDir is the folder of d, "" for the root's folders.
func parentDir(d string) string {
	if p := path.Dir(d); p != "." {
		return p
	}
	return ""
}

// dirTree is the entries of folder d (made, empty, when it has none yet).
func dirTree(trees map[string]map[string]treeEntry, d string) map[string]treeEntry {
	if d == "." {
		d = ""
	}
	t := trees[d]
	if t == nil {
		t = map[string]treeEntry{}
		trees[d] = t
	}
	return t
}

// modeForMktree is a mode as mktree reads it.
func modeForMktree(m string) string {
	if m == "40000" {
		return "040000"
	}
	return m
}

// treeObject is the tree object of entries, in git's order (a folder sorts
// as if its name ended in "/"), and its id.
func treeObject(entries map[string]treeEntry) string {
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	key := func(n string) string {
		if entries[n].isTree() {
			return n + "/"
		}
		return n
	}
	sort.Slice(names, func(i, j int) bool { return key(names[i]) < key(names[j]) })
	var body bytes.Buffer
	for _, n := range names {
		e := entries[n]
		raw, _ := hex.DecodeString(e.id)
		body.WriteString(e.mode)
		body.WriteByte(' ')
		body.WriteString(n)
		body.WriteByte(0)
		body.Write(raw)
	}
	h := sha1.New()
	fmt.Fprintf(h, "tree %d\x00", body.Len())
	h.Write(body.Bytes())
	return hex.EncodeToString(h.Sum(nil))
}

// readTrees reads, with one cat-file, the trees of the folders dirs in the
// tree root (a folder that is not there, or is no folder, has none).
func (r *Repo) readTrees(ctx context.Context, root string, dirs map[string]bool) (map[string]map[string]treeEntry, error) {
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		if strings.ContainsAny(d, "\n") {
			return nil, errNotHere
		}
		names = append(names, d)
	}
	sort.Strings(names)
	var in bytes.Buffer
	for _, d := range names {
		if d == "" {
			in.WriteString(root + "\n")
		} else {
			in.WriteString(root + ":" + d + "\n")
		}
	}
	var out bytes.Buffer
	if _, err := r.run(ctx, Cmd{Args: []string{"cat-file", "--batch"}, Stdin: in.Bytes(), Stdout: &out}); err != nil {
		return nil, err
	}
	trees := map[string]map[string]treeEntry{}
	br := bufio.NewReader(&out)
	for _, d := range names {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("cat-file stopped early: %w", err)
		}
		f := strings.Fields(line)
		if len(f) >= 2 && f[len(f)-1] == "missing" {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("cat-file printed %q", line)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, err
		}
		body := make([]byte, size+1) // and the newline after it
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
		if f[1] != "tree" {
			continue // a file where a folder of the change will be
		}
		entries, err := parseTree(body[:size])
		if err != nil {
			return nil, fmt.Errorf("the tree of %q: %w", d, err)
		}
		trees[d] = entries
	}
	return trees, nil
}

// parseTree reads a tree object's entries.
func parseTree(b []byte) (map[string]treeEntry, error) {
	out := map[string]treeEntry{}
	for len(b) > 0 {
		sp := bytes.IndexByte(b, ' ')
		nul := bytes.IndexByte(b, 0)
		if sp < 0 || nul < sp || len(b) < nul+21 {
			return nil, errors.New("a damaged tree")
		}
		out[string(b[sp+1:nul])] = treeEntry{mode: string(b[:sp]), id: hex.EncodeToString(b[nul+1 : nul+21])}
		b = b[nul+21:]
	}
	return out, nil
}

// describe is the changes of a commit for its message, renames paired.
func describe(paths []string, changes map[string]pathChange, before map[string]treeEntry) []Change {
	var added, gone []string
	var out []Change
	for _, p := range paths {
		c := changes[p]
		b, had := before[p]
		switch {
		case c.id == "" && had:
			gone = append(gone, p)
		case c.id != "" && !had:
			added = append(added, p)
		case c.id != "" && b.id != c.id:
			out = append(out, Change{'M', p, p})
		}
	}
	byID := map[string][]string{}
	for _, p := range gone {
		byID[before[p].id] = append(byID[before[p].id], p)
	}
	paired := map[string]bool{}
	var leftAdded []string
	for _, p := range added {
		if olds := byID[changes[p].id]; len(olds) > 0 {
			out = append(out, Change{'R', olds[0], p})
			paired[olds[0]] = true
			byID[changes[p].id] = olds[1:]
			continue
		}
		leftAdded = append(leftAdded, p)
	}
	var leftGone []string
	for _, p := range gone {
		if !paired[p] {
			leftGone = append(leftGone, p)
		}
	}
	if len(leftGone) == 1 && len(leftAdded) == 1 && filepath.Ext(leftGone[0]) == filepath.Ext(leftAdded[0]) {
		out = append(out, Change{'R', leftGone[0], leftAdded[0]})
		leftGone, leftAdded = nil, nil
	}
	for _, p := range leftAdded {
		out = append(out, Change{'A', p, p})
	}
	for _, p := range leftGone {
		out = append(out, Change{'D', p, p})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].New < out[j].New })
	return out
}

// syncIndex gives the real index the entries the commit gave these paths
// (and takes out the ones it removed), in one update-index: a person's own
// staged changes elsewhere stay as they are.
func (r *Repo) syncIndex(ctx context.Context, paths []string, changes map[string]pathChange, before map[string]treeEntry) error {
	var in bytes.Buffer
	zero := strings.Repeat("0", 40)
	for _, p := range paths {
		if changes[p].id == "" {
			fmt.Fprintf(&in, "0 %s 0\t%s\x00", zero, p)
		}
	}
	for _, p := range paths {
		c := changes[p]
		if c.id == "" {
			continue
		}
		mode := c.mode
		if mode == "" {
			mode = "100644"
			if b, ok := before[p]; ok && b.mode == "100755" {
				mode = b.mode
			}
		}
		fmt.Fprintf(&in, "%s %s 0\t%s\x00", mode, c.id, p)
	}
	if in.Len() == 0 {
		return nil
	}
	_, err := r.gitIn(ctx, nil, in.Bytes(), "-c", "core.splitIndex=true", "update-index", "-z", "--index-info")
	return err
}
