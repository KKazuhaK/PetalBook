package space

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/page"
)

// HistoryItem is one version of a page: a commit that changed its file.
type HistoryItem struct {
	Commit string
	Time   time.Time
	Author string
	Path   string
	// Via is "token" or "mcp" for a change made with an API token or through
	// MCP (its Changed-via trailer), "" otherwise.
	Via string
	// Rev is the rev of the file in that version; "" for a deletion.
	Rev string
	// Change is add, edit (the content changed), move (only the path changed)
	// or delete.
	Change string
}

// VersionPage is a page as it was in one version.
type VersionPage struct {
	Commit string
	Time   time.Time
	Author string
	Path   string
	Rev    string
	Meta   page.Meta
	Body   string
}

// ErrNoVersion: the commit is not a version of this page.
var ErrNoVersion = errors.New("no such version of this page")

const (
	// maxVersionScan bounds how many pages of history one lookup of a version
	// walks when the commit is not near the path's own recent history.
	maxVersionScan = 50
	scanPage       = 100
)

var commitIDRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func classify(v gitstore.Version) string {
	switch v.Status {
	case 'A', 'C':
		return "add"
	case 'D':
		return "delete"
	case 'R':
		if v.Similarity >= 100 {
			return "move"
		}
	}
	return "edit"
}

// histCursor says where a page of history continues: after Commit (which was
// the last version returned) along Path. With Fresh the walk starts at Commit
// itself, which was not returned yet: the page's earlier name was found there.
type histCursor struct {
	Commit, Path string
	Fresh        bool
}

func encodeCursor(c histCursor) string {
	mode := "after"
	if c.Fresh {
		mode = "from"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(c.Commit + "\n" + mode + "\n" + c.Path))
}

func decodeCursor(s string) (histCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	parts := strings.SplitN(string(raw), "\n", 3)
	if err != nil || len(parts) != 3 || !commitIDRe.MatchString(parts[0]) || (parts[1] != "after" && parts[1] != "from") ||
		checkRel(parts[2]) != nil || !strings.HasSuffix(strings.ToLower(parts[2]), ".md") {
		return histCursor{}, invalidf("Invalid cursor.")
	}
	return histCursor{Commit: parts[0], Path: parts[2], Fresh: parts[1] == "from"}, nil
}

// walk returns up to limit versions of the page starting where cur says (nil:
// at the top), and where the next page continues (nil: the history ends).
//
// The page is followed by its id, not only by what Git recognises as a rename.
// Changing a title renames the file, and when the text changed a lot in the
// same commit Git sees an unrelated new file and its history stops there. So
// when the walk along one path ends in the commit that added it, the commit is
// searched for a file it deleted that carried this page's id, and the walk
// goes on along that path. Such a version is an edit, not the page's creation.
func (s *Space) walk(ctx context.Context, repo *gitstore.Repo, id, rel string, limit int, cur *histCursor) ([]gitstore.Version, *histCursor, error) {
	accept := belongs(id)
	q := gitstore.HistoryQuery{Start: "HEAD", Path: rel, Accept: accept}
	if cur != nil {
		q.Start, q.Path = cur.Commit, cur.Path
		if !cur.Fresh {
			q.SkipFirst = cur.Commit
		}
	}
	var out []gitstore.Version
	// lastAdd is the newest-to-oldest walk's final version when it is an
	// addition: the place to look for an earlier name. It may be the version a
	// cursor continued after, which this call did not return.
	for hops := 0; hops < 64; hops++ {
		q.Limit = limit - len(out)
		var (
			vs   []gitstore.Version
			next *gitstore.Cursor
			err  error
		)
		if q.Limit > 0 {
			vs, next, err = repo.FileHistory(ctx, q)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, vs...)
		}
		if next != nil {
			return out, &histCursor{Commit: next.Commit, Path: next.Path}, nil
		}
		// This path's history is used up. Where did it begin?
		var first *gitstore.Version
		switch {
		case len(vs) > 0:
			first = &out[len(out)-1]
		case q.Limit > 0 && q.SkipFirst != "":
			// Nothing after the cursor's commit: that commit is where the path begins.
			one, _, err := repo.FileHistory(ctx, gitstore.HistoryQuery{Start: q.SkipFirst, Path: q.Path, Limit: 1, Accept: accept})
			if err != nil {
				return nil, nil, err
			}
			if len(one) == 1 && one[0].Commit == q.SkipFirst {
				first = &one[0]
			}
		}
		if first == nil || (first.Status != 'A' && first.Status != 'C') {
			return out, nil, nil
		}
		parent, old, err := repo.Predecessor(ctx, first.Commit, isPagePath, accept)
		if err != nil {
			return nil, nil, err
		}
		if old == "" {
			return out, nil, nil
		}
		if len(vs) > 0 {
			out[len(out)-1].Status = 'M' // the page was renamed and rewritten here, not created
		}
		if len(out) >= limit {
			return out, &histCursor{Commit: parent, Path: old, Fresh: true}, nil
		}
		q = gitstore.HistoryQuery{Start: parent, Path: old, Accept: accept}
	}
	return out, nil, nil
}

