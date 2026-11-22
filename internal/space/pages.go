package space

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/PetalBook/internal/index"
	"github.com/KazuhaHub/PetalBook/internal/page"
)

// Page is a page as the API returns it.
type Page struct {
	ID      string
	Path    string
	Folder  string
	Rev     string
	Updated time.Time
	Meta    page.Meta
	// Header is the header text exactly as the file has it.
	Header string
	Body   string
}

func pageFrom(rel string, data []byte, mtime time.Time) *Page {
	doc := page.Parse(data)
	return &Page{
		ID: doc.Meta().ID(), Path: rel, Folder: folderOf(rel), Rev: page.Rev(data),
		Updated: mtime.UTC(), Meta: doc.Meta(), Header: doc.HeaderText(), Body: doc.Body,
	}
}

func folderOf(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

// readFile reads a page file and its mtime from one open file, so the two
// belong together. It refuses links.
func (s *Space) readFile(rel string) ([]byte, time.Time, error) {
	p, err := s.abs(rel)
	if err != nil {
		return nil, time.Time{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !st.Mode().IsRegular() {
		return nil, time.Time{}, errors.New("not a regular file")
	}
	data, err := io.ReadAll(f)
	return data, st.ModTime(), err
}

// load finds the file of page id and checks that the file really holds that
// page. The index can be stale: files may have been moved or replaced outside
// the server since it last looked, and acting on the indexed path would then
// touch some other page. When the file at the indexed path is not the page and
// resync is true, the index is brought up to date (at most once per
// syncEvery; Sync walks without the write lock) and the lookup repeated; if
// the id is still not found the page is gone and the result is ErrNotFound.
// Callers that hold the space lock pass resync false: they call refresh
// first, outside the lock.
func (s *Space) load(id string, resync bool) (rel string, data []byte, mtime time.Time, err error) {
	try := func() (string, []byte, time.Time, bool) {
		rel, ok := s.idx.PathByID(id)
		if !ok {
			return "", nil, time.Time{}, false
		}
		data, mtime, err := s.readFile(rel)
		if err != nil || page.Parse(data).Meta().ID() != id {
			return "", nil, time.Time{}, false
		}
		return rel, data, mtime, true
	}
	if rel, data, mtime, ok := try(); ok {
		return rel, data, mtime, nil
	}
	if !resync {
		return "", nil, time.Time{}, ErrNotFound
	}
	// An id the index has a row for, but whose file is not that page any more,
	// is a stale row: one re-sync repairs it (or drops it), so it is not
	// throttled. An id the index has never heard of is the case that must be
	// cheap to repeat, so only that is throttled.
	_, indexed := s.idx.PathByID(id)
	if (indexed && s.resyncNow()) || (!indexed && s.resync()) {
		if rel, data, mtime, ok := try(); ok {
			return rel, data, mtime, nil
		}
		if _, still := s.idx.PathByID(id); still {
			s.idx.Remove(id) // the row points at a file that is not this page
		}
	}
	return "", nil, time.Time{}, ErrNotFound
}

// refresh makes sure the index knows where page id is before a write takes the
// space lock, so the directory walk of a re-sync never blocks other writers.
func (s *Space) refresh(id string) { s.load(id, true) }

// resync brings the index up to date with the files, but not more often than
// syncEvery. It reports whether it ran.
func (s *Space) resync() bool {
	s.syncMu.Lock()
	if time.Since(s.lastSync) < s.syncEvery {
		s.syncMu.Unlock()
		return false
	}
	s.lastSync = time.Now()
	s.syncMu.Unlock()
	return s.resyncNow()
}

// resyncNow runs the sync without the throttle.
func (s *Space) resyncNow() bool {
	s.syncMu.Lock()
	s.lastSync = time.Now()
	s.syncMu.Unlock()
	s.walks.Add(1)
	if _, err := s.idx.Sync(s.src, false); err != nil {
		s.log.Warn("re-sync of the index failed", "error", err)
		return false
	}
	return true
}

// Get returns a page. It reads one file; the index only maps id to path.
func (s *Space) Get(id string) (*Page, error) {
	rel, data, mtime, err := s.load(id, true)
	if err != nil {
		return nil, err
	}
	return pageFrom(rel, data, mtime), nil
}

// CreateInput is the body of a create request.
type CreateInput struct {
	Folder string
	Meta   page.Meta
	Body   string

	// For an import (see Import): the creation date the page came with, and
	// the body once the page's id is known (attachments are addressed by it).
	created string
	bodyFor func(id string) string
}

// newMeta prepares the header of a page being created: fields that are empty
// are left out, so an untitled page has no title field at all (not an empty
// one). Nothing is rejected; unknown and odd fields pass through as given.
func newMeta(in page.Meta) page.Meta {
	out := make(page.Meta, 0, len(in))
	for _, f := range in {
		if cleared(f) {
			continue
		}
		switch v := f.Value.(type) {
		case string:
			if f.Key == "title" || f.Key == "subtitle" || f.Key == "source" || f.Key == "type" {
				f.Value = strings.TrimSpace(v)
			}
		case []any:
			if f.Key == "tags" {
				seen := map[string]bool{}
				tags := []any{}
				for _, e := range v {
					if t, ok := e.(string); ok {
						if t = strings.TrimSpace(t); t == "" || seen[t] {
							continue
						}
						seen[t] = true
						tags = append(tags, t)
					} else {
						tags = append(tags, e)
					}
				}
				f.Value = tags
			}
		}
		out = append(out, f)
	}
	return out
}

// cleared reports a known field that carries nothing: null, blank text or an
// empty list.
func cleared(f page.Field) bool {
	switch f.Key {
	case "title", "subtitle", "source", "type":
		if f.Value == nil {
			return true
		}
		s, ok := f.Value.(string)
		return ok && strings.TrimSpace(s) == ""
	case "tags":
		if f.Value == nil {
			return true
		}
		l, ok := f.Value.([]any)
		return ok && len(l) == 0
	case "created":
		return f.Value == nil
	}
	return false
}

// placeAfterKnown inserts f after the last of id, title, subtitle, source and tags,
// so a new `created` sits where the design document shows it.
func placeAfterKnown(m page.Meta, f page.Field) page.Meta {
	at := 0
	for i, x := range m {
		switch x.Key {
		case "id", "title", "subtitle", "source", "tags":
			at = i + 1
		}
	}
	out := make(page.Meta, 0, len(m)+1)
	out = append(out, m[:at]...)
	out = append(out, f)
	return append(out, m[at:]...)
}

func checkBody(body string) error {
	if !utf8.ValidString(body) {
		return invalidf("body is not valid UTF-8")
	}
	return nil
}

// Create makes a new page. The id and created date are assigned here; any in
// the input are ignored.
func (s *Space) Create(in CreateInput) (*Page, error) {
	if err := checkRel(in.Folder); err != nil {
		return nil, err
	}
	if err := checkBody(in.Body); err != nil {
		return nil, err
	}
	meta := make(page.Meta, 0, len(in.Meta))
	for _, f := range in.Meta {
		if f.Key != "id" && f.Key != "created" {
			meta = append(meta, f)
		}
	}
	meta = newMeta(meta)
	return mutate(s, func(tx *txn) (*Page, error) { return s.createLocked(tx, in, meta) })
}

func (s *Space) createLocked(tx *txn, in CreateInput, meta page.Meta) (*Page, error) {
	now := s.now().UTC()
	var id string
	for i := 0; ; i++ {
		if id = page.NewID(now); !s.idx.HasID(id) {
			break
		}
		if i >= maxNameTries {
			return nil, errors.New("could not find an unused page id")
		}
	}
	created := now.Format("2006-01-02")
	if in.created != "" {
		created = in.created
	}
	if in.bodyFor != nil {
		in.Body = in.bodyFor(id)
	}
	meta = append(page.Meta{{Key: "id", Value: id}}, meta...)
	meta = placeAfterKnown(meta, page.Field{Key: "created", Value: created})

	doc := page.Parse(nil)
	if err := doc.SetMeta(meta); err != nil {
		return nil, invalidf("%v", err)
	}
	doc.SetBody(in.Body)
	data := doc.Bytes()

	stem := page.FileStem(meta.Title())
	if stem == "" {
		stem = page.UntitledStem(created, id, now)
	}
	folder, dirAbs, err := s.ensureFolder(tx, in.Folder)
	if err != nil {
		return nil, err
	}
	name, err := s.createNamed(folder, dirAbs, stem, data)
	if err != nil {
		return nil, err
	}
	return s.afterWrite(tx, path.Join(folder, name), data)
}

// afterWrite indexes a file the server just wrote and tells the committer.
// touched are the paths the commit must include.
func (s *Space) afterWrite(tx *txn, rel string, data []byte, touched ...string) (*Page, error) {
	abs := filepath.Join(s.root, filepath.FromSlash(rel))
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	// The file is part of the change from the moment it is written: if the index
	// refuses it, undoing the change takes the file back too.
	tx.touch(append(touched, rel)...)
	if _, err := s.idx.Put(rel, data, st.ModTime().UnixNano()); err != nil {
		return nil, err
	}
	if len(touched) > 0 {
		// A move or rename may free an id that a duplicate was waiting for.
		if err := s.idx.RetryDuplicates(s.src); err != nil {
			s.log.Warn("could not re-check duplicate ids", "error", err)
		}
	}
	return pageFrom(rel, data, st.ModTime()), nil
}

// saveMeta builds the header a save writes from the one the client sent.
//
//   - Header order is the file's, not the request's: JSON objects do not
//     carry order reliably (browsers move integer-like keys to the front).
//     Keys already in the file keep their place, keys new in the request are
//     appended in request order, keys the request leaves out are removed.
//   - The id is always the page's, and `created` is kept when the client
//     leaves it out.
//   - Values equal to what the file has are kept exactly as written; a known
//     field the client emptied is dropped rather than written empty.
func saveMeta(id string, old, req page.Meta) (page.Meta, error) {
	in := req.Clone()
	if i := in.Index("id"); i >= 0 {
		if v := in[i].Value; v != nil && v != id {
			return nil, invalidf("meta.id does not match the page id")
		}
		in[i].Value = id
	} else {
		in = append(in, page.Field{Key: "id", Value: id})
	}
	if in.Index("created") < 0 {
		if v, ok := old.Get("created"); ok {
			in = append(in, page.Field{Key: "created", Value: v})
		}
	}
	kept := in[:0:0]
	for _, f := range in {
		if ov, ok := old.Get(f.Key); ok && page.ValuesEqual(ov, f.Value) {
			kept = append(kept, f)
		} else if !cleared(f) {
			kept = append(kept, f)
		}
	}
	out := make(page.Meta, 0, len(kept))
	for _, of := range old {
		if i := kept.Index(of.Key); i >= 0 {
			out = append(out, kept[i])
		}
	}
	for _, f := range kept {
		if old.Index(f.Key) < 0 {
			out = append(out, f)
		}
	}
	return out, nil
}

// Save replaces a page's header and body. baseRev must be the rev the client
// read; if the file has changed since, a *ConflictError carries the current
// page. A changed title renames the file; the id never changes.
func (s *Space) Save(id, baseRev string, meta page.Meta, body string) (*Page, error) {
	if err := checkBody(body); err != nil {
		return nil, err
	}
	s.refresh(id)
	return mutate(s, func(tx *txn) (*Page, error) { return s.saveLocked(tx, id, baseRev, meta, body) })
}

func (s *Space) saveLocked(tx *txn, id, baseRev string, meta page.Meta, body string) (*Page, error) {
	rel, data, mtime, err := s.load(id, false)
	if err != nil {
		return nil, err
	}
	if baseRev != page.Rev(data) {
		return nil, &ConflictError{Current: pageFrom(rel, data, mtime)}
	}
	doc := page.Parse(data)
	old := doc.Meta()
	newMeta, err := saveMeta(id, old, meta)
	if err != nil {
		return nil, err
	}
	if err := doc.SetMeta(newMeta); err != nil {
		return nil, invalidf("%v", err)
	}
	doc.SetBody(body)
	newData := doc.Bytes()

	wantRename := old.Title() != newMeta.Title()
	if bytes.Equal(newData, data) && !wantRename {
		return pageFrom(rel, data, mtime), nil
	}
	abs, err := s.abs(rel)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(newData, data) {
		if err := s.cm.Record(jrec{Op: "w", Path: rel, Rev: page.Rev(newData)}); err != nil {
			return nil, err
		}
		if err := writeAtomic(s.tmpDir(), abs, newData); err != nil {
			return nil, err
		}
	}
	newRel := rel
	if wantRename {
		stem := page.FileStem(newMeta.Title())
		if stem == "" {
			stem = page.UntitledStem(newMeta.Created(), id, s.now())
		}
		cur := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
		if !page.SameStem(cur, stem) {
			dirAbs := filepath.Dir(abs)
			name, err := s.moveNamed(abs, dirAbs, stem, s.moveIntent(rel, folderOf(rel)))
			if err != nil {
				// The content is saved; only the name is left as it was.
				s.log.Warn("could not rename page file", "from", rel, "stem", stem, "error", err)
			} else {
				newRel = path.Join(folderOf(rel), name)
			}
		}
	}
	return s.afterWrite(tx, newRel, newData, rel)
}

// CheckDraft applies the checks of Save to a draft without writing anything,
// and returns the page as the file has it now, so the caller can tell whether
// the draft's base is still current. It answers ErrNotFound for a page that is
// not there.
func (s *Space) CheckDraft(id string, meta page.Meta, body string) (*Page, error) {
	p, _, err := s.Equivalent(id, meta, body)
	return p, err
}

// Equivalent is CheckDraft that also reports whether saving this header and
// body would leave the file exactly as it is, whatever the rev they started
// from: a draft of that kind has nothing to conflict with.
func (s *Space) Equivalent(id string, meta page.Meta, body string) (*Page, bool, error) {
	if err := checkBody(body); err != nil {
		return nil, false, err
	}
	rel, data, mtime, err := s.load(id, true)
	if err != nil {
		return nil, false, err
	}
	doc := page.Parse(data)
	old := doc.Meta()
	newMeta, err := saveMeta(id, old, meta)
	if err != nil {
		return nil, false, err
	}
	if err := doc.SetMeta(newMeta); err != nil {
		return nil, false, invalidf("%v", err)
	}
	doc.SetBody(body)
	same := bytes.Equal(doc.Bytes(), data) && old.Title() == newMeta.Title()
	return pageFrom(rel, data, mtime), same, nil
}

// Move puts a page in another folder, creating it if needed. A folder that
// differs only in case from an existing one is the existing one.
func (s *Space) Move(id, folder string) (*Page, error) {
	if err := checkRel(folder); err != nil {
		return nil, err
	}
	s.refresh(id)
	return mutate(s, func(tx *txn) (*Page, error) { return s.moveLocked(tx, id, folder) })
}

func (s *Space) moveLocked(tx *txn, id, folder string) (*Page, error) {
	rel, data, mtime, err := s.load(id, false)
	if err != nil {
		return nil, err
	}
	canon, dirAbs, err := s.ensureFolder(tx, folder)
	if err != nil {
		return nil, err
	}
	if folderOf(rel) == canon {
		return pageFrom(rel, data, mtime), nil
	}
	// A file named after the title gets the plain title name again where it is
	// free (dropping a collision number it only had because of its old
	// folder); any other name was chosen by a person and stays.
	stem := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	if t := page.FileStem(page.Parse(data).Meta().Title()); t != "" && page.SameStem(stem, t) {
		stem = t
	}
	src, err := s.abs(rel)
	if err != nil {
		return nil, err
	}
	name, err := s.moveNamed(src, dirAbs, stem, s.moveIntent(rel, canon))
	if err != nil {
		return nil, err
	}
	return s.afterWrite(tx, path.Join(canon, name), data, rel)
}

// Delete removes the page file; Git history keeps it.
func (s *Space) Delete(id string) error {
	s.refresh(id)
	_, err := mutate(s, func(tx *txn) (struct{}, error) { return struct{}{}, s.deleteLocked(tx, id) })
	return err
}

func (s *Space) deleteLocked(tx *txn, id string) error {
	rel, _, _, err := s.load(id, false)
	if err != nil {
		return err
	}
	abs, err := s.abs(rel)
	if err != nil {
		return err
	}
	if err := s.cm.Record(jrec{Op: "d", Path: rel}); err != nil {
		return err
	}
	// The file is part of the change from the moment it is removed: if the index
	// refuses, undoing the change puts the file back.
	tx.touch(rel)
	if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := s.idx.Remove(id); err != nil {
		return err
	}
	// A page that was a duplicate of this one may be a page now.
	if err := s.idx.RetryDuplicates(s.src); err != nil {
		s.log.Warn("could not re-check duplicate ids", "error", err)
	}
	return nil
}

// CreateFolder makes a folder and returns its path as it is on disk (the
// case of existing parents wins). Git does not track empty directories, so a
// .gitkeep file goes in; it is not a page and is never listed.
func (s *Space) CreateFolder(rel string) (string, error) {
	if err := checkRel(rel); err != nil {
		return "", err
	}
	if rel == "" {
		return "", ErrExists
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	if s.rem.Load() != nil {
		return s.createFolderRemoteLocked(rel)
	}
	canon, existed, err := s.resolveFolder(rel)
	if err != nil {
		return "", err
	}
	if existed {
		return "", ErrExists
	}
	if err := notPageNames(strings.Split(canon, "/")); err != nil {
		return "", err
	}
	abs, err := s.abs(canon)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	if err := s.cm.Record(jrec{Op: "t", Path: canon + "/.gitkeep"}); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(abs, ".gitkeep"), nil, 0o644); err != nil {
		return "", err
	}
	if err := s.idx.AddFolder(canon); err != nil {
		return "", err
	}
	s.cm.Touch(canon + "/.gitkeep")
	return canon, nil
}

// createFolderRemoteLocked makes a folder of a space that follows a remote.
// Git does not carry an empty folder, so it exists only here: no .gitkeep, no
// commit, nothing to push.
func (s *Space) createFolderRemoteLocked(rel string) (string, error) {
	canon, existed, err := s.resolveFolder(rel)
	if err != nil {
		return "", err
	}
	if existed {
		return "", ErrExists
	}
	if err := notPageNames(strings.Split(canon, "/")); err != nil {
		return "", err
	}
	abs, err := s.abs(canon)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	if err := s.idx.AddFolder(canon); err != nil {
		return "", err
	}
	return canon, nil
}

// List queries the index. See index.Query.
func (s *Space) List(q index.Query) (index.Result, error) { return s.idx.List(q) }

// Tree lists every folder with its page count.
func (s *Space) Tree() ([]index.FolderCount, error) { return s.idx.Tree() }

// Tags lists every tag with its page count.
func (s *Space) Tags() ([]index.TagCount, error) { return s.idx.Tags() }

// Reindex rebuilds the index from the files. It blocks writes while it runs.
func (s *Space) Reindex() (index.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return index.Stats{}, ErrClosed
	}
	return s.idx.Sync(s.src, true)
}
