package spaces

// Moving the attachments of a space into its attachment repository
// (docs/api.md "POST …/remote/assets/move"). A move is a job of the kind
// assets.move on the server's jobs runner (internal/jobs): its class is IO (one
// per tenant, two on the server), its lock key space:<id> (one at a time for a
// space, and never with another job on the space's storage), and a restart, or
// a close of the server, queues it again: it goes on from what the attachment
// repository has. One still running when the close stops waiting for it
// (jobs.Options.StopWait) is left running in its row, and the next start
// resumes it as after a crash: every step below is safe to cut short. Opening
// a space whose move is queued finishes a switch a stop cut short at once
// (settleSwitch), without waiting for the job's slot. The remote row of the attachment repository names the last
// move (job_id), and records once the switch has landed (used = moved).
//
// The steps, and what a crash at each leaves:
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

	"petalbook.app/server/internal/assetrepo"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/jobs"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/store"
)

// MoveKind is the kind of the jobs that move a space's attachments.
const MoveKind = "assets.move"

// The phases of a move, as MoveView shows them.
const (
	MoveCopying   = "copying"
	MoveSwitching = "switching"
	MoveDone      = "done"
	MoveFailed    = "failed"
)

// moveRetries is how often the switch is tried again when the remote moved
// under it.
const moveRetries = 3

// spaceLock is the lock key of the jobs that change where a space's files are
// kept (assets.move now; remote.move in 3.6).
func spaceLock(id string) string { return "space:" + id }

func (m *Manager) assetsHook(step string) error {
	if m.opt.AssetsHook != nil {
		return m.opt.AssetsHook(step)
	}
	return nil
}

// UseJobs gives the manager the server's jobs runner: the moves of
// attachments are registered on it, and enqueued and read through it. Call it
// before the runner's Start, which takes up the moves a stop left. Close
// closes the runner (the API closes it first; a second close does nothing).
func (m *Manager) UseJobs(r *jobs.Runner) {
	if m.single != nil {
		return
	}
	m.AssetCache()
	m.as.mu.Lock()
	m.as.jobs = r
	m.as.mu.Unlock()
	r.Register(MoveKind, moveKind{m})
}

func (m *Manager) runner() *jobs.Runner {
	m.AssetCache()
	m.as.mu.Lock()
	defer m.as.mu.Unlock()
	return m.as.jobs
}

// StartMove starts moving the attachments of the space into its attachment
// repository, as a job made by the given person. started is false (and nothing
// is done) when assets/ has no files.
func (m *Manager) StartMove(ctx context.Context, h *Handle, byID, byName string) (v *AssetsView, started bool, err error) {
	if m.single != nil {
		return nil, false, ErrNotFound
	}
	r := m.runner()
	if r == nil {
		return nil, false, errors.New("the server runs no jobs")
	}
	id := h.Info.ID
	m.remoteMu.Lock()
	defer m.remoteMu.Unlock()
	row, err := store.GetRemote(ctx, m.opt.Store.Q(), id, store.RoleAssets)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.URL == "") {
		return nil, false, ErrNoAssets
	}
	if err != nil {
		return nil, false, err
	}
	if m.moveActive(ctx, row) {
		return nil, false, assetsConflict("The attachments are being moved already.")
	}
	m.settleMove(ctx, h, row)
	files, err := h.Space.AssetsAtHead(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(files) == 0 {
		return m.assetsView(ctx, h, row), false, nil
	}
	var bytes int64
	for _, f := range files {
		bytes += f.Size
	}
	sp, err := store.GetSpace(ctx, m.opt.Store.Q(), store.AllTenants(), id) // the space the caller has open, by id
	if err != nil {
		return nil, false, err
	}
	// The row names the job before it exists, so that the job is never without
	// it; an enqueue that fails puts the old name back.
	jobID := page.NewID(m.opt.Now())
	if err := store.SetAssetsMoveJob(ctx, m.opt.Store.Q(), id, jobID); err != nil {
		return nil, false, err
	}
	// Uploads go into assets/ of the space from now on, until the switch.
	m.setMoving(id, MoveCopying)
	_, err = r.Enqueue(ctx, store.Job{ID: jobID, TenantID: sp.TenantID, Kind: MoveKind, Subject: id, LockKey: spaceLock(id),
		MadeBy: byID, MadeByName: byName, Total: int64(len(files)), BytesTotal: bytes})
	if err != nil {
		m.clearMoving(id)
		if rerr := store.SetAssetsMoveJob(context.WithoutCancel(ctx), m.opt.Store.Q(), id, row.JobID); rerr != nil {
			m.log.Warn("could not put back the last move of attachments of a space", "space", id, "error", rerr)
		}
		if errors.Is(err, store.ErrJobLocked) {
			return nil, false, assetsConflict("Another job is changing where the space's files are kept.")
		}
		return nil, false, err
	}
	row.JobID = jobID
	return m.assetsView(ctx, h, row), true, nil
}

