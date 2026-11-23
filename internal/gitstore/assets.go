package gitstore

// The plumbing of an attachment repository (milestone 3.1). The server keeps
// no files of it: a clone with every commit and tree and no contents (the
// "meta" repository), from which the table of the current files is read. New
// files are put in by building the commit with Git's own plumbing (the
// contents are hashed from where they are, the trees are made from the trees
// of the last commit), in a scratch repository that borrows the meta one's
// objects, so the contents never stay in the meta repository; the scratch
// repository is removed after the push. Contents are read by fetching them by
// their ids into a scratch repository, copying them out and removing it.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// treeNode is a tree entry as `ls-tree` prints and `mktree` reads it.
type treeNode struct {
	mode, typ, id, name string
}

func parseLsTree(out string) ([]treeNode, error) {
	var es []treeNode
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, name, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree output %q", rec)
		}
		es = append(es, treeNode{f[0], f[1], f[2], name})
	}
	return es, nil
}

// lsTree lists one level of a tree.
func (r *Repo) lsTree(ctx context.Context, tree string) ([]treeNode, error) {
	if tree == "" {
		return nil, nil
	}
	if !commitRe.MatchString(tree) {
		return nil, errors.New("not a tree id")
	}
	out, err := r.run(ctx, Cmd{Args: []string{"ls-tree", "-z", tree}})
	if err != nil {
		return nil, err
	}
	return parseLsTree(out)
}

// mkTree writes a tree of these entries. The contents of the files in them
// need not be in the repository (a partial clone lacks most).
func (r *Repo) mkTree(ctx context.Context, es []treeNode) (string, error) {
	sort.Slice(es, func(i, j int) bool { return treeSortKey(es[i]) < treeSortKey(es[j]) })
	var in bytes.Buffer
	for _, e := range es {
		fmt.Fprintf(&in, "%s %s %s\t%s\x00", e.mode, e.typ, e.id, e.name)
	}
	return r.gitIn(ctx, nil, in.Bytes(), "mktree", "-z", "--missing")
}

// treeSortKey orders names as Git does inside a tree (a tree sorts as if its
// name ended in a slash); mktree does not reorder, it refuses unsorted input.
func treeSortKey(e treeNode) string {
	if e.typ == "tree" {
		return e.name + "/"
	}
	return e.name
}

// TreeEdit is a change to a tree: files to put (path to blob id) and paths to
// take out (a file, or a folder with everything in it).
type TreeEdit struct {
	Put    map[string]string
	Remove []string
}

type editNode struct {
	kids   map[string]*editNode
	put    string // blob id, for a leaf
	remove bool
}

// EditTree makes a tree of base (a tree id; "" for an empty one) with the edit
// applied, and returns its id; empty is true when nothing is left in it (no
// tree is made then).
func (r *Repo) EditTree(ctx context.Context, base string, e TreeEdit) (tree string, empty bool, err error) {
	root := &editNode{kids: map[string]*editNode{}}
	walk := func(p string) *editNode {
		n := root
		for _, seg := range strings.Split(p, "/") {
			k := n.kids[seg]
			if k == nil {
				k = &editNode{kids: map[string]*editNode{}}
				n.kids[seg] = k
			}
			n = k
		}
		return n
	}
	for p, id := range e.Put {
		if !hexRe.MatchString(id) || p == "" || strings.ContainsAny(p, "\x00\n") {
			return "", false, fmt.Errorf("cannot put %q at %q", id, p)
		}
		walk(p).put = id
	}
	for _, p := range e.Remove {
		walk(p).remove = true
	}
	return r.editLevel(ctx, base, root)
}

