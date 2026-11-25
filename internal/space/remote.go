package space

// A space that follows a remote repository. The remote branch is the truth:
// every change to a file is made on top of its current state and pushed before
// the request is answered; a puller (owned by the spaces manager) fetches and
// fast-forwards between changes.
//
// Locking. s.mu serialises every change to files and the index, as in local
// mode, and a remote change holds it across its network round trips: other
// changes wait for it, reads never do (they take no lock but the index's own).
// A fetch, which only adds objects and refs, runs outside s.mu under fetchMu.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/page"
)

// States of a remote (the state field of the API).
const (
	RemoteOK        = "ok"
	RemoteOffline   = "offline"
	RemoteRewritten = "rewritten"
	RemoteError     = "error"
)

// Kinds of attention an error state needs. Only the last one is sticky: the
// owner is told until they ask for a sync; the others clear themselves when the
// remote is reachable and accepting again.
const (
	KindAuth    = "auth"
	KindHostKey = "hostkey"
	KindClone   = "clone"
	KindRescued = "rescued"
)

// IsConnectionKind reports whether an error kind is one of a connection
// credential's (connection_expired and the like; docs/api.md, "Remote"): they
// clear themselves like auth.
func IsConnectionKind(kind string) bool {
	switch kind {
	case "connection_expired", "connection_missing", "installation_gone", "app_missing_permission",
		"provider_not_configured", "repo_not_in_installation":
		return true
	}
	return false
}

// RemoteStatus is what the server knows about the remote right now.
type RemoteStatus struct {
	State     string
	Kind      string // see the Kind constants; "" outside an error state
	Error     string // for people
	LastGood  string // the last remote commit the server accepted
	RemoteTip string // when rewritten: the remote's commit that does not contain LastGood
	LastSync  time.Time
}

// RemoteOptions turns on remote mode in Open, and is what Connect is given.
type RemoteOptions struct {
	URL, Branch string
	Cred        gitstore.Credential
	// Status is the status stored for it; the zero value is a fresh connection.
	Status RemoteStatus
	// OnStatus is told whenever the status changes (and at least every half
	// minute while syncs succeed); it persists it. It must not call back into
	// the space.
	OnStatus func(RemoteStatus)
	// NetTimeout bounds each command that talks to the remote (default 60 s).
	NetTimeout time.Duration
	// FetchFresh: a fetch is skipped when one succeeded this recently (default 5 s).
	// FailFast: after a network failure, attempts within this long answer with
	// the same failure at once (default 3 s).
	FetchFresh, FailFast time.Duration
	// Runner and AllowUnsafe are for tests (a recorder, local paths as remotes).
	Runner      gitstore.Runner
	AllowUnsafe bool
	// Partial asks for a partial clone (milestone 3.1) when connecting makes
	// the space take the remote's content. It does not change a clone that is
	// full already, and a space that is partial is found out by its clone.
	Partial bool
}

// Errors of remote mode the API maps.
var (
	// ErrDiverged: connecting would have to merge two histories that both
	// have content of their own.
	ErrDiverged = errors.New("the space and the remote repository both have content of their own")
	// ErrNotTip: the commit offered for acceptance is not the remote's tip.
	ErrNotTip = gitstore.ErrNotRemoteTip
	// ErrNothingToAccept: the remote history was not rewritten.
	ErrNothingToAccept = gitstore.ErrNothingToAccept
	// ErrNoRemote: the space is local.
	ErrNoRemote = errors.New("the space has no remote repository")
)

// UnavailableError means the remote cannot be reached, refuses the credential,
// or is paused; nothing was changed. Msg is fit to show.
type UnavailableError struct {
	Msg string
	Err error
}

func (e *UnavailableError) Error() string { return e.Msg }
func (e *UnavailableError) Unwrap() error { return e.Err }

func unavailable(err error, format string, a ...any) error {
	return &UnavailableError{Msg: fmt.Sprintf(format, a...), Err: err}
}

const (
	// maxAttempts bounds how often a change is made again after the remote
	// moved under it.
	maxAttempts = 4
	// persistEvery: successful syncs are written to the state database at most this often.
	persistEvery = 30 * time.Second
	// bigDelta: a pull that changed more files than this refreshes the index
	// with its mtime-based sync instead of file by file.
	bigDelta = 2000
)

type remote struct {
	// rollbackDue says the change the journal holds still has to be taken
	// back (the start could not): changes are refused until it is (under s.mu).
	rollbackDue bool
	repo        *gitstore.Repo
	url, branch string
	freshFor    time.Duration
	failFast    time.Duration
	onStatus    func(RemoteStatus)

	fetchMu   sync.Mutex
	lastFetch time.Time
	failedAt  time.Time
	failErr   error

	// partial is set while the clone is partial; pstate is the fetches of
	// contents that are under way (partial.go).
	partial atomic.Bool
	pstate  partialState

	// sincePack counts the changes pushed since the last packing and packEvery
	// is how many make the next one (0 means DefaultMaintainEvery); both under s.mu.
	sincePack, packEvery int

	mu          sync.Mutex
	st          RemoteStatus
	persisted   RemoteStatus
	persistedAt time.Time
	blocked     string // why changes are refused until an administrator looks, "" if none

	// Group commit (all under s.mu). pending are the changes committed and
	// not pushed yet, in order; the remote's one pusher (runPusher) takes
	// them as one group at a time. busy is set while it pushes a group with
	// the lock let go. work wakes the pusher; idle is signalled when it is
	// idle with nothing pending, and when it has ended.
	pending  []*pendingChange
	group    []*pendingChange // the group the pusher is answering
	busy     bool
	draining int // changes waiting for the group to go out before their repair
	// closing is set while a disconnect, a connection, an accept or Close
	// waits for the pusher to finish: no change is queued meanwhile.
	closing bool
	// stop asks the pusher to end; pusherDone is closed when it has.
	stop       bool
	pusherDone chan struct{}
	work, idle *sync.Cond
}

// pendingChange is a change of a space that follows a remote, committed and
// waiting for its push.
type pendingChange struct {
	tx     *txn
	commit string
	mark   uint64        // the journal's last frame once it was committed
	done   chan struct{} // closed when the change is answered (pushed or taken back)
	retry  bool          // taken back because the remote moved on: made again
	err    error         // taken back, and why
	crash  any           // a panic of the pusher, raised again by the request
}

func (r *remote) status() RemoteStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// set changes the status and persists it when something that people see changed.
func (r *remote) set(f func(*RemoteStatus)) {
	r.mu.Lock()
	f(&r.st)
	st := r.st
	changed := st.State != r.persisted.State || st.Kind != r.persisted.Kind || st.Error != r.persisted.Error ||
		st.RemoteTip != r.persisted.RemoteTip || st.LastGood != r.persisted.LastGood && r.persisted.LastGood == ""
	if changed || time.Since(r.persistedAt) >= persistEvery {
		r.persisted, r.persistedAt = st, time.Now()
	} else {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	if r.onStatus != nil {
		r.onStatus(st)
	}
}

func (r *remote) lastGood() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st.LastGood
}

// markOK records a successful look at the remote: the sync time, and a
// return to "ok" from every state that fixes itself.
func (r *remote) markOK() {
	r.set(func(st *RemoteStatus) {
		st.LastSync = time.Now().UTC()
		switch st.State {
		case RemoteOffline, RemoteRewritten:
			st.State, st.Kind, st.Error, st.RemoteTip = RemoteOK, "", "", ""
		case RemoteError:
			if st.Kind == KindAuth || st.Kind == KindHostKey || IsConnectionKind(st.Kind) {
				st.State, st.Kind, st.Error = RemoteOK, "", ""
			}
		}
	})
}

func (r *remote) setLastGood(h string) {
	r.set(func(st *RemoteStatus) { st.LastGood = h })
}

// Remote returns the status of the remote, or false for a local space.
func (s *Space) Remote() (RemoteStatus, bool) {
	r := s.rem.Load()
	if r == nil {
		return RemoteStatus{}, false
	}
	return r.status(), true
}

// IsRemote reports whether the space follows a remote repository.
func (s *Space) IsRemote() bool { return s.rem.Load() != nil }

// RemoteInfo is the address and branch the space follows.
func (s *Space) RemoteInfo() (url, branch string, ok bool) {
	r := s.rem.Load()
	if r == nil {
		return "", "", false
	}
	return r.url, r.branch, true
}

