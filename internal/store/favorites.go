package store

import (
	"context"
	"errors"
	"time"
)

// Kinds of favourite.
const (
	FavPage   = "page"
	FavFolder = "folder"
	FavView   = "view"
)

// MaxFavorites is the most favourites one person has in one space.
const MaxFavorites = 200

// ErrFavoritesFull is the refusal of a favourite beyond MaxFavorites.
var ErrFavoritesFull = errors.New("too many favourites")

// Favorite is one favourite of a person in a space. Target is the page id, the
// folder path, or `folder:<path>#<view id>` / `tag:<tag>#<view id>`.
type Favorite struct {
	Kind    string
	Target  string
	Created time.Time
}

// ListFavorites returns a person's favourites in a space in their order: one
// query on the index of the order, at most MaxFavorites rows.
func ListFavorites(ctx context.Context, q Q, userID, spaceID string) ([]Favorite, error) {
	rows, err := q.Query(ctx, `SELECT kind, target, created FROM favorites
		WHERE user_id = ? AND space_id = ? ORDER BY position, kind, target`, userID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Favorite
	for rows.Next() {
		var f Favorite
		var created int64
		if err := rows.Scan(&f.Kind, &f.Target, &created); err != nil {
			return nil, err
		}
		f.Created = fromMS(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddFavorite puts a favourite at the end of the list. One that is already
// there stays where it is (added reports false). Beyond MaxFavorites it is
// ErrFavoritesFull. Run it in a transaction (Store.Tx) with LockFavorites so
// that two requests cannot both pass the count.
func AddFavorite(ctx context.Context, q Q, userID, spaceID string, f Favorite, now time.Time) (added bool, err error) {
	var have int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM favorites WHERE user_id = ? AND space_id = ? AND kind = ? AND target = ?`,
		userID, spaceID, f.Kind, f.Target).Scan(&have); err != nil {
		return false, err
	}
	if have > 0 {
		return false, nil
	}
	var n int
	var last int64
	if err := q.QueryRow(ctx, `SELECT COUNT(*), COALESCE(MAX(position), 0) FROM favorites WHERE user_id = ? AND space_id = ?`,
		userID, spaceID).Scan(&n, &last); err != nil {
		return false, err
	}
	if n >= MaxFavorites {
		return false, ErrFavoritesFull
	}
	_, err = q.Exec(ctx, `INSERT INTO favorites(user_id, space_id, kind, target, position, created) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, spaceID, f.Kind, f.Target, last+1, ms(now))
	return err == nil, err
}

// LockFavorites makes the rest of the transaction the only one changing the
// favourites of this person in this space: the user's row is the lock (a
// no-op write; PostgreSQL holds the row until commit, SQLite's transactions
// are serialized already).
func LockFavorites(ctx context.Context, q Q, userID string) error {
	res, err := q.Exec(ctx, `UPDATE users SET id = id WHERE id = ?`, userID)
	return oneRow(res, err)
}

// RemoveFavorite takes a favourite off the list; it reports whether there was one.
func RemoveFavorite(ctx context.Context, q Q, userID, spaceID, kind, target string) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM favorites WHERE user_id = ? AND space_id = ? AND kind = ? AND target = ?`,
		userID, spaceID, kind, target)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// OrderFavorites puts the favourites in the order of items. Those of the list
// that items does not name keep their relative order after the ones it does;
// names it has that are not favourites are ignored.
func OrderFavorites(ctx context.Context, q Q, userID, spaceID string, items []Favorite) error {
	cur, err := ListFavorites(ctx, q, userID, spaceID)
	if err != nil {
		return err
	}
	type key struct{ kind, target string }
	have := make(map[key]bool, len(cur))
	for _, f := range cur {
		have[key{f.Kind, f.Target}] = true
	}
	var order []key
	seen := map[key]bool{}
	for _, f := range items {
		k := key{f.Kind, f.Target}
		if have[k] && !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
	}
	for _, f := range cur {
		if k := (key{f.Kind, f.Target}); !seen[k] {
			order = append(order, k)
		}
	}
	for i, k := range order {
		if _, err := q.Exec(ctx, `UPDATE favorites SET position = ? WHERE user_id = ? AND space_id = ? AND kind = ? AND target = ?`,
			int64(i+1), userID, spaceID, k.kind, k.target); err != nil {
			return err
		}
	}
	return nil
}

// MoveFolderFavorites makes the favourites of every person in a space follow a
// folder that was moved or renamed from `from` to `to`: those of the folder
// itself and of the folders below it, and the saved views of those folders
// (`folder:<path>#<id>`). A favourite already at the new place was made after
// the move (nothing was there before it) and is kept: the person's old one
// that would land on it is dropped instead. So running it again does nothing
// more. Cost: the favourites of the space under the folder, found by the
// index of the space.
func MoveFolderFavorites(ctx context.Context, q Q, spaceID, from, to string) error {
	rows, err := q.Query(ctx, `SELECT user_id, kind, target FROM favorites WHERE space_id = ? AND kind IN (?, ?)`, spaceID, FavFolder, FavView)
	if err != nil {
		return err
	}
	type row struct{ user, kind, target, to string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.user, &r.kind, &r.target); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	type fav struct{ user, kind, target string }
	have := make(map[fav]bool, len(all))
	for _, r := range all {
		have[fav{r.user, r.kind, r.target}] = true
	}
	var moves, gone []row
	for _, r := range all {
		path, rebuild := r.target, func(p string) string { return p }
		if r.kind == FavView {
			var id string
			var ok bool
			if path, id, ok = SplitViewTarget("folder:", r.target); !ok {
				continue
			}
			rebuild = func(p string) string { return "folder:" + p + "#" + id }
		}
		if !underFolder(path, from) {
			continue
		}
		r.to = rebuild(to + path[len(from):])
		if have[fav{r.user, r.kind, r.to}] {
			gone = append(gone, r)
		} else {
			moves = append(moves, r)
		}
	}
	// The person already has the favourite at the new place.
	for _, r := range gone {
		if _, err := q.Exec(ctx, `DELETE FROM favorites WHERE user_id = ? AND space_id = ? AND kind = ? AND target = ?`, r.user, spaceID, r.kind, r.target); err != nil {
			return err
		}
	}
	for _, r := range moves {
		if _, err := q.Exec(ctx, `UPDATE favorites SET target = ? WHERE user_id = ? AND space_id = ? AND kind = ? AND target = ?`, r.to, r.user, spaceID, r.kind, r.target); err != nil {
			return err
		}
	}
	return nil
}

// underFolder reports whether path is the folder at or a folder below it.
func underFolder(path, at string) bool {
	return path == at || (len(path) > len(at) && path[:len(at)] == at && path[len(at)] == '/')
}

// SplitViewTarget cuts `<prefix><path or tag>#<view id>` into the scope and
// the view id. The id is after the last `#`.
func SplitViewTarget(prefix, target string) (scope, id string, ok bool) {
	if len(target) <= len(prefix) || target[:len(prefix)] != prefix {
		return "", "", false
	}
	rest := target[len(prefix):]
	for i := len(rest) - 1; i >= 0; i-- {
		if rest[i] == '#' {
			return rest[:i], rest[i+1:], i+1 < len(rest)
		}
	}
	return "", "", false
}

// FolderMove is a folder move whose favourites may not follow yet.
type FolderMove struct {
	From, To string
	// Created is when it was recorded.
	Created time.Time
}

// RecordFolderMove notes, before a folder is moved, that the favourites of it
// have to follow (see MoveFolderFavorites); noting it again is no error.
func RecordFolderMove(ctx context.Context, q Q, spaceID, from, to string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO folder_moves(space_id, from_path, to_path, created) VALUES (?, ?, ?, ?)
		ON CONFLICT (space_id, from_path, to_path) DO NOTHING`, spaceID, from, to, ms(now))
	return err
}

// PendingFolderMoves are the moves of a space whose favourites may not follow
// yet, oldest first.
func PendingFolderMoves(ctx context.Context, q Q, spaceID string) ([]FolderMove, error) {
	rows, err := q.Query(ctx, `SELECT from_path, to_path, created FROM folder_moves WHERE space_id = ? ORDER BY created, from_path, to_path`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderMove
	for rows.Next() {
		var m FolderMove
		var at int64
		if err := rows.Scan(&m.From, &m.To, &at); err != nil {
			return nil, err
		}
		m.Created = fromMS(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DoneFolderMove forgets a recorded move.
func DoneFolderMove(ctx context.Context, q Q, spaceID, from, to string) error {
	_, err := q.Exec(ctx, `DELETE FROM folder_moves WHERE space_id = ? AND from_path = ? AND to_path = ?`, spaceID, from, to)
	return err
}
