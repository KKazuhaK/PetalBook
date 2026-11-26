package space

import (
	"errors"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/links"
	"petalbook.app/server/internal/page"
)

// BacklinksPageSize is how many pages one request of backlinks lists.
const BacklinksPageSize = 20

// excerptRunes is the most characters of the paragraph a backlink shows.
const excerptRunes = 240

// Backlink is a page that links to another, with the paragraph of the link.
type Backlink struct {
	ID       string
	Path     string
	Title    string // the page's title, or what stands for it (see page.Label)
	Untitled bool
	// Excerpt is the paragraph the link is in, as plain text; "" when the file
	// could not be read or no longer has the link.
	Excerpt string
}

// Backlinks lists the pages that link to page id, BacklinksPageSize at a time,
// the most recently changed first. It reads the files of the pages it lists
// (for the excerpt) and no others. total is the number of pages that link to
// it; next is "" at the end. An evicted space has no files: its backlinks come
// from the index alone, without excerpts.
func (s *Space) Backlinks(id, cursor string) (items []Backlink, next string, total int, err error) {
	rel, err := s.locate(id)
	if err != nil {
		return nil, "", 0, err
	}
	rows, next, total, err := s.idx.Backlinks(id, cursor, BacklinksPageSize)
	if err != nil {
		if errors.Is(err, index.ErrBadCursor) {
			return nil, "", 0, invalidf("Bad cursor.")
		}
		return nil, "", 0, err
	}
	items = make([]Backlink, 0, len(rows))
	for _, r := range rows {
		b := Backlink{ID: r.ID, Path: r.Path, Title: r.Label, Untitled: r.Untitled}
		if s.evicted {
			items = append(items, b)
			continue
		}
		if data, _, err := s.readFile(r.Path); err == nil {
			for _, l := range links.Extract(r.Path, data) {
				if (l.ID != "" && l.ID == id) || (l.ID == "" && l.Path == rel) {
					b.Excerpt = excerptAround(data, l)
					break
				}
			}
		}
		items = append(items, b)
	}
	return items, next, total, nil
}

// excerptAround is the paragraph a link is in, as plain text, cut to about
// excerptRunes characters around the link.
func excerptAround(data []byte, l links.Link) string {
	if l.BlockStart < 0 || l.BlockEnd > len(data) || l.BlockStart > l.BlockEnd {
		return ""
	}
	text := strings.Join(strings.Fields(page.PlainText(string(data[l.BlockStart:l.BlockEnd]))), " ")
	r := []rune(text)
	if len(r) <= excerptRunes {
		return text
	}
	at := 0
	if l.Text != "" {
		if i := strings.Index(text, l.Text); i >= 0 {
			at = utf8.RuneCountInString(text[:i])
		}
	}
	start := max(0, min(at-excerptRunes/3, len(r)-excerptRunes))
	out := strings.TrimSpace(string(r[start : start+excerptRunes]))
	if start > 0 {
		out = "…" + out
	}
	if start+excerptRunes < len(r) {
		out += "…"
	}
	return out
}

// LinkTarget is the page a link leads to.
type LinkTarget struct {
	ID, Path, Title string
	Untitled        bool
}

// OutLink is a link of a page.
type OutLink struct {
	Text   string
	Path   string // the file the link names, in the space
	ID     string // the id the link carries, or ""
	Anchor string
	// Target is the page it leads to now (by id when the link has one, else by
	// its path), nil for a broken link.
	Target *LinkTarget
}

// Links lists the links of page id to other pages, in the order of the text.
func (s *Space) Links(id string) ([]OutLink, error) {
	if _, err := s.locate(id); err != nil {
		return nil, err
	}
	rows, ok, err := s.idx.OutLinks(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]OutLink, len(rows))
	for i, r := range rows {
		out[i] = OutLink{Text: r.Text, Path: r.Path, ID: r.ID, Anchor: r.Anchor}
		if r.Target != nil {
			out[i].Target = &LinkTarget{ID: r.Target.ID, Path: r.Target.Path, Title: r.Target.Label, Untitled: r.Target.Untitled}
		}
	}
	return out, nil
}

