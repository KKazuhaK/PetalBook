package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// importEarlierDrafts takes into st the drafts of the state database at
// file, which an earlier version left in .petalbook/cache when the state
// directory was in use already (space.EarlierStatePattern). A draft is
// imported when the current database has none for that page and person; one
// that is the same as the current draft is there already. Either is then
// removed from the file. One that differs from the current draft is not
// imported over it: it stays in the file, held so that petalbook draft list
// shows it, with a warning that says how to read it, so neither text is
// lost. When every draft is in st, the file is renamed to
// state-imported-<UTC>.db, so it is not looked at again, and kept.
func importEarlierDrafts(ctx context.Context, st *store.Store, file string, idle, max time.Duration, log *slog.Logger) {
	old, err := store.Open(ctx, file)
	if err != nil {
		log.Warn("the state database an earlier version left could not be read; it is kept", "file", file, "error", err)
		return
	}
	ds, _, err := store.SpaceDrafts(ctx, old.Q(), spaces.SingleID)
	if err != nil {
		old.Close()
		log.Warn("the drafts of the state database an earlier version left could not be read; it is kept", "file", file, "error", err)
		return
	}
	imported, same, kept := 0, 0, 0
	for _, d := range ds {
		cur, err := store.GetDraft(ctx, st.Q(), d.SpaceID, d.PageID, d.UserID)
		switch {
		case err == nil && cur.BaseRev == d.BaseRev && cur.Meta == d.Meta && cur.Body == d.Body:
			same++
			store.DeleteDraft(ctx, old.Q(), d.SpaceID, d.PageID, d.UserID)
		case err == nil:
			// Held in the file, so that petalbook draft list shows it.
			kept++
			store.HoldDraft(ctx, old.Q(), d.SpaceID, d.PageID, d.UserID, d.Version)
		case errors.Is(err, store.ErrNotFound):
			nd, err := store.PutDraft(ctx, st.Q(), d, "", idle, max)
			if err == nil && d.Held {
				err = store.HoldDraft(ctx, st.Q(), nd.SpaceID, nd.PageID, nd.UserID, nd.Version)
			}
			if err != nil {
				kept++
				log.Warn("a draft an earlier version left could not be imported", "page", d.PageID, "error", err)
			} else {
				// It is in st now: the file no longer holds it, so it is
				// never imported again after it was applied.
				imported++
				store.DeleteDraft(ctx, old.Q(), d.SpaceID, d.PageID, d.UserID)
			}
		default:
			kept++
			log.Warn("a draft an earlier version left could not be compared with the current one", "page", d.PageID, "error", err)
		}
	}
	old.Close()
	if imported > 0 {
		log.Info("imported the drafts an earlier version left", "file", file, "drafts", imported)
	}
	if kept > 0 {
		log.Warn("drafts an earlier version left differ from the current ones and are kept apart; read them with: petalbook draft list --space <space> --database <file>",
			"file", file, "drafts", kept)
		return
	}
	done := filepath.Join(filepath.Dir(file), strings.Replace(filepath.Base(file), "state-earlier-", "state-imported-", 1))
	for _, ext := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(file + ext); err == nil {
			os.Rename(file+ext, done+ext)
		}
	}
	if imported == 0 && same > 0 {
		log.Info("the drafts an earlier version left were all there already", "file", file, "drafts", same)
	}
}
