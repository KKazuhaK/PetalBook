package store

import (
	"context"
	"database/sql"
	"errors"
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

// Remote is the remote a space follows, with its credential. Secrets are
// still encrypted here.
type Remote struct {
	SpaceID    string
	URL        string // "" while the space only has a key pair
	Branch     string
	CredType   string // "ssh" or "token"
	Username   string
	TokenEnc   string
	PrivKeyEnc string
	PubKey     string
	LastGood   string
	State      string
	ErrorKind  string // what kind of attention the error needs: "", "auth", "hostkey", "clone", "rescued", or a github_ code
	LastError  string
	RemoteTip  string // when rewritten: the remote's commit
	LastSync   time.Time
	Updated    time.Time

	// The GitHub credential (CredType "github"): which token syncs the space
	// and what it syncs. No secret is stored with the row.
	GHVia            string // "app" or "user"
	GHInstallationID int64  // via "app"
	GHRepoID         int64
	GHUserID         string // via "user": the PetalBook user whose authorisation syncs it
}

// Connected reports whether the space follows a remote (it is not just a key).
func (r *Remote) Connected() bool { return r.URL != "" }

const remoteCols = `space_id, url, branch, cred_type, username, token_enc, privkey_enc, pubkey, last_good, state, error_kind, last_error, remote_tip, last_sync, updated, gh_via, gh_installation_id, gh_repo_id, gh_user_id`

func scanRemote(row interface{ Scan(...any) error }) (*Remote, error) {
	var r Remote
	var sync, upd int64
	if err := row.Scan(&r.SpaceID, &r.URL, &r.Branch, &r.CredType, &r.Username, &r.TokenEnc, &r.PrivKeyEnc, &r.PubKey, &r.LastGood,
		&r.State, &r.ErrorKind, &r.LastError, &r.RemoteTip, &sync, &upd, &r.GHVia, &r.GHInstallationID, &r.GHRepoID, &r.GHUserID); err != nil {
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

// GetRemote returns the remote row of a space, or ErrNotFound.
func GetRemote(ctx context.Context, q Q, spaceID string) (*Remote, error) {
	return scanRemote(q.QueryRow(ctx, `SELECT `+remoteCols+` FROM remotes WHERE space_id = ?`, spaceID))
}

// PutRemote inserts or replaces a remote row. When the space is connected with
// a github credential, the reminders of its repository as an empty leftover are
// cleared, in the same transaction.
func PutRemote(ctx context.Context, q Q, r Remote) error {
	return atomically(ctx, q, func(q Q) error {
		var sync int64
		if !r.LastSync.IsZero() {
			sync = ms(r.LastSync)
		}
		_, err := q.Exec(ctx, `INSERT INTO remotes(`+remoteCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT (space_id) DO UPDATE SET url = excluded.url, branch = excluded.branch, cred_type = excluded.cred_type,
			username = excluded.username, token_enc = excluded.token_enc, privkey_enc = excluded.privkey_enc, pubkey = excluded.pubkey,
			last_good = excluded.last_good, state = excluded.state, error_kind = excluded.error_kind, last_error = excluded.last_error,
			remote_tip = excluded.remote_tip, last_sync = excluded.last_sync, updated = excluded.updated,
			gh_via = excluded.gh_via, gh_installation_id = excluded.gh_installation_id, gh_repo_id = excluded.gh_repo_id, gh_user_id = excluded.gh_user_id`,
			r.SpaceID, r.URL, r.Branch, r.CredType, r.Username, r.TokenEnc, r.PrivKeyEnc, r.PubKey, r.LastGood,
			r.State, r.ErrorKind, r.LastError, r.RemoteTip, sync, ms(r.Updated), r.GHVia, r.GHInstallationID, r.GHRepoID, r.GHUserID)
		if err != nil || r.URL == "" || r.GHRepoID == 0 {
			return err
		}
		// A space that follows the repository answers every reminder of it.
		_, err = q.Exec(ctx, `DELETE FROM github_leftovers WHERE repo_id = ?`, r.GHRepoID)
		return err
	})
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

// UpdateRemoteStatus records the outcome of a sync. It does nothing for a
// space that has no row (it was disconnected meanwhile).
func UpdateRemoteStatus(ctx context.Context, q Q, spaceID string, s RemoteStatus, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE remotes SET state = ?, error_kind = ?, last_error = ?, remote_tip = ?,
		last_good = CASE WHEN CAST(? AS TEXT) = '' THEN last_good ELSE CAST(? AS TEXT) END,
		last_sync = CASE WHEN CAST(? AS BIGINT) = 0 THEN last_sync ELSE CAST(? AS BIGINT) END, updated = ?
		WHERE space_id = ? AND url <> ''`,
		s.State, s.ErrorKind, s.LastError, s.RemoteTip, s.LastGood, s.LastGood, syncMS(s.LastSync), syncMS(s.LastSync), ms(now), spaceID)
	return err
}

func syncMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

// DeleteRemote removes the row of a space: the remote, and the credential with it.
func DeleteRemote(ctx context.Context, q Q, spaceID string) error {
	_, err := q.Exec(ctx, `DELETE FROM remotes WHERE space_id = ?`, spaceID)
	return err
}

// RemoteStates maps the id of every space that follows a remote to its state.
func RemoteStates(ctx context.Context, q Q) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT space_id, state FROM remotes WHERE url <> ''`)
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
