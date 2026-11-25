package spaces

// Exports (milestone 3.5, docs/api.md "Exports"): a zip of what some spaces
// hold, made before a deletion or any time an administrator may see a space.
// Each space is a folder: its files at the current commit (git archive, from the
// clone or from the recycle bin), the unsaved drafts of its people
// (drafts.json), the addresses of its remotes without any credential
// (REMOTE.txt), the files of its attachment repository, and with history a git
// bundle. An export never changes the space: it reads a clone that has been
// flushed, and it is made in the background, one at a time per tenant and two on
// the server.

import (
	"archive/tar"
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
)

// ErrExportsFull: the exports would be over exports.maxBytes (507).
var ErrExportsFull = errors.New("the exports are over the room they may take")

// Defaults of the settings exports.days and exports.maxBytes.
const (
	DefaultExportDays  = 7
	DefaultExportBytes = 20 << 30
	maxExportsAtOnce   = 2
)

// ExportLimits are exports.days and exports.maxBytes, read when they are used.
type ExportLimits struct {
	Days  int
	Bytes int64
}

// SetExportLimits gives the manager the settings of the exports.
func (m *Manager) SetExportLimits(f func(context.Context) ExportLimits) {
	m.exp.mu.Lock()
	m.exp.limits = f
	m.exp.mu.Unlock()
}

func (m *Manager) exportLimits(ctx context.Context) ExportLimits {
	m.exp.mu.Lock()
	f := m.exp.limits
	m.exp.mu.Unlock()
	l := ExportLimits{}
	if f != nil {
		l = f(ctx)
	}
	if l.Days <= 0 {
		l.Days = DefaultExportDays
	}
	if l.Bytes <= 0 {
		l.Bytes = DefaultExportBytes
	}
	return l
}

type exportState struct {
	mu     sync.Mutex
	limits func(context.Context) ExportLimits
	tenant map[string]chan struct{}
	global chan struct{}
	wg     sync.WaitGroup
}

func (m *Manager) exportsDir() string { return filepath.Join(m.opt.DataDir, "exports") }

// ExportPath is where the file of a ready export is.
func (m *Manager) ExportPath(e *store.Export) string {
	if e.Path == "" {
		return ""
	}
	return filepath.Join(m.opt.DataDir, filepath.FromSlash(e.Path))
}

// ExportRequest says what to export and for whom.
type ExportRequest struct {
	TenantID, By, ByName string
	// Sources are checked by the caller: spaces and recycle items that are in
	// the caller's scope.
	Sources []store.ExportSource
	History bool
	// SiteOnly: only site administrators may see it (see store.Export).
	SiteOnly bool
}

