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

// CountGitHubSpaces is how many spaces have a github credential.
func CountGitHubSpaces(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM remotes WHERE url <> '' AND cred_type = 'github'`).Scan(&n)
	return
}

// CountUserGitHubSpaces is how many spaces sync through a user's authorisation.
func CountUserGitHubSpaces(ctx context.Context, q Q, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM remotes WHERE url <> '' AND cred_type = 'github' AND gh_via = 'user' AND gh_user_id = ?`, userID).Scan(&n)
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
	rows, err := q.Query(ctx, `SELECT gh_installation_id, COUNT(*) FROM remotes
		WHERE url <> '' AND cred_type = 'github' AND gh_via = 'app' AND gh_installation_id IN (`+strings.Join(marks, ",")+`) GROUP BY gh_installation_id`, args...)
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
// account, to list it without asking GitHub. Who it is shared with is in
// github_installation_tenants.
type GitHubInstallation struct {
	ID          int64
	Account     string
	AccountType string
	Updated     time.Time
}

const ghInstCols = `id, account, account_type, updated`

func scanGitHubInstallation(row interface{ Scan(...any) error }) (*GitHubInstallation, error) {
	var i GitHubInstallation
	var up int64
	if err := row.Scan(&i.ID, &i.Account, &i.AccountType, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	i.Updated = fromMS(up)
	return &i, nil
}

// GetGitHubInstallation returns what is stored of an installation, or ErrNotFound.
func GetGitHubInstallation(ctx context.Context, q Q, id int64) (*GitHubInstallation, error) {
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM github_installations WHERE id = ?`, id))
}

// PutGitHubInstallation stores an installation. Its sharing is left as it is.
func PutGitHubInstallation(ctx context.Context, q Q, i GitHubInstallation) error {
	_, err := q.Exec(ctx, `INSERT INTO github_installations(`+ghInstCols+`) VALUES (?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET account = excluded.account, account_type = excluded.account_type, updated = excluded.updated`,
		i.ID, i.Account, i.AccountType, ms(i.Updated))
	return err
}

// SetGitHubInstallationTenants replaces the set of tenants an installation is
// shared with (an empty set stops sharing). Callers run it in a transaction.
func SetGitHubInstallationTenants(ctx context.Context, q Q, id int64, tenants []string) error {
	if _, err := q.Exec(ctx, `DELETE FROM github_installation_tenants WHERE installation_id = ?`, id); err != nil {
		return err
	}
	for _, t := range tenants {
		if _, err := q.Exec(ctx, `INSERT INTO github_installation_tenants(installation_id, tenant_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, t); err != nil {
			return err
		}
	}
	return nil
}

// GitHubInstallationTenants says which tenants each of the installations is
// shared with (sorted); an installation shared with none is not in the map.
func GitHubInstallationTenants(ctx context.Context, q Q, ids []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	rows, err := q.Query(ctx, `SELECT installation_id, tenant_id FROM github_installation_tenants
		WHERE installation_id IN (`+strings.Join(marks, ",")+`) ORDER BY installation_id, tenant_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// sharedWith is the condition that installation column col is shared with a
// tenant in the scope.
func sharedWith(sc Scope, col string) (string, []any) {
	c, a := sc.cond("tenant_id")
	return col + ` IN (SELECT installation_id FROM github_installation_tenants WHERE ` + c + `)`, a
}

// GitHubInstallationShared says whether an installation is shared with the
// tenant of the scope.
func GitHubInstallationShared(ctx context.Context, q Q, sc Scope, id int64) (bool, error) {
	c, a := sharedWith(sc, "id")
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM github_installations WHERE id = ? AND `+c, append([]any{id}, a...)...).Scan(&n)
	return n > 0, err
}

// ListGitHubInstallationsOf lists the installations shared with the tenant of
// the scope, with an id above after, in id order, at most limit.
func ListGitHubInstallationsOf(ctx context.Context, q Q, sc Scope, after int64, limit int) ([]GitHubInstallation, error) {
	c, a := sharedWith(sc, "id")
	rows, err := q.Query(ctx, `SELECT `+ghInstCols+` FROM github_installations WHERE id > ? AND `+c+` ORDER BY id LIMIT ?`,
		append(append([]any{after}, a...), limit)...)
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

// GitHubInstallationOf returns the installation on an account (by login, case
// ignored) shared with the tenant of the scope, or ErrNotFound.
func GitHubInstallationOf(ctx context.Context, q Q, sc Scope, login string) (*GitHubInstallation, error) {
	c, a := sharedWith(sc, "id")
	return scanGitHubInstallation(q.QueryRow(ctx, `SELECT `+ghInstCols+` FROM github_installations
		WHERE lower(account) = ? AND `+c+` ORDER BY id LIMIT 1`, append([]any{strings.ToLower(login)}, a...)...))
}

// MaxGitHubLeftovers is how many reminders of empty repositories a user keeps:
// the newest.
const MaxGitHubLeftovers = 20

// GitHubLeftover is a reminder that a failed attempt to make a space with a
// new repository left an empty one on GitHub.
type GitHubLeftover struct {
	UserID    string
	RepoID    int64
	FullName  string
	HTMLURL   string
	SpaceName string // the name the space was to have
	Created   time.Time
}

// PutGitHubLeftover stores a reminder for a user, replacing the one for the
// same repository, and keeps only the user's newest MaxGitHubLeftovers.
func PutGitHubLeftover(ctx context.Context, q Q, l GitHubLeftover) error {
	if _, err := q.Exec(ctx, `INSERT INTO github_leftovers(user_id, repo_id, full_name, html_url, space_name, created) VALUES (?,?,?,?,?,?)
		ON CONFLICT (user_id, repo_id) DO UPDATE SET full_name = excluded.full_name, html_url = excluded.html_url,
		space_name = excluded.space_name, created = excluded.created`,
		l.UserID, l.RepoID, l.FullName, l.HTMLURL, l.SpaceName, ms(l.Created)); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM github_leftovers WHERE user_id = ? AND repo_id NOT IN
		(SELECT repo_id FROM github_leftovers WHERE user_id = ? ORDER BY created DESC, repo_id DESC LIMIT ?)`, l.UserID, l.UserID, MaxGitHubLeftovers)
	return err
}

// ListGitHubLeftovers returns a user's reminders, newest first.
func ListGitHubLeftovers(ctx context.Context, q Q, userID string) ([]GitHubLeftover, error) {
	rows, err := q.Query(ctx, `SELECT repo_id, full_name, html_url, space_name, created FROM github_leftovers
		WHERE user_id = ? ORDER BY created DESC, repo_id DESC LIMIT ?`, userID, MaxGitHubLeftovers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubLeftover
	for rows.Next() {
		l := GitHubLeftover{UserID: userID}
		var c int64
		if err := rows.Scan(&l.RepoID, &l.FullName, &l.HTMLURL, &l.SpaceName, &c); err != nil {
			return nil, err
		}
		l.Created = fromMS(c)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteGitHubLeftover removes a user's reminder of a repository and reports
// whether there was one.
func DeleteGitHubLeftover(ctx context.Context, q Q, userID string, repoID int64) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM github_leftovers WHERE user_id = ? AND repo_id = ?`, userID, repoID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UserTenant returns the tenant of a user, or ErrNotFound.
func UserTenant(ctx context.Context, q Q, userID string) (string, error) {
	var t string
	err := q.QueryRow(ctx, `SELECT tenant_id FROM users WHERE id = ?`, userID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}

// DeleteGitHubInstallations forgets every installation (the App was deleted).
func DeleteGitHubInstallations(ctx context.Context, q Q) error {
	_, err := q.Exec(ctx, `DELETE FROM github_installations`)
	return err
}

// MemberStanding is every role through which a user reaches a space (their
// direct role, if any, then the roles of their groups that are members of it)
// and whether they are disabled; ErrNotFound when they reach it by neither
// (or are not a user). Their permission is the union of the roles.
func MemberStanding(ctx context.Context, q Q, spaceID, userID string) (roles []string, disabled bool, err error) {
	rows, err := q.Query(ctx, `SELECT x.role, u.disabled FROM users u JOIN (
			SELECT role, 0 AS ord FROM space_members WHERE space_id = ? AND user_id = ?
			UNION ALL
			SELECT sg.role, 1 AS ord FROM space_groups sg JOIN group_members gm ON gm.group_id = sg.group_id
				WHERE sg.space_id = ? AND gm.user_id = ?
		) x ON 1 = 1
		WHERE u.id = ? ORDER BY x.ord, x.role`, spaceID, userID, spaceID, userID, userID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var d int64
		if err := rows.Scan(&role, &d); err != nil {
			return nil, false, err
		}
		roles = append(roles, role)
		disabled = d != 0
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(roles) == 0 {
		return nil, false, ErrNotFound
	}
	return roles, disabled, nil
}