func (s *Space) newRemote(o *RemoteOptions) *remote {
	if o.FetchFresh <= 0 {
		o.FetchFresh = 5 * time.Second
	}
	if o.FailFast <= 0 {
		o.FailFast = 3 * time.Second
	}
	repo := gitstore.New(gitstore.Options{Dir: s.root, Runner: o.Runner, Cred: o.Cred, NetTimeout: o.NetTimeout, AllowUnsafe: o.AllowUnsafe})
	r := &remote{repo: repo, url: o.URL, branch: o.Branch, freshFor: o.FetchFresh, failFast: o.FailFast, onStatus: o.OnStatus, st: o.Status}
	if r.st.State == "" {
		r.st.State = RemoteOK
	}
	r.persisted, r.persistedAt = r.st, time.Now()
	return r
}

// initRemote runs at Open for a space that follows a remote. It touches no
// network: what a crash left behind is committed here, and what has to be
// pushed (or, if the remote moved, set aside) is done by the first sync, which
// every change and every pull starts with.
func (s *Space) initRemote(o *RemoteOptions, recovered map[string]Author, rollback string) error {
	r := s.newRemote(o)
	r.blocked = rollback
	// The journal of a change that could not be taken back stays: the next
	// start, or clearing the notice, takes it back before changes are let in.
	r.rollbackDue = rollback != ""
	ctx := s.ctx
	if r.st.LastGood = r.repo.ReadLastGood(ctx, r.branch); r.st.LastGood == "" {
		if r.st.LastGood = o.Status.LastGood; r.st.LastGood == "" {
			r.st.LastGood = r.repo.RemoteTip(ctx, r.branch)
		}
	}
	if r.st.LastGood != "" {
		r.repo.WriteLastGood(ctx, r.branch, r.st.LastGood)
	}
	s.rem.Store(r)
	s.cm.pauseMaintenance() // see committer.pauseMaintenance
	s.enterPartial(ctx, r)
	// origin must be the address the database has.
	if cur := r.repo.RemoteURL(ctx); cur != o.URL {
		if err := r.repo.SetRemote(ctx, o.URL); err != nil {
			return err
		}
	}
	if what, err := r.repo.Busy(ctx); err == nil && what != "" && r.blocked == "" {
		r.blocked = "the clone is in the middle of a " + what
	}
	// Files written but never committed: the journal says which. They are
	// committed now, on top of whatever the clone was at; the first sync pushes
	// them or, if the remote has moved on, keeps them under a ref.
	if len(recovered) > 0 && r.blocked == "" {
		// One commit per person, as the background committer makes them.
		_, committer := s.cm.git.identities(Author{})
		for _, grp := range s.cm.git.groupByAuthor(recovered) {
			made, err := r.repo.CommitPaths(ctx, grp.paths, messageVia(grp.via), grp.id, committer)
			if err != nil {
				r.blocked = "could not commit what an earlier run left uncommitted: " + gitstore.Scrub(err.Error())
				break
			} else if made {
				s.log.Info("committed changes an earlier run had not committed", "paths", len(grp.paths), "author", grp.id.Name)
			}
		}
	}
	if r.blocked == "" {
		// Anything still modified is something this server did not make.
		if mod, err := r.repo.ModifiedTracked(ctx); err != nil {
			r.blocked = "could not look at the clone: " + gitstore.Scrub(err.Error())
		} else if len(mod) > 0 {
			sort.Strings(mod)
			r.blocked = "the clone has changes the server did not make (" + strings.Join(firstN(mod, 3), ", ") + "); an administrator must look at it"
		}
	}
	if r.blocked != "" {
		s.log.Error("changes are refused: the clone is not in the state the server left it in", "reason", r.blocked)
		r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindClone, r.blocked })
	}
	if !r.rollbackDue {
		if err := s.cm.clearJournal(); err != nil {
			return err
		}
	}
	s.startPusher(r)
	return nil
}

// rollbackRemote takes back the change of a space that follows a remote that
// an earlier run journaled and did not finish (the journal of such a space
// holds one change at most: it is cleared once the change is pushed or
// undone). Its commit stays when it is on the remote already, as far as the
// clone last saw it: the push went through, the person was just not told.
// Otherwise the clone goes back to the commit the change started from ("rp"),
// and the files the change named to what that commit has; a folder it
// renamed goes back first. Nothing here reaches the network. A later sync
// brings back a commit whose push did go through unseen.
func rollbackRemote(ctx context.Context, repo *gitstore.Repo, branch, root string, recs []jrec, log *slog.Logger) (problem string) {
	if len(recs) == 0 {
		return ""
	}
	head := repo.Head(ctx)
	parent := ""
	// The first change the journal holds is the earliest not pushed (with
	// group commit several can be): back to the commit it started from.
	for _, r := range recs {
		if r.Op == "rp" {
			parent = r.Rev
			break
		}
	}
	if parent == "" {
		parent = head
	}
	// The change stays when its commit is on the remote: then its files go
	// back to that commit (HEAD), so that nothing half written is left of a
	// step after it (a crash between the push of a repair of the way to a
	// folder and the record of the commit the change then started from).
	to, stays := parent, false
	if head != "" && head != parent {
		if tip := repo.RemoteTip(ctx, branch); tip != "" {
			if on, err := repo.IsAncestor(ctx, head, tip); err == nil && on {
				to, stays = head, true
			}
		}
	}
	ok := func(p string) bool {
		return p != "" && fs.ValidPath(p) && !strings.HasPrefix(p, ".git/") && p != ".git"
	}
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if ok(p) && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	folder := func(from, to string) {
		if !stays && ok(from) && ok(to) {
			takeBackFolder(root, from, to)
		}
	}
	var old []string
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		switch {
		case r.Op == "md":
			folder(r.From, r.To)
		case r.Op == "p" && r.Dir:
			folder(r.From, r.Path)
		case r.Op == "r" && r.Dir:
			// A folder renamed through a third name: back from either. Its
			// files are journaled one by one ("t").
			folder(r.From, r.To)
			folder(r.From, r.Path)
		case r.Op == "c":
			// A name a create took: its transaction, if journaled, is
			// put back below with the rest; a name taken before that is
			// only a second name of the create's temporary file.
			if !stays && ok(r.From) {
				takeBackClaim(root, r.From, r.Rev, func(string) bool { return false })
			}
		case r.Op == "l" || r.Op == "ld" || r.Op == "m" || r.Op == "t" || r.Op == "d" || r.Op == "w" ||
			r.Op == "b" || r.Op == "bd" || r.Op == "u" || r.Op == "ud" || r.Op == "f" || r.Op == "fd" || r.Op == "fp":
			// Records an earlier build wrote: what they name is not known
			// to be files, and nothing of them is done.
			old = append(old, r.Op)
		case r.Op == "rp" || r.Op == "x" || r.Op == "a" || r.Op == "k" || r.Op == "mdd":
		default:
			add(r.Path)
			add(r.From)
			add(r.To)
		}
	}
	if len(old) > 0 {
		log.Warn("the journal has records an earlier build wrote; they are passed over", "records", old)
	}
	// Only files go to Discard: a name that is a folder on disk (a record
	// that names one) is not one to put back.
	files := paths[:0]
	for _, p := range paths {
		if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil && fi.IsDir() {
			continue
		}
		files = append(files, p)
	}
	if to == "" || len(files) == 0 && head == to {
		return ""
	}
	if err := repo.Discard(ctx, to, files); err != nil {
		// The space opens; changes are refused until someone looks.
		return "could not take back the change an earlier run did not push: " + gitstore.Scrub(err.Error())
	}
	if stays {
		log.Info("the change an earlier run left in the journal was pushed; it stays", "commit", head)
	} else {
		log.Info("took back a change an earlier run did not push", "files", len(files), "to", to)
	}
	return ""
}

func firstN(l []string, n int) []string {
	if len(l) > n {
		return append(append([]string{}, l[:n]...), "…")
	}
	return l
}

// --- fetching -----------------------------------------------------------------