// locate is the path of page id, checked against its file as load does. An
// evicted space has no file to check: the index's path is the answer.
func (s *Space) locate(id string) (string, error) {
	if s.evicted {
		rel, ok := s.idx.PathByID(id)
		if !ok {
			return "", ErrNotFound
		}
		return rel, nil
	}
	rel, _, _, err := s.load(id, true)
	return rel, err
}

// --- keeping links right when pages move ---------------------------------------

// movedPage is a page file that has a new path (or is about to).
type movedPage struct{ id, from, to string }

// fileEdit is the new content of a page file.
type fileEdit struct {
	rel  string
	old  string // the blob id of the content the edit was made from
	data []byte
	// step is the rewrite, for the drafts and open editors of the file (see
	// rebase.go); nil when the file is not a page's.
	step *rebaseStep
}

// relinker works out where the links of a file should point once pages have
// moved, and the edits that make them.
type relinker struct {
	s     *Space
	byID  map[string]string    // id -> the path the page moved to
	byOld map[string]movedPage // old path -> the move
	dirs  []stepMove           // folders that moved (a rebase; see applyStep)
	// dup is the set of ids that two files claim: links that carry one are
	// left as they are (which file is meant is for a person to say). Read
	// when a link with an id is first met.
	dup    map[string]bool
	pathOf map[string]string // id -> path now, "" for none
	idOf   map[string]string // path -> id, "" for none
}

func (s *Space) newRelinker(dup map[string]bool) *relinker {
	return &relinker{s: s, byID: map[string]string{}, byOld: map[string]movedPage{}, dup: dup,
		pathOf: map[string]string{}, idOf: map[string]string{}}
}

func (r *relinker) duplicated(id string) bool {
	if r.dup == nil {
		d, err := r.s.idx.DuplicateIDs()
		if err != nil {
			return true // not known: better left alone
		}
		r.dup = d
	}
	return r.dup[id]
}

func (r *relinker) pageByID(id string) string {
	p, ok := r.pathOf[id]
	if !ok {
		p, _ = r.s.idx.PathByID(id)
		r.pathOf[id] = p
	}
	return p
}

func (r *relinker) pageAt(p string) string {
	id, ok := r.idOf[p]
	if !ok {
		id, _ = r.s.idx.IDAt(p)
		r.idOf[p] = id
	}
	return id
}

// target is the page link l should name (tid "" when no page is known) and
// its path; ok is false for a link to leave as it is.
func (r *relinker) target(l links.Link) (tid, tpath string, ok bool) {
	if l.ID != "" {
		if r.duplicated(l.ID) {
			return "", "", false
		}
		if p, ok := r.byID[l.ID]; ok {
			return l.ID, p, true
		}
		if p := r.pageByID(l.ID); p != "" {
			return l.ID, p, true
		}
		return "", l.Path, true // no such page: stays where it points
	}
	if m, ok := r.byOld[l.Path]; ok {
		return m.id, m.to, true
	}
	for _, d := range r.dirs {
		if under(l.Path, d.From) {
			p := d.To + strings.TrimPrefix(l.Path, d.From)
			return r.pageAt(p), p, true
		}
	}
	return r.pageAt(l.Path), l.Path, true
}

// edits are the changes to data, a page file whose links were written from
// oldAt and that is at newAt: only the paths of the links change (and, with
// addIDs, the id is put into a link that has none); every other byte is kept.
// A link that already points where it should is left alone.
func (r *relinker) edits(data []byte, oldAt, newAt string, addIDs bool) []links.Edit {
	var edits []links.Edit
	for _, l := range links.Extract(oldAt, data) {
		if l.DestStart < 0 {
			continue
		}
		tid, tpath, ok := r.target(l)
		if !ok || links.Target(newAt, l.Raw, l.Rooted) == tpath {
			continue
		}
		if e, ok := l.DestEdit(data, newAt, tpath); ok {
			edits = append(edits, e)
			if addIDs && l.ID == "" && tid != "" {
				if e, ok := l.IDEdit(tid); ok {
					edits = append(edits, e)
				}
			}
		}
	}
	return edits
}

