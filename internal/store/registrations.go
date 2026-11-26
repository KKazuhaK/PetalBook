package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Registration is a sign-up waiting for its link to be followed (docs/api.md,
// "Registration"). Only the hash of the token is kept, and the password only as
// a hash.
type Registration struct {
	ID           string
	TenantID     string
	Username     string
	Name         string
	Email        string
	PasswordHash string
	TokenHash    string
	// Terms is the JSON of what was accepted (when, and which addresses); ""
	// when no terms were asked.
	Terms   string
	Created time.Time
	Expires time.Time
}

const registrationCols = `id, tenant_id, username, name, email, password_hash, token_hash, terms, created, expires`

// InsertRegistration holds a username for a registration. A pending
// registration of the same username that has expired is removed first; one
// that is live is a unique violation (see UniqueOn). Run it in a transaction.
func InsertRegistration(ctx context.Context, q Q, r Registration, now time.Time) error {
	if _, err := q.Exec(ctx, `DELETE FROM registrations WHERE username = ? AND expires <= ?`, r.Username, ms(now)); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO registrations(`+registrationCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TenantID, r.Username, r.Name, r.Email, r.PasswordHash, r.TokenHash, r.Terms, ms(r.Created), ms(r.Expires))
	return err
}

// PendingUsername reports whether a live registration holds the username.
func PendingUsername(ctx context.Context, q Q, username string, now time.Time) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM registrations WHERE username = ? AND expires > ?`, username, ms(now)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CountPendingRegistrations is how many live registrations a tenant has.
func CountPendingRegistrations(ctx context.Context, q Q, tenant string, now time.Time) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM registrations WHERE tenant_id = ? AND expires > ?`, tenant, ms(now)).Scan(&n)
	return
}

// FindRegistration returns the live registration with this token hash, or ErrNotFound.
func FindRegistration(ctx context.Context, q Q, tokenHash string, now time.Time) (*Registration, error) {
	var r Registration
	var created, expires int64
	err := q.QueryRow(ctx, `SELECT `+registrationCols+` FROM registrations WHERE token_hash = ? AND expires > ?`, tokenHash, ms(now)).
		Scan(&r.ID, &r.TenantID, &r.Username, &r.Name, &r.Email, &r.PasswordHash, &r.TokenHash, &r.Terms, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.Created, r.Expires = fromMS(created), fromMS(expires)
	return &r, err
}

// DeleteRegistration removes a registration; false when it is already gone
// (someone used the link first).
func DeleteRegistration(ctx context.Context, q Q, id string) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM registrations WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteRegistrationsOf cancels every pending registration of a tenant.
func DeleteRegistrationsOf(ctx context.Context, q Q, tenant string) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM registrations WHERE tenant_id = ?`, tenant)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TenantsWithRegistrations lists the tenants that have a pending registration.
func TenantsWithRegistrations(ctx context.Context, q Q) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT tenant_id FROM registrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteExpiredRegistrations is the background clean-up.
func DeleteExpiredRegistrations(ctx context.Context, q Q, now time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM registrations WHERE expires <= ?`, ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// EmailToken is a link sent to an address: Kind is TokenVerify or TokenReset.
type EmailToken struct {
	Hash    string
	Kind    string
	UserID  string
	Email   string
	Created time.Time
	Expires time.Time
}

// The kinds of email token.
const (
	TokenVerify = "verify"
	TokenReset  = "reset"
)

// ReplaceEmailToken stores a token and removes the person's earlier tokens of
// the kind (asking again makes the earlier links unusable); a verification
// link replaces only the earlier links of its own address, so that confirming
// one address of an account leaves the link of another working. Run it in a
// transaction.
func ReplaceEmailToken(ctx context.Context, q Q, t EmailToken) error {
	var err error
	if t.Kind == TokenVerify {
		_, err = q.Exec(ctx, `DELETE FROM email_tokens WHERE user_id = ? AND kind = ? AND email = ?`, t.UserID, t.Kind, t.Email)
	} else {
		_, err = q.Exec(ctx, `DELETE FROM email_tokens WHERE user_id = ? AND kind = ?`, t.UserID, t.Kind)
	}
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO email_tokens(hash, kind, user_id, email, created, expires, used) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		t.Hash, t.Kind, t.UserID, t.Email, ms(t.Created), ms(t.Expires))
	return err
}

// FindEmailToken returns the token with this hash if it is of the kind, unused
// and not expired at now, else ErrNotFound.
func FindEmailToken(ctx context.Context, q Q, kind, hash string, now time.Time) (*EmailToken, error) {
	var t EmailToken
	var created, expires int64
	err := q.QueryRow(ctx, `SELECT hash, kind, user_id, email, created, expires FROM email_tokens WHERE hash = ? AND kind = ? AND used = 0 AND expires > ?`,
		hash, kind, ms(now)).Scan(&t.Hash, &t.Kind, &t.UserID, &t.Email, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	t.Created, t.Expires = fromMS(created), fromMS(expires)
	return &t, err
}

// UseEmailToken marks a token used; false when someone used it first or it
// expired: the update is the single point where two uses are told apart.
func UseEmailToken(ctx context.Context, q Q, hash string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE email_tokens SET used = ? WHERE hash = ? AND used = 0 AND expires > ?`, ms(now), hash, ms(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteEmailTokens removes a person's tokens of a kind ("" for every kind): a
// password change makes reset links unusable, an address change verify links.
func DeleteEmailTokens(ctx context.Context, q Q, userID, kind string) error {
	if kind == "" {
		_, err := q.Exec(ctx, `DELETE FROM email_tokens WHERE user_id = ?`, userID)
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM email_tokens WHERE user_id = ? AND kind = ?`, userID, kind)
	return err
}

// DeleteEmailTokensFor removes a person's tokens, of every kind, sent to one
// address (the address was removed from the account).
func DeleteEmailTokensFor(ctx context.Context, q Q, userID, email string) error {
	_, err := q.Exec(ctx, `DELETE FROM email_tokens WHERE user_id = ? AND email = ?`, userID, email)
	return err
}

// CountEmailTokens is how many unused, unexpired tokens of a kind a person has
// (the claims of addresses another account holds unverified wait as tokens).
func CountEmailTokens(ctx context.Context, q Q, userID, kind string, now time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM email_tokens WHERE user_id = ? AND kind = ? AND used = 0 AND expires > ?`, userID, kind, ms(now)).Scan(&n)
	return n, err
}

// EmailClaim is a verification link of an address the account has no row for:
// the claim of an address another account holds unverified.
type EmailClaim struct {
	Email   string
	Created time.Time
}

// PendingEmailClaims lists a person's unused, unexpired verification links of
// addresses they have no row for, oldest first.
func PendingEmailClaims(ctx context.Context, q Q, userID string, now time.Time) ([]EmailClaim, error) {
	rows, err := q.Query(ctx, `SELECT t.email, t.created FROM email_tokens t WHERE t.user_id = ? AND t.kind = ? AND t.used = 0 AND t.expires > ?
		AND NOT EXISTS (SELECT 1 FROM user_emails e WHERE e.user_id = t.user_id AND e.email = t.email) ORDER BY t.created, t.email`, userID, TokenVerify, ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmailClaim
	for rows.Next() {
		var c EmailClaim
		var created int64
		if err := rows.Scan(&c.Email, &created); err != nil {
			return nil, err
		}
		c.Created = fromMS(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteOldEmailTokens is the background clean-up of tokens that expired before.
func DeleteOldEmailTokens(ctx context.Context, q Q, before time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM email_tokens WHERE expires < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetTermsAccepted records what a person accepted when they registered.
func SetTermsAccepted(ctx context.Context, q Q, userID, terms string) error {
	res, err := q.Exec(ctx, `UPDATE users SET terms_accepted = ? WHERE id = ?`, terms, userID)
	return oneRow(res, err)
}

// GetTermsAccepted is the JSON recorded by SetTermsAccepted ("" for none).
func GetTermsAccepted(ctx context.Context, q Q, userID string) (string, error) {
	var t string
	err := q.QueryRow(ctx, `SELECT terms_accepted FROM users WHERE id = ?`, userID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}

// AnyLayeredValue reports whether a record of the layer holds exactly this
// JSON value for the key (any layer id): used to ask whether any tenant has
// stored "true" for a setting.
func AnyLayeredValue(ctx context.Context, q Q, layer, key, value string) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM layered_settings WHERE layer = ? AND key = ? AND value = ? LIMIT 1`, layer, key, value).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
