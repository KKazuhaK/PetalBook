package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// A repository link is a person's account at a provider that holds
// repositories (GitLab, Gitea; GitHub joins in 5c): a row of linked_accounts
// that carries tokens. The sign-in links of links.go are the same table with
// no tokens. One person has at most one link per provider, and one account (the
// subject) at a provider belongs to one person. The tokens are still sealed
// here; the one service that renews and hands them out is internal/connections.

// Link states of a repository link.
const (
	LinkOK = "ok"
	// LinkExpired: the tokens ran out or were revoked and are gone; the person
	// authorises again.
	LinkExpired = "expired"
)

func syncOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

// repoLinkSQL is the condition that a linked_accounts row is a repository link
// and not only a sign-in: it holds tokens, or it had them and lost them.
const repoLinkSQL = `(access_enc <> '' OR state <> 'ok')`

// RepoLink is a repository link as stored.
type RepoLink struct {
	ID, ProviderID, UserID string
	// Subject is the account's id on the host.
	Subject string
	Login   string
	// SignIn: the link also signs the person in (a GitHub link, 5c).
	SignIn         bool
	AccessEnc      string
	RefreshEnc     string
	AccessExpires  time.Time // zero: the token does not expire
	RefreshExpires time.Time // zero: there is no refresh token, or it does not expire
	// RenewAt is when the renewal loop renews the pair next; zero: never.
	RenewAt time.Time
	State   string
	Version int64
	// Admin: the host said the account is an administrator of the host.
	Admin     bool
	Connected time.Time
	Updated   time.Time
	// RenewingUntil is the end of the lease of the renewal that is asking the
	// host for a new pair (ClaimLinkRenewal); zero: none.
	RenewingUntil time.Time
}

const repoLinkCols = `id, provider_id, user_id, subject, login, signin, access_enc, refresh_enc, access_expires, refresh_expires, renew_at, state, version, is_admin, created, updated`

// repoLinkRead is what a read of a link selects: the columns that are written
// with it, and its renewal lease.
const repoLinkRead = repoLinkCols + `, renewing_until`

func scanRepoLink(row interface{ Scan(...any) error }) (*RepoLink, error) {
	var a RepoLink
	var si, ae, re, ra, c, u, admin, lease int64
	if err := row.Scan(&a.ID, &a.ProviderID, &a.UserID, &a.Subject, &a.Login, &si, &a.AccessEnc, &a.RefreshEnc, &ae, &re, &ra, &a.State, &a.Version, &admin, &c, &u, &lease); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.SignIn = si != 0
	if ae > 0 {
		a.AccessExpires = fromMS(ae)
	}
	if re > 0 {
		a.RefreshExpires = fromMS(re)
	}
	if ra > 0 {
		a.RenewAt = fromMS(ra)
	}
	a.Admin = admin != 0
	if lease > 0 {
		a.RenewingUntil = fromMS(lease)
	}
	a.Connected, a.Updated = fromMS(c), fromMS(u)
	return &a, nil
}

// GetRepoLink returns a person's repository link at a provider, or ErrNotFound.
func GetRepoLink(ctx context.Context, q Q, providerID, userID string) (*RepoLink, error) {
	return scanRepoLink(q.QueryRow(ctx, `SELECT `+repoLinkRead+` FROM linked_accounts WHERE provider_id = ? AND user_id = ? AND `+repoLinkSQL, providerID, userID))
}

// PutRepoLink stores a repository link, replacing the person's earlier one at
// the provider: the id of that one stays, the version goes up by one, and a
// sign-in the link had stays if the account is the same one (a sign-in, and
// the address it brought, are not carried to another account at the host).
// A renewal lease of the old pair is dropped. ErrExists when another person
// has the account.
// It returns the link's id.
func PutRepoLink(ctx context.Context, q Q, a RepoLink) (string, error) {
	return PutRepoLinkGuarded(ctx, q, a, nil)
}

// SigninMove is asked, in the transaction of PutRepoLinkGuarded, whether a
// person's link that signs them in (prev) may move to another account of the
// host (and lose its sign-in): an error refuses, and nothing is written.
type SigninMove func(ctx context.Context, q Q, prev *Link) error