// fetch brings the remote branch up to date unless that was done a moment ago
// (and force is false). It runs without s.mu.
func (s *Space) fetch(ctx context.Context, r *remote, force bool) error {
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	if !force && r.failErr != nil && time.Since(r.failedAt) < r.failFast {
		return r.failErr
	}
	if !force && !r.lastFetch.IsZero() && time.Since(r.lastFetch) < r.freshFor {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if err := r.repo.Fetch(ctx, r.branch, false); err != nil {
		r.failedAt, r.failErr = time.Now(), err
		return err
	}
	r.lastFetch, r.failErr = time.Now(), nil
	return nil
}

// noteFetched records that a command other than fetch has just seen the remote.
func (r *remote) noteFetched() {
	r.fetchMu.Lock()
	r.lastFetch, r.failErr = time.Now(), nil
	r.fetchMu.Unlock()
}

// netFailure turns a failed network command into the state people see and the
// error a caller returns.
func (s *Space) netFailure(r *remote, err error) error {
	var ne *gitstore.NetError
	msg := "could not reach the remote"
	if errors.As(err, &ne) {
		msg = ne.Msg
	}
	msg = gitstore.Scrub(msg)
	var ce *gitstore.CredentialError
	if errors.As(err, &ce) && ce.Code != "" && !ce.Offline {
		// The credential itself cannot be had (a connection ran out):
		// reads go on, writes are refused, and the code says what to fix.
		r.set(func(st *RemoteStatus) {
			if st.State != RemoteRewritten {
				st.State, st.Kind, st.Error = RemoteError, ce.Code, msg
			}
		})
		return unavailable(err, "%s", msg)
	}
	switch gitstore.KindOf(err) {
	case gitstore.KindAuth:
		r.set(func(st *RemoteStatus) {
			if st.State != RemoteRewritten {
				st.State, st.Kind, st.Error = RemoteError, KindAuth, msg
			}
		})
	case gitstore.KindHostKey:
		r.set(func(st *RemoteStatus) {
			if st.State != RemoteRewritten {
				st.State, st.Kind, st.Error = RemoteError, KindHostKey, msg
			}
		})
	default:
		r.set(func(st *RemoteStatus) {
			if st.State != RemoteRewritten && st.Kind != KindRescued && !(st.State == RemoteError && st.Kind == KindClone) {
				st.State, st.Kind, st.Error = RemoteOffline, "", msg
			}
		})
	}
	return unavailable(err, "%s", msg)
}

// netFailureOr is netFailure for an error that may or may not come from the
// network: other errors are returned as they are.
func (s *Space) netFailureOr(r *remote, err error) error {
	if gitstore.KindOf(err) != gitstore.KindOther || errors.Is(err, context.Canceled) {
		return s.netFailure(r, err)
	}
	return err
}

// --- the state of the clone ------------------------------------------------------

// syncTipLocked makes the clone exactly the remote's tip, or fails with an
// *UnavailableError: it fetches (unless that was done a moment ago), checks the
// history was not rewritten, then fast-forwards, pushes commits that were left
// unpushed, or sets them aside if the remote went another way. Callers hold s.mu.
func (s *Space) syncTipLocked(ctx context.Context, r *remote) error {
	if r.blocked != "" {
		return unavailable(nil, "%s", r.blocked)
	}
	if len(r.pending) > 0 {
		// Changes are waiting for their push: this one goes on top of them,
		// and out with them. A remote that moved meanwhile refuses that
		// push, and every change of it is made again on the new tip.
		return nil
	}
	if err := s.fetch(ctx, r, false); err != nil {
		return s.netFailure(r, err)
	}
	return s.reconcileLocked(ctx, r)
}

// reconcileLocked looks at the fetched remote branch against the clone.
func (s *Space) reconcileLocked(ctx context.Context, r *remote) error {
	if len(r.pending) > 0 || r.busy {
		return nil // the push under way brings the clone and the remote together
	}
	repo := r.repo
	for pass := 0; pass < 3; pass++ {
		tip, err := repo.CheckRemote(ctx, r.branch, r.lastGood())
		var hr *gitstore.HistoryRewrittenError
		switch {
		case errors.As(err, &hr):
			first := r.status().State != RemoteRewritten || r.status().RemoteTip != hr.Remote
			r.set(func(st *RemoteStatus) {
				st.State, st.Kind, st.Error, st.RemoteTip = RemoteRewritten, "", hr.Error(), hr.Remote
			})
			if first {
				s.log.Error("the remote history was rewritten: changes are paused, reads keep working", "lastGood", hr.LastGood, "remote", hr.Remote)
			}
			return unavailable(err, "%s", hr.Error())
		case errors.Is(err, gitstore.ErrNoBranch):
			msg := "the remote has no branch " + r.branch + " any more"
			r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, "", msg })
			return unavailable(err, "%s", msg)
		case err != nil:
			return err
		}
		head := repo.Head(ctx)
		ahead, behind, err := repo.AheadBehind(ctx, r.branch)
		if err != nil {
			return err
		}
		switch {
		case ahead == 0 && behind == 0:
		case ahead == 0:
			if err := s.prefetchMove(ctx, r, head, tip); err != nil {
				return s.netFailureOr(r, err)
			}
			if err := repo.FastForward(ctx, r.branch); err != nil {
				msg := "the clone could not be moved to the remote: " + gitstore.Scrub(err.Error())
				r.blocked = msg
				r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindClone, msg })
				return unavailable(err, "%s", msg)
			}
			// A pull may put a link or a file where a folder of .petalbook
			// belongs: nothing of the server's own is kept there (its state is
			// in the Git directory), and the settings behind it are read as
			// restored until a change writes there and puts it right.
			ids, all := s.applyDelta(ctx, r, head, tip)
			// Pages the pull moved or renamed: the links that carry their ids
			// are put right as one commit of the service, pushed on the next
			// pass (it is a commit that was never pushed).
			if s.repairAfterPull(ctx, r, ids, all) {
				continue
			}
		case behind == 0:
			// Commits that were never pushed (a crash). Push them as they are.
			if err := repo.Push(ctx, r.branch); err != nil {
				if gitstore.KindOf(err) == gitstore.KindRejected {
					if ferr := s.fetch(ctx, r, true); ferr != nil {
						return s.netFailure(r, ferr)
					}
					continue // the remote moved: look again
				}
				return s.netFailure(r, err)
			}
			r.noteFetched()
			s.log.Info("pushed commits an earlier run had left unpushed", "commits", ahead)
		default:
			if err := s.prefetchMove(ctx, r, head, tip); err != nil {
				return s.netFailureOr(r, err)
			}
			ref, err := repo.KeepUnpushed(ctx, head, time.Now())
			if err != nil {
				return err
			}
			if err := repo.ResetHard(ctx, tip); err != nil {
				return err
			}
			s.applyDelta(ctx, r, head, tip)
			msg := fmt.Sprintf("%d change(s) made here were never pushed and the remote has moved on; they are kept as %s in the server's clone, and the space now follows the remote", ahead, ref)
			s.log.Error("diverged from the remote: unpushed commits set aside", "ref", ref, "commits", ahead)
			r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindRescued, msg })
		}
		if h := repo.Head(ctx); h != "" {
			if err := repo.WriteLastGood(ctx, r.branch, h); err != nil {
				s.log.Warn("could not store the last good commit", "error", err)
			}
			r.setLastGood(h)
		}
		r.markOK()
		return nil
	}
	return unavailable(nil, "the remote keeps changing; try again")
}

// applyDelta brings the index in line with a move of the work tree from one
// commit to another: exactly the files that differ, not a walk of the tree.
//
// It returns the ids of the pages the pull changed, for the repair of the links
// to them; all is true when the delta was too big to list (or could not be) and
// every link has to be looked at.
func (s *Space) applyDelta(ctx context.Context, r *remote, from, to string) (ids []string, all bool) {
	defer s.forgetAssetsDir()
	if from == "" || from == to {
		return nil, false
	}
	changes, err := r.repo.DiffNames(ctx, from, to)
	if err != nil || len(changes) > bigDelta {
		if err != nil {
			s.log.Warn("could not list what a pull changed; syncing the index by walking the files", "error", err)
		}
		if _, err := s.idx.Sync(s.src, false); err != nil {
			s.log.Error("could not update the index after a pull", "error", err)
		}
		s.syncTypes()
		return nil, true
	}
	var pages, dirs []string
	for _, c := range changes {
		if isPagePath(c.New) {
			pages = append(pages, c.New)
		}
		dirs = append(dirs, path.Dir(c.New))
	}
	if _, err := s.idx.Refresh(s.src, pages, visibleDirs(dirs)); err != nil {
		s.log.Error("could not update the index after a pull", "error", err)
		return nil, true
	}
	if err := s.idx.RetryDuplicates(s.src); err != nil {
		s.log.Warn("could not re-check duplicate ids", "error", err)
	}
	s.syncTypes() // a pull may have brought other types or bindings
	if len(pages) > 0 {
		if at, err := s.idx.PagesAt(pages); err == nil {
			for _, id := range at {
				ids = append(ids, id)
			}
		}
	}
	return ids, false
}

