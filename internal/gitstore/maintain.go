package gitstore

import "context"

// Maintain packs the loose objects that commits leave behind. Commits are
// built with plumbing (commitChanges), which never makes Git pack anything, so
// without this a clone only grows in files: about 90 KB of loose objects per
// commit, and about 250 KB in a folder of 10 000 files.
//
// It runs three commands:
//
//	repack -d -q --geometric=2
//	                   packs the loose objects that a ref (or the index) reaches
//	                   into a new pack, and merges small packs so that each is at
//	                   least twice the size of the next (a few packs, never one per
//	                   run). Every object of a merged pack is kept; no -a, so
//	                   nothing unreachable is dropped.
//	prune-packed -q    deletes the loose copies of objects a pack now holds.
//	commit-graph write --reachable
//	                   a cache that makes walking the history cheaper.
//
// It never removes an object that is not in a pack. Still, the caller must not
// run it while a commit is being built or while Git fetches into the same
// repository: it is meant to run between two of those, under the lock they
// share.
func (r *Repo) Maintain(ctx context.Context) error {
	for _, args := range maintainArgs() {
		if _, err := r.git(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}
