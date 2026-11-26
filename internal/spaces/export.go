package spaces

// Exports (milestone 3.5, docs/api.md "Exports"): a zip of what some spaces
// hold, made before a deletion or any time an administrator may see a space.
// Each space is a folder: its files at the current commit (git archive, from the
// clone or from the recycle bin), the unsaved drafts of its people
// (drafts.json), the addresses of its remotes without any credential
// (REMOTE.txt), the files of its attachment repository, and with history a git
// bundle. An export never changes the space: it reads a clone that has been
// flushed, and it is made in the background by the jobs runner (a job of kind
// "export", class jobs.IO: one at a time per tenant and two on the server).

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
	"petalbook.app/server/internal/jobs"
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
)

// ExportLimits are exports.days and exports.maxBytes, read when they are used:
// Days as the export's tenant has it (a site administrator may force it for
// one tenant), Bytes the room of every export on the server.
type ExportLimits struct {
	Days  int
	Bytes int64
}

// SetExportLimits gives the manager the settings of the exports.
func (m *Manager) SetExportLimits(f func(ctx context.Context, tenant string) ExportLimits) {
	m.exp.mu.Lock()
	m.exp.limits = f
	m.exp.mu.Unlock()
}

func (m *Manager) exportLimits(ctx context.Context, tenant string) ExportLimits {
	m.exp.mu.Lock()
	f := m.exp.limits
	m.exp.mu.Unlock()
	l := ExportLimits{}
	if f != nil {
		l = f(ctx, tenant)
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
	limits func(context.Context, string) ExportLimits
}

// exportKind runs the export jobs: a failure ends the job failed, and a
// restart does not take one up again (what it wrote is removed).
type exportKind struct{ m *Manager }

func (exportKind) Class() jobs.Class { return jobs.IO }

func (k exportKind) Recover(_ context.Context, j *store.Job) jobs.Outcome {
	// A crash between the rename and the write of the end leaves a whole zip
	// that no job that is done names: it goes with the half one.
	k.m.removeExportFiles(j)
	return jobs.Fail
}

func (k exportKind) Run(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	return k.m.runExport(ctx, j, p)
}

func (m *Manager) exportsDir() string { return filepath.Join(m.opt.DataDir, "exports") }

// ExportPath is where the file of an export job that is done is ("" for any
// other).
func (m *Manager) ExportPath(j *store.Job) string {
	if j.State != store.JobDone {
		return ""
	}
	r := store.ExportResultOf(j)
	if r.Path == "" {
		return ""
	}
	return filepath.Join(m.opt.DataDir, filepath.FromSlash(r.Path))
}

// ExportRequest says what to export and for whom.
type ExportRequest struct {
	TenantID, By, ByName string
	// Capacity is the acting scope of the request (see store.Job).
	Capacity string
	// Sources are checked by the caller: spaces and recycle items that are in
	// the caller's scope.
	Sources []store.ExportSource
	History bool
	// SiteOnly: only site administrators may see it (see store.Export).
	SiteOnly bool
}

// StartExport adds an export job and returns it, queued; the runner makes it in
// the background. ErrExportsFull when the exports already hold what they may
// (the export is not made).
func (m *Manager) StartExport(ctx context.Context, req ExportRequest) (*store.Job, error) {
	lim := m.exportLimits(ctx, req.TenantID)
	used, err := store.ExportBytes(ctx, m.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	if used >= lim.Bytes {
		return nil, ErrExportsFull
	}
	now := m.opt.Now()
	params, err := json.Marshal(store.ExportParams{Spaces: req.Sources, History: req.History})
	if err != nil {
		return nil, err
	}
	return m.runner().Enqueue(ctx, store.Job{ID: page.NewID(now), TenantID: req.TenantID, Kind: store.JobExport, MadeBy: req.By, MadeByName: req.ByName,
		Capacity: req.Capacity, SiteOnly: req.SiteOnly, Params: params, Total: int64(len(req.Sources)), Expires: now.Add(time.Duration(lim.Days) * 24 * time.Hour)})
}

func (m *Manager) runExport(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	params := store.ExportParamsOf(j)
	lim := m.exportLimits(ctx, j.TenantID)
	used, err := store.ExportBytes(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	if used >= lim.Bytes {
		return ErrExportsFull
	}
	p.SetTotal(int64(len(params.Spaces)), 0)
	if err := os.MkdirAll(m.exportsDir(), 0o755); err != nil {
		return err
	}
	final := filepath.Join(m.exportsDir(), j.ID+".zip")
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cw := &countWriter{w: f, room: lim.Bytes - used}
	var sent int64
	step := func(n int64) {
		p.Step(n, cw.n-sent)
		sent = cw.n
	}
	warnings, err := m.writeExport(ctx, zip.NewWriter(cw), cw, params, step)
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
		m.log.Warn("an export failed", "export", j.ID, "error", err)
		return err
	}
	step(0)
	res, _ := json.Marshal(store.ExportResult{Path: path.Join("exports", j.ID+".zip"), Warnings: strings.Join(warnings, "\n")})
	p.SetResult(res)
	return nil
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

func (m *Manager) writeExport(ctx context.Context, zw *zip.Writer, cw *countWriter, params store.ExportParams, step func(n int64)) (warnings []string, err error) {
	used := map[string]bool{}
	for _, src := range params.Spaces {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		w, err := m.exportSource(ctx, zw, src, params.History, used)
		warnings = append(warnings, w...)
		if err != nil {
			return warnings, err
		}
		step(1)
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
		if r, err := store.GetRemote(ctx, q, spaceID, store.RoleContent); err == nil {
			remoteURL = r.URL
		}
		if r, err := store.GetRemote(ctx, q, spaceID, store.RoleAssets); err == nil {
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

// DeleteExportFile removes the file of an export, whatever the job's state:
// the one its result names, and <id>.zip and <id>.zip.tmp, which a crash
// between the rename and the end's write, or an end that could not be written,
// leaves behind a job that is not done.
func (m *Manager) DeleteExportFile(j *store.Job) {
	if p := m.ExportPath(j); p != "" {
		os.Remove(p)
	}
	m.removeExportFiles(j)
}

// removeExportFiles removes <id>.zip and <id>.zip.tmp of an export job.
func (m *Manager) removeExportFiles(j *store.Job) {
	if j.ID == "" || filepath.Base(j.ID) != j.ID || j.ID == "." || j.ID == ".." {
		return // ids are the server's; never a path
	}
	final := filepath.Join(m.exportsDir(), j.ID+".zip")
	os.Remove(final)
	os.Remove(final + ".tmp")
}

// ExpireExports deletes the exports whose days are over: their files and rows.
func (m *Manager) ExpireExports(ctx context.Context) error {
	list, err := store.ExpiredExports(ctx, m.opt.Store.Q(), m.opt.Now(), 100)
	if err != nil {
		return err
	}
	for _, j := range list {
		m.DeleteExportFile(j)
		if err := store.DeleteJob(ctx, m.opt.Store.Q(), j.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}
