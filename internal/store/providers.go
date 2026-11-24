package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The providers of milestone 3.8: an OAuth application on a GitLab or a Gitea
// host, a person's connection to an account there, and the reminders of empty
// repositories. GitHub keeps its own tables (github.go).

// ConnProviderSite is the owner of a provider the whole server shares; any other
// owner is a tenant's id.
const ConnProviderSite = "site"

// ConnProviderRow is a provider as stored. SecretEnc is still sealed here.
type ConnProviderRow struct {
	ID, Type, Host, Scheme, Name, Owner string
	ClientID, SecretEnc                 string
	State                               string
	PlatformVersion                     string
	PartialSync                         string
	PartialChecked, Checked             time.Time
	UpdatedBy                           string
	Created, Updated                    time.Time
}

const connProviderCols = `id, type, host, scheme, name, owner, client_id, secret_enc, state, platform_version, partial_sync, partial_checked, checked, updated_by, created, updated`

func scanConnProvider(row interface{ Scan(...any) error }) (*ConnProviderRow, error) {
	var p ConnProviderRow
	var pc, ck, cr, up int64
	if err := row.Scan(&p.ID, &p.Type, &p.Host, &p.Scheme, &p.Name, &p.Owner, &p.ClientID, &p.SecretEnc, &p.State, &p.PlatformVersion,
		&p.PartialSync, &pc, &ck, &p.UpdatedBy, &cr, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if pc > 0 {
		p.PartialChecked = fromMS(pc)
	}
	if ck > 0 {
		p.Checked = fromMS(ck)
	}
	p.Created, p.Updated = fromMS(cr), fromMS(up)
	return &p, nil
}

// ListConnProviderRows returns every provider, by id.
func ListConnProviderRows(ctx context.Context, q Q) ([]ConnProviderRow, error) {
	rows, err := q.Query(ctx, `SELECT `+connProviderCols+` FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnProviderRow
	for rows.Next() {
		p, err := scanConnProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetConnProviderRow returns one provider, or ErrNotFound.
func GetConnProviderRow(ctx context.Context, q Q, id string) (*ConnProviderRow, error) {
	return scanConnProvider(q.QueryRow(ctx, `SELECT `+connProviderCols+` FROM providers WHERE id = ?`, id))
}

// CountConnProviderRows is how many providers an owner ("site" or a tenant id) has.
func CountConnProviderRows(ctx context.Context, q Q, owner string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM providers WHERE owner = ?`, owner).Scan(&n)
	return
}

// InsertConnProviderRow adds a provider. ErrExists when the id, or the owner's
// host, is taken.
func InsertConnProviderRow(ctx context.Context, q Q, p ConnProviderRow) error {
	_, err := q.Exec(ctx, `INSERT INTO providers(`+connProviderCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Type, p.Host, p.Scheme, p.Name, p.Owner, p.ClientID, p.SecretEnc, p.State, p.PlatformVersion, p.PartialSync,
		syncOrZero(p.PartialChecked), syncOrZero(p.Checked), p.UpdatedBy, ms(p.Created), ms(p.Updated))
	if _, ok := UniqueOn(err); ok {
		return ErrExists
	}
	return err
}

// UpdateConnProviderRow replaces what an administrator can change of a provider
// (its id, type, host and owner are fixed) and what a check found.
func UpdateConnProviderRow(ctx context.Context, q Q, p ConnProviderRow) error {
	res, err := q.Exec(ctx, `UPDATE providers SET name = ?, client_id = ?, secret_enc = ?, state = ?, platform_version = ?, checked = ?, updated_by = ?, updated = ? WHERE id = ?`,
		p.Name, p.ClientID, p.SecretEnc, p.State, p.PlatformVersion, syncOrZero(p.Checked), p.UpdatedBy, ms(p.Updated), p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetConnProviderState records what a check found of a provider's host: the
// state, the version (never an empty one over a known one) and when. Only the
// application the check was made with is written to: the client id and the
// sealed secret it saw must still be the stored ones, or the verdict is
// about another application and is dropped (it reports whether it was written).
// Nothing else of the row, the secret least, is written from a copy.
func SetConnProviderState(ctx context.Context, q Q, id, clientID, secretEnc, state, version string, checked time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE providers SET state = ?, platform_version = CASE WHEN ? = '' THEN platform_version ELSE ? END, checked = ?
		WHERE id = ? AND client_id = ? AND secret_enc = ?`, state, version, version, syncOrZero(checked), id, clientID, secretEnc)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetConnProviderPartial records whether the provider's host can filter a fetch.
func SetConnProviderPartial(ctx context.Context, q Q, id, partial string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE providers SET partial_sync = ?, partial_checked = ? WHERE id = ?`, partial, ms(at), id)
	return err
}

// DeleteConnProviderRow removes a provider; its connections and reminders go with it.
func DeleteConnProviderRow(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM providers WHERE id = ?`, id)
	return err
}

// DeleteTenantConnProviderRows removes the providers of a tenant (it is being deleted).
func DeleteTenantConnProviderRows(ctx context.Context, q Q, tenantID string) error {
	_, err := q.Exec(ctx, `DELETE FROM providers WHERE owner = ?`, tenantID)
	return err
}

// --- people's connections ----------------------------------------------------------------

// ProviderAuthorization is a user's connection to an account on a provider's
// host. The tokens are still sealed here.
type ProviderAuthorization struct {
	Provider       string
	UserID         string
	AccountID      string
	Login          string
	AccessEnc      string
	RefreshEnc     string
	AccessExpires  time.Time // zero: the token does not expire
	RefreshExpires time.Time // zero: there is no refresh token, or it does not expire
	State          string
	Version        int64
	// Admin: the host said the account is an administrator of the host.
	Admin     bool
	Connected time.Time
	Updated   time.Time
}

const pauthCols = `provider, user_id, account_id, login, access_enc, refresh_enc, access_expires, refresh_expires, state, version, is_admin, connected, updated`

func scanProviderAuthorization(row interface{ Scan(...any) error }) (*ProviderAuthorization, error) {
	var a ProviderAuthorization
	var ae, re, c, u, admin int64
	if err := row.Scan(&a.Provider, &a.UserID, &a.AccountID, &a.Login, &a.AccessEnc, &a.RefreshEnc, &ae, &re, &a.State, &a.Version, &admin, &c, &u); err != nil {
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
	a.Admin = admin != 0
	a.Connected, a.Updated = fromMS(c), fromMS(u)
	return &a, nil
}

// GetProviderAuthorization returns a user's connection to a provider, or ErrNotFound.
func GetProviderAuthorization(ctx context.Context, q Q, provider, userID string) (*ProviderAuthorization, error) {
	return scanProviderAuthorization(q.QueryRow(ctx, `SELECT `+pauthCols+` FROM provider_authorizations WHERE provider = ? AND user_id = ?`, provider, userID))
}

// ProviderAccountUser returns the user who has connected an account of a
// provider, or ErrNotFound.
func ProviderAccountUser(ctx context.Context, q Q, provider, accountID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT user_id FROM provider_authorizations WHERE provider = ? AND account_id = ?`, provider, accountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// PutProviderAuthorization stores a connection, replacing the user's earlier
// one on the provider (the version goes up by one). ErrExists when another user
// has the account.
func PutProviderAuthorization(ctx context.Context, q Q, a ProviderAuthorization) error {
	if other, err := ProviderAccountUser(ctx, q, a.Provider, a.AccountID); err == nil && other != a.UserID {
		return ErrExists
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO provider_authorizations(`+pauthCols+`) VALUES (?,?,?,?,?,?,?,?,?,1,?,?,?)
		ON CONFLICT (provider, user_id) DO UPDATE SET account_id = excluded.account_id, login = excluded.login, access_enc = excluded.access_enc,
		refresh_enc = excluded.refresh_enc, access_expires = excluded.access_expires, refresh_expires = excluded.refresh_expires,
		state = excluded.state, version = provider_authorizations.version + 1, is_admin = excluded.is_admin, connected = excluded.connected, updated = excluded.updated`,
		a.Provider, a.UserID, a.AccountID, a.Login, a.AccessEnc, a.RefreshEnc, syncOrZero(a.AccessExpires), syncOrZero(a.RefreshExpires), a.State, b2i(a.Admin), ms(a.Connected), ms(a.Updated))
	if _, ok := UniqueOn(err); ok {
		return ErrExists // another user connected the account since the check
	}
	return err
}

// ReplaceProviderTokens stores a renewed pair, but only if the connection is
// still at version from. It reports whether it did; the version goes up by one.
func ReplaceProviderTokens(ctx context.Context, q Q, provider, userID string, from int64, accessEnc, refreshEnc string, accessExpires, refreshExpires, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE provider_authorizations SET access_enc = ?, refresh_enc = ?, access_expires = ?, refresh_expires = ?,
		state = 'ok', version = version + 1, updated = ? WHERE provider = ? AND user_id = ? AND version = ?`,
		accessEnc, refreshEnc, syncOrZero(accessExpires), syncOrZero(refreshExpires), ms(now), provider, userID, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireProviderAuthorization marks a connection expired and drops its tokens,
// only if it is still at version from.
func ExpireProviderAuthorization(ctx context.Context, q Q, provider, userID string, from int64, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE provider_authorizations SET state = 'expired', access_enc = '', refresh_enc = '',
		access_expires = 0, refresh_expires = 0, version = version + 1, updated = ? WHERE provider = ? AND user_id = ? AND version = ?`, ms(now), provider, userID, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteProviderAuthorization removes a user's connection to a provider.
func DeleteProviderAuthorization(ctx context.Context, q Q, provider, userID string) error {
	_, err := q.Exec(ctx, `DELETE FROM provider_authorizations WHERE provider = ? AND user_id = ?`, provider, userID)
	return err
}

// DueProviderRenewals lists the connections to a provider in good standing
// whose refresh token is in the last tenth of its life (counted from when the
// pair was made, which is when the row was last written) and ends before
// within is up, soonest first (then by user id), at most limit of them, after
// the cursor. A token's own life, not a fixed window, says when it is due: a
// host whose tokens live 30 days is not asked about them every day.
func DueProviderRenewals(ctx context.Context, q Q, provider string, now time.Time, within time.Duration, after DueCursor, limit int) ([]ProviderAuthorization, error) {
	ae := syncOrZero(after.RefreshExpires)
	rows, err := q.Query(ctx, `SELECT `+pauthCols+` FROM provider_authorizations
		WHERE provider = ? AND state = 'ok' AND refresh_enc <> '' AND refresh_expires > 0 AND refresh_expires < ?
		AND (refresh_expires - ?) * 10 <= (refresh_expires - updated)
		AND (refresh_expires > ? OR (refresh_expires = ? AND user_id > ?))
		ORDER BY refresh_expires, user_id LIMIT ?`, provider, ms(now.Add(within)), ms(now), ae, ae, after.UserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderAuthorization
	for rows.Next() {
		a, err := scanProviderAuthorization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ExpireProviderAuthorizations ends every connection to a provider that is in
// good standing and drops its tokens (the provider's application was replaced:
// tokens of the old one are not to be used). It returns how many it ended.
func ExpireProviderAuthorizations(ctx context.Context, q Q, provider string, now time.Time) (int64, error) {
	res, err := q.Exec(ctx, `UPDATE provider_authorizations SET state = 'expired', access_enc = '', refresh_enc = '',
		access_expires = 0, refresh_expires = 0, version = version + 1, updated = ? WHERE provider = ? AND state = 'ok'`, ms(now), provider)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListProviderAuthorizations returns every connection to a provider (it is being removed, to revoke them).
func ListProviderAuthorizations(ctx context.Context, q Q, provider string) ([]ProviderAuthorization, error) {
	rows, err := q.Query(ctx, `SELECT `+pauthCols+` FROM provider_authorizations WHERE provider = ? ORDER BY user_id`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderAuthorization
	for rows.Next() {
		a, err := scanProviderAuthorization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// CountProviderAuthorizations is how many users are connected to a provider.
func CountProviderAuthorizations(ctx context.Context, q Q, provider string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM provider_authorizations WHERE provider = ?`, provider).Scan(&n)
	return
}

// CountProviderSpaces is how many spaces sync through a provider, for their
// own repository or their attachment repository.
func CountProviderSpaces(ctx context.Context, q Q, provider string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM (SELECT space_id, cred_type, provider, gh_via, gh_installation_id, gh_user_id FROM remotes WHERE url <> ''
		UNION ALL SELECT space_id, cred_type, provider, gh_via, gh_installation_id, gh_user_id FROM asset_remotes WHERE url <> '') g WHERE cred_type = 'connection' AND provider = ?`, provider).Scan(&n)
	return
}

// CountUserProviderSpaces is how many spaces sync through a user's connection to a provider.
func CountUserProviderSpaces(ctx context.Context, q Q, provider, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM (SELECT space_id, cred_type, provider, gh_via, gh_installation_id, gh_user_id FROM remotes WHERE url <> ''
		UNION ALL SELECT space_id, cred_type, provider, gh_via, gh_installation_id, gh_user_id FROM asset_remotes WHERE url <> '') g
		WHERE cred_type = 'connection' AND provider = ? AND gh_via = 'user' AND gh_user_id = ?`, provider, userID).Scan(&n)
	return
}

// --- reminders ----------------------------------------------------------------------------

// ProviderLeftover is a reminder that a failed attempt to make a space with a
// new repository left an empty one on a provider.
type ProviderLeftover struct {
	Provider  string
	UserID    string
	RepoID    int64
	FullName  string
	HTMLURL   string
	SpaceName string
	Created   time.Time
}

// PutProviderLeftover stores a reminder for a user, replacing the one for the
// same repository, and keeps only the user's newest MaxGitHubLeftovers on the provider.
func PutProviderLeftover(ctx context.Context, q Q, l ProviderLeftover) error {
	if _, err := q.Exec(ctx, `INSERT INTO provider_leftovers(provider, user_id, repo_id, full_name, html_url, space_name, created) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT (provider, user_id, repo_id) DO UPDATE SET full_name = excluded.full_name, html_url = excluded.html_url,
		space_name = excluded.space_name, created = excluded.created`,
		l.Provider, l.UserID, l.RepoID, l.FullName, l.HTMLURL, l.SpaceName, ms(l.Created)); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM provider_leftovers WHERE provider = ? AND user_id = ? AND repo_id NOT IN
		(SELECT repo_id FROM provider_leftovers WHERE provider = ? AND user_id = ? ORDER BY created DESC, repo_id DESC LIMIT ?)`,
		l.Provider, l.UserID, l.Provider, l.UserID, MaxGitHubLeftovers)
	return err
}

// ListProviderLeftovers returns a user's reminders on a provider, newest first.
func ListProviderLeftovers(ctx context.Context, q Q, provider, userID string) ([]ProviderLeftover, error) {
	rows, err := q.Query(ctx, `SELECT repo_id, full_name, html_url, space_name, created FROM provider_leftovers
		WHERE provider = ? AND user_id = ? ORDER BY created DESC, repo_id DESC LIMIT ?`, provider, userID, MaxGitHubLeftovers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderLeftover
	for rows.Next() {
		l := ProviderLeftover{Provider: provider, UserID: userID}
		var c int64
		if err := rows.Scan(&l.RepoID, &l.FullName, &l.HTMLURL, &l.SpaceName, &c); err != nil {
			return nil, err
		}
		l.Created = fromMS(c)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteProviderLeftover removes a user's reminder of a repository and reports
// whether there was one.
func DeleteProviderLeftover(ctx context.Context, q Q, provider, userID string, repoID int64) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM provider_leftovers WHERE provider = ? AND user_id = ? AND repo_id = ?`, provider, userID, repoID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ConnProviderIDLike is whether s is a possible provider id: lowercase letters,
// digits and '-', 1 to 40, not starting or ending with '-'.
func ConnProviderIDLike(s string) bool {
	if len(s) == 0 || len(s) > 40 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return !strings.HasPrefix(s, "t-")
}