// repairAfterPull makes the links that carry the id of a page a pull moved
// name where the page is now, in one commit made by the service, and reports
// whether it made one. Links to ids that two files claim, or that no page has,
// are left as they are (the validation page lists them, see docs/design/milestones/phase-2.md, 2.5). A
// failure undoes what was written and is logged: the next pull tries again.
func (s *Space) repairAfterPull(ctx context.Context, r *remote, ids []string, all bool) bool {
	if !all && len(ids) == 0 {
		return false
	}
	if all {
		ids = nil
	}
	parent := r.repo.Head(ctx)
	tx := &txn{s: s, remote: true, parent: parent}
	paths, err := s.repairStale(tx, ids)
	if err != nil || len(paths) == 0 {
		if err != nil {
			s.log.Warn("could not repair links after a pull", "error", err)
			s.undoLocked(ctx, r, tx, parent)
		}
		return false
	}
	tx.paths = uniq(tx.paths)
	author, committer := s.cm.git.identities(Author{})
	made, err := r.repo.CommitPaths(ctx, tx.paths, commitMessage, author, committer)
	if err != nil {
		s.log.Warn("could not commit the repair of links after a pull", "error", err)
		s.undoLocked(ctx, r, tx, parent)
		return false
	}
	s.heal.committed(tx.paths)
	if made {
		s.log.Info("repaired the paths of links to pages that were moved outside", "files", len(tx.paths))
	}
	return made
}

// Pull fetches the remote and moves the space forward to it. force fetches
// even if a fetch succeeded a moment ago (an explicit "sync now"). It returns
// an *UnavailableError when the remote cannot be reached or is paused; the
// space keeps serving what it has.
func (s *Space) Pull(ctx context.Context, force bool) error {
	r := s.rem.Load()
	if r == nil {
		return nil
	}
	if err := s.fetch(ctx, r, force); err != nil {
		return s.netFailure(r, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.rem.Load() != r {
		return nil // disconnected meanwhile
	}
	if r.blocked != "" {
		return unavailable(nil, "%s", r.blocked)
	}
	return s.reconcileLocked(s.ctx, r)
}

// ClearNotice forgets an error that only needed to be seen (unpushed commits
// that were set aside) and looks at the clone again; an administrator's fix of
// a clone that was refused is picked up here too.
func (s *Space) ClearNotice() {
	r := s.rem.Load()
	if r == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.rollbackDue && !s.retryRollbackLocked(r) {
		return
	}
	if r.blocked != "" {
		if mod, err := r.repo.ModifiedTracked(s.ctx); err == nil && len(mod) == 0 {
			if what, err := r.repo.Busy(s.ctx); err == nil && what == "" {
				r.blocked = ""
				r.set(func(st *RemoteStatus) {
					if st.Kind == KindClone {
						st.State, st.Kind, st.Error = RemoteOK, "", ""
					}
				})
			}
		}
	}
	r.set(func(st *RemoteStatus) {
		if st.Kind == KindRescued {
			st.State, st.Kind, st.Error = RemoteOK, "", ""
		}
	})
}

// retryRollbackLocked takes back the change that the start could not take
// back (rollbackRemote) and reports whether it is done: then the journal is
// cleared and the files are indexed as they are now.
func (s *Space) retryRollbackLocked(r *remote) bool {
	recs, err := s.cm.j.records()
	problem := ""
	if err != nil {
		problem = "could not read the journal: " + err.Error()
	} else {
		problem = rollbackRemote(s.ctx, r.repo, r.branch, s.root, recs, s.log)
	}
	if problem != "" {
		r.blocked = problem
		r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindClone, problem })
		return false
	}
	r.rollbackDue = false
	if err := s.cm.clearJournal(); err != nil {
		s.log.Warn("could not clear the commit journal", "error", err)
	}
	if _, err := s.idx.Sync(s.src, false); err != nil {
		s.log.Error("could not index the files of a change taken back", "error", err)
	}
	s.typesDirty.Store(true)
	return true
}

