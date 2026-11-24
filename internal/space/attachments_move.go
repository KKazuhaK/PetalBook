package space

// Moving the attachments of a space into its attachment repository (docs/api.md
// "POST …/remote/assets/move"). The space supplies what only it can: the list
// of the files of assets/ at the current commit, and the commit that takes
// assets/ out of the space's repository, made with the space's writes held and
// only after the caller has checked that the attachment repository has every
// file.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// SwitchMessage is the message of the commit that takes assets/ out of the
// space's repository.
const SwitchMessage = "Move the attachments to their own repository"

// SpaceAsset is a file of assets/ at the current commit.
type SpaceAsset struct {
	Rel  string // below assets/
	ID   string // the content id
	Path string // where the file is in the work tree
	Size int64
}

// AssetsAtHead lists the files of assets/ the space's repository has at its
// current commit and the work tree has. A file the commit has and the work
// tree lacks is an error: it could not be copied.
func (s *Space) AssetsAtHead(ctx context.Context) ([]SpaceAsset, error) {
	r := s.rem.Load()
	if r == nil {
		return nil, ErrNoRemote
	}
	return s.assetsAtHead(ctx, r)
}

func (s *Space) assetsAtHead(ctx context.Context, r *remote) ([]SpaceAsset, error) {
	head := r.repo.Head(ctx)
	if head == "" {
		return nil, nil
	}
	es, err := r.repo.ListTreeUnder(ctx, head, AssetsDir)
	if err != nil {
		return nil, err
	}
	out := make([]SpaceAsset, 0, len(es))
	for _, e := range es {
		rel, ok := strings.CutPrefix(e.Path, AssetsDir+"/")
		if !ok || e.Type != "blob" || e.Mode == "120000" {
			continue
		}
		p := filepath.Join(s.root, filepath.FromSlash(e.Path))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is in the repository but not on disk as a file; the move cannot copy it", e.Path)
		}
		out = append(out, SpaceAsset{Rel: rel, ID: e.ID, Path: p, Size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// SwitchAssets takes assets/ out of the space's repository, with the writes of
// the space held. ensure is given the files of assets/ at the current commit
// and must make sure the attachment repository has every one of them, as seen
// on the remote (it copies what is missing and checks); only when it returns
// nil is the commit made. hook, when set, is called at each step and an error
// stops there (tests make the server "crash" this way).
//
// The commit is made without a work tree, the work tree is put in line with it
// (the attachments are removed from disk), and the commit is pushed. When the
// push fails the commit is taken back and the files are as they were. When the
// server stops between the two, the commit is pushed by the next start, like
// any commit that was never pushed.
func (s *Space) SwitchAssets(ctx context.Context, ensure func(context.Context, []SpaceAsset) error, hook func(step string) error) error {
	step := func(n string) error {
		if hook == nil {
			return nil
		}
		return hook(n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	defer s.forgetAssetsDir()
	r := s.rem.Load()
	if r == nil {
		return ErrNoRemote
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	if err := s.syncTipLocked(ctx, r); err != nil {
		return err
	}
	files, err := s.assetsAtHead(ctx, r)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		if err := ensure(ctx, files); err != nil {
			return err
		}
	}
	if err := step("verified"); err != nil {
		return err
	}
	parent := r.repo.Head(ctx)
	author, committer := s.cm.git.identities(Author{})
	commit, err := r.repo.CommitWithout(ctx, AssetsDir, "/"+AssetsDir+"/", SwitchMessage, author, committer)
	if err != nil {
		return err
	}
	if commit == "" {
		s.removeEmptyAssetDirs()
		return nil
	}
	undo := func() {
		uctx, ucancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer ucancel()
		if err := r.repo.ResetHard(uctx, parent); err != nil {
			s.log.Error("could not take back the commit that moves the attachments", "error", err)
			r.blocked = "a change could not be undone completely: " + gitstore.Scrub(err.Error())
			r.set(func(st *RemoteStatus) { st.State, st.Kind, st.Error = RemoteError, KindClone, r.blocked })
		}
	}
	if err := step("committed"); err != nil {
		return err
	}
	// The work tree follows the commit: the attachments leave the disk, and
	// .gitignore is the one of the commit.
	if err := r.repo.ResetHard(ctx, commit); err != nil {
		undo()
		return err
	}
	if err := step("reset"); err != nil {
		return err
	}
	if perr := r.repo.Push(ctx, r.branch); perr != nil {
		landed, lerr := s.landed(ctx, r)
		if lerr != nil || !landed {
			undo()
			if lerr != nil {
				return s.netFailure(r, lerr)
			}
			if gitstore.KindOf(perr) == gitstore.KindRejected {
				return unavailable(perr, "the remote changed while the attachments were being moved; try again")
			}
			return unavailable(perr, "%s", gitstore.Scrub(perr.Error()))
		}
	}
	s.pushedLocked(ctx, r)
	s.removeEmptyAssetDirs()
	return step("pushed")
}

// AssetsSwitched reports whether the space's HEAD is past a switch of its
// attachments: assets/ is out of its tree and /assets/ is in its .gitignore.
// A move whose switch was pushed although its answer was lost is done once the
// space has it.
func (s *Space) AssetsSwitched(ctx context.Context) (bool, error) {
	r := s.rem.Load()
	if r == nil {
		return false, nil
	}
	head := r.repo.Head(ctx)
	if head == "" {
		return false, nil
	}
	es, err := r.repo.ListTreeUnder(ctx, head, AssetsDir)
	if err != nil || len(es) > 0 {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(s.root, ".gitignore"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "/"+AssetsDir+"/" {
			return true, nil
		}
	}
	return false, nil
}

// removeEmptyAssetDirs removes what is left of assets/ when it holds no file.
// Files that are in it (untracked, now ignored) stay: nothing here deletes a
// file nobody committed.
func (s *Space) removeEmptyAssetDirs() {
	root := filepath.Join(s.root, AssetsDir)
	var dirs []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // fails, rightly, when something is in it
	}
}

// LocalAssets counts the files in the space's own assets/ folder (tracked or
// not, ignored or not), up to limit, without reading the history: what a
// removal of the clone would take with it that the attachment repository may
// not have.
func (s *Space) LocalAssets(limit int) int {
	n := 0
	filepath.WalkDir(filepath.Join(s.root, AssetsDir), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			n++
			if n >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return n
}

// RecoverSwitch finishes a switch the server was stopped in the middle of: the
// commit was made but the work tree was not put in line with it. It does
// nothing unless HEAD is exactly that commit (by its message and by assets/
// being out of its tree). The commit is then pushed by the next pull.
func (s *Space) RecoverSwitch(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.forgetAssetsDir()
	r := s.rem.Load()
	if r == nil || s.closed {
		return false, nil
	}
	head := r.repo.Head(ctx)
	if head == "" || r.repo.CommitSubject(ctx, head) != SwitchMessage {
		return false, nil
	}
	if es, err := r.repo.ListTreeUnder(ctx, head, AssetsDir); err != nil || len(es) > 0 {
		return false, err
	}
	if err := r.repo.ResetHard(ctx, head); err != nil {
		return false, err
	}
	s.removeEmptyAssetDirs()
	r.blocked = ""
	r.set(func(st *RemoteStatus) {
		if st.Kind == KindClone {
			st.State, st.Kind, st.Error = RemoteOK, "", ""
		}
	})
	return true, nil
}

var _ = errors.New
var _ = path.Join