// StartExport records an export and makes it in the background. ErrExportsFull
// when the exports already hold what they may (the export is not made).
func (m *Manager) StartExport(ctx context.Context, req ExportRequest) (*store.Export, error) {
	lim := m.exportLimits(ctx)
	used, err := store.ExportBytes(ctx, m.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	if used >= lim.Bytes {
		return nil, ErrExportsFull
	}
	now := m.opt.Now()
	e := store.Export{ID: page.NewID(now), TenantID: req.TenantID, MadeBy: req.By, MadeByName: req.ByName, Spaces: req.Sources, History: req.History, SiteOnly: req.SiteOnly,
		State: store.ExportQueued, Created: now, Expires: now.Add(time.Duration(lim.Days) * 24 * time.Hour)}
	if err := store.InsertExport(ctx, m.opt.Store.Q(), e); err != nil {
		return nil, err
	}
	m.exp.wg.Add(1)
	go func() {
		defer m.exp.wg.Done()
		m.runExport(m.closedCtx(), e, lim.Bytes-used)
	}()
	return &e, nil
}

func (m *Manager) exportSlots(ctx context.Context, tenant string) (release func(), err error) {
	m.exp.mu.Lock()
	if m.exp.global == nil {
		m.exp.global = make(chan struct{}, maxExportsAtOnce)
		m.exp.tenant = map[string]chan struct{}{}
	}
	tc := m.exp.tenant[tenant]
	if tc == nil {
		tc = make(chan struct{}, 1)
		m.exp.tenant[tenant] = tc
	}
	g := m.exp.global
	m.exp.mu.Unlock()
	select {
	case tc <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case g <- struct{}{}:
	case <-ctx.Done():
		<-tc
		return nil, ctx.Err()
	}
	return func() { <-g; <-tc }, nil
}

func (m *Manager) runExport(ctx context.Context, e store.Export, room int64) {
	q := m.opt.Store.Q()
	fail := func(err error) {
		m.log.Warn("an export failed", "export", e.ID, "error", err)
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		store.SetExportState(context.WithoutCancel(ctx), q, e.ID, store.ExportFailed, msg, 0, "")
	}
	release, err := m.exportSlots(ctx, e.TenantID)
	if err != nil {
		fail(errors.New("the server stopped before the export was made"))
		return
	}
	defer release()
	if err := store.SetExportState(ctx, q, e.ID, store.ExportRunning, "", 0, ""); err != nil {
		return // deleted while it waited
	}
	if err := os.MkdirAll(m.exportsDir(), 0o755); err != nil {
		fail(err)
		return
	}
	final := filepath.Join(m.exportsDir(), e.ID+".zip")
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fail(err)
		return
	}
	cw := &countWriter{w: f, room: room}
	warnings, err := m.writeExport(ctx, zip.NewWriter(cw), cw, e)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, final)
	}
	if err != nil {
		os.Remove(tmp)
		if cw.over {
			err = ErrExportsFull
		}
		fail(err)
		return
	}
	rel := path.Join("exports", e.ID+".zip")
	if err := store.SetExportState(context.WithoutCancel(ctx), q, e.ID, store.ExportReady, strings.Join(warnings, "\n"), cw.n, rel); err != nil {
		os.Remove(final) // the export was deleted while it was made
	}
}