// Accept accepts the remote's rewritten history: the space moves to that
// commit. Unpushed commits the clone still has are kept under a ref first.
func (s *Space) Accept(ctx context.Context, commit string) error {
	r := s.rem.Load()
	if r == nil {
		return ErrNoRemote
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The changes waiting for their push are pushed or taken back first:
	// none is taken back over the history accepted.
	r, err := s.drainRemoteLocked()
	if err != nil {
		return err
	}
	if r == nil {
		return ErrNoRemote // disconnected meanwhile
	}
	defer s.resumeLocked(r)
	if r.blocked != "" {
		return unavailable(nil, "%s", r.blocked)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	r.fetchMu.Lock() // AcceptTip fetches: not at the same time as a pull's fetch
	tip, err := r.repo.AcceptTip(ctx, r.branch, r.lastGood(), commit)
	r.fetchMu.Unlock()
	if err != nil {
		if gitstore.KindOf(err) != gitstore.KindOther {
			return s.netFailure(r, err)
		}
		return err
	}
	r.noteFetched()
	old := r.repo.Head(ctx)
	if err := s.prefetchMove(ctx, r, old, tip); err != nil {
		return s.netFailureOr(r, err)
	}
	note := ""
	if lg := r.lastGood(); old != "" && lg != "" {
		if n, err := r.repo.CountNotIn(ctx, lg); err == nil && n > 0 {
			ref, err := r.repo.KeepUnpushed(ctx, old, time.Now())
			if err != nil {
				return err
			}
			note = fmt.Sprintf("%d change(s) made here were never pushed; they are kept as %s in the server's clone", n, ref)
		}
	}
	if err := r.repo.ResetHard(ctx, tip); err != nil {
		return err
	}
	s.applyDelta(ctx, r, old, tip)
	if err := r.repo.WriteLastGood(ctx, r.branch, tip); err != nil {
		s.log.Warn("could not store the last good commit", "error", err)
	}
	r.setLastGood(tip)
	r.set(func(st *RemoteStatus) {
		st.State, st.Kind, st.Error, st.RemoteTip = RemoteOK, "", "", ""
		if note != "" {
			st.State, st.Kind, st.Error = RemoteError, KindRescued, note
		}
	})
	r.markOK()
	s.log.Warn("remote history accepted by the owner", "remote", tip)
	return nil
}

// --- a change to files, made on top of the remote ------------------------------

// txn is one change to the files. In local mode it only hands paths to the
// committer; in remote mode it collects them for the commit that follows.
type txn struct {
	s       *Space
	by      Author // who is making the change
	remote  bool
	paths   []string // files the change wrote, moved or deleted
	dirs    []string // directories whose folder rows may change
	created []string // directories the change made, deepest last
	// undo are steps that take back what Git cannot: a directory that was
	// renamed (with whatever in it Git does not track), directories that were
	// removed. They run last-first before Git's side is put back. A step
	// returns the paths of the change that are gone again because of it, so
	// that nothing goes looking for them (under another spelling of an old
	// name, on a file system that ignores case, that would find the old file).
	undo []func() []string
	// parent is the commit the change started from (a remote space).
	parent string
	// assets are uploads that go to the attachment repository before the
	// change is committed (attachments.go).
	assets []pendingAsset
	// txs are the journal transactions of the change: an undo gives them up.
	txs []uint64
	// writes are the transactions of journalFiles (see endWrites).
	writes []*writeTx
	// journaled are files a transaction of the change already holds: their
	// writes are not journaled again.
	journaled map[string]bool
	// undoFailed is set by a step that could not do its part: what is wrong,
	// for the administrator.
	undoFailed string
}

// runUndo takes back the steps of the change that the file system needs undone
// (see undo), last first. A local space runs it itself when a change fails
// after the folder it moved has moved.
func (t *txn) runUndo() {
	for i := len(t.undo) - 1; i >= 0; i-- {
		t.undo[i]()
	}
	t.undo = nil
}

// record journals what the change is about to do, as the change of its author.
func (t *txn) record(recs ...jrec) error { return t.s.cm.Record(t.by, recs...) }

// prepareBuild journals a transaction put together file by file
// (committer.PrepareBuild) as one of the change.
func (t *txn) prepareBuild(b *txBuild) (uint64, error) {
	tx, err := t.s.cm.PrepareBuild(t.by, b)
	if err != nil {
		return 0, err
	}
	t.txs = append(t.txs, tx)
	return tx, nil
}

// stopHere is what a test's step hook panics with to stop a change where a
// crash would: no more is written after it.
type stopHere interface{ stopHere() }

// fileWrite is a file a change is about to write with data, or to remove
// (del).
type fileWrite struct {
	rel    string
	data   []byte
	del    bool
	create bool // the file is new: made only where nothing is (Old "")
}

// writeTx is a transaction journaled by journalFiles: applied once its
// writes are made.
type writeTx struct {
	t    *txn
	id   uint64
	done bool
}

// journalFiles journals, as one transaction of the change, what it is about
// to do to these files: their content now (read here) and what they get.
// Call applied on the result once the writes are made; one that never is,
// because the change failed first, is given up when the change ends
// (endWrites), so that the next start does not make it.
func (t *txn) journalFiles(ws ...fileWrite) (*writeTx, error) {
	files := make([]txFile, len(ws))
	for i, w := range ws {
		f := txFile{path: w.rel}
		if !w.create {
			f.old = revOf(filepath.Join(t.s.root, filepath.FromSlash(w.rel)))
		}
		if !w.del {
			f.rev, f.data = page.Rev(w.data), w.data
		}
		files[i] = f
	}
	id, _, err := t.prepare(files)
	if err != nil {
		return nil, err
	}
	w := &writeTx{t: t, id: id}
	t.writes = append(t.writes, w)
	return w, nil
}

// applied journals the transaction applied: its files are written.
func (w *writeTx) applied() {
	if w == nil || w.done {
		return
	}
	w.done = true
	w.t.s.cm.Applied(w.id)
}

// endWrites gives up the transactions of journalFiles whose writes were never
// made (the change failed before them).
func (t *txn) endWrites() {
	var left []uint64
	for _, w := range t.writes {
		if !w.done && w.id != 0 {
			w.done = true
			left = append(left, w.id)
		}
	}
	t.writes = nil
	if len(left) > 0 {
		t.s.cm.AbortAll(left)
	}
}

// keepIn is the record of healer.keep (and of the writing of a settings
// file) for a change: the file is journaled with its content and taken into
// the change; a thing that is no file (data nil) is taken in as it is.
func (s *Space) keepIn(tx *txn) func(rel string, data []byte) (func(), error) {
	return func(rel string, data []byte) (func(), error) {
		tx.touch(rel)
		if data == nil {
			return func() {}, nil
		}
		w, err := tx.journalFiles(fileWrite{rel: rel, data: data})
		if err != nil {
			return nil, err
		}
		return w.applied, nil
	}
}

// writeFileIn writes data to rel as a transaction of the change: journaled,
// taken into the change, written.
func (s *Space) writeFileIn(tx *txn, rel string, data []byte) error {
	w, err := tx.journalFiles(fileWrite{rel: rel, data: data})
	if err != nil {
		return err
	}
	tx.touch(rel)
	if err := writeIn(s.root, rel, data); err != nil {
		return err
	}
	w.applied()
	return nil
}

// removeFileIn removes rel as a transaction of the change.
func (s *Space) removeFileIn(tx *txn, rel string) error {
	w, err := tx.journalFiles(fileWrite{rel: rel, del: true})
	if err != nil {
		return err
	}
	tx.touch(rel)
	if err := removeIn(s.root, rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	w.applied()
	return nil
}

// prepare journals the files the change is about to write as one transaction
// (committer.Prepare). journaled is false only for a space with no journal.
func (t *txn) prepare(files []txFile) (tx uint64, journaled bool, err error) {
	if t.s.cm == nil || t.s.cm.j == nil {
		return 0, false, nil
	}
	tx, err = t.s.cm.Prepare(t.by, files)
	if err != nil {
		return 0, false, err
	}
	t.txs = append(t.txs, tx)
	return tx, true, nil
}

func (t *txn) touch(paths ...string) {
	if !t.remote {
		t.s.cm.Touch(t.by, paths...)
		return
	}
	for _, p := range paths {
		t.paths = append(t.paths, p)
		t.dirs = append(t.dirs, path.Dir(p))
	}
}

// mutate runs op, a change made by by, under the space lock the way the
// space's mode requires.
func mutate[T any](s *Space, by Author, op func(*txn) (T, error)) (T, error) {
	return mutateIn(context.Background(), s, by, op)
}

// mutateIn is mutate for a request that may stop waiting: once its change
// is committed and queued for the push, rctx ending answers the request
// with rctx's error, and the change stays queued and is still pushed.
// Everything up to that point, and the push, runs under the space's own
// context.
func mutateIn[T any](rctx context.Context, s *Space, by Author, op func(*txn) (T, error)) (T, error) {
	var zero T
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return zero, ErrClosed
	}
	if s.gate != nil {
		if err := s.gate(); err != nil {
			return zero, err
		}
	}
	// A change may make or take away a folder at the root.
	defer s.forgetAssetsDir()
	// A change to the types or to the folders they are bound to changes which
	// pages have problems with their type (see checks.go).
	defer func() {
		if s.typesDirty.Swap(false) {
			s.syncTypes()
		}
	}()
	r := s.rem.Load()
	if r == nil {
		s.cm.SwitchTo(by)
		defer s.cm.begin()()
		tx := &txn{s: s, by: by}
		defer func() {
			// A panic ends the change as a failure: its writes not made are
			// given up. Not a test's crash, which stands for the process
			// gone, where nothing more is written.
			if r := recover(); r != nil {
				if _, crash := r.(stopHere); !crash {
					tx.endWrites()
				}
				panic(r)
			}
		}()
		res, err := op(tx)
		tx.endWrites()
		return res, err
	}
	ctx := s.ctx
	// refused is why the change may not go on after it waited: the space was
	// closed, or its remote is being changed.
	refused := func() error {
		switch {
		case s.closed:
			return ErrClosed
		case s.rem.Load() != r || r.closing:
			return refusedWhileChanging()
		}
		return nil
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		for r.draining > 0 && refused() == nil {
			r.idleCond(&s.mu).Wait() // a repair waits for the changes before it
		}
		if err := refused(); err != nil {
			return zero, err
		}
		if err := s.syncTipLocked(ctx, r); err != nil {
			return zero, err
		}
		parent := r.repo.Head(ctx)
		// The commit the change starts from: a crash before its push has
		// the next start put the files of the change back to it.
		if err := s.cm.Record(by, jrec{Op: "rp", Rev: parent}); err != nil {
			return zero, err
		}
		tx := &txn{s: s, by: by, remote: true, parent: parent}
		res, err := op(tx)
		tx.endWrites()
		if err == nil {
			// The attachments go first: a page committed after this never
			// refers to one that is not on its remote.
			// A failure there is the attachment repository's, never the
			// space remote's: its state is left as it is.
			err = s.pushAssets(ctx, tx)
		}
		if err != nil {
			s.undoLocked(ctx, r, tx, tx.parent)
			if errors.Is(err, errPushPending) {
				// A repair that has to be pushed first, with changes of
				// others waiting for their push: no new change joins them
				// (draining), the pusher sends them out, and this change is
				// made again.
				r.draining++
				for (len(r.pending) > 0 || r.busy) && refused() == nil {
					r.idleCond(&s.mu).Wait()
				}
				r.draining--
				r.idleCond(&s.mu).Broadcast()
				attempt--
				continue
			}
			if errors.Is(err, errRemoteMoved) {
				// A repair the change needed first could not be pushed: it
				// was taken back, and the change is made again on the new tip.
				s.log.Info("push rejected, making the change again on the new tip", "attempt", attempt)
				continue
			}
			return zero, err
		}
		tx.paths = uniq(tx.paths)
		if len(tx.paths) == 0 {
			s.cm.clearJournal()
			return res, nil
		}
		author, committer := s.cm.git.identities(by)
		made, err := r.repo.CommitPaths(ctx, tx.paths, messageVia(by.Via), author, committer)
		if err == nil {
			err = s.stepped("remote committed")
		}
		if err != nil {
			s.undoLocked(ctx, r, tx, tx.parent)
			return zero, err
		}
		s.heal.committed(tx.paths)
		if !made { // the change netted out to nothing
			s.cm.clearJournal()
			return res, nil
		}
		// Queued for the pusher, which pushes it with the changes committed
		// meanwhile (group commit); the answer waits for that push only.
		pc := &pendingChange{tx: tx, commit: r.repo.Head(ctx), mark: s.cm.j.lastSeq(), done: make(chan struct{})}
		r.pending = append(r.pending, pc)
		r.workCond(&s.mu).Signal()
		s.mu.Unlock()
		select {
		case <-pc.done:
		case <-rctx.Done():
			s.mu.Lock()
			return zero, rctx.Err()
		}
		s.mu.Lock()
		if pc.crash != nil {
			panic(pc.crash)
		}
		switch {
		case pc.err == nil && !pc.retry:
			return res, nil
		case pc.retry:
			s.log.Info("push rejected, making the change again on the new tip", "attempt", attempt)
			continue
		}
		return zero, pc.err
	}
	return zero, unavailable(nil, "the remote keeps changing; try again in a moment")
}

// errPushPending is a change that has to push something of its own first (a
// repair of the way to a folder) while changes of others wait for theirs.
var errPushPending = errors.New("changes are waiting for their push")

// refusedWhileChanging answers a change that comes while the space's remote
// is being disconnected or replaced, or a history accepted.
func refusedWhileChanging() error {
	return unavailable(nil, "the space's remote is being changed; try again in a moment")
}

func (r *remote) idleCond(mu *sync.Mutex) *sync.Cond {
	if r.idle == nil {
		r.idle = sync.NewCond(mu)
	}
	return r.idle
}

func (r *remote) workCond(mu *sync.Mutex) *sync.Cond {
	if r.work == nil {
		r.work = sync.NewCond(mu)
	}
	return r.work
}

// startPusher starts the one pusher of r; it runs until stopPusherLocked.
func (s *Space) startPusher(r *remote) {
	r.pusherDone = make(chan struct{})
	r.stop = false
	go s.runPusher(r)
}

// runPusher is the pusher of a space that follows a remote: while changes
// wait for their push, it takes all of them as one group and pushes it
// (pushGroupLocked) under the space's own context. It holds the space lock
// except while it waits for work and while git pushes.
func (s *Space) runPusher(r *remote) {
	s.mu.Lock()
	defer func() {
		if p := recover(); p != nil {
			// A panic in the middle of a push (a test's crash, or a bug):
			// the requests waiting are where it shows, as it did when a
			// request pushed for itself; each raises it again. The pusher
			// carries on in a new goroutine.
			for _, pc := range append(r.group, r.pending...) {
				select {
				case <-pc.done:
				default:
					pc.crash = p
					close(pc.done)
				}
			}
			r.group, r.pending, r.busy = nil, nil, false
			r.idleCond(&s.mu).Broadcast()
			if !r.stop {
				go s.runPusher(r)
				s.mu.Unlock()
				return
			}
		}
		close(r.pusherDone)
		r.idleCond(&s.mu).Broadcast()
		s.mu.Unlock()
	}()
	work, idle := r.workCond(&s.mu), r.idleCond(&s.mu)
	for {
		for len(r.pending) == 0 && !r.stop {
			work.Wait()
		}
		if len(r.pending) == 0 {
			return // stopped, with nothing left
		}
		r.busy, r.group = true, r.pending
		s.pushGroupLocked(s.ctx, r)
		r.busy, r.group = false, nil
		if len(r.pending) == 0 {
			idle.Broadcast()
		}
	}
}

// pusherEnded reports whether r's pusher has ended (or never started).
func (r *remote) pusherEnded() bool {
	if r.pusherDone == nil {
		return true
	}
	select {
	case <-r.pusherDone:
		return true
	default:
		return false
	}
}

// drainLocked, under s.mu, waits until none of r's changes waits for its
// push and its pusher is idle, and from then on refuses new changes of r
// (closing) until resumeLocked: what comes next (disconnecting, connecting
// elsewhere, accepting a history, closing) must neither be pushed through
// nor taken back from under by a push of r. It first waits for another that
// drains r, and is false when r is no longer the space's remote after that.
func (s *Space) drainLocked(r *remote) bool {
	idle := r.idleCond(&s.mu)
	for r.closing {
		idle.Wait()
	}
	if s.rem.Load() != r {
		return false
	}
	r.closing = true
	for len(r.pending) > 0 || r.busy {
		idle.Wait()
	}
	return true
}

// drainRemoteLocked drains the space's remote (drainLocked), whichever it is
// once others that drain it are done: nil when the space has none, ErrClosed
// when it was closed meanwhile.
func (s *Space) drainRemoteLocked() (*remote, error) {
	for {
		if s.closed {
			return nil, ErrClosed
		}
		r := s.rem.Load()
		if r == nil {
			return nil, nil
		}
		if s.drainLocked(r) {
			if s.closed {
				s.resumeLocked(r)
				return nil, ErrClosed
			}
			return r, nil
		}
	}
}

// resumeLocked lets changes of r in again after drainLocked.
func (s *Space) resumeLocked(r *remote) {
	r.closing = false
	r.idleCond(&s.mu).Broadcast()
}

// stopPusherLocked ends r's pusher and waits for it, under s.mu. r is
// drained (drainLocked): its pusher has nothing left to push.
func (s *Space) stopPusherLocked(r *remote) {
	r.stop = true
	r.workCond(&s.mu).Broadcast()
	for !r.pusherEnded() {
		r.idleCond(&s.mu).Wait()
	}
}

// pushGroupLocked pushes the changes waiting for their push as one group,
// called by the pusher with s.mu held. The space lock is let go while git
// pushes, so that changes coming meanwhile are made and committed on top,
// and go out with the next group. Every change whose commit the push has is
// answered. A push that the remote refuses, or that fails, takes back every
// change waiting (last first), and each is made again on the new tip by its
// request, or answered with the failure.
func (s *Space) pushGroupLocked(ctx context.Context, r *remote) {
	// fail takes back every change waiting, last first: each is made again
	// on the new tip (err nil), or answered with err.
	fail := func(err error) {
		batch := r.pending
		r.pending = nil
		for i := len(batch) - 1; i >= 0; i-- {
			s.undoLocked(ctx, r, batch[i].tx, batch[i].tx.parent)
		}
		for _, pc := range batch {
			pc.retry, pc.err = err == nil, err
			close(pc.done)
		}
	}
	parent := r.pending[0].tx.parent
	top := r.pending[len(r.pending)-1].commit
	var perr error
	func() {
		s.mu.Unlock()
		defer s.mu.Lock() // also when the push panics: the pusher's ending needs it
		r.fetchMu.Lock()
		defer r.fetchMu.Unlock()
		perr = r.repo.Push(ctx, r.branch)
	}()
	pushed := ""
	if perr == nil {
		s.stepped("remote pushed")
		pushed = r.repo.RemoteTip(ctx, r.branch)
	} else if landed, lerr := s.landedAt(ctx, r, top); lerr == nil && landed {
		// The answer was lost, but the push went through.
		pushed = r.repo.RemoteTip(ctx, r.branch)
	} else {
		moved := lerr == nil && r.repo.RemoteTip(ctx, r.branch) != parent
		var err error
		switch {
		case lerr != nil:
			err = s.netFailure(r, lerr)
		case !moved:
			// Nothing moved, so replaying cannot help: the remote refused this change.
			err = unavailable(perr, "%s", gitstore.Scrub(perr.Error()))
		default:
			r.noteFetched() // landedAt has just looked at the remote
		}
		fail(err)
		return
	}
	// Every change whose commit the push has is answered.
	n := 0
	for pushed != "" && n < len(r.pending) {
		if ok, err := r.repo.IsAncestor(ctx, r.pending[n].commit, pushed); err != nil || !ok {
			break
		}
		n++
	}
	if n == 0 {
		// What went out cannot be told: no change is answered as done.
		fail(unavailable(nil, "could not tell what the push brought to the remote; try again"))
		return
	}
	done := r.pending[:n]
	r.pending = append([]*pendingChange(nil), r.pending[n:]...)
	s.pushedUpToLocked(ctx, r, pushed, done[len(done)-1].mark)
	for _, pc := range done {
		close(pc.done)
		s.packLocked(ctx, r)
	}
}

// landedAt is landed for the commit top: whether the remote has it.
func (s *Space) landedAt(ctx context.Context, r *remote, top string) (bool, error) {
	if err := s.fetch(ctx, r, true); err != nil {
		return false, err
	}
	tip := r.repo.RemoteTip(ctx, r.branch)
	if top == "" || tip == "" {
		return false, nil
	}
	return r.repo.IsAncestor(ctx, top, tip)
}

// visibleDirs keeps the folders the index lists: not hidden ones, not
// .petalbook, not the attachments folder at the root (see ignoredDir).
func visibleDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if d == "." || d == "" {
			continue
		}
		ok := true
		for i, seg := range strings.Split(d, "/") {
			if strings.HasPrefix(seg, ".") || (i == 0 && seg == "assets") {
				ok = false
			}
		}
		if ok {
			out = append(out, d)
		}
	}
	return out
}