func (r *Repo) editLevel(ctx context.Context, tree string, n *editNode) (string, bool, error) {
	cur, err := r.lsTree(ctx, tree)
	if err != nil {
		return "", false, err
	}
	byName := map[string]treeNode{}
	for _, c := range cur {
		byName[c.name] = c
	}
	for name, k := range n.kids {
		old, had := byName[name]
		switch {
		case len(k.kids) == 0 && k.put != "":
			byName[name] = treeNode{"100644", "blob", k.put, name}
		case len(k.kids) == 0 && k.remove:
			delete(byName, name)
		default:
			sub := ""
			if had && old.typ == "tree" && !k.remove {
				sub = old.id
			}
			id, empty, err := r.editLevel(ctx, sub, k)
			if err != nil {
				return "", false, err
			}
			if empty {
				delete(byName, name)
			} else {
				byName[name] = treeNode{"040000", "tree", id, name}
			}
		}
	}
	if len(byName) == 0 {
		return "", true, nil
	}
	es := make([]treeNode, 0, len(byName))
	for _, e := range byName {
		es = append(es, e)
	}
	id, err := r.mkTree(ctx, es)
	return id, false, err
}

// HashFiles stores the contents of these files (absolute paths of regular
// files) in the repository and returns their blob ids, in the same order.
func (r *Repo) HashFiles(ctx context.Context, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, f := range files {
		if strings.ContainsAny(f, "\n\x00") || !filepath.IsAbs(f) {
			return nil, fmt.Errorf("cannot hash %q", f)
		}
		in.WriteString(f)
		in.WriteByte('\n')
	}
	out, err := r.run(ctx, Cmd{Args: []string{"hash-object", "-w", "--no-filters", "--stdin-paths"}, Stdin: in.Bytes()})
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	if len(ids) != len(files) {
		return nil, fmt.Errorf("hash-object answered %d ids for %d files", len(ids), len(files))
	}
	return ids, nil
}

// BlobID is the id Git gives data as a file's contents.
func BlobID(data []byte) string { return blobSum(data) }

