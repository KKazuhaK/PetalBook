package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Move states of the attachments of a space (see AssetMove).
const (
	MoveCopying   = "copying"
	MoveSwitching = "switching"
	MoveDone      = "done"
	MoveFailed    = "failed"
)

// AssetMove is how far moving the attachments of a space into its attachment
// repository has come. State is "" when there was never a move.
type AssetMove struct {
	State            string
	Files, FilesDone int64
	Bytes, BytesDone int64
	Error            string
	Updated          time.Time
}

// Running reports a move that has not ended.
func (m AssetMove) Running() bool { return m.State == MoveCopying || m.State == MoveSwitching }

// AssetRemote is the attachment repository of a space: a remote with the
// credential of a Remote, whether an attachment has gone to it, and the move.
type AssetRemote struct {
	Remote
	Used bool
	Move AssetMove
}

const assetCols = remoteCols + `, used, move_state, move_files, move_files_done, move_bytes, move_bytes_done, move_error, move_updated`

func scanAssetRemote(row interface{ Scan(...any) error }) (*AssetRemote, error) {
	var r AssetRemote
	var sync, upd, used, mupd int64
	m := &r.Move
	if err := row.Scan(&r.SpaceID, &r.URL, &r.Branch, &r.CredType, &r.Username, &r.TokenEnc, &r.PrivKeyEnc, &r.PubKey, &r.LastGood,
		&r.State, &r.ErrorKind, &r.LastError, &r.RemoteTip, &sync, &upd, &r.GHVia, &r.GHInstallationID, &r.GHRepoID, &r.GHUserID,
		&used, &m.State, &m.Files, &m.FilesDone, &m.Bytes, &m.BytesDone, &m.Error, &mupd); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if sync > 0 {
		r.LastSync = fromMS(sync)
	}
	r.Updated = fromMS(upd)
	r.Used = used != 0
	if mupd > 0 {
		m.Updated = fromMS(mupd)
	}
	return &r, nil
}

// GetAssetRemote returns the attachment repository of a space, or ErrNotFound.
func GetAssetRemote(ctx context.Context, q Q, spaceID string) (*AssetRemote, error) {
	return scanAssetRemote(q.QueryRow(ctx, `SELECT `+assetCols+` FROM asset_remotes WHERE space_id = ?`, spaceID))
}

// PutAssetRemote inserts the attachment repository of a space, or replaces its
// address and credential; whether anything went to it and the move are kept.
func PutAssetRemote(ctx context.Context, q Q, r Remote) error {
	var sync int64
	if !r.LastSync.IsZero() {
		sync = ms(r.LastSync)
	}
	_, err := q.Exec(ctx, `INSERT INTO asset_remotes(`+remoteCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (space_id) DO UPDATE SET url = excluded.url, branch = excluded.branch, cred_type = excluded.cred_type,
		username = excluded.username, token_enc = excluded.token_enc, privkey_enc = excluded.privkey_enc, pubkey = excluded.pubkey,
		last_good = excluded.last_good, state = excluded.state, error_kind = excluded.error_kind, last_error = excluded.last_error,
		remote_tip = excluded.remote_tip, last_sync = excluded.last_sync, updated = excluded.updated,
		gh_via = excluded.gh_via, gh_installation_id = excluded.gh_installation_id, gh_repo_id = excluded.gh_repo_id, gh_user_id = excluded.gh_user_id`,
		r.SpaceID, r.URL, r.Branch, r.CredType, r.Username, r.TokenEnc, r.PrivKeyEnc, r.PubKey, r.LastGood,
		r.State, r.ErrorKind, r.LastError, r.RemoteTip, sync, ms(r.Updated), r.GHVia, r.GHInstallationID, r.GHRepoID, r.GHUserID)
	return err
}

// UpdateAssetRemoteStatus records what the server last saw of the repository.
func UpdateAssetRemoteStatus(ctx context.Context, q Q, spaceID string, s RemoteStatus, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE asset_remotes SET state = ?, error_kind = ?, last_error = ?, remote_tip = ?,
		last_good = CASE WHEN CAST(? AS TEXT) = '' THEN last_good ELSE CAST(? AS TEXT) END,
		last_sync = CASE WHEN CAST(? AS BIGINT) = 0 THEN last_sync ELSE CAST(? AS BIGINT) END, updated = ?
		WHERE space_id = ?`,
		s.State, s.ErrorKind, s.LastError, s.RemoteTip, s.LastGood, s.LastGood, syncMS(s.LastSync), syncMS(s.LastSync), ms(now), spaceID)
	return err
}

// MarkAssetsUsed records that an attachment has gone to the repository: it
// cannot be removed from the space any more.
func MarkAssetsUsed(ctx context.Context, q Q, spaceID string) error {
	_, err := q.Exec(ctx, `UPDATE asset_remotes SET used = 1 WHERE space_id = ? AND used = 0`, spaceID)
	return err
}

// DeleteAssetRemote forgets the attachment repository of a space.
func DeleteAssetRemote(ctx context.Context, q Q, spaceID string) error {
	_, err := q.Exec(ctx, `DELETE FROM asset_remotes WHERE space_id = ?`, spaceID)
	return err
}

// SetAssetMove stores the progress of a move.
func SetAssetMove(ctx context.Context, q Q, spaceID string, m AssetMove, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE asset_remotes SET move_state = ?, move_files = ?, move_files_done = ?,
		move_bytes = ?, move_bytes_done = ?, move_error = ?, move_updated = ? WHERE space_id = ?`,
		m.State, m.Files, m.FilesDone, m.Bytes, m.BytesDone, m.Error, ms(now), spaceID)
	return err
}

// MovesRunning lists the spaces whose move was under way (the server stopped
// before it ended).
func MovesRunning(ctx context.Context, q Q) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT space_id FROM asset_remotes WHERE move_state IN (?, ?) ORDER BY space_id`, MoveCopying, MoveSwitching)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AssetRepositoryStates maps the id of every space that has an attachment
// repository to whether its files are in it (the move is done or an
// attachment has gone to it) : used for the "assets" field of the lists.
func AssetRepositories(ctx context.Context, q Q) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT space_id, CASE WHEN used <> 0 OR move_state = 'done' THEN 1 ELSE 0 END FROM asset_remotes WHERE url <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n != 0
	}
	return out, rows.Err()
}