// planLinks works out the files that change because pages have moved (the
// files are at their new paths, the index still has the old state): the pages
// that link to a moved page, and the moved pages themselves, which are
// somewhere else now and so name their targets differently. Only the paths of
// the links are changed, and the id is added to a link that lacks one; every
// other byte is kept. A link that already points where it should is left
// alone, so planning again after a crash finds what is left to do. A link that
// carries an id two files claim is left alone too (as repairStale does).
// moves says what moved, for the rebase steps (rebase.go).
//
// Cost: the index tells which pages to read; nothing else is read.
func (s *Space) planLinks(mv []movedPage, moves []stepMove) ([]fileEdit, error) {
	if len(mv) == 0 {
		return nil, nil
	}
	dup, err := s.idx.DuplicateIDs()
	if err != nil {
		return nil, err
	}
	rl := s.newRelinker(dup)
	ids := make([]string, 0, len(mv))
	froms := make([]string, 0, len(mv))
	byID := make(map[string]movedPage, len(mv))
	for _, m := range mv {
		byID[m.id] = m
		rl.byID[m.id], rl.byOld[m.from] = m.to, m
		ids = append(ids, m.id)
		froms = append(froms, m.from)
	}
	refs, err := s.idx.Referrers(ids, froms)
	if err != nil {
		return nil, err
	}
	own, err := s.idx.WithLinks(ids)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var sources []index.PageRef
	for _, r := range append(refs, own...) {
		if !seen[r.ID] {
			seen[r.ID] = true
			sources = append(sources, r)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })

	var out []fileEdit
	for _, src := range sources {
		oldAt, newAt := src.Path, src.Path
		if m, ok := byID[src.ID]; ok {
			oldAt, newAt = m.from, m.to
		}
		data, _, err := s.readFile(newAt)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if edits := rl.edits(data, oldAt, newAt, true); len(edits) > 0 {
			nd := links.Apply(data, edits)
			out = append(out, fileEdit{rel: newAt, old: page.Rev(data), data: nd, step: &rebaseStep{
				ID: src.ID, Old: page.Rev(data), New: page.Rev(nd), OldAt: oldAt, NewAt: newAt, Moves: moves,
			}})
		}
	}
	return out, nil
}

// writeEdits writes the new content of files as part of the change tx: each
// is journaled (all with one sync) before it is replaced, and handed to the
// commit. The rewrites are remembered for the drafts and open editors of the
// files (rebase.go). The index is not touched; the caller refreshes it for the
// paths returned.
func (s *Space) writeEdits(tx *txn, edits []fileEdit) ([]string, error) {
	pe, err := s.prepareEdits(tx, edits, nil, nil)
	if err != nil {
		return nil, err
	}
	return pe.apply()
}

// txMove is the move that a transaction of link rewrites makes in a local
// space: a page whose new name to is already a second name of its file (see
// moveNamed), with its content, or a folder already renamed (dir).
type txMove struct {
	from, to string
	dir      bool
	renamed  bool   // a page renamed already (a change of spelling): nothing to remove
	data     []byte // a page's content
	dev, ino uint64 // a folder's identity (fileID), 0 when not known
}

// preparedEdits is a rewrite of files journaled and not yet written.
type preparedEdits struct {
	s         *Space
	tx        *txn
	id        uint64
	journaled bool
	edits     []fileEdit
	paths     []string
	mv        *txMove
	extra     []txFile        // more files of the transaction, written after the edits (see prepareEdits)
	skip      map[string]bool // files of extra the caller writes itself
	done      bool
}