// countWriter counts what is written and refuses what does not fit in room.
type countWriter struct {
	w    io.Writer
	n    int64
	room int64
	over bool
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.room > 0 && c.n+int64(len(p)) > c.room {
		c.over = true
		return 0, ErrExportsFull
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// exportedDraft is one draft as drafts.json has it.
type exportedDraft struct {
	Page     string          `json:"page"`
	UserID   string          `json:"userId"`
	Username string          `json:"username"`
	BaseRev  string          `json:"baseRev"`
	Meta     json.RawMessage `json:"meta"`
	Body     string          `json:"body"`
	Held     bool            `json:"held"`
	Updated  time.Time       `json:"updated"`
}

func (m *Manager) writeExport(ctx context.Context, zw *zip.Writer, cw *countWriter, e store.Export) (warnings []string, err error) {
	used := map[string]bool{}
	for _, src := range e.Spaces {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		w, err := m.exportSource(ctx, zw, src, e.History, used)
		warnings = append(warnings, w...)
		if err != nil {
			return warnings, err
		}
	}
	return warnings, zw.Close()
}

// folderFor is the folder of a space in the zip: its name and its id.
func folderFor(name, id string, used map[string]bool) string {
	stem := page.FileStem(name)
	if stem == "" {
		stem = "space"
	}
	dir := stem + "-" + id
	for i := 2; used[dir]; i++ {
		dir = fmt.Sprintf("%s-%s-%d", stem, id, i)
	}
	used[dir] = true
	return dir
}

func (m *Manager) exportSource(ctx context.Context, zw *zip.Writer, src store.ExportSource, history bool, used map[string]bool) (warnings []string, err error) {
	q := m.opt.Store.Q()
	var (
		name, spaceID string
		clone         string // the directory of the repository
		drafts        []exportedDraft
		remoteURL     string
		assetsURL     string
		release       = func() {}
	)
	switch {
	case src.Item != "":
		it, err := store.GetRecycleItem(ctx, q, store.AllTenants(), src.Item, true)
		if err != nil {
			return nil, err
		}
		if it.State != store.RecycleReady {
			return nil, fmt.Errorf("the recycle item %s is not at rest", it.ID)
		}
		name, spaceID = it.Name, it.SpaceID
		clone = filepath.Join(m.RecycleDir(it.ID), "space")
		v, err := store.RecycledView(ctx, q, it)
		if err != nil {
			return nil, err
		}
		remoteURL, assetsURL = v.RemoteURL, v.AssetsURL
		for _, d := range v.Drafts {
			drafts = append(drafts, exportedDraft{Page: d.PageID, UserID: d.UserID, Username: d.Username, BaseRev: d.BaseRev, Meta: rawJSON(d.Meta), Body: d.Body, Held: d.Held, Updated: d.Updated})
		}
		if !pathExists(clone) {
			return []string{name + ": the space was evicted when it was deleted, so there are no files to export"}, m.writeSmall(zw, folderFor(name, spaceID, used), drafts, remoteURL, assetsURL, nil)
		}
	default:
		sp, err := store.GetSpace(ctx, q, store.AllTenants(), src.Space)
		if err != nil {
			return nil, err
		}
		name, spaceID = sp.Name, sp.ID
		// admin: a disabled space is exported too. An evicted space is restored
		// from its remote first (and fails if the remote cannot be reached).
		s, rel, err := m.acquire(ctx, spaceID, true)
		if err != nil {
			return nil, fmt.Errorf("%s: the space could not be opened for the export: %w", name, err)
		}
		release = rel
		if err := s.Flush(); err != nil {
			release()
			return nil, fmt.Errorf("%s: what the space holds could not be committed first: %w", name, err)
		}
		clone, _ = m.paths(spaceID)
		ds, names, err := store.SpaceDrafts(ctx, q, spaceID)
		if err != nil {
			release()
			return nil, err
		}
		for i, d := range ds {
			drafts = append(drafts, exportedDraft{Page: d.PageID, UserID: d.UserID, Username: names[i], BaseRev: d.BaseRev, Meta: rawJSON(d.Meta), Body: d.Body, Held: d.Held, Updated: d.Updated.UTC()})
		}
		if r, err := store.GetRemote(ctx, q, spaceID); err == nil {
			remoteURL = r.URL
		}
		if r, err := store.GetAssetRemote(ctx, q, spaceID); err == nil {
			assetsURL = r.URL
		}
	}
	defer release()
	folder := folderFor(name, spaceID, used)
	repo := gitstore.New(gitstore.Options{Dir: clone})
	if head := repo.Head(ctx); head != "" {
		if err := m.zipArchive(ctx, zw, repo, head, folder); err != nil {
			return warnings, fmt.Errorf("%s: %w", name, err)
		}
	}
	if history {
		tmp := filepath.Join(m.exportsDir(), "bundle-"+page.NewID(m.opt.Now())+".tmp")
		if err := repo.Bundle(ctx, tmp); err != nil {
			return warnings, fmt.Errorf("%s: the history could not be bundled: %w", name, err)
		}
		err := m.zipFile(zw, path.Join(folder, "history.bundle"), tmp)
		os.Remove(tmp)
		if err != nil {
			return warnings, err
		}
	}
	var assetWarn []string
	if src.Item == "" && assetsURL != "" {
		assetWarn = m.zipAssets(ctx, zw, spaceID, folder)
		for _, w := range assetWarn {
			warnings = append(warnings, name+": "+w)
		}
	}
	return warnings, m.writeSmall(zw, folder, drafts, remoteURL, assetsURL, assetWarn)
}

func rawJSON(s string) json.RawMessage {
	b := json.RawMessage(s)
	if !json.Valid(b) {
		b, _ = json.Marshal(s)
	}
	return b
}

// writeSmall writes the drafts, the addresses and what could not be included.
func (m *Manager) writeSmall(zw *zip.Writer, folder string, drafts []exportedDraft, remoteURL, assetsURL string, notIncluded []string) error {
	put := func(name string, data []byte) error {
		w, err := zw.Create(path.Join(folder, name))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if drafts == nil {
		drafts = []exportedDraft{}
	}
	b, err := json.MarshalIndent(drafts, "", "  ")
	if err != nil {
		return err
	}
	if err := put("drafts.json", b); err != nil {
		return err
	}
	var r strings.Builder
	if remoteURL != "" {
		r.WriteString("remote: " + remoteURL + "\n")
	}
	if assetsURL != "" {
		r.WriteString("attachments: " + assetsURL + "\n")
	}
	if r.Len() == 0 {
		r.WriteString("This space has no remote repository.\n")
	}
	if err := put("REMOTE.txt", []byte(r.String())); err != nil {
		return err
	}
	if len(notIncluded) > 0 {
		return put("NOT-INCLUDED.txt", []byte(strings.Join(notIncluded, "\n")+"\n"))
	}
	return nil
}

// zipArchive adds the files of a commit to the zip under folder.
func (m *Manager) zipArchive(ctx context.Context, zw *zip.Writer, repo *gitstore.Repo, commit, folder string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(repo.Archive(ctx, commit, pw)) }()
	defer pr.Close()
	tr := tar.NewReader(pr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if h.Typeflag == tar.TypeXGlobalHeader || name == "." || name == "" {
			continue
		}
		// Names come from a repository anyone with write access can shape: never
		// out of the folder, never absolute.
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			continue
		}
		// A person's own preferences are theirs, not the space's: an export an administrator
		// makes of a personal space does not carry them.
		if name == space.PrefsFile {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			fh := &zip.FileHeader{Name: path.Join(folder, name), Method: zip.Deflate, Modified: h.ModTime}
			fh.SetMode(os.FileMode(h.Mode) & 0o755)
			w, err := zw.CreateHeader(fh)
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				return err
			}
		default:
			// directories are implied by the files; links are never followed or kept
		}
	}
}

