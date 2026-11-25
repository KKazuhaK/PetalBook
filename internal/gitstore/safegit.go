package gitstore

// PetalBook never rewrites or deletes anything on the remote. Every push is a
// fast-forward push of HEAD to one branch, and this file is the only place
// that knows what such a command line looks like: pushArgs builds it, and
// refuseDestructive (which runs in front of every real git call) refuses
// anything that could force, delete or mirror. The static test in
// safegit_test.go keeps these words out of every other non-test source file.
//
// This is the file that carries the safety guarantee. It is kept as close as
// possible to the one in RackBook's gitstore; additions to the allow list are
// marked "added:" with the reason.

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var branchRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._/-]*$`)

// ValidBranch reports whether b is a branch name the store will use.
func ValidBranch(b string) error { return validBranch(b) }

func validBranch(b string) error {
	if !branchRe.MatchString(b) || strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, ".") {
		return fmt.Errorf("refusing to push to unusable branch name %q", b)
	}
	return nil
}

// pushArgs is the only builder of push command lines. The result is always
// `push --quiet origin HEAD:refs/heads/<branch>`: no force, no "+" or ":"
// refspec, no deletion.
func pushArgs(branch string) ([]string, error) {
	if err := validBranch(branch); err != nil {
		return nil, err
	}
	return []string{"push", "--quiet", "origin", "HEAD:refs/heads/" + branch}, nil
}

// keepRefPrefix is where commits that never reached the remote are kept when
// the clone has to move to the remote's history (see KeepUnpushed).
const keepRefPrefix = "refs/petalbook/unpushed/"

var (
	keepRefRe = regexp.MustCompile(`^refs/petalbook/unpushed/[0-9A-Za-z._-]{1,64}$`)
	commitRe  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// keepRefArgs builds the one ref-creating command: a new ref under
// refs/petalbook/unpushed/ pointing at a full commit id. Never a deletion and
// never an existing ref outside that namespace.
// added: crash recovery and an accepted rewrite must keep unpushed work
// reachable before the clone moves to the remote.
func keepRefArgs(name, commit string) ([]string, error) {
	ref := keepRefPrefix + name
	if !keepRefRe.MatchString(ref) || strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") || !commitRe.MatchString(commit) {
		return nil, fmt.Errorf("refusing to make the ref %q at %q", ref, commit)
	}
	return []string{"update-ref", ref, commit}, nil
}

// advanceHeadArgs builds the command that makes a new commit the head of the
// branch: a compare-and-swap of HEAD (which names the branch) from the commit
// it was built on to the new one. old is "" when there is no commit yet; the
// ref must then not exist. Nothing else about HEAD is ever written this way:
// no deletion, no other ref, no move without saying what it moves from.
// added: commits are assembled without reading the work tree (commitChanges).
func advanceHeadArgs(commit, old string) ([]string, error) {
	if old == "" {
		old = strings.Repeat("0", len(commit))
	}
	if !commitRe.MatchString(commit) || !commitRe.MatchString(old) || len(commit) != len(old) || strings.Trim(commit, "0") == "" {
		return nil, fmt.Errorf("refusing to move HEAD to %q from %q", commit, old)
	}
	return []string{"update-ref", "HEAD", commit, old}, nil
}

// commitTreeArgs builds the command that makes a commit object of a tree,
// with the commit before it as its only parent ("" for the first commit) and
// the message on standard input.
func commitTreeArgs(tree, parent string) ([]string, error) {
	if !commitRe.MatchString(tree) || (parent != "" && !commitRe.MatchString(parent)) {
		return nil, fmt.Errorf("refusing to make a commit of %q on %q", tree, parent)
	}
	// A repository's own configuration may ask for signed commits; there is
	// no key here to sign with, and a commit that can never be made would
	// hold every later one up.
	args := []string{"-c", "commit.gpgsign=false", "commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return append(args, "-F", "-"), nil
}

// maintainArgs are the commands of Repo.Maintain, the only builder of them;
// refuseDestructive accepts exactly these shapes.
func maintainArgs() [][]string {
	return [][]string{
		{"repack", "-d", "-q", "--geometric=2"},
		{"prune-packed", "-q"},
		{"commit-graph", "write", "--reachable"},
	}
}

var errDestructive = errors.New("refusing a git command that could rewrite or delete history")

// allowedConfigKeys are the only "-c key=value" settings passed by callers.
var allowedConfigKeys = map[string]bool{"commit.gpgsign": true, "core.splitindex": true, "safe.directory": true, "core.quotepath": true, "core.autocrlf": true}

// Subcommand returns the git subcommand of a command line and its arguments,
// skipping the global options the runner allows.
func Subcommand(args []string) (string, []string) { return subcommand(args) }

// subcommand returns the git subcommand and its arguments. Only the global
// options PetalBook itself uses may precede it: -C <dir>, -c <allowed key>=<value>
// and --no-pager. For anything else (--git-dir, --attr-source, --foo x, ...) it
// returns "", which refuseDestructive refuses.
func subcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" && i+1 < len(args):
			i++
		case a == "-c" && i+1 < len(args):
			key, _, ok := strings.Cut(args[i+1], "=")
			if !ok || !allowedConfigKeys[strings.ToLower(key)] {
				return "", nil
			}
			i++
		case a == "--no-pager":
		case strings.HasPrefix(a, "-"):
			return "", nil
		default:
			return a, args[i+1:]
		}
	}
	return "", nil
}

// allowedOptions lists, for every subcommand PetalBook runs, the only option
// tokens it may be given (a trailing "*" matches a prefix). Anything else that
// starts with "-" is refused, which keeps out --upload-pack, --receive-pack,
// --exec, --output, -u, -o/--push-option, --ext-diff, --textconv and every
// other option that runs a program or writes somewhere. Everything after a bare
// "--" is a path. A new need has to be added here on purpose. push, remote,
// update-ref and config are checked exactly below instead (config: only
// `config --local --unset-all` of a key that makes a clone partial).
//
// The additions to RackBook's list, and why:
//
//	add, rm      --pathspec-from-file=- / --pathspec-file-nul: commit exactly a list
//	             of paths given on stdin, so the list is not limited by argv size.
//	             -A, --cached, -r, -q, --ignore-unmatch: stage a change or a deletion
//	             of those paths (never the whole tree).
//	cat-file     --batch: read many blobs of the history in one process.
//	checkout     --pathspec-from-file=- ...: restore the files of a discarded change in one command.
//	diff         --cached/--name-status/-z/--no-renames/-M: what a commit changed, and what
//	             moved between two commits (the index update after a pull).
//	fetch        --no-tags: fetch only the branch, not every tag of the remote.
//	init         --quiet and --initial-branch=*: a new space's repository.
//	log          --follow, --name-status, -z, --no-color, --format=*: page history.
//	ls-remote    --heads: whether the remote has the branch yet (connecting).
//	ls-tree      -r, -z, --name-only: which of the discarded files exist in the parent commit.
//	rev-list     --left-right: how far ahead and behind in one command.
//	read-tree HEAD, ls-files -s -z, update-index --index-info, write-tree,
//	commit-tree, update-ref HEAD: a commit is assembled in an index of its own
//	             from the last commit and the staged entries of the change, and
//	             the branch moved to it (Repo.commitChanges). No file of the
//	             work tree is read but those of the change; what grows with the
//	             space is only the index in memory (about 60 ms for 20 000
//	             files). Their arguments are checked exactly in refuseDestructive.
//	fetch --filter=blob:none, --stdin, --no-write-fetch-head: a partial clone
//	             (milestone 3.1) is made with a filter, and the contents of the
//	             files that are missing are fetched by their ids, in the form
//	             Git's own lazy fetch uses, with the server's credential.
//	cat-file --batch-check: which of a list of ids are not in the clone.
//	rev-list --all: with --objects --missing=print, what a clone lacks in any
//	             ref before a pinned space's clone stops being partial.
//	log, diff    --raw, --no-abbrev: the blob ids of a change, so that the
//	             contents a partial clone lacks can be named; log -M100%: only
//	             exact renames, which need no content (--follow would otherwise
//	             read the files to compare them).
//	status       --untracked-files=normal: files in the work tree that no commit has.
//	status       --porcelain, -z, --untracked-files=no: modified tracked files when a space opens.
//
//	hash-object  -w --no-filters --stdin-paths: the contents of an attachment, read from
//	             where it is, become a blob (an attachment repository, below). The
//	             same, and --stdin, for the files of a commit that is cut from the
//	             contents the server wrote, not the work tree (CommitPathsAs).
//	mktree       -z --missing: a tree made of entries whose contents this clone does not
//	             have; with ls-tree (no -r), commit-tree and update-ref HEAD, the commit that
//	             adds files to an attachment repository, or takes a space's attachments
//	             out of its repository, is built without a work tree or an index.
//	cat-file blob <id>: the contents of a file fetched by its id, copied to the cache.
//
//	repack -d -q --geometric=2, prune-packed -q, commit-graph write --reachable: Repo.Maintain
//	             packs the loose objects commits leave behind. Exactly these three
//	             forms (checked in refuseDestructive): repack without -a only packs
//	             what is loose and reachable and never drops an unreachable object,
//	             prune-packed only deletes loose objects that a pack holds, and the
//	             commit graph is a cache. gc, prune and repack -a/-A/-k/--cruft, which
//	             could remove an object a commit in flight has written but not yet
//	             referenced, stay refused.
//
// Dropped from RackBook's list because nothing here needs them: --grep and
// --fixed-strings (trailer search), merge-base's use for pull requests.
var allowedOptions = map[string][]string{
	"add":          {"--", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"},
	"update-index": {"--add", "--replace", "-z", "--stdin", "--index-info"},
	"read-tree":    {},
	"write-tree":   {},
	"commit-tree":  {},
	"config":       {},
	"ls-files":     {"-s", "-z", "--"},
	"rm":           {"--", "--cached", "-r", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"},
	"cat-file":     {"--batch", "--batch-check"},
	"checkout":     {"--quiet", "--", "--pathspec-from-file=-", "--pathspec-file-nul"},
	"diff":         {"--cached", "--name-status", "-z", "-M", "--no-renames", "--raw", "--no-abbrev", "--"},
	"fetch":        {"--quiet", "--no-tags", "--filter=blob:none", "--stdin", "--no-write-fetch-head"},
	"init":         {"--quiet", "-q", "--initial-branch=*"},
	"log":          {"-n", "--format=*", "--follow", "--name-status", "-z", "--no-color", "-M100%", "--raw", "--no-abbrev", "--"},
	"ls-remote":    {"--heads"},
	"ls-tree":      {"-r", "-z", "--name-only", "--"},
	"merge":        {"--quiet", "--ff-only"},
	"merge-base":   {"--is-ancestor"},
	"push":         {},
	"remote":       {},
	"update-ref":   {},
	"hash-object":  {"-w", "--no-filters", "--stdin-paths", "--stdin"},
	"mktree":       {"-z", "--missing"},
	"version":      {},
	"for-each-ref": {"--format=*"},
	"reset":        {"--quiet", "--hard"},
	"rev-list":     {"--count", "--left-right", "--not", "--objects", "--missing=print", "--all"},
	"rev-parse":    {"--is-inside-work-tree", "--abbrev-ref", "--absolute-git-dir", "--show-prefix", "--revs-only", "--verify", "--quiet", "--short"},
	"status":       {"--porcelain", "-z", "--untracked-files=no", "--untracked-files=normal"},
	// An export of a space (3.5): the files of a commit as a tar, and a bundle
	// of the whole history. Both only read the repository (the bundle writes
	// one file, named by the caller, outside it).
	"archive": {},
	"bundle":  {},
	// Packing (Repo.Maintain); their arguments are checked exactly below.
	"repack":       {},
	"prune-packed": {},
	"commit-graph": {},
}

// partialConfigKeys are the keys of a clone's own configuration that make it
// partial: the only ones `git config` is run for, to remove them or to read
// whether the clone is one (partialKeysPattern).
var partialConfigKeys = map[string]bool{"remote.origin.partialclonefilter": true, "remote.origin.promisor": true, "extensions.partialclone": true}

// partialKeysPattern is the one pattern `git config --get-regexp` is run with:
// the keys that say origin is a promisor remote of the clone.
const partialKeysPattern = `^(remote\.origin\.promisor|extensions\.partialclone)$`

func optionAllowed(sub, opt string) bool {
	for _, a := range allowedOptions[sub] {
		if a == opt || (strings.HasSuffix(a, "*") && strings.HasPrefix(opt, strings.TrimSuffix(a, "*"))) {
			return true
		}
	}
	return false
}

// remoteArgsOK checks the four forms of `remote` the store uses: get-url,
// add, set-url (a URL that passed ValidateURL, which never starts with a dash)
// and removal of origin.
// added: connecting a space to a repository, and disconnecting it again; all
// of them change only this clone's own configuration.
func remoteArgsOK(rest []string) bool {
	switch {
	case len(rest) == 2 && rest[0] == "get-url" && rest[1] == "origin":
		return true
	case len(rest) == 2 && rest[0] == "remove" && rest[1] == "origin":
		return true
	case len(rest) == 3 && (rest[0] == "add" || rest[0] == "set-url") && rest[1] == "origin":
		return rest[2] != "" && !strings.HasPrefix(rest[2], "-")
	}
	return false
}

// refuseDestructive is a last line of defence in the real runner. A push must
// be exactly what pushArgs builds, only the subcommands of allowedOptions may
// run, and each may only use its own options.
func refuseDestructive(args []string) error {
	sub, rest := subcommand(args)
	if _, ok := allowedOptions[sub]; !ok {
		return fmt.Errorf("%w: git %s (only -C, -c <known key>, --no-pager may precede the subcommand; subcommands: see allowedOptions)", errDestructive, sub)
	}
	switch sub {
	case "push":
		if len(rest) != 3 || rest[0] != "--quiet" || rest[1] != "origin" || !strings.HasPrefix(rest[2], "HEAD:refs/heads/") ||
			validBranch(strings.TrimPrefix(rest[2], "HEAD:refs/heads/")) != nil {
			return fmt.Errorf("%w: push %s", errDestructive, strings.Join(rest, " "))
		}
	case "remote":
		if !remoteArgsOK(rest) {
			return fmt.Errorf("%w: remote %s", errDestructive, strings.Join(rest, " "))
		}
	case "config":
		// Two shapes: the removal of a key that makes a clone partial, when a
		// pinned space's clone is made full (Repo.LeavePartial), and the read
		// of whether it is one (Repo.IsPartial).
		if len(rest) == 3 && rest[0] == "--local" && rest[1] == "--get-regexp" && rest[2] == partialKeysPattern {
			break
		}
		if len(rest) != 3 || rest[0] != "--local" || rest[1] != "--unset-all" || !partialConfigKeys[rest[2]] {
			return fmt.Errorf("%w: config %s", errDestructive, strings.Join(rest, " "))
		}
	case "update-ref":
		// Two shapes and no other: HEAD moved from a named commit to a new one
		// (never deleted, never without the old value), or a new ref made under
		// refs/petalbook/unpushed/.
		if len(rest) == 3 && rest[0] == "HEAD" {
			if _, err := advanceHeadArgs(rest[1], rest[2]); err != nil {
				return fmt.Errorf("%w: update-ref %s", errDestructive, strings.Join(rest, " "))
			}
			break
		}
		if len(rest) != 2 || !strings.HasPrefix(rest[0], keepRefPrefix) {
			return fmt.Errorf("%w: update-ref %s", errDestructive, strings.Join(rest, " "))
		}
		if _, err := keepRefArgs(strings.TrimPrefix(rest[0], keepRefPrefix), rest[1]); err != nil {
			return fmt.Errorf("%w: update-ref %s", errDestructive, strings.Join(rest, " "))
		}
	case "archive":
		// Exactly: archive --format=tar <a commit id>. The id is never an option.
		if len(rest) != 2 || rest[0] != "--format=tar" || !commitIDLike(rest[1]) {
			return fmt.Errorf("%w: archive %s", errDestructive, strings.Join(rest, " "))
		}
	case "bundle":
		// Exactly: bundle create <file> --all, the file an absolute path.
		if len(rest) != 3 || rest[0] != "create" || rest[2] != "--all" || !filepath.IsAbs(rest[1]) || strings.HasPrefix(rest[1], "-") {
			return fmt.Errorf("%w: bundle %s", errDestructive, strings.Join(rest, " "))
		}
	case "repack":
		// Exactly: repack -d -q --geometric=2: loose objects are packed, and small
		// packs merged into larger ones (every object of a merged pack is kept).
		// No -a/-A (those rewrite every pack and, with
		// -d, may drop unreachable objects), no -k, no cruft.
		if len(rest) != 3 || rest[0] != "-d" || rest[1] != "-q" || rest[2] != "--geometric=2" {
			return fmt.Errorf("%w: repack %s", errDestructive, strings.Join(rest, " "))
		}
	case "prune-packed":
		if len(rest) != 1 || rest[0] != "-q" {
			return fmt.Errorf("%w: prune-packed %s", errDestructive, strings.Join(rest, " "))
		}
	case "commit-graph":
		if len(rest) != 2 || rest[0] != "write" || rest[1] != "--reachable" {
			return fmt.Errorf("%w: commit-graph %s", errDestructive, strings.Join(rest, " "))
		}
	case "write-tree", "read-tree":
		// write-tree takes nothing; read-tree only the commit to read.
		if (sub == "write-tree" && len(rest) != 0) || (sub == "read-tree" && (len(rest) != 1 || rest[0] != "HEAD")) {
			return fmt.Errorf("%w: %s %s", errDestructive, sub, strings.Join(rest, " "))
		}
	case "commit-tree":
		ok := (len(rest) == 3 || len(rest) == 5) && rest[len(rest)-2] == "-F" && rest[len(rest)-1] == "-"
		if ok {
			parent := ""
			if len(rest) == 5 {
				ok = rest[1] == "-p"
				parent = rest[2]
			}
			if _, err := commitTreeArgs(rest[0], parent); err != nil || (len(rest) == 5 && parent == "") {
				ok = false
			}
		}
		if !ok {
			return fmt.Errorf("%w: commit-tree %s", errDestructive, strings.Join(rest, " "))
		}
	default:
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			if a == "--" {
				break
			}
			if sub == "log" && a == "-n" {
				i++ // the count that follows is a value
				continue
			}
			if strings.HasPrefix(a, "-") && !optionAllowed(sub, a) {
				return fmt.Errorf("%w: git %s %s (option not allowed)", errDestructive, sub, a)
			}
		}
	}
	return nil
}

// commitIDLike is true for a full commit id (40 or 64 hex characters).
func commitIDLike(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