// prepareEdits journals edits, the move mv when there is one, and the files
// of extra as one transaction, and writes nothing. extra are files written
// after the edits (with data), or files that move with a folder (ref).
func (s *Space) prepareEdits(tx *txn, edits []fileEdit, mv *txMove, extra []txFile) (*preparedEdits, error) {
	pe := &preparedEdits{s: s, tx: tx, mv: mv, extra: extra}
	if len(edits) == 0 && mv == nil && len(extra) == 0 {
		pe.done = true
		return pe, nil
	}
	var files []txFile
	if mv != nil {
		f := txFile{path: mv.to, from: mv.from, dir: mv.dir, dev: mv.dev, ino: mv.ino}
		if !mv.dir {
			f.old, f.rev, f.data = page.Rev(mv.data), page.Rev(mv.data), mv.data
		}
		files = append(files, f)
	}
	for _, e := range edits {
		pe.paths = append(pe.paths, e.rel)
		if mv != nil && !mv.dir && e.rel == mv.to {
			// The moved page's own links change: its entry gets that content.
			files[0].rev, files[0].data = page.Rev(e.data), e.data
			pe.edits = append(pe.edits, e)
			continue
		}
		files = append(files, txFile{path: e.rel, old: e.old, rev: page.Rev(e.data), data: e.data})
		pe.edits = append(pe.edits, e)
	}
	// Files that move with a folder go first: a later entry of the same
	// path (its links rewritten) is what it ends up as.
	var refs, after []txFile
	for _, f := range extra {
		if f.ref {
			refs = append(refs, f)
		} else {
			after = append(after, f)
		}
	}
	files = append(files[:min(len(files), moveEntries(mv))], append(refs, files[min(len(files), moveEntries(mv)):]...)...)
	files = append(files, after...)
	// One transaction with every file's content: a crash part way is
	// finished by writing what is left of it (a local space) or taken back
	// (one that follows a remote), never planned again from files that may
	// already be rewritten.
	id, journaled, err := tx.prepare(files)
	if err != nil {
		return nil, err
	}
	pe.id, pe.journaled = id, journaled
	if tx.remote && mv != nil && !mv.dir {
		// Undoing the change puts both names back as the parent commit has
		// them, whatever got done of the move.
		tx.touch(mv.from, mv.to)
	}
	if tx.remote {
		for _, f := range extra {
			if !f.ref && !f.dir {
				tx.touch(f.path)
			}
		}
	}
	if err := s.stepped("edits journaled"); err != nil {
		pe.stop()
		return pe, err
	}
	return pe, nil
}

// moveEntries is how many entries of a transaction the move mv takes first.
func moveEntries(mv *txMove) int {
	if mv == nil {
		return 0
	}
	return 1
}

// abort gives the transaction up before anything of it was written.
func (pe *preparedEdits) abort() {
	if pe.done {
		return
	}
	pe.done = true
	pe.s.cm.Abort(pe.id)
}

// stop is the end of a transaction whose writing stopped part way: the files
// written are handed to the commit as they are, and the next start replays
// the rest.
func (pe *preparedEdits) stop() {
	if pe.done {
		return
	}
	pe.done = true
	if pe.journaled && !pe.tx.remote {
		pe.tx.touch(pe.paths...)
		pe.s.cm.forget(pe.id)
	}
}

// apply writes the files of the transaction, removes the old name of a page
// it moves, and journals it applied.
func (pe *preparedEdits) apply() ([]string, error) {
	if pe.done {
		return pe.paths, nil
	}
	s, tx, edits, paths, journaled, id := pe.s, pe.tx, pe.edits, pe.paths, pe.journaled, pe.id
	// In a space that follows a remote the files are part of the change
	// from the first replacement: an undo takes them back. In a local one
	// they are handed to the commit once all are written, with the content
	// the transaction wrote (Applied), or as they are if the writing stops.
	if !journaled || tx.remote {
		tx.touch(paths...)
	}
	var steps []rebaseStep
	defer pe.stop()
	defer func() { s.rb.add(steps...) }()
	for _, e := range edits {
		abs, err := s.abs(e.rel)
		if err != nil {
			return paths, err
		}
		if err := writeAtomic(abs, e.data); err != nil {
			return paths, err
		}
		if e.step != nil {
			steps = append(steps, *e.step)
		}
		if err := s.stepped("edit written"); err != nil {
			return paths, err
		}
	}
	for _, f := range pe.extra {
		if f.ref || f.dir || pe.skip[f.path] {
			continue
		}
		var err error
		if f.rev == "" {
			err = removeIn(s.root, f.path)
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		} else {
			err = writeIn(s.root, f.path, f.data)
		}
		if err != nil {
			return paths, err
		}
	}
	if mv := pe.mv; mv != nil && !mv.dir && !mv.renamed {
		from, err := s.abs(mv.from)
		if err != nil {
			return paths, err
		}
		if err := s.ops.Remove(from); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return paths, &moveError{err}
		}
	}
	pe.done = true
	s.cm.Applied(id)
	return paths, nil
}