func uniq(l []string) []string {
	sort.Strings(l)
	out := l[:0]
	for i, p := range l {
		if i == 0 || p != l[i-1] {
			out = append(out, p)
		}
	}
	return out
}

// landed answers "did the push go through after all?": it fetches, and
// reports whether HEAD is contained in the remote branch.
func (s *Space) landed(ctx context.Context, r *remote) (bool, error) {
	if err := s.fetch(ctx, r, true); err != nil {
		return false, err
	}
	head, tip := r.repo.Head(ctx), r.repo.RemoteTip(ctx, r.branch)
	if head == "" || tip == "" {
		return false, nil
	}
	return r.repo.IsAncestor(ctx, head, tip)
}

// pushedUpToLocked records that the changes of a group commit up to commit
// pushed reached the remote, their journal up to mark done with.
func (s *Space) pushedUpToLocked(ctx context.Context, r *remote, pushed string, mark uint64) {
	if len(r.pending) == 0 {
		s.pushedLocked(ctx, r)
		return
	}
	if pushed != "" {
		if err := r.repo.WriteLastGood(ctx, r.branch, pushed); err != nil {
			s.log.Warn("could not store the last good commit", "error", err)
		}
		r.setLastGood(pushed)
	}
	r.noteFetched()
	r.markOK()
	if err := s.cm.trimJournal(mark); err != nil {
		s.log.Warn("could not trim the commit journal", "error", err)
	}
}

