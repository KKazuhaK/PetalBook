package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Credential type of a remote that syncs through the server's GitHub App.
const CredGitHub = "github"

// States of a user's GitHub authorisation.
const (
	GitHubOK      = "ok"
	GitHubExpired = "expired"
)

// GitHubAuthorization is a user's connection to their GitHub account. The
// tokens are still sealed here.
type GitHubAuthorization struct {
	UserID         string
	GitHubID       int64
	Login          string
	AccessEnc      string
	RefreshEnc     string
	AccessExpires  time.Time // zero: the token does not expire
	RefreshExpires time.Time // zero: there is no refresh token, or it does not expire
	State          string
	Version        int64
	Connected      time.Time
	Updated        time.Time
}

const ghAuthCols = `user_id, github_id, login, access_enc, refresh_enc, access_expires, refresh_expires, state, version, connected, updated`

func scanGitHubAuthorization(row interface{ Scan(...any) error }) (*GitHubAuthorization, error) {
	var a GitHubAuthorization
	var ae, re, c, u int64
	if err := row.Scan(&a.UserID, &a.GitHubID, &a.Login, &a.AccessEnc, &a.RefreshEnc, &ae, &re, &a.State, &a.Version, &c, &u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if ae > 0 {
		a.AccessExpires = fromMS(ae)
	}
	if re > 0 {
		a.RefreshExpires = fromMS(re)
	}
	a.Connected, a.Updated = fromMS(c), fromMS(u)
	return &a, nil
}

func syncOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

// GetGitHubAuthorization returns a user's connection, or ErrNotFound.
func GetGitHubAuthorization(ctx context.Context, q Q, userID string) (*GitHubAuthorization, error) {
	return scanGitHubAuthorization(q.QueryRow(ctx, `SELECT `+ghAuthCols+` FROM github_authorizations WHERE user_id = ?`, userID))
}

// GitHubAccountUser returns the user who has connected a GitHub account, or ErrNotFound.
func GitHubAccountUser(ctx context.Context, q Q, githubID int64) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT user_id FROM github_authorizations WHERE github_id = ?`, githubID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// PutGitHubAuthorization stores a connection, replacing the user's earlier one
// (the version goes up by one). ErrExists when another user has the GitHub account.
func PutGitHubAuthorization(ctx context.Context, q Q, a GitHubAuthorization) error {
	if other, err := GitHubAccountUser(ctx, q, a.GitHubID); err == nil && other != a.UserID {
		return ErrExists
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if testHookBeforeGitHubPut != nil {
		testHookBeforeGitHubPut()
	}
	_, err := q.Exec(ctx, `INSERT INTO github_authorizations(`+ghAuthCols+`) VALUES (?,?,?,?,?,?,?,?,1,?,?)
		ON CONFLICT (user_id) DO UPDATE SET github_id = excluded.github_id, login = excluded.login, access_enc = excluded.access_enc,
		refresh_enc = excluded.refresh_enc, access_expires = excluded.access_expires, refresh_expires = excluded.refresh_expires,
		state = excluded.state, version = github_authorizations.version + 1, connected = excluded.connected, updated = excluded.updated`,
		a.UserID, a.GitHubID, a.Login, a.AccessEnc, a.RefreshEnc, syncOrZero(a.AccessExpires), syncOrZero(a.RefreshExpires), a.State, ms(a.Connected), ms(a.Updated))
	if on, ok := UniqueOn(err); ok && strings.Contains(on, "github_id") {
		return ErrExists // another user connected the account since the check
	}
	return err
}

// testHookBeforeGitHubPut is called between PutGitHubAuthorization's check and
// its write (tests).
var testHookBeforeGitHubPut func()

// ReplaceGitHubTokens stores a renewed pair, but only if the connection is
// still at version from (nothing else replaced the pair meanwhile). It reports
// whether it did; the version goes up by one.
func ReplaceGitHubTokens(ctx context.Context, q Q, userID string, from int64, accessEnc, refreshEnc string, accessExpires, refreshExpires, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE github_authorizations SET access_enc = ?, refresh_enc = ?, access_expires = ?, refresh_expires = ?,
		state = 'ok', version = version + 1, updated = ? WHERE user_id = ? AND version = ?`,
		accessEnc, refreshEnc, syncOrZero(accessExpires), syncOrZero(refreshExpires), ms(now), userID, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireGitHubAuthorization marks a connection expired and drops its tokens,
// only if it is still at version from.
func ExpireGitHubAuthorization(ctx context.Context, q Q, userID string, from int64, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE github_authorizations SET state = 'expired', access_enc = '', refresh_enc = '',
		access_expires = 0, refresh_expires = 0, version = version + 1, updated = ? WHERE user_id = ? AND version = ?`, ms(now), userID, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteGitHubAuthorization removes a user's connection.
func DeleteGitHubAuthorization(ctx context.Context, q Q, userID string) error {
	_, err := q.Exec(ctx, `DELETE FROM github_authorizations WHERE user_id = ?`, userID)
	return err
}

// DeleteGitHubAuthorizations removes every connection (the App was deleted).
func DeleteGitHubAuthorizations(ctx context.Context, q Q) error {
	_, err := q.Exec(ctx, `DELETE FROM github_authorizations`)
	return err
}

// ListGitHubAuthorizations returns every connection, for revoking them.
func ListGitHubAuthorizations(ctx context.Context, q Q) ([]GitHubAuthorization, error) {
	rows, err := q.Query(ctx, `SELECT `+ghAuthCols+` FROM github_authorizations ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubAuthorization
	for rows.Next() {
		a, err := scanGitHubAuthorization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// DueCursor is where a listing of due renewals goes on from: after the row
// with this refresh end and user id. The zero value starts at the beginning.
type DueCursor struct {
	RefreshExpires time.Time
	UserID         string
}

// Next is the cursor after a row.
func (a *GitHubAuthorization) Next() DueCursor {
	return DueCursor{RefreshExpires: a.RefreshExpires, UserID: a.UserID}
}

// DueGitHubRenewals lists the connections in good standing whose refresh token
// ends before the given time, soonest first (then by user id), at most limit of
// them, after the cursor.
func DueGitHubRenewals(ctx context.Context, q Q, before time.Time, after DueCursor, limit int) ([]GitHubAuthorization, error) {
	ae := syncOrZero(after.RefreshExpires)
	rows, err := q.Query(ctx, `SELECT `+ghAuthCols+` FROM github_authorizations
		WHERE state = 'ok' AND refresh_enc <> '' AND refresh_expires > 0 AND refresh_expires < ?
		AND (refresh_expires > ? OR (refresh_expires = ? AND user_id > ?))
		ORDER BY refresh_expires, user_id LIMIT ?`, ms(before), ae, ae, after.UserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubAuthorization
	for rows.Next() {
		a, err := scanGitHubAuthorization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// CountGitHubAuthorizations is how many users are connected.
func CountGitHubAuthorizations(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM github_authorizations`).Scan(&n)
	return
}

// githubRemotes is every remote of a space that uses a github credential: the
// space's own repository and its attachment repository.
const githubRemotes = `(SELECT space_id, gh_via, gh_installation_id, gh_user_id FROM remotes WHERE url <> '' AND cred_type = 'github'
	UNION ALL SELECT space_id, gh_via, gh_installation_id, gh_user_id FROM asset_remotes WHERE url <> '' AND cred_type = 'github')`

// CountGitHubSpaces is how many spaces have a github credential, for their
// own repository or their attachment repository.
func CountGitHubSpaces(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM `+githubRemotes+` g`).Scan(&n)
	return
}

// CountUserGitHubSpaces is how many spaces sync through a user's authorisation.
func CountUserGitHubSpaces(ctx context.Context, q Q, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM `+githubRemotes+` g WHERE gh_via = 'user' AND gh_user_id = ?`, userID).Scan(&n)
	return
}

// GitHubSpaceCounts is how many spaces sync through each of the installations.
func GitHubSpaceCounts(ctx context.Context, q Q, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	rows, err := q.Query(ctx, `SELECT gh_installation_id, COUNT(DISTINCT space_id) FROM `+githubRemotes+` g
		WHERE gh_via = 'app' AND gh_installation_id IN (`+strings.Join(marks, ",")+`) GROUP BY gh_installation_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// GitHubInstallation is an installation the administrator has looked at: its
// account (to list it without asking GitHub) and whether it is shared.
type GitHubInstallation struct {
	ID          int64
	Account     string
	AccountType string
	Shared      bool
	Updated     time.Time
}

const ghInstCols = `id, account, account_type, shared, updated`

func scanGitHubInstallation(row interface{ Scan(...any) error }) (*GitHubInstallation, error) {
	var i GitHubInstallation
	var sh, up int64
	if err := row.Scan(&i.ID, &i.Account, &i.AccountType, &sh, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	i.Shared, i.Updated = sh != 0, fromMS(up)
	return &i, nil
}

// GetGitHubInstallation returns what is stored of an installation, or ErrNotFound.
func GetGitHubInstallation(ctx context.Context, q Q, id int64) (*GitHubInstallation, error) {
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM github_installations WHERE id = ?`, id))
}

// PutGitHubInstallation stores an installation with its shared mark.
func PutGitHubInstallation(ctx context.Context, q Q, i GitHubInstallation) error {
	sh := 0
	if i.Shared {
		sh = 1
	}
	_, err := q.Exec(ctx, `INSERT INTO github_installations(`+ghInstCols+`) VALUES (?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET account = excluded.account, account_type = excluded.account_type, shared = excluded.shared, updated = excluded.updated`,
		i.ID, i.Account, i.AccountType, sh, ms(i.Updated))
	return err
}

// SharedGitHubInstallationIDs says which of the installations are shared.
func SharedGitHubInstallationIDs(ctx context.Context, q Q, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	rows, err := q.Query(ctx, `SELECT id FROM github_installations WHERE shared = 1 AND id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ListSharedGitHubInstallations lists shared installations with an id above
// after, in id order, at most limit.
func ListSharedGitHubInstallations(ctx context.Context, q Q, after int64, limit int) ([]GitHubInstallation, error) {
	rows, err := q.Query(ctx, `SELECT `+ghInstCols+` FROM github_installations WHERE shared = 1 AND id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubInstallation
	for rows.Next() {
		i, err := scanGitHubInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// SharedGitHubInstallationOf returns the shared installation on an account (by
// login, case ignored), or ErrNotFound.
func SharedGitHubInstallationOf(ctx context.Context, q Q, login string) (*GitHubInstallation, error) {
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM github_installations
		WHERE shared = 1 AND lower(account) = ? ORDER BY id LIMIT 1`, strings.ToLower(login)))
}

// DeleteGitHubInstallations forgets every installation (the App was deleted).
func DeleteGitHubInstallations(ctx context.Context, q Q) error {
	_, err := q.Exec(ctx, `DELETE FROM github_installations`)
	return err
}

// MemberStanding is a user's role in a space and whether they are disabled;
// ErrNotFound when they are not a member (or not a user).
func MemberStanding(ctx context.Context, q Q, spaceID, userID string) (role string, disabled bool, err error) {
	var d int64
	err = q.QueryRow(ctx, `SELECT m.role, u.disabled FROM space_members m JOIN users u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.user_id = ?`, spaceID, userID).Scan(&role, &d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return role, d != 0, err
}