// PutRepoLinkGuarded is PutRepoLink that asks move first when the link it
// replaces signs the person in with another account: the person's link is
// locked, so what move decides holds for the write.
func PutRepoLinkGuarded(ctx context.Context, q Q, a RepoLink, move SigninMove) (string, error) {
	var id string
	err := atomically(ctx, q, func(q Q) error {
		if other, err := FindLink(ctx, q, a.ProviderID, a.Subject); err == nil && other.UserID != a.UserID {
			return ErrExists
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if move != nil {
			if _, err := q.Exec(ctx, `UPDATE linked_accounts SET id = id WHERE provider_id = ? AND user_id = ?`, a.ProviderID, a.UserID); err != nil {
				return err
			}
			prev, err := FindUserLink(ctx, q, a.ProviderID, a.UserID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if err == nil && prev.SignIn && prev.Subject != a.Subject {
				if err := move(ctx, q, prev); err != nil {
					return err
				}
			}
		}
		if err := q.QueryRow(ctx, `INSERT INTO linked_accounts(`+repoLinkCols+`, last_used) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,0)
			ON CONFLICT (provider_id, user_id) DO UPDATE SET
			signin = CASE WHEN linked_accounts.subject = excluded.subject THEN linked_accounts.signin ELSE excluded.signin END,
			email = CASE WHEN linked_accounts.subject = excluded.subject THEN linked_accounts.email ELSE '' END,
			email_trusted = CASE WHEN linked_accounts.subject = excluded.subject THEN linked_accounts.email_trusted ELSE 0 END,
			subject = excluded.subject, login = excluded.login, access_enc = excluded.access_enc,
			refresh_enc = excluded.refresh_enc, access_expires = excluded.access_expires, refresh_expires = excluded.refresh_expires,
			renew_at = excluded.renew_at, renewing_until = 0, state = excluded.state, version = linked_accounts.version + 1, is_admin = excluded.is_admin,
			created = excluded.created, updated = excluded.updated
			RETURNING id`,
			a.ID, a.ProviderID, a.UserID, a.Subject, a.Login, b2i(a.SignIn), a.AccessEnc, a.RefreshEnc, syncOrZero(a.AccessExpires), syncOrZero(a.RefreshExpires),
			syncOrZero(a.RenewAt), a.State, b2i(a.Admin), ms(a.Connected), ms(a.Updated)).Scan(&id); err != nil {
			return err
		}
		return settleConnected(ctx, q, a.UserID)
	})
	if _, ok := UniqueOn(err); ok {
		return "", ErrExists // another person linked the account since the check
	}
	return id, err
}

// ReplaceLinkTokens stores a renewed pair, but only if the link is still at
// version from, and ends the renewal's lease. It reports whether it did; the
// version goes up by one.
func ReplaceLinkTokens(ctx context.Context, q Q, id string, from int64, accessEnc, refreshEnc string, accessExpires, refreshExpires, renewAt, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE linked_accounts SET access_enc = ?, refresh_enc = ?, access_expires = ?, refresh_expires = ?, renew_at = ?,
		renewing_until = 0, state = 'ok', version = version + 1, updated = ? WHERE id = ? AND version = ?`,
		accessEnc, refreshEnc, syncOrZero(accessExpires), syncOrZero(refreshExpires), syncOrZero(renewAt), ms(now), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireLink marks a link as needing the person to authorise again and drops
// its tokens, only if it is still at version from. It reports whether it did:
// of any number of callers that give up on one link at once, exactly one gets
// true.
func ExpireLink(ctx context.Context, q Q, id string, from int64, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE linked_accounts SET state = 'expired', access_enc = '', refresh_enc = '',
		access_expires = 0, refresh_expires = 0, renew_at = 0, version = version + 1, updated = ? WHERE id = ? AND version = ? AND state = 'ok'`, ms(now), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireProviderLinks ends every repository link at a provider that is in good
// standing and drops its tokens (the provider's application was replaced: the
// old one's tokens are not to be used), and tells each person whose link it
// ended that they must authorise again (a NotifyLinkReauth, as ExpireLink's
// callers do; name is the provider's name). Only the rows the write moved are
// told, in the same transaction. It returns how many it ended.
func ExpireProviderLinks(ctx context.Context, q Q, providerID, name string, now time.Time) (int64, error) {
	var n int64
	err := atomically(ctx, q, func(q Q) error {
		rows, err := q.Query(ctx, `UPDATE linked_accounts SET state = 'expired', access_enc = '', refresh_enc = '',
			access_expires = 0, refresh_expires = 0, renew_at = 0, renewing_until = 0, version = version + 1, updated = ?
			WHERE provider_id = ? AND state = 'ok' AND access_enc <> '' RETURNING user_id, login`, ms(now), providerID)
		if err != nil {
			return err
		}
		type ended struct{ user, login string }
		var all []ended
		for rows.Next() {
			var e ended
			if err := rows.Scan(&e.user, &e.login); err != nil {
				rows.Close()
				return err
			}
			all = append(all, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range all {
			if err := NotifyUser(ctx, q, e.user, NotifyLinkReauth, providerID,
				map[string]string{"provider": providerID, "name": name, "login": e.login}, now); err != nil {
				return err
			}
		}
		n = int64(len(all))
		return nil
	})
	return n, err
}

// ClaimLinkRenewal takes the lease of renewing a link, until until: only if the
// link is still at version from and in good standing, and nobody holds a lease
// that has not ended by now. It reports whether it did. Of any number of
// renewals in any number of processes, one holds a link's lease at a time, so
// a refresh token is handed to the host once.
func ClaimLinkRenewal(ctx context.Context, q Q, id string, from int64, now, until time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE linked_accounts SET renewing_until = ? WHERE id = ? AND version = ? AND state = 'ok' AND renewing_until <= ?`,
		ms(until), id, from, ms(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseLinkRenewal gives a lease that ClaimLinkRenewal took until until back
// (a lease taken since by someone else, or a link replaced since, stays).
func ReleaseLinkRenewal(ctx context.Context, q Q, id string, until time.Time) error {
	_, err := q.Exec(ctx, `UPDATE linked_accounts SET renewing_until = 0 WHERE id = ? AND renewing_until = ?`, id, ms(until))
	return err
}

// DeleteRepoLink removes a person's repository link at a provider; a link that
// also signs the person in keeps that and loses its tokens.
func DeleteRepoLink(ctx context.Context, q Q, providerID, userID string) error {
	return atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `DELETE FROM linked_accounts WHERE provider_id = ? AND user_id = ? AND signin = 0`, providerID, userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE linked_accounts SET access_enc = '', refresh_enc = '', access_expires = 0, refresh_expires = 0, renew_at = 0,
			state = 'ok', version = version + 1 WHERE provider_id = ? AND user_id = ? AND signin = 1`, providerID, userID); err != nil {
			return err
		}
		return settleConnected(ctx, q, userID)
	})
}

// DeleteUserRepoLinks removes every repository link a person has (they are
// being deleted; the rows would go with the person by cascade, this settles
// the count of connected people first).
func DeleteUserRepoLinks(ctx context.Context, q Q, userID string) error {
	return atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `DELETE FROM linked_accounts WHERE user_id = ? AND signin = 0`, userID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `UPDATE linked_accounts SET access_enc = '', refresh_enc = '', access_expires = 0, refresh_expires = 0, renew_at = 0,
			state = 'ok', version = version + 1 WHERE user_id = ? AND signin = 1`, userID); err != nil {
			return err
		}
		return settleConnected(ctx, q, userID)
	})
}