// setMoving holds the uploads of the space in assets/ (backend.Direct) and
// records the phase of its move.
func (m *Manager) setMoving(id, phase string) {
	m.AssetCache()
	m.as.mu.Lock()
	m.as.moving[id] = true
	m.as.phase[id] = phase
	m.as.mu.Unlock()
}

func (m *Manager) clearMoving(id string) {
	m.AssetCache()
	m.as.mu.Lock()
	delete(m.as.moving, id)
	delete(m.as.phase, id)
	m.as.mu.Unlock()
}

func (m *Manager) setPhase(id, phase string) {
	m.as.mu.Lock()
	m.as.phase[id] = phase
	m.as.mu.Unlock()
}

// moveJob is the last move of the attachments of the space, from the runner
// (its memory while it runs here) or its row; nil when there was none or it
// was pruned.
func (m *Manager) moveJob(ctx context.Context, row *store.Remote) *store.Job {
	if row.JobID == "" {
		return nil
	}
	var j *store.Job
	var err error
	if r := m.runner(); r != nil {
		j, err = r.Get(ctx, store.AllTenants(), row.JobID)
	} else {
		j, err = store.GetJob(ctx, m.opt.Store.Q(), store.AllTenants(), row.JobID)
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("could not read the move of attachments of a space", "space", row.SpaceID, "job", row.JobID, "error", err)
		}
		return nil
	}
	return j
}

// moveActive reports whether a move of the space's attachments is queued or
// running.
func (m *Manager) moveActive(ctx context.Context, row *store.Remote) bool {
	if m.moveRunning(row.SpaceID) {
		return true
	}
	j := m.moveJob(ctx, row)
	return j != nil && !store.JobEnded(j.State)
}

// moveView is how far the last move has come, for the API.
func (m *Manager) moveView(ctx context.Context, row *store.Remote) *MoveView {
	j := m.moveJob(ctx, row)
	if j == nil {
		if row.Moved() {
			return &MoveView{State: MoveDone} // its job was pruned
		}
		return nil
	}
	v := &MoveView{Files: j.Total, FilesDone: j.Done, Bytes: j.BytesTotal, BytesDone: j.BytesDone}
	switch {
	case !store.JobEnded(j.State):
		m.as.mu.Lock()
		v.State = m.as.phase[row.SpaceID]
		m.as.mu.Unlock()
		if v.State == "" {
			v.State = MoveCopying // queued, or running elsewhere
		}
	case row.Moved() || j.State == store.JobDone:
		v.State, v.FilesDone, v.BytesDone = MoveDone, v.Files, v.Bytes
	default:
		v.State, v.Error = MoveFailed, j.Error
		if v.Error == "" {
			v.Error = "The move was " + j.State + "."
		}
	}
	return v
}

// settleMove records as done a move that ended without being done although
// its switch was pushed (the answer was lost, and whether it landed could not
// be checked): once the space's HEAD is past the switch there is nothing left
// to move.
func (m *Manager) settleMove(ctx context.Context, h *Handle, row *store.Remote) {
	if row.Moved() || m.moveRunning(h.Info.ID) {
		return
	}
	j := m.moveJob(ctx, row)
	if j == nil || !store.JobEnded(j.State) || j.State == store.JobDone {
		return
	}
	if ok, err := h.Space.AssetsSwitched(ctx); err != nil || !ok {
		return
	}
	if err := store.MarkAssetsMoved(ctx, m.opt.Store.Q(), h.Info.ID); err != nil {
		m.log.Warn("could not record a move of attachments whose switch landed", "space", h.Info.ID, "error", err)
		return
	}
	if err := store.SetJobState(ctx, m.opt.Store.Q(), j.ID, j.State, store.JobDone, m.opt.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.log.Warn("could not record a move of attachments whose switch landed as done", "space", h.Info.ID, "job", j.ID, "error", err)
	}
	m.log.Info("a move of attachments that was taken for failed had landed; it is done", "space", h.Info.ID)
	row.Used = store.AssetsMoved
}

// settleSwitch is the part of opening a space whose move is queued or running
// (since a stop, say) that cannot wait for the move's job, which may wait long
// for its slot: a switch the stop cut short between its commit and the reset
// of the work tree is finished (space.RecoverSwitch), and once the space is
// past the switch its uploads go to the attachment repository again, as they
// do once the switch is pushed; assets/ is ignored by its .gitignore. The
// job, when it runs, finds nothing left to copy and pushes the switch.
func (m *Manager) settleSwitch(ctx context.Context, id string, sp *space.Space) {
	if !m.moveRunning(id) {
		return
	}
	if recovered, err := sp.RecoverSwitch(ctx); err != nil {
		m.log.Warn("could not finish a switch of attachments a stop cut short; the move's job tries again", "space", id, "error", err)
		return
	} else if recovered {
		m.log.Info("finished a switch of attachments a stop of the server cut short", "space", id)
	}
	if ok, err := sp.AssetsSwitched(ctx); err == nil && ok {
		m.releaseUploads(id)
		m.setPhase(id, MoveSwitching)
	}
}