// pushedLocked records a change that reached the remote.
func (s *Space) pushedLocked(ctx context.Context, r *remote) {
	if h := r.repo.Head(ctx); h != "" {
		if err := r.repo.WriteLastGood(ctx, r.branch, h); err != nil {
			s.log.Warn("could not store the last good commit", "error", err)
		}
		r.setLastGood(h)
	}
	r.noteFetched()
	r.markOK()
	if err := s.cm.clearJournal(); err != nil {
		s.log.Warn("could not clear the commit journal", "error", err)
	}
}

// packLocked packs the Git objects after every packEvery-th change that was
// pushed (50 by default): the committer does not commit for a remote space, so
// this is where its loose objects are dealt with. It runs under s.mu, as the
// change did, and under fetchMu, so that it is never alongside a fetch or a
// push of this clone; the change is already on the remote. A failure is a
// warning only.
func (s *Space) packLocked(ctx context.Context, r *remote) {
	r.sincePack++
	every := r.packEvery
	if every <= 0 {
		every = DefaultMaintainEvery
	}
	if r.sincePack < every {
		return
	}
	r.sincePack = 0
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	r.fetchMu.Lock()
	err := r.repo.Maintain(ctx)
	r.fetchMu.Unlock()
	if err != nil {
		s.log.Warn("could not pack the Git objects; will try again later", "error", err)
	}
}

// restoreSpaceFile puts space.yml back as it is in the commit the change
// started from. It is the first step of undoing a change that edited it.
func (s *Space) restoreSpaceFile(tx *txn) {
	r := s.rem.Load()
	if r == nil || tx.parent == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.repo.Discard(ctx, tx.parent, []string{spaceFile}); err != nil {
		// The undo as a whole discards it again, and says so if it cannot.
		s.log.Warn("could not put space.yml back first while undoing a change", "error", err)
	}
}

// dropCreated removes the folders a change made (the parents of a folder it
// moved, say) that are still empty, and their rows in the index. A local space
// has no undo that does it; a space that follows a remote does it in undoLocked.
func (s *Space) dropCreated(tx *txn) {
	if len(tx.created) == 0 {
		return
	}
	dirs := append([]string{}, tx.created...)
	for i := len(tx.created) - 1; i >= 0; i-- {
		os.Remove(filepath.Join(s.root, filepath.FromSlash(tx.created[i]))) // fails, rightly, when not empty
		dirs = append(dirs, path.Dir(tx.created[i]))
	}
	tx.created = nil
	if _, err := s.idx.Refresh(s.src, nil, visibleDirs(dirs)); err != nil {
		s.log.Error("could not put the index back after taking a change back", "error", err)
	}
}

// undoLocked takes a change back completely: the commit (if it was made), the
// files, the folders it made and the index rows. Only the files the change
// touched are reset; nothing else in the clone is.
func (s *Space) undoLocked(ctx context.Context, r *remote, tx *txn, parent string) {
	// An undo must finish even when the request or the space is being shut down:
	// stopped half way it leaves a file or a commit that nobody asked for.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	paths := uniq(tx.paths)
	// Given up first: a crash from here on has the next start take the change
	// back from its parent commit, never finish it.
	s.cm.AbortAll(tx.txs)
	tx.txs = nil
	s.stepped("remote undoing")
	// What the change wrote is taken back: it is not newer than the history.
	defer s.heal.committed(paths)
	settled := map[string]bool{}
	for i := len(tx.undo) - 1; i >= 0; i-- {
		for _, p := range tx.undo[i]() {
			settled[p] = true
		}
	}
	discard := paths
	if len(settled) > 0 {
		discard = make([]string, 0, len(paths))
		for _, p := range paths {
			if !settled[p] {
				discard = append(discard, p)
			}
		}
	}
	undone := true
	if tx.undoFailed != "" {
		undone = false
		s.log.Error("could not undo a change; changes are refused until an administrator looks", "error", tx.undoFailed)
		r.blocked = "a change could not be undone completely: " + gitstore.Scrub(tx.undoFailed)
		r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindClone, r.blocked })
	}
	if parent != "" {
		if err := r.repo.Discard(ctx, parent, discard); err != nil {
			undone = false
			s.log.Error("could not undo a change; changes are refused until an administrator looks", "error", err)
			r.blocked = "a change could not be undone completely: " + gitstore.Scrub(err.Error())
			r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindClone, r.blocked })
		}
	}
	for i := len(tx.created) - 1; i >= 0; i-- {
		os.Remove(filepath.Join(s.root, filepath.FromSlash(tx.created[i]))) // fails, rightly, when not empty
	}
	var pages []string
	for _, p := range paths {
		if isPagePath(p) {
			pages = append(pages, p)
		}
	}
	dirs := append(append([]string{}, tx.dirs...), tx.created...)
	for _, c := range tx.created {
		dirs = append(dirs, path.Dir(c))
	}
	if _, err := s.idx.Refresh(s.src, pages, visibleDirs(dirs)); err != nil {
		s.log.Error("could not put the index back after undoing a change", "error", err)
	}
	if err := s.idx.RetryDuplicates(s.src); err != nil {
		s.log.Warn("could not re-check duplicate ids", "error", err)
	}
	// Which of two files with one id is the page depends on the order they were
	// seen in, and a change that was taken back has disturbed that order. Rare
	// (an undo in a space that has duplicate ids), so the index is simply rebuilt
	// to what a fresh start would decide.
	if probs, err := s.idx.Problems(); err == nil {
		for _, p := range probs {
			if p.Code == "duplicate_id" {
				if _, err := s.idx.Sync(s.src, true); err != nil {
					s.log.Error("could not rebuild the index after undoing a change", "error", err)
				}
				break
			}
		}
	}
	// The journal names the files of the change. It is forgotten only when they
	// are back as they were; otherwise the next start still knows about them.
	if !undone {
		return
	}
	// Changes of others still waiting for their push keep theirs (this
	// change's transactions are given up above).
	if len(r.pending) > 0 {
		return
	}
	if err := s.cm.clearJournal(); err != nil {
		s.log.Warn("could not clear the commit journal", "error", err)
	}
}

