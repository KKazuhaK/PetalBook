package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Remote states.
const (
	RemoteLocal     = "local" // no remote (a row that only holds an SSH key)
	RemoteOK        = "ok"
	RemoteOffline   = "offline"
	RemoteRewritten = "rewritten"
	RemoteError     = "error"
)

// The roles of a space's remotes: one row each, keyed (space_id, role).
const (
	// RoleContent is the repository the space follows.
	RoleContent = "content"
	// RoleAssets is the space's attachment repository (3.1).
	RoleAssets = "assets"
)

// What has gone to an attachment repository (Remote.Used).
const (
	// AssetsUnused: nothing yet; it may still be taken away.
	AssetsUnused = 0
	// AssetsUsed: an attachment has gone to it.
	AssetsUsed = 1
	// AssetsMoved: the space's own attachments were moved into it; the space's
	// repository ignores assets/ from then on.
	AssetsMoved = 2
)

// Remote is one remote of a space, with its credential. Secrets are still
// encrypted here.
type Remote struct {
	SpaceID    string
	Role       string // RoleContent ("" is taken for it) or RoleAssets
	URL        string // "" while the space only has a key pair
	Branch     string
	CredType   string // "ssh", "token" or "connection"
	Provider   string // the provider the remote goes through: a provider's id for a connection, "git" otherwise
	Username   string
	TokenEnc   string
	PrivKeyEnc string
	PubKey     string
	LastGood   string
	State      string
	ErrorKind  string // what kind of attention the error needs: "", "auth", "hostkey", "clone", "rescued", or a code of a connection (connection_expired and the like)
	LastError  string
	RemoteTip  string // when rewritten: the remote's commit
	LastSync   time.Time
	Updated    time.Time

	// A connection's credential (CredType "connection"): which of the
	// provider's credentials syncs the space and what it syncs. No secret is
	// stored with the row.
	Via            string // "app" or "user"
	InstallationID int64  // via "app"
	RepoID         int64  // the provider's id of the repository
	CredUserID     string // via "user": the PetalBook user whose link syncs it
	// TargetID is reserved: the storage target (3.6) that made the repository.
	TargetID string

	// Role "assets" only: what has gone to it (AssetsUnused, AssetsUsed,
	// AssetsMoved) and the last move of the space's attachments into it (a job's
	// id; the job may have been pruned). PutRemote keeps both while the address
	// stays the same.
	Used  int
	JobID string
}

// Connected reports whether the space follows a remote (it is not just a key).
func (r *Remote) Connected() bool { return r.URL != "" }

// HasAssets reports whether attachments are in the attachment repository: one
// went to it, or the space's were moved into it. It cannot be taken away then.
func (r *Remote) HasAssets() bool { return r.Used != AssetsUnused }

// Moved reports whether the space's own attachments were moved into it.
func (r *Remote) Moved() bool { return r.Used == AssetsMoved }

func roleOr(role string) string {
	if role == "" {
		return RoleContent
	}
	return role
}

const remoteCols = `space_id, role, url, branch, cred_type, username, token_enc, privkey_enc, pubkey, last_good, state, error_kind, last_error, remote_tip,
	last_sync, updated, provider, via, installation_id, repo_id, cred_user_id, target_id, used, job_id`