// takeBackName removes the new name of a page move that was only taken (a
// second name of the file, see moveNamed) and will not be made.
func (s *Space) takeBackName(mv *txMove) {
	from, err1 := s.abs(mv.from)
	to, err2 := s.abs(mv.to)
	if err1 != nil || err2 != nil {
		return
	}
	fa, errA := s.ops.Lstat(from)
	fb, errB := s.ops.Lstat(to)
	if errA == nil && errB == nil && s.ops.SameFile(fa, fb) && !strings.EqualFold(from, to) {
		s.ops.Remove(to)
	}
}

// moveError is a move whose old name could not be removed: the change fails,
// in a local space too (the next start finishes it from the journal).
type moveError struct{ err error }

func (e *moveError) Error() string { return e.err.Error() }
func (e *moveError) Unwrap() error { return e.err }

// stepped tells the test hook step, if there is one, that the change got to
// the step name.
func (s *Space) stepped(name string) error {
	if s.step == nil {
		return nil
	}
	return s.step(name)
}

// pageMoves says, for the rebase steps, which pages moved.
func pageMoves(mv []movedPage) []stepMove {
	out := make([]stepMove, len(mv))
	for i, m := range mv {
		out[i] = stepMove{ID: m.id, From: m.from, To: m.to}
	}
	return out
}

// relink makes the links of the space right after pages moved: it plans and
// writes the changes and brings the index up to date for the files it wrote.
// moves says what moved (nil: the pages of mv). own is the file of a moved
// page that the caller is about to index with data; when the plan changes it,
// the new content is returned.
//
// move, in a local space, is the move of the page own (its new name already
// taken, see moveNamed): it is one transaction with the rewrites, and is made
// even if the links cannot be planned.
func (s *Space) relink(tx *txn, mv []movedPage, moves []stepMove, own string, data []byte, move *txMove) ([]byte, error) {
	if moves == nil {
		moves = pageMoves(mv)
	}
	edits, perr := s.planLinks(mv, moves)
	if perr != nil {
		if move == nil {
			return data, perr
		}
		edits = nil
	}
	for _, e := range edits {
		if e.rel == own {
			data = e.data
		}
	}
	pe, err := s.prepareEdits(tx, edits, move, nil)
	if err != nil {
		if move == nil {
			return data, err
		}
		if pe == nil {
			// Nothing journaled: the name taken for the move goes again.
			s.takeBackName(move)
		}
		return data, &moveError{err}
	}
	paths, err := pe.apply()
	if err != nil && move != nil {
		// Part of the move is made: the change fails, and the next start
		// finishes it from the journal.
		var me *moveError
		if !errors.As(err, &me) {
			err = &moveError{err}
		}
		return data, err
	}
	if err == nil {
		err = perr
	}
	if err != nil {
		return data, err
	}
	var others []string
	for _, p := range paths {
		if p != own {
			others = append(others, p)
		}
	}
	if len(others) > 0 {
		if _, err := s.idx.Refresh(s.src, others, nil); err != nil {
			return data, err
		}
	}
	return data, nil
}

// afterRelink says what a change does when its links could not be made right:
// a space that follows a remote undoes the whole change; a local one keeps it,
// leaves the journal record open (so that the next start finishes it) and goes
// on.
func (s *Space) afterRelink(tx *txn, err error) error {
	if err == nil {
		return nil
	}
	var me *moveError
	if tx.remote || errors.As(err, &me) {
		return err
	}
	s.log.Error("could not update the links to a moved page; the next start will try again", "error", err)
	return nil
}

