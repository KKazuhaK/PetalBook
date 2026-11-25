package spaces

// Moving the attachments of a space into its attachment repository
// (docs/api.md "POST …/remote/assets/move"). The steps, and what a crash at each
// leaves:
//
//  1. copying: the files of assets/ at the current commit are committed to the
//     attachment repository and pushed, in commits of at most
//     assetrepo.MaxCommitBytes. The space is writable; uploads go into assets/
//     of the space meanwhile. A crash leaves some files pushed: the next run
//     compares with the repository's table and goes on from there.
//  2. switching: with the space's writes held, whatever assets/ gained is
//     pushed too and the repository is checked, on the remote, to have every
//     file with the contents the space has (assetrepo.Missing); only then does
//     one commit of the space take assets/ out and add /assets/ to .gitignore.
//     That commit is made without touching the files, the work tree is put in
//     line with it, and it is pushed. A crash before the commit changes
//     nothing; after it the commit is pushed by the next start (or the work
//     tree finished by space.RecoverSwitch first).
//  3. the clone is replaced by a partial one: the space is evicted when it may
//     be, and the next open fetches only what the new current version has.
//
// Nothing is deleted from the space's repository before the attachment
// repository has the files, as seen on its remote; and nothing is ever deleted
// there: the history of the space still has the attachments.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"petalbook.app/server/internal/assetrepo"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
)

// moveRetries is how often the switch is tried again when the remote moved
// under it.
const moveRetries = 3

func (m *Manager) assetsHook(step string) error {
	if m.opt.AssetsHook != nil {
		return m.opt.AssetsHook(step)
	}
	return nil
}

