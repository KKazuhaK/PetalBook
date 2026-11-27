package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GitHubInstaller is who installed an installation of the GitHub App, as
// GitHub's signed installation webhook said.
type GitHubInstaller struct {
	InstallationID int64
	AccountID      int64
	Account        string
	AccountType    string
	SenderID       int64
	Sender         string
	Created        time.Time
}

// PutGitHubInstaller records who installed an installation. GitHub redelivers
// webhooks: the same event again leaves the row as it is (one statement).
func PutGitHubInstaller(ctx context.Context, q Q, in GitHubInstaller) error {
	_, err := q.Exec(ctx, `INSERT INTO github_installers(installation_id, account_id, account, account_type, sender_id, sender, created)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT (installation_id) DO UPDATE SET account_id = excluded.account_id, account = excluded.account,
			account_type = excluded.account_type, sender_id = excluded.sender_id, sender = excluded.sender`,
		in.InstallationID, in.AccountID, in.Account, in.AccountType, in.SenderID, in.Sender, ms(in.Created))
	return err
}

// GetGitHubInstaller returns who installed an installation, or ErrNotFound
// when no webhook said so.
func GetGitHubInstaller(ctx context.Context, q Q, id int64) (*GitHubInstaller, error) {
	var in GitHubInstaller
	var cr int64
	err := q.QueryRow(ctx, `SELECT installation_id, account_id, account, account_type, sender_id, sender, created FROM github_installers WHERE installation_id = ?`, id).
		Scan(&in.InstallationID, &in.AccountID, &in.Account, &in.AccountType, &in.SenderID, &in.Sender, &cr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	in.Created = fromMS(cr)
	return &in, nil
}

// DeleteGitHubInstaller forgets an uninstalled installation: who installed it
// and every claim waiting for it.
func DeleteGitHubInstaller(ctx context.Context, q Q, id int64) error {
	return atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `DELETE FROM github_installers WHERE installation_id = ?`, id); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `DELETE FROM github_pending_claims WHERE installation_id = ?`, id)
		return err
	})
}

// States of a pending claim besides a failure code.
const (
	PendingWaiting  = ""
	PendingClaiming = "claiming"
)

// GitHubPendingClaim is an organisation's claim of an installation that waits
// for GitHub's webhook to say who installed it.
type GitHubPendingClaim struct {
	InstallationID int64
	UserID         string // the claimer
	Owner          InstallOwner
	ClaimerGitHub  int64 // the claimer's GitHub user id, from their own token
	AccountID      int64
	Account        string
	AccountType    string
	State          string
	Created        time.Time
	Expires        time.Time
}

const pendingCols = `installation_id, user_id, COALESCE(owner_user, ''), COALESCE(owner_tenant, ''), claimer_gh, account_id, account, account_type, state, created, expires`

func scanPending(row interface{ Scan(...any) error }) (*GitHubPendingClaim, error) {
	var p GitHubPendingClaim
	var cr, ex int64
	if err := row.Scan(&p.InstallationID, &p.UserID, &p.Owner.UserID, &p.Owner.TenantID, &p.ClaimerGitHub, &p.AccountID, &p.Account, &p.AccountType, &p.State, &cr, &ex); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Created, p.Expires = fromMS(cr), fromMS(ex)
	return &p, nil
}

// PutGitHubPendingClaim stores a claim that waits (it replaces the claimer's
// earlier one of the installation), and drops the claims that expired.
func PutGitHubPendingClaim(ctx context.Context, q Q, p GitHubPendingClaim) error {
	if !p.Owner.Valid() || p.InstallationID <= 0 || p.UserID == "" {
		return errors.New("store: a pending claim names an installation, its claimer and one owner")
	}
	return atomically(ctx, q, func(q Q) error {
		if _, err := q.Exec(ctx, `DELETE FROM github_pending_claims WHERE expires <= ?`, ms(p.Created)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO github_pending_claims(installation_id, user_id, owner_user, owner_tenant, claimer_gh, account_id, account, account_type, state, created, expires)
			VALUES (?,?,?,?,?,?,?,?,'',?,?)
			ON CONFLICT (installation_id, user_id) DO UPDATE SET owner_user = excluded.owner_user, owner_tenant = excluded.owner_tenant,
				claimer_gh = excluded.claimer_gh, account_id = excluded.account_id, account = excluded.account, account_type = excluded.account_type,
				state = '', created = excluded.created, expires = excluded.expires`,
			p.InstallationID, p.UserID, nullIfEmpty(p.Owner.UserID), nullIfEmpty(p.Owner.TenantID), p.ClaimerGitHub, p.AccountID, p.Account, p.AccountType,
			ms(p.Created), ms(p.Expires))
		return err
	})
}

// WaitingGitHubClaims are the claims of an installation that still wait (at
// most 20: each claimer has one).
func WaitingGitHubClaims(ctx context.Context, q Q, id int64, now time.Time) ([]GitHubPendingClaim, error) {
	rows, err := q.Query(ctx, `SELECT `+pendingCols+` FROM github_pending_claims WHERE installation_id = ? AND state = '' AND expires > ? ORDER BY created, user_id LIMIT 20`, id, ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubPendingClaim
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// TakeGitHubPendingClaim marks a waiting claim as being completed, and
// returns it; ErrNotFound when it does not wait (any more): of two requests
// that would complete it (the webhook, and the callback that saw the webhook
// had come) only one gets it.
func TakeGitHubPendingClaim(ctx context.Context, q Q, id int64, userID string, now time.Time) (*GitHubPendingClaim, error) {
	res, err := q.Exec(ctx, `UPDATE github_pending_claims SET state = ? WHERE installation_id = ? AND user_id = ? AND state = '' AND expires > ?`,
		PendingClaiming, id, userID, ms(now))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return scanPending(q.QueryRow(ctx, `SELECT `+pendingCols+` FROM github_pending_claims WHERE installation_id = ? AND user_id = ?`, id, userID))
}

// EndGitHubPendingClaim ends a claim being completed: removed when it was
// connected (code ""), else kept with the code it failed with until it expires.
func EndGitHubPendingClaim(ctx context.Context, q Q, id int64, userID, code string) error {
	if code == "" {
		_, err := q.Exec(ctx, `DELETE FROM github_pending_claims WHERE installation_id = ? AND user_id = ?`, id, userID)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE github_pending_claims SET state = ? WHERE installation_id = ? AND user_id = ?`, code, id, userID)
	return err
}

// ListGitHubPendingClaimsOwned lists the owner's claims that have not expired
// (waiting, or failed), newest first, at most 20.
func ListGitHubPendingClaimsOwned(ctx context.Context, q Q, o InstallOwner, now time.Time) ([]GitHubPendingClaim, error) {
	if !o.Valid() {
		return nil, nil
	}
	c, a := ownerCond("", o)
	rows, err := q.Query(ctx, `SELECT `+pendingCols+` FROM github_pending_claims WHERE `+c+` AND expires > ? ORDER BY created DESC, installation_id LIMIT 20`,
		append(a, ms(now))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubPendingClaim
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