// withIDs puts right the links in a page file that is being saved: a link
// without an id that leads to a page of the space gets the id of the target
// (one with a title of its own is left alone), and a link whose id is that of
// a page at another path is pointed at that path (only the path changes). A
// link to an id that two files claim, or that no page has, is left as it is.
// rel is the path of the file.
func (s *Space) withIDs(rel string, data []byte) []byte {
	ls := links.Extract(rel, data)
	if len(ls) == 0 {
		return data
	}
	rl := s.newRelinker(nil)
	var edits []links.Edit
	for _, l := range ls {
		if l.DestStart < 0 {
			continue
		}
		if l.ID != "" {
			p := rl.pageByID(l.ID)
			if p == "" || p == l.Path || rl.duplicated(l.ID) {
				continue
			}
			if e, ok := l.DestEdit(data, rel, p); ok {
				edits = append(edits, e)
			}
			continue
		}
		if l.TitleStart >= 0 {
			continue
		}
		if id := rl.pageAt(l.Path); id != "" {
			if e, ok := l.IDEdit(id); ok {
				edits = append(edits, e)
			}
		}
	}
	return links.Apply(data, edits)
}

// repairStale rewrites the paths of links that carry the id of a page that now
// lies at another path (it was moved or renamed outside PetalBook), as part of
// the change tx. ids limits it to the links to those pages, nil means all. A
// link to an id that more than one file claims is not touched, and neither is
// one whose id no page has. It returns the files it changed (already indexed).
func (s *Space) repairStale(tx *txn, ids []string) ([]string, error) {
	stale, err := s.idx.StaleLinks(ids)
	if err != nil || len(stale) == 0 {
		return nil, err
	}
	dup, err := s.idx.DuplicateIDs()
	if err != nil {
		return nil, err
	}
	type source struct {
		id      string
		targets map[string]string // target id -> its path
	}
	bySrc := map[string]*source{}
	var order []string
	for _, st := range stale {
		if dup[st.Target.ID] {
			continue
		}
		m := bySrc[st.Src.Path]
		if m == nil {
			m = &source{id: st.Src.ID, targets: map[string]string{}}
			bySrc[st.Src.Path] = m
			order = append(order, st.Src.Path)
		}
		m.targets[st.Target.ID] = st.Target.Path
	}
	sort.Strings(order)
	var edits []fileEdit
	for _, src := range order {
		data, _, err := s.readFile(src)
		if err != nil {
			continue
		}
		var es []links.Edit
		for _, l := range links.Extract(src, data) {
			tpath, ok := bySrc[src].targets[l.ID]
			if !ok || l.ID == "" || links.Target(src, l.Raw, l.Rooted) == tpath {
				continue
			}
			if e, ok := l.DestEdit(data, src, tpath); ok {
				es = append(es, e)
			}
		}
		if len(es) > 0 {
			nd := links.Apply(data, es)
			var moves []stepMove
			for id, p := range bySrc[src].targets {
				moves = append(moves, stepMove{ID: id, To: p})
			}
			sort.Slice(moves, func(i, j int) bool { return moves[i].ID < moves[j].ID })
			edits = append(edits, fileEdit{rel: src, old: page.Rev(data), data: nd, step: &rebaseStep{
				ID: bySrc[src].id, Old: page.Rev(data), New: page.Rev(nd), OldAt: src, NewAt: src, Moves: moves,
			}})
		}
	}
	paths, err := s.writeEdits(tx, edits)
	if err != nil {
		return paths, err
	}
	if len(paths) > 0 {
		if _, err := s.idx.Refresh(s.src, paths, nil); err != nil {
			return paths, err
		}
	}
	return paths, nil
}

// RepairLinks puts right the links that carry the id of a page that was moved
// or renamed outside PetalBook, as one change by the service. It returns the
// number of files changed.
func (s *Space) RepairLinks() (int, error) {
	n := 0
	_, err := mutate(s, Author{}, func(tx *txn) (struct{}, error) {
		paths, err := s.repairStale(tx, nil)
		n = len(paths)
		return struct{}{}, err
	})
	return n, err
}