// deleteProviderRows deletes providers by a condition, their links with them
// (by cascade), and settles whether each person who had one is connected.
func deleteProviderRows(ctx context.Context, q Q, where string, arg string) error {
	return atomically(ctx, q, func(q Q) error {
		rows, err := q.Query(ctx, `SELECT DISTINCT user_id FROM linked_accounts WHERE `+repoLinkSQL+` AND provider_id IN (SELECT id FROM providers WHERE `+where+`) ORDER BY user_id`, arg)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM providers WHERE `+where, arg); err != nil {
			return err
		}
		for _, id := range ids {
			if err := settleConnected(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DueLinkCursor is where the previous page of DueLinkRenewals ended.
type DueLinkCursor struct {
	RenewAt time.Time
	ID      string
}

// DueLinkRenewals lists the links in good standing whose renewal time has come
// (renew_at is set when the pair is written: in the last tenth of the refresh
// token's life, and within the renewal window), soonest first, at most limit of
// them, after the cursor. One range scan of linked_accounts_renew.
func DueLinkRenewals(ctx context.Context, q Q, now time.Time, after DueLinkCursor, limit int) ([]RepoLink, error) {
	ra := syncOrZero(after.RenewAt)
	rows, err := q.Query(ctx, `SELECT `+repoLinkRead+` FROM linked_accounts
		WHERE refresh_enc <> '' AND state = 'ok' AND renew_at > 0 AND renew_at <= ?
		AND (renew_at > ? OR (renew_at = ? AND id > ?))
		ORDER BY renew_at, id LIMIT ?`, ms(now), ra, ra, after.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoLink
	for rows.Next() {
		a, err := scanRepoLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// NextLinkRenewal is the soonest renewal time after t of a link in good
// standing; false when there is none. The loop asks it with the time its pass
// looked for due links at, so a link that came due during the pass is in it.
func NextLinkRenewal(ctx context.Context, q Q, t time.Time) (time.Time, bool, error) {
	var next sql.NullInt64
	err := q.QueryRow(ctx, `SELECT MIN(renew_at) FROM linked_accounts WHERE refresh_enc <> '' AND state = 'ok' AND renew_at > ?`, ms(t)).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	return fromMS(next.Int64), true, nil
}

// CountProviderLinks is how many people have a repository link at a provider.
func CountProviderLinks(ctx context.Context, q Q, providerID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM linked_accounts WHERE provider_id = ? AND `+repoLinkSQL, providerID).Scan(&n)
	return
}

// ProviderInUseError refuses to delete a provider that spaces sync through
// (Spaces of them), found in the deleting transaction.
type ProviderInUseError struct{ Spaces int }

func (e *ProviderInUseError) Error() string { return "spaces sync through the provider" }

// lockUnusedProvider locks a provider's row and refuses (ProviderInUseError)
// when a space syncs through it. A remote put on the provider locks the row
// too (PutRemote), so no space starts to sync through it between this check
// and the deletion of the row in the same transaction.
func lockUnusedProvider(ctx context.Context, q Q, id string) error {
	if _, err := q.Exec(ctx, `UPDATE providers SET id = id WHERE id = ?`, id); err != nil {
		return err
	}
	n, err := CountProviderSpaces(ctx, q, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return &ProviderInUseError{Spaces: n}
	}
	return nil
}

// CountProviderSpaces is how many spaces sync through a provider, for their
// own repository or their attachment repository.
func CountProviderSpaces(ctx context.Context, q Q, provider string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM remotes WHERE url <> '' AND cred_type = 'connection' AND provider = ?`, provider).Scan(&n)
	return
}

// CountUserProviderSpaces is how many spaces sync through a user's link at a provider.
func CountUserProviderSpaces(ctx context.Context, q Q, provider, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT space_id) FROM remotes
		WHERE cred_user_id = ? AND cred_user_id <> '' AND url <> '' AND cred_type = 'connection' AND provider = ? AND via = 'user'`, userID, provider).Scan(&n)
	return
}