// releaseUploads lets the space's uploads go to the attachment repository
// (backend.Direct) while its move goes on.
func (m *Manager) releaseUploads(id string) {
	m.as.mu.Lock()
	delete(m.as.moving, id)
	m.as.mu.Unlock()
}

// moveKind runs the moves on the jobs runner.
type moveKind struct{ m *Manager }

func (moveKind) Class() jobs.Class { return jobs.IO }

// Recover takes a move up again: it goes on from what the attachment
// repository has. Until it runs, the space's uploads go into assets/.
// EndedInQueue: a move cancelled or broken before it ran holds no uploads.
func (k moveKind) EndedInQueue(_ context.Context, j *store.Job) { k.m.clearMoving(j.Subject) }

func (k moveKind) Recover(_ context.Context, j *store.Job) jobs.Outcome {
	if !k.m.isClosed() {
		k.m.setMoving(j.Subject, MoveCopying)
	}
	return jobs.Resume
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// errCrash marks a hook's error: the run ends without a record, as a crash does.
type errCrash struct{ error }

func (k moveKind) Run(ctx context.Context, j *store.Job, p *jobs.Progress) error {
	m, id := k.m, j.Subject
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopCtx := context.AfterFunc(m.closedCtx(), cancel)
	defer stopCtx()
	// The uploads are held in assets/ since the enqueue (or the start's
	// Recover), unless the space was found past the switch when it was opened.
	m.setPhase(id, MoveCopying)
	defer m.clearMoving(id)
	err := m.assetsHook("started")
	if err != nil {
		err = errCrash{err}
	} else {
		err = m.doMove(ctx, id, j, p)
	}
	var crash errCrash
	switch {
	case err == nil:
		m.log.Info("moved the attachments of a space into its attachment repository", "space", id)
		// Step 3: the clone is replaced by a partial one. When the space is
		// in use it stays as it is; eviction does the same later.
		if eerr := m.Evict(context.WithoutCancel(ctx), id, EvictOptions{AdminRequest: true}); eerr != nil {
			m.log.Info("the clone of a space whose attachments moved was not replaced yet", "space", id, "reason", eerr.Error())
		}
		return nil
	case errors.As(err, &crash):
		// A test's crash: nothing more happens until the server stops, and
		// the next start goes on with the move.
		m.log.Warn("moving the attachments of a space was cut short; it goes on at the next start", "space", id, "error", err)
		<-ctx.Done()
		return ctx.Err()
	case ctx.Err() != nil:
		m.log.Warn("moving the attachments of a space was cut short; it goes on at the next start", "space", id, "error", err)
		return ctx.Err()
	default:
		m.log.Warn("moving the attachments of a space failed", "space", id, "error", err)
		return errors.New(errText(err))
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

// moveProgress turns the counts of a run, which starts again from what the
// repository has, into the steps of the job.
type moveProgress struct {
	p                 *jobs.Progress
	files, filesDone  int64
	bytes, bytesDone  int64
	stepped, stepByte int64 // what the job has been told is done
}

func (mp *moveProgress) set(files, bytes, done, doneBytes int64) {
	mp.files, mp.bytes, mp.filesDone, mp.bytesDone = files, bytes, done, doneBytes
	mp.p.SetTotal(files, bytes)
	mp.flush()
}

func (mp *moveProgress) add(n, bytes int64) {
	mp.filesDone += n
	mp.bytesDone += bytes
	mp.flush()
}

func (mp *moveProgress) flush() {
	n, b := mp.filesDone-mp.stepped, mp.bytesDone-mp.stepByte
	mp.stepped, mp.stepByte = mp.filesDone, mp.bytesDone
	mp.p.Step(n, b)
}

func (m *Manager) doMove(ctx context.Context, id string, j *store.Job, p *jobs.Progress) error {
	sp, release, err := m.Acquire(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	repo, _, err := m.assetRepo(ctx, id)
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
	if ok, err := sp.AssetsSwitched(ctx); err == nil && ok {
		m.releaseUploads(id) // past the switch: nothing goes into assets/ any more
	}
	prog := &moveProgress{p: p, stepped: j.Done, stepByte: j.BytesDone}
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
		prog.set(total, totalBytes, done, doneBytes)
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
			prog.add(int64(len(chunk)), n)
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
	m.setPhase(id, MoveSwitching)
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
			m.releaseUploads(id)
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
	// The switch landed: recorded on the row, which outlives the job, on a
	// context of its own (a close now must not leave it unrecorded). Should it
	// fail every try the job fails, and settleMove records it once it sees the
	// space's HEAD past the switch.
	rctx := context.WithoutCancel(ctx)
	for try := 0; ; try++ {
		if err = store.MarkAssetsMoved(rctx, m.opt.Store.Q(), id); err == nil || try >= 2 {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("the switch landed but could not be recorded: %w", err)
	}
	prog.add(prog.files-prog.filesDone, prog.bytes-prog.bytesDone)
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