func (m *Manager) zipFile(zw *zip.Writer, name, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// zipAssets adds the files of the space's attachment repository, fetching each
// that is not on the server. What cannot be had is listed, and the export goes on
// with a note in the zip, so that it is clear what it lacks.
func (m *Manager) zipAssets(ctx context.Context, zw *zip.Writer, spaceID, folder string) (notIncluded []string) {
	repo, _, err := m.assetRepo(ctx, spaceID)
	if err != nil || repo == nil {
		if err != nil {
			return []string{"the attachment repository could not be opened: " + err.Error()}
		}
		return nil
	}
	files := repo.Files()
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if ctx.Err() != nil {
			return append(notIncluded, "stopped before the attachment "+rel)
		}
		clean := path.Clean(rel)
		if path.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
			continue
		}
		f, _, err := repo.Open(ctx, rel)
		if err != nil {
			notIncluded = append(notIncluded, "assets/"+rel+": "+err.Error())
			continue
		}
		w, werr := zw.CreateHeader(&zip.FileHeader{Name: path.Join(folder, "assets", clean), Method: zip.Deflate})
		if werr == nil {
			_, werr = io.Copy(w, f)
		}
		f.Close()
		if werr != nil {
			notIncluded = append(notIncluded, "assets/"+rel+": "+werr.Error())
		}
	}
	return notIncluded
}

// DeleteExportFile removes the file of an export, if there is one.
func (m *Manager) DeleteExportFile(e *store.Export) {
	if p := m.ExportPath(e); p != "" {
		os.Remove(p)
	}
}

// ExpireExports deletes the exports whose days are over, and finds the ones a
// restart left half made.
func (m *Manager) ExpireExports(ctx context.Context) error {
	list, err := store.ExpiredExports(ctx, m.opt.Store.Q(), m.opt.Now(), 100)
	if err != nil {
		return err
	}
	for i := range list {
		m.DeleteExportFile(&list[i])
		if err := store.DeleteExport(ctx, m.opt.Store.Q(), list[i].ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// recoverExports marks the exports a restart interrupted as failed, and removes
// what they wrote.
func (m *Manager) recoverExports(ctx context.Context) error {
	list, err := store.ExportsInFlight(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	for _, e := range list {
		os.Remove(filepath.Join(m.exportsDir(), e.ID+".zip.tmp"))
		if err := store.SetExportState(ctx, m.opt.Store.Q(), e.ID, store.ExportFailed, "the server restarted before the export was made", 0, ""); err != nil {
			return err
		}
	}
	return nil
}