// --- reminders ----------------------------------------------------------------------------

// RepoLeftover is a reminder that a failed attempt to make a space with a new
// repository left an empty one on a provider.
type RepoLeftover struct {
	ProviderID string
	UserID     string
	RepoID     int64
	FullName   string
	HTMLURL    string
	SpaceName  string
	Created    time.Time
}

// PutRepoLeftover stores a reminder for a user, replacing the one for the same
// repository, and keeps only the user's newest MaxGitHubLeftovers on the provider.
func PutRepoLeftover(ctx context.Context, q Q, l RepoLeftover) error {
	rid := strconv.FormatInt(l.RepoID, 10)
	return atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `INSERT INTO repo_leftovers(provider_id, user_id, repo_id, full_name, html_url, space_name, created) VALUES (?,?,?,?,?,?,?)
			ON CONFLICT (provider_id, user_id, repo_id) DO UPDATE SET full_name = excluded.full_name, html_url = excluded.html_url,
			space_name = excluded.space_name, created = excluded.created`,
			l.ProviderID, l.UserID, rid, l.FullName, l.HTMLURL, l.SpaceName, ms(l.Created)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `DELETE FROM repo_leftovers WHERE provider_id = ? AND user_id = ? AND repo_id NOT IN
			(SELECT repo_id FROM repo_leftovers WHERE provider_id = ? AND user_id = ? ORDER BY created DESC, repo_id DESC LIMIT ?)`,
			l.ProviderID, l.UserID, l.ProviderID, l.UserID, MaxGitHubLeftovers)
		return err
	})
}

// ListRepoLeftovers returns a user's reminders at a provider, newest first.
func ListRepoLeftovers(ctx context.Context, q Q, providerID, userID string) ([]RepoLeftover, error) {
	rows, err := q.Query(ctx, `SELECT repo_id, full_name, html_url, space_name, created FROM repo_leftovers
		WHERE provider_id = ? AND user_id = ? ORDER BY created DESC, repo_id DESC LIMIT ?`, providerID, userID, MaxGitHubLeftovers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoLeftover
	for rows.Next() {
		l := RepoLeftover{ProviderID: providerID, UserID: userID}
		var rid string
		var c int64
		if err := rows.Scan(&rid, &l.FullName, &l.HTMLURL, &l.SpaceName, &c); err != nil {
			return nil, err
		}
		l.RepoID, _ = strconv.ParseInt(rid, 10, 64)
		l.Created = fromMS(c)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteRepoLeftover removes a user's reminder of a repository and reports
// whether there was one.
func DeleteRepoLeftover(ctx context.Context, q Q, providerID, userID string, repoID int64) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM repo_leftovers WHERE provider_id = ? AND user_id = ? AND repo_id = ?`, providerID, userID, strconv.FormatInt(repoID, 10))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