// StartMove starts moving the attachments of the space into its attachment
// repository. started is false (and nothing is done) when assets/ has no files.
func (m *Manager) StartMove(ctx context.Context, h *Handle) (v *AssetsView, started bool, err error) {
	if m.single != nil {
		return nil, false, ErrNotFound
	}
	id := h.Info.ID
	m.remoteMu.Lock()
	defer m.remoteMu.Unlock()
	row, err := store.GetAssetRemote(ctx, m.opt.Store.Q(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return nil, false, ErrNoAssets
	}
	if err != nil {
		return nil, false, err
	}
	if row.Move.Running() || m.moveRunning(id) {
		return nil, false, assetsConflict("The attachments are being moved already.")
	}
	m.settleMove(ctx, h, row)
	files, err := h.Space.AssetsAtHead(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(files) == 0 {
		return m.assetsView(h, row), false, nil
	}
	var bytes int64
	for _, f := range files {
		bytes += f.Size
	}
	mv := store.AssetMove{State: store.MoveCopying, Files: int64(len(files)), Bytes: bytes}
	if err := store.SetAssetMove(ctx, m.opt.Store.Q(), id, mv, m.opt.Now()); err != nil {
		return nil, false, err
	}
	m.AssetCache()
	m.as.mu.Lock()
	m.as.moving[id] = true
	m.as.mu.Unlock()
	m.as.wg.Add(1)
	go func() { defer m.as.wg.Done(); m.runMove(id) }()
	row.Move = mv
	return m.assetsView(h, row), true, nil
}

// settleMove records as done a move that failed although its switch was
// pushed (the answer was lost, and whether it landed could not be checked):
// once the space's HEAD is past the switch there is nothing left to move.
func (m *Manager) settleMove(ctx context.Context, h *Handle, row *store.AssetRemote) {
	if row.Move.State != store.MoveFailed || m.moveRunning(h.Info.ID) {
		return
	}
	if ok, err := h.Space.AssetsSwitched(ctx); err != nil || !ok {
		return
	}
	mv := row.Move
	mv.State, mv.Error = store.MoveDone, ""
	mv.FilesDone, mv.BytesDone = mv.Files, mv.Bytes
	if err := store.SetAssetMove(ctx, m.opt.Store.Q(), h.Info.ID, mv, m.opt.Now()); err != nil {
		m.log.Warn("could not record a move of attachments whose switch landed", "space", h.Info.ID, "error", err)
		return
	}
	m.log.Info("a move of attachments that was taken for failed had landed; it is done", "space", h.Info.ID)
	row.Move = mv
}

// ResumeMoves goes on with the moves a stop of the server cut short.
func (m *Manager) ResumeMoves(ctx context.Context) {
	if m.single != nil {
		return
	}
	ids, err := store.MovesRunning(ctx, m.opt.Store.Q())
	if err != nil {
		m.log.Warn("could not look for moves of attachments that were cut short", "error", err)
		return
	}
	m.AssetCache()
	for _, id := range ids {
		m.as.mu.Lock()
		already := m.as.moving[id]
		m.as.moving[id] = true
		m.as.mu.Unlock()
		if !already {
			m.log.Info("going on with moving the attachments of a space", "space", id)
			m.as.wg.Add(1)
			go func() { defer m.as.wg.Done(); m.runMove(id) }()
		}
	}
}

// errCrash marks a hook's error: the run ends without a record, as a crash does.
type errCrash struct{ error }

func (m *Manager) runMove(id string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopCtx := context.AfterFunc(m.closedCtx(), cancel)
	defer stopCtx()
	defer func() {
		m.as.mu.Lock()
		delete(m.as.moving, id)
		m.as.mu.Unlock()
	}()
	err := m.doMove(ctx, id)
	var crash errCrash
	switch {
	case err == nil:
		m.log.Info("moved the attachments of a space into its attachment repository", "space", id)
		// Step 3: the clone is replaced by a partial one. When the space is
		// in use it stays as it is; eviction does the same later.
		if eerr := m.Evict(context.Background(), id, EvictOptions{AdminRequest: true}); eerr != nil {
			m.log.Info("the clone of a space whose attachments moved was not replaced yet", "space", id, "reason", eerr.Error())
		}
	case errors.As(err, &crash), ctx.Err() != nil:
		m.log.Warn("moving the attachments of a space was cut short; it goes on at the next start", "space", id, "error", err)
	default:
		m.log.Warn("moving the attachments of a space failed", "space", id, "error", err)
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rcancel()
		if row, gerr := store.GetAssetRemote(rctx, m.opt.Store.Q(), id); gerr == nil {
			mv := row.Move
			mv.State, mv.Error = store.MoveFailed, errText(err)
			store.SetAssetMove(rctx, m.opt.Store.Q(), id, mv, m.opt.Now())
		}
	}
}

func errText(err error) string {
	msg := gitstore.Scrub(err.Error())
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

// closedCtx is a context that ends when the manager is closed.
func (m *Manager) closedCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closeCtx == nil {
		m.closeCtx, m.closeCancel = context.WithCancel(context.Background())
		if m.closed {
			m.closeCancel()
		}
	}
	return m.closeCtx
}

type moveProgress struct {
	m        *Manager
	id       string
	mv       store.AssetMove
	lastSave time.Time
}

func (p *moveProgress) save(ctx context.Context, force bool) {
	if !force && time.Since(p.lastSave) < time.Second {
		return
	}
	p.lastSave = time.Now()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := store.SetAssetMove(sctx, p.m.opt.Store.Q(), p.id, p.mv, p.m.opt.Now()); err != nil {
		p.m.log.Warn("could not store the progress of moving attachments", "space", p.id, "error", err)
	}
}

func (m *Manager) doMove(ctx context.Context, id string) error {
	sp, release, err := m.Acquire(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	repo, row, err := m.assetRepo(ctx, id)
	if err != nil {
		return err
	}
	if repo == nil {
		return errors.New("the space has no attachment repository any more")
	}
	svc := m.serviceIdentity()
	// A switch the server was stopped in the middle of: the work tree is put in
	// line with the commit before anything else.
	if recovered, err := sp.RecoverSwitch(ctx); err != nil {
		return err
	} else if recovered {
		m.log.Info("finished a switch of attachments a stop of the server cut short", "space", id)
	}
	prog := &moveProgress{m: m, id: id, mv: row.Move}
	prog.mv.State, prog.mv.Error = store.MoveCopying, ""
	// copyMissing pushes the files the repository does not have, in commits of
	// at most MaxCommitBytes, and counts what is there as done.
	copyMissing := func(ctx context.Context, files []space.SpaceAsset) error {
		if _, _, err := repo.Refresh(ctx, true); err != nil {
			return err
		}
		have := repo.Files()
		var todo []space.SpaceAsset
		var total, done, doneBytes, totalBytes int64
		for _, f := range files {
			total++
			totalBytes += f.Size
			if cur, ok := have[f.Rel]; ok {
				if cur != f.ID {
					return fmt.Errorf("the attachment repository has a different file at %s", f.Rel)
				}
				done++
				doneBytes += f.Size
				continue
			}
			todo = append(todo, f)
		}
		prog.mv.Files, prog.mv.Bytes, prog.mv.FilesDone, prog.mv.BytesDone = total, totalBytes, done, doneBytes
		prog.save(ctx, true)
		sort.Slice(todo, func(i, j int) bool { return todo[i].Rel < todo[j].Rel })
		for len(todo) > 0 {
			var chunk []assetrepo.File
			var n int64
			i := 0
			for ; i < len(todo); i++ {
				if len(chunk) > 0 && n+todo[i].Size > assetrepo.MaxCommitBytes {
					break
				}
				chunk = append(chunk, assetrepo.File{Rel: todo[i].Rel, Path: todo[i].Path, ID: todo[i].ID, Size: todo[i].Size})
				n += todo[i].Size
			}
			if err := repo.Put(ctx, chunk, svc, svc, fmt.Sprintf("Add %d attachments moved from the space", len(chunk))); err != nil {
				return err
			}
			todo = todo[i:]
			prog.mv.FilesDone += int64(len(chunk))
			prog.mv.BytesDone += n
			prog.save(ctx, false)
			if err := m.assetsHook("copied"); err != nil {
				return errCrash{err}
			}
		}
		return nil
	}
	files, err := sp.AssetsAtHead(ctx)
	if err != nil {
		return err
	}
	if err := copyMissing(ctx, files); err != nil {
		return err
	}
	if err := m.assetsHook("copied-all"); err != nil {
		return errCrash{err}
	}
	prog.mv.State = store.MoveSwitching
	prog.save(ctx, true)
	if err := m.assetsHook("switching"); err != nil {
		return errCrash{err}
	}
	ensure := func(ctx context.Context, files []space.SpaceAsset) error {
		if err := copyMissing(ctx, files); err != nil {
			return err
		}
		// The check that lets the space's own copy go: what the attachment
		// repository's remote has, fetched just now, against what the space has.
		want := make(map[string]string, len(files))
		for _, f := range files {
			want[f.Rel] = f.ID
		}
		miss, err := repo.Missing(ctx, want)
		if err != nil {
			return err
		}
		if len(miss) > 0 {
			return fmt.Errorf("%d attachments are not in the attachment repository on its remote (the first is %s)", len(miss), miss[0])
		}
		return nil
	}
	hook := func(step string) error {
		if step == "pushed" {
			// With the space's writes still held: from here on uploads go to
			// the attachment repository again, never to a new assets/ folder.
			m.as.mu.Lock()
			delete(m.as.moving, id)
			m.as.mu.Unlock()
		}
		if err := m.assetsHook("switch-" + step); err != nil {
			return errCrash{err}
		}
		return nil
	}
	for try := 0; ; try++ {
		err = sp.SwitchAssets(ctx, ensure, hook)
		var ue *space.UnavailableError
		if err == nil || !errors.As(err, &ue) || try >= moveRetries || !isRemoteMoved(err) {
			break
		}
	}
	if err != nil {
		return err
	}
	prog.mv.State, prog.mv.Error = store.MoveDone, ""
	prog.mv.FilesDone, prog.mv.BytesDone = prog.mv.Files, prog.mv.Bytes
	prog.save(ctx, true)
	return nil
}

func isRemoteMoved(err error) bool {
	var ue *space.UnavailableError
	return errors.As(err, &ue) && gitstore.KindOf(ue.Err) == gitstore.KindRejected
}

// serviceIdentity is the service's own Git identity (commits of a move).
func (m *Manager) serviceIdentity() gitstore.Identity {
	name, email, err := space.ParseCommitter(m.opt.Committer)
	if err != nil {
		name, email, _ = space.ParseCommitter("")
	}
	return gitstore.Identity{Name: name, Email: email}
}