// belongs is the test every version has to pass: the file, as it was then,
// has this page's id in its header. A path that another page was moved or
// created over fails it, so the history of one page never shows another's.
func belongs(id string) func(string, []byte) bool {
	return func(_ string, head []byte) bool { return page.Parse(head).Meta().ID() == id }
}

// History lists the versions of a page, newest first, following the page
// through renames and moves. cursor is the Next of the previous page. One page
// of history costs two git processes, and starts where the cursor says, not at
// the top of the history.
func (s *Space) History(ctx context.Context, id string, limit int, cursor string) ([]HistoryItem, string, error) {
	rel, _, _, err := s.load(id, true)
	if err != nil {
		return nil, "", err
	}
	if limit < 1 {
		limit = 30
	}
	var cur *histCursor
	if cursor != "" {
		c, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		cur = &c
	}
	repo := s.cm.git.r()
	if repo.Head(ctx) == "" {
		return []HistoryItem{}, "", nil
	}
	vs, next, err := s.walk(ctx, repo, id, rel, limit, cur)
	if err != nil {
		return nil, "", s.historyErr(err)
	}
	items := make([]HistoryItem, len(vs))
	for i, v := range vs {
		items[i] = HistoryItem{Commit: v.Commit, Time: v.Time, Author: v.Author, Via: viaName(v.Via), Path: v.Path, Rev: v.Rev, Change: classify(v)}
	}
	n := ""
	if next != nil {
		n = encodeCursor(*next)
	}
	return items, n, nil
}

func (s *Space) historyErr(err error) error {
	var nc *NotCachedError
	if errors.As(err, &nc) {
		return err
	}
	if errors.Is(err, gitstore.ErrMissing) {
		return &NotCachedError{Err: err}
	}
	var ge *gitstore.GitError
	if errors.As(err, &ge) {
		s.log.Warn("reading the history failed", "error", err)
	}
	return err
}

// Version returns the page as it was in a commit. It is ErrNoVersion unless
// that commit is one of the versions History lists for this page (and not a
// deletion), whatever the commit is: nothing but this page's own file can be
// read through it.
func (s *Space) Version(ctx context.Context, id, commit string) (*VersionPage, error) {
	if !commitIDRe.MatchString(commit) {
		return nil, ErrNoVersion
	}
	rel, _, _, err := s.load(id, true)
	if err != nil {
		return nil, err
	}
	repo := s.cm.git.r()
	if repo.CommitOf(ctx, commit) != commit { // not an object of this repository, or not a commit
		return nil, ErrNoVersion
	}
	accept := belongs(id)
	// Usually the commit is a recent change of the file where it is now: one
	// look, starting at the commit itself.
	var found *gitstore.Version
	vs, _, err := repo.FileHistory(ctx, gitstore.HistoryQuery{Start: commit, Path: rel, Limit: 1, Accept: accept})
	if err != nil {
		return nil, s.historyErr(err)
	}
	if len(vs) == 1 && vs[0].Commit == commit {
		found = &vs[0]
	}
	// Otherwise the file had another name then: walk the page's history.
	if found == nil {
		var cur *histCursor
		for i := 0; i < maxVersionScan && found == nil; i++ {
			vs, next, err := s.walk(ctx, repo, id, rel, scanPage, cur)
			if err != nil {
				return nil, s.historyErr(err)
			}
			for j := range vs {
				if vs[j].Commit == commit {
					found = &vs[j]
					break
				}
			}
			if next == nil {
				break
			}
			cur = next
		}
	}
	if found == nil || found.Status == 'D' {
		return nil, ErrNoVersion
	}
	data, oid, err := repo.ReadBlob(ctx, commit, found.Path)
	if errors.Is(err, gitstore.ErrNoSuchFile) {
		return nil, ErrNoVersion
	}
	if err != nil {
		return nil, s.historyErr(err)
	}
	doc := page.Parse(data)
	if doc.Meta().ID() != id {
		return nil, ErrNoVersion
	}
	return &VersionPage{Commit: commit, Time: found.Time, Author: found.Author, Path: found.Path, Rev: oid, Meta: doc.Meta(), Body: doc.Body}, nil
}

// viaName is the short name of a Changed-via trailer value, "" for no trailer
// or one this server does not write.
func viaName(trailer string) string {
	switch trailer {
	case ViaToken:
		return "token"
	case ViaMCP:
		return "mcp"
	}
	return ""
}