func scanRemote(row interface{ Scan(...any) error }) (*Remote, error) {
	var r Remote
	var sync, upd int64
	if err := row.Scan(&r.SpaceID, &r.Role, &r.URL, &r.Branch, &r.CredType, &r.Username, &r.TokenEnc, &r.PrivKeyEnc, &r.PubKey, &r.LastGood,
		&r.State, &r.ErrorKind, &r.LastError, &r.RemoteTip, &sync, &upd, &r.Provider, &r.Via, &r.InstallationID, &r.RepoID, &r.CredUserID,
		&r.TargetID, &r.Used, &r.JobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if sync > 0 {
		r.LastSync = fromMS(sync)
	}
	r.Updated = fromMS(upd)
	return &r, nil
}

// GetRemote returns the remote of a space in a role, or ErrNotFound: one read
// of the primary key.
func GetRemote(ctx context.Context, q Q, spaceID, role string) (*Remote, error) {
	return scanRemote(q.QueryRow(ctx, `SELECT `+remoteCols+` FROM remotes WHERE space_id = ? AND role = ?`, spaceID, roleOr(role)))
}

// PutRemote inserts or replaces the remote of a space in its role (r.Role, ""
// for RoleContent): its address, credential and state. Of an attachment
// repository, Used and JobID are kept while the address stays the same (another
// repository starts with what r says, unused). When the remote goes to a repository of
// a provider, the reminders of that repository as an empty leftover are
// cleared, in the same transaction.
func PutRemote(ctx context.Context, q Q, r Remote) error {
	r.Role = roleOr(r.Role)
	put := func(q Q) error {
		if r.Provider == "" {
			r.Provider = ProviderGit
			if r.CredType == CredConnection {
				r.Provider = ProviderGitHub
			}
		}
		var sync int64
		if !r.LastSync.IsZero() {
			sync = ms(r.LastSync)
		}
		_, err := q.Exec(ctx, `INSERT INTO remotes(`+remoteCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (space_id, role) DO UPDATE SET url = excluded.url, branch = excluded.branch, cred_type = excluded.cred_type,
			username = excluded.username, token_enc = excluded.token_enc, privkey_enc = excluded.privkey_enc, pubkey = excluded.pubkey,
			last_good = excluded.last_good, state = excluded.state, error_kind = excluded.error_kind, last_error = excluded.last_error,
			remote_tip = excluded.remote_tip, last_sync = excluded.last_sync, updated = excluded.updated, provider = excluded.provider,
			via = excluded.via, installation_id = excluded.installation_id, repo_id = excluded.repo_id, cred_user_id = excluded.cred_user_id,
			target_id = excluded.target_id,
			used = CASE WHEN remotes.url = excluded.url THEN remotes.used ELSE excluded.used END,
			job_id = CASE WHEN remotes.url = excluded.url THEN remotes.job_id ELSE excluded.job_id END`,
			r.SpaceID, r.Role, r.URL, r.Branch, r.CredType, r.Username, r.TokenEnc, r.PrivKeyEnc, r.PubKey, r.LastGood,
			r.State, r.ErrorKind, r.LastError, r.RemoteTip, sync, ms(r.Updated), r.Provider, r.Via, r.InstallationID, r.RepoID, r.CredUserID,
			r.TargetID, r.Used, r.JobID)
		if err != nil || r.URL == "" || r.RepoID == 0 {
			return err
		}
		// A space that follows the repository (in either role) answers every
		// reminder of it, at the provider it is on.
		prov := r.Provider
		if prov == "" {
			prov = ProviderGitHub
		}
		_, err = q.Exec(ctx, `DELETE FROM repo_leftovers WHERE provider_id = ? AND repo_id = ?`, prov, strconv.FormatInt(r.RepoID, 10))
		return err
	}
	if r.Role == RoleContent {
		// The space's own remote counts in its usage (whether it syncs).
		return withSpace(ctx, q, r.SpaceID, put)
	}
	return atomically(ctx, q, put)
}

// RemoteStatus is what changes while a space follows its remote.
type RemoteStatus struct {
	State     string
	ErrorKind string
	LastError string
	LastGood  string // "" leaves it as it is
	RemoteTip string
	LastSync  time.Time // zero leaves it as it is
}

// UpdateRemoteStatus records the outcome of a sync of a space's remote in a
// role. It does nothing for a space that has no row (it was disconnected
// meanwhile). The state of the space's own remote is copied to the space
// (filters read it there).
func UpdateRemoteStatus(ctx context.Context, q Q, spaceID, role string, s RemoteStatus, now time.Time) error {
	role = roleOr(role)
	const set = `UPDATE remotes SET state = ?, error_kind = ?, last_error = ?, remote_tip = ?,
			last_good = CASE WHEN CAST(? AS TEXT) = '' THEN last_good ELSE CAST(? AS TEXT) END,
			last_sync = CASE WHEN CAST(? AS BIGINT) = 0 THEN last_sync ELSE CAST(? AS BIGINT) END, updated = ?
			WHERE space_id = ? AND role = ?`
	args := []any{s.State, s.ErrorKind, s.LastError, s.RemoteTip, s.LastGood, s.LastGood, syncMS(s.LastSync), syncMS(s.LastSync), ms(now), spaceID, role}
	if role != RoleContent {
		_, err := q.Exec(ctx, set, args...)
		return err
	}
	return atomically(ctx, q, func(q Q) error {
		res, err := q.Exec(ctx, set+` AND url <> ''`, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = q.Exec(ctx, `UPDATE spaces SET remote_state = ? WHERE id = ? AND remote_state <> ?`, s.State, spaceID, s.State)
		return err
	})
}

func syncMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

// DeleteRemote removes the remote of a space in a role, and its credential
// with it.
func DeleteRemote(ctx context.Context, q Q, spaceID, role string) error {
	role = roleOr(role)
	del := func(q Q) error {
		_, err := q.Exec(ctx, `DELETE FROM remotes WHERE space_id = ? AND role = ?`, spaceID, role)
		return err
	}
	if role != RoleContent {
		return del(q)
	}
	err := withSpace(ctx, q, spaceID, del)
	if errors.Is(err, ErrNotFound) {
		return nil // the space is gone, and its remote with it
	}
	return err
}

// RemoteStates maps the id of every space that follows a remote to its state.
func RemoteStates(ctx context.Context, q Q) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT space_id, state FROM remotes WHERE role = 'content' AND url <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// MarkAssetsUsed records that an attachment has gone to the space's attachment
// repository: it cannot be removed from the space any more.
func MarkAssetsUsed(ctx context.Context, q Q, spaceID string) error {
	_, err := q.Exec(ctx, `UPDATE remotes SET used = ? WHERE space_id = ? AND role = 'assets' AND used = ?`, AssetsUsed, spaceID, AssetsUnused)
	return err
}

// MarkAssetsMoved records that the space's own attachments were moved into its
// attachment repository (the switch landed).
func MarkAssetsMoved(ctx context.Context, q Q, spaceID string) error {
	_, err := q.Exec(ctx, `UPDATE remotes SET used = ? WHERE space_id = ? AND role = 'assets' AND url <> ''`, AssetsMoved, spaceID)
	return err
}

// SetAssetsMoveJob names the job that moves the space's attachments into its
// attachment repository (ErrNotFound: it has none).
func SetAssetsMoveJob(ctx context.Context, q Q, spaceID, jobID string) error {
	res, err := q.Exec(ctx, `UPDATE remotes SET job_id = ? WHERE space_id = ? AND role = 'assets' AND url <> ''`, jobID, spaceID)
	return oneRow(res, err)
}

// AssetRepositories maps the id of every space that has an attachment
// repository to whether attachments are in it (Remote.HasAssets): used for the
// "assets" field of the lists.
func AssetRepositories(ctx context.Context, q Q) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT space_id, used FROM remotes WHERE role = 'assets' AND url <> ''`)
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
		out[id] = n != AssetsUnused
	}
	return out, rows.Err()
}