// --- connecting ----------------------------------------------------------------------

// Connect makes the space follow a remote repository, deciding by the four
// cases of docs/api.md; it does not return until the clone and the remote agree.
// Everything is undone if it fails. Network problems are *UnavailableError.
func (s *Space) Connect(ctx context.Context, o RemoteOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The changes waiting for their push go to the remote they were made
	// for, before it is replaced (and before fetchMu, which the pusher takes
	// to push).
	prev, err := s.drainRemoteLocked()
	if err != nil {
		return err
	}
	if prev != nil {
		defer s.resumeLocked(prev) // it stays when the connection fails
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()

	if prev != nil {
		prev.fetchMu.Lock() // not at the same time as a pull of the remote being replaced
		defer prev.fetchMu.Unlock()
	}
	// What is pending locally is committed first, so the history that is
	// compared and pushed is complete.
	if prev == nil {
		if err := s.cm.Flush(); err != nil {
			return fmt.Errorf("could not commit the space's pending changes before connecting: %w", err)
		}
		// No packing while the clone is fetched into and pushed from under
		// the space lock alone (it waits for one that is running).
		s.cm.pauseMaintenance()
	}
	nr := s.newRemote(&o)
	repo := nr.repo
	oldURL := repo.RemoteURL(ctx)
	if err := repo.SetRemote(ctx, o.URL); err != nil {
		return err
	}
	oldHead := repo.Head(ctx)
	ok := false
	defer func() {
		if ok {
			return
		}
		// All or nothing: the clone and its configuration are as they were, also
		// when the request that asked for the connection has gone away.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if prev == nil {
			s.cm.resumeMaintenance()
		}
		if h := repo.Head(ctx); h != oldHead && oldHead != "" {
			if err := repo.ResetHard(ctx, oldHead); err != nil {
				s.log.Error("could not put the clone back after a failed connection", "error", err)
			}
			s.applyDelta(ctx, nr, h, oldHead)
		}
		if oldURL != "" {
			repo.SetRemote(ctx, oldURL)
		} else {
			repo.RemoveRemote(ctx)
		}
	}()

	fail := func(err error) error {
		if gitstore.KindOf(err) != gitstore.KindOther || errors.Is(err, context.Canceled) {
			var ne *gitstore.NetError
			msg := "could not reach the remote"
			if errors.As(err, &ne) {
				msg = gitstore.Scrub(ne.Msg)
			}
			return unavailable(err, "%s", msg)
		}
		return err
	}
	has, err := repo.HasBranch(ctx, o.Branch)
	if err != nil {
		return fail(err)
	}
	if !has {
		// A new, empty repository: the space's history goes there.
		if repo.Head(ctx) == "" {
			return errors.New("the space has no history to push yet")
		}
		if err := repo.Push(ctx, o.Branch); err != nil {
			return fail(err)
		}
		if err := repo.Fetch(ctx, o.Branch, true); err != nil {
			return fail(err)
		}
	} else {
		// A space that has nothing of its own takes the remote's content: it
		// is cloned partially when that is asked for, so only the current
		// version's files come over. A space with history of its own keeps its
		// full clone.
		partial := false
		if o.Partial && prev == nil {
			if p, err := s.pristineLocked(ctx, repo); err == nil && p {
				partial = true
			}
		}
		if partial {
			if err := repo.FetchFiltered(ctx, o.Branch, true); err != nil {
				return fail(err)
			}
		} else if err := repo.Fetch(ctx, o.Branch, true); err != nil {
			return fail(err)
		}
		tip := repo.RemoteTip(ctx, o.Branch)
		ahead, behind, err := repo.AheadBehind(ctx, o.Branch)
		if err != nil {
			return err
		}
		prefetch := func() error {
			if !partial {
				return nil
			}
			if err := repo.PrefetchMove(ctx, oldHead, tip); err != nil {
				return fail(err)
			}
			return nil
		}
		switch {
		case ahead == 0 && behind == 0:
		case ahead == 0: // the remote contains the space: move it forward
			if err := prefetch(); err != nil {
				return err
			}
			if err := repo.FastForward(ctx, o.Branch); err != nil {
				return err
			}
			s.applyDelta(ctx, nr, oldHead, tip)
		case behind == 0: // the space contains the remote: the remote moves forward
			if err := repo.Push(ctx, o.Branch); err != nil {
				return fail(err)
			}
		default:
			pristine, err := s.pristineLocked(ctx, repo)
			if err != nil {
				return err
			}
			if !pristine {
				return ErrDiverged
			}
			if err := prefetch(); err != nil {
				return err
			}
			// Nothing of the space's own: it takes the remote's content and history.
			if err := repo.ResetHard(ctx, tip); err != nil {
				return err
			}
			s.applyDelta(ctx, nr, oldHead, tip)
			if err := s.ensureConfigLocked(ctx, nr); err != nil {
				return fail(err)
			}
		}
	}
	head := repo.Head(ctx)
	if err := repo.WriteLastGood(ctx, o.Branch, head); err != nil {
		return err
	}
	nr.st = RemoteStatus{State: RemoteOK, LastGood: head, LastSync: time.Now().UTC()}
	nr.persisted, nr.persistedAt = nr.st, time.Now()
	nr.noteFetched()
	if err := s.cm.clearJournal(); err != nil {
		return err
	}
	if prev != nil {
		s.stopPusherLocked(prev)
	}
	s.rem.Store(nr)
	s.enterPartial(ctx, nr)
	s.startPusher(nr)
	ok = true
	return nil
}

// pristineLocked reports that nothing has ever been made in the space: no
// page or folder in the index, nothing of the working tree, and no commit
// that touched anything outside .petalbook.
func (s *Space) pristineLocked(ctx context.Context, repo *gitstore.Repo) (bool, error) {
	tree, err := s.idx.Tree()
	if err != nil {
		return false, err
	}
	probs, err := s.idx.Problems()
	if err != nil {
		return false, err
	}
	if len(tree) > 1 || (len(tree) == 1 && (tree[0].Path != "" || tree[0].Pages > 0)) || len(probs) > 0 {
		return false, nil
	}
	touched, err := repo.TouchesOutside(ctx, ".petalbook")
	if err != nil {
		return false, err
	}
	return !touched, nil
}

// ensureConfigLocked makes sure the space has its .petalbook/space.yml after
// taking a remote's content: when the remote has none, it is made, committed
// and pushed as part of connecting (never left untracked).
func (s *Space) ensureConfigLocked(ctx context.Context, r *remote) error {
	if _, blocked, err := lstatIn(s.root, spaceFile); err == nil || blocked != "" {
		return nil // there (or behind what is put right with the next change)
	}
	if _, _, err := ensureSpaceConfig(s.root, s.name); err != nil {
		return err
	}
	author, committer := s.cm.git.identities(Author{})
	made, err := r.repo.CommitPaths(ctx, []string{".petalbook/space.yml"}, commitMessage, author, committer)
	if err != nil || !made {
		return err
	}
	if err := r.repo.Push(ctx, r.branch); err != nil {
		return err
	}
	return nil
}

// Disconnect makes the space local again, with everything it has. Nothing is
// changed on the remote.
func (s *Space) Disconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The changes waiting for their push go out (or are taken back) first,
	// through this remote: nothing of them is pushed or undone afterwards.
	r, err := s.drainRemoteLocked()
	if r == nil {
		return err
	}
	defer s.resumeLocked(r) // kept when the remote cannot be removed
	if err := r.repo.RemoveRemote(s.ctx); err != nil {
		return err
	}
	s.stopPusherLocked(r)
	s.rem.Store(nil)
	s.cm.resumeMaintenance()
	// Nothing of the server's own is in the work tree to put in order: its
	// state is in the Git directory, in local mode as in this one.
	return nil
}