// CommitTree makes a commit of the tree on top of parent ("" for the first),
// and moves HEAD to it from parent (a compare-and-swap).
func (r *Repo) CommitTree(ctx context.Context, tree, parent, message string, author, committer Identity) (string, error) {
	args, err := commitTreeArgs(tree, parent)
	if err != nil {
		return "", err
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
	commit, err := r.gitIn(ctx, env, []byte(message+"\n"), args...)
	if err != nil {
		return "", err
	}
	if args, err = advanceHeadArgs(commit, parent); err != nil {
		return "", err
	}
	if _, err := r.git(ctx, args...); err != nil {
		return "", err
	}
	return commit, nil
}

// TreeOf is the tree id of a commit ("" for none).
func (r *Repo) TreeOf(ctx context.Context, commit string) string {
	if commit == "" || !commitRe.MatchString(commit) {
		return ""
	}
	out, err := r.git(ctx, "rev-parse", "--verify", "--quiet", commit+"^{tree}")
	if err != nil {
		return ""
	}
	return out
}

// Scratch makes a repository in dir (which must not exist) that has the same
// origin and credential as r and borrows r's objects, so that commits can be
// built on r's history without copying it, and removed after the push. The
// caller removes dir.
func (r *Repo) Scratch(ctx context.Context, dir string) (*Repo, error) {
	gd, err := r.absGitDir(ctx)
	if err != nil {
		return nil, err
	}
	o := r.opt
	o.Dir = dir
	s := New(o)
	if err := s.Init(ctx, "main"); err != nil {
		return nil, err
	}
	sgd, err := s.absGitDir(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(sgd, "objects", "info", "alternates"), []byte(filepath.Join(gd, "objects")+"\n"), 0o644); err != nil {
		return nil, err
	}
	if u := r.RemoteURL(ctx); u != "" {
		if err := s.SetRemote(ctx, u); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// SetHead points HEAD at an existing commit of a repository that has none
// (the scratch repository), so that a commit can be built on it.
func (r *Repo) SetHead(ctx context.Context, commit string) error {
	args, err := advanceHeadArgs(commit, "")
	if err != nil {
		return err
	}
	_, err = r.git(ctx, args...)
	return err
}

// CatBlob writes the contents of a blob to w.
func (r *Repo) CatBlob(ctx context.Context, id string, w io.Writer) error {
	if !hexRe.MatchString(id) {
		return fmt.Errorf("not an object id: %q", id)
	}
	_, _, err := r.runner().Run(ctx, Cmd{Dir: r.opt.Dir, Args: []string{"cat-file", "blob", id}, Stdout: w, AllowUnsafe: r.opt.AllowUnsafe})
	if err != nil {
		return &GitError{Sub: "cat-file", Err: err}
	}
	return nil
}

// FilterRemoveDir builds the tree of the commit HEAD points at with a folder
// taken out and the lines added to the file .gitignore (appended when they
// are not in it): the tree of the commit that stops a space from holding its
// attachments. The commit is made on top of HEAD, moves HEAD to it, and is
// returned; nothing else in the repository or the work tree changes.
func (r *Repo) CommitWithout(ctx context.Context, dir string, ignoreLine string, message string, author, committer Identity) (commit string, err error) {
	head := r.Head(ctx)
	if head == "" {
		return "", errors.New("no commit to build on")
	}
	tree := r.TreeOf(ctx, head)
	root, err := r.lsTree(ctx, tree)
	if err != nil {
		return "", err
	}
	put := map[string]string{}
	have := false
	for _, e := range root {
		if e.name == dir && e.typ == "tree" {
			have = true
		}
		if e.name == ".gitignore" && e.typ == "blob" {
			var b bytes.Buffer
			if err := r.CatBlob(ctx, e.id, &b); err != nil {
				return "", err
			}
			add, text := ensureLine(b.String(), ignoreLine)
			if add {
				id, err := r.hashText(ctx, text)
				if err != nil {
					return "", err
				}
				put[".gitignore"] = id
			}
		}
	}
	if _, ok := put[".gitignore"]; !ok && !hasName(root, ".gitignore") {
		id, err := r.hashText(ctx, ignoreLine+"\n")
		if err != nil {
			return "", err
		}
		put[".gitignore"] = id
	}
	var remove []string
	if have {
		remove = []string{dir}
	}
	if len(put) == 0 && len(remove) == 0 {
		return "", nil
	}
	newTree, empty, err := r.EditTree(ctx, tree, TreeEdit{Put: put, Remove: remove})
	if err != nil {
		return "", err
	}
	if empty {
		return "", errors.New("the commit would have nothing in it")
	}
	return r.CommitTree(ctx, newTree, head, message, author, committer)
}

func hasName(es []treeNode, name string) bool {
	for _, e := range es {
		if e.name == name {
			return true
		}
	}
	return false
}

// ensureLine appends line to text when no line of it is that; reports whether
// it did.
func ensureLine(text, line string) (bool, string) {
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == line {
			return false, text
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return true, text + line + "\n"
}

// hashText stores text as a file's contents: through a file of its own in the
// git directory, because hash-object reads standard input only as paths here.
func (r *Repo) hashText(ctx context.Context, text string) (string, error) {
	gd, err := r.absGitDir(ctx)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(gd, "petalbook-text-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ids, err := r.HashFiles(ctx, []string{f.Name()})
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// AssetPath cleans a path inside an attachment repository: slash separated,
// no empty, "." or ".." segment, nothing hidden.
func AssetPath(p string) bool {
	if p == "" || path.Clean(p) != p || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

func blobSum(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// ListTreeUnder is ListTree for the files below one folder of the commit.
func (r *Repo) ListTreeUnder(ctx context.Context, commit, dir string) ([]TreeEntry, error) {
	if !commitRe.MatchString(commit) {
		return nil, errors.New("not a commit id")
	}
	if dir == "" || strings.ContainsAny(dir, "\x00\n") || strings.HasPrefix(dir, "-") {
		return nil, fmt.Errorf("unusable folder %q", dir)
	}
	out, err := r.run(ctx, Cmd{Args: []string{"ls-tree", "-r", "-z", commit, "--", dir}})
	if err != nil {
		return nil, err
	}
	var es []TreeEntry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree output %q", rec)
		}
		es = append(es, TreeEntry{Mode: f[0], Type: f[1], ID: f[2], Path: p})
	}
	return es, nil
}

// CommitSubject is the first line of a commit's message.
func (r *Repo) CommitSubject(ctx context.Context, commit string) string {
	if !commitRe.MatchString(commit) {
		return ""
	}
	out, err := r.git(ctx, "log", "-n", "1", "--format=%s", commit)
	if err != nil {
		return ""
	}
	return out
}
