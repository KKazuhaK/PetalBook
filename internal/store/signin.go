package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// --- sign-in providers -------------------------------------------------------

// ProviderRow is a sign-in provider as stored. Config is JSON with nothing
// secret in it; SecretEnc is sealed.
type ProviderRow struct {
	Slug, Kind, Name, Icon string
	Enabled                bool
	Position               int
	AutoCreate             bool
	MergeVerifiedEmail     bool
	LegacyCallback         bool
	Issuer                 string
	Config                 string
	SecretEnc              string
	Cache                  string
	StatusOK               bool
	StatusChecked          time.Time
	StatusMessage          string
	Created, Updated       time.Time
	// GroupsVersion is bumped by UpdateProvider (see LockProviderGroups).
	GroupsVersion int64
	// TenantID is where autoCreate puts new accounts.
	TenantID string
}

const providerCols = `slug, kind, name, icon, enabled, position, auto_create, merge_verified_email, legacy_callback, issuer, config, secret_enc, cache, status_ok, status_checked, status_message, created, updated, groups_version, tenant_id`

func scanProvider(sc interface{ Scan(...any) error }) (*ProviderRow, error) {
	var p ProviderRow
	var en, pos, ac, mv, lc, ok, chk, cr, up int64
	if err := sc.Scan(&p.Slug, &p.Kind, &p.Name, &p.Icon, &en, &pos, &ac, &mv, &lc, &p.Issuer, &p.Config, &p.SecretEnc, &p.Cache, &ok, &chk, &p.StatusMessage, &cr, &up, &p.GroupsVersion, &p.TenantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Enabled, p.Position, p.AutoCreate, p.MergeVerifiedEmail, p.LegacyCallback, p.StatusOK = en != 0, int(pos), ac != 0, mv != 0, lc != 0, ok != 0
	p.StatusChecked, p.Created, p.Updated = fromMS(chk), fromMS(cr), fromMS(up)
	if chk == 0 {
		p.StatusChecked = time.Time{}
	}
	return &p, nil
}

// ListProviders returns every provider in the administrators' order.
func ListProviders(ctx context.Context, q Q) ([]ProviderRow, error) {
	rows, err := q.Query(ctx, `SELECT `+providerCols+` FROM sign_in_providers ORDER BY position, name, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderRow
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetProvider returns one provider or ErrNotFound.
func GetProvider(ctx context.Context, q Q, slug string) (*ProviderRow, error) {
	return scanProvider(q.QueryRow(ctx, `SELECT `+providerCols+` FROM sign_in_providers WHERE slug = ?`, slug))
}

// InsertProvider adds a provider; a taken slug is a unique violation.
func InsertProvider(ctx context.Context, q Q, p ProviderRow) error {
	_, err := q.Exec(ctx, `INSERT INTO sign_in_providers(`+providerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Slug, p.Kind, p.Name, p.Icon, b2i(p.Enabled), p.Position, b2i(p.AutoCreate), b2i(p.MergeVerifiedEmail), b2i(p.LegacyCallback), p.Issuer,
		p.Config, p.SecretEnc, p.Cache, b2i(p.StatusOK), zeroMS(p.StatusChecked), p.StatusMessage, ms(p.Created), ms(p.Updated), p.GroupsVersion, tenantOrDefault(p.TenantID))
	return err
}

// UpdateProvider replaces everything but the slug, the kind and the creation
// time, and bumps the groups version.
func UpdateProvider(ctx context.Context, q Q, p ProviderRow) error {
	res, err := q.Exec(ctx, `UPDATE sign_in_providers SET name = ?, icon = ?, enabled = ?, position = ?, auto_create = ?, merge_verified_email = ?, legacy_callback = ?, issuer = ?,
		config = ?, secret_enc = ?, cache = ?, status_ok = ?, status_checked = ?, status_message = ?, updated = ?, tenant_id = ?, groups_version = groups_version + 1 WHERE slug = ?`,
		p.Name, p.Icon, b2i(p.Enabled), p.Position, b2i(p.AutoCreate), b2i(p.MergeVerifiedEmail), b2i(p.LegacyCallback), p.Issuer,
		p.Config, p.SecretEnc, p.Cache, b2i(p.StatusOK), zeroMS(p.StatusChecked), p.StatusMessage, ms(p.Updated), tenantOrDefault(p.TenantID), p.Slug)
	return oneRow(res, err)
}

// LockProviderGroups locks a provider's row for the rest of the transaction if
// its groups version is still version. It reports false, locking nothing, when
// the provider has changed since or is gone: a write of the provider holds the
// row until it commits, so this waits for it and then sees the new version.
func LockProviderGroups(ctx context.Context, q Q, slug string, version int64) (bool, error) {
	err := oneRow(q.Exec(ctx, `UPDATE sign_in_providers SET slug = slug WHERE slug = ? AND groups_version = ?`, slug, version))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// SetProviderStatus records the result of a discovery or metadata fetch (and
// the document, when it succeeded).
func SetProviderStatus(ctx context.Context, q Q, slug string, ok bool, at time.Time, message, cache string, keepCache bool) error {
	if keepCache {
		_, err := q.Exec(ctx, `UPDATE sign_in_providers SET status_ok = ?, status_checked = ?, status_message = ? WHERE slug = ?`, b2i(ok), ms(at), message, slug)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE sign_in_providers SET status_ok = ?, status_checked = ?, status_message = ?, cache = ? WHERE slug = ?`, b2i(ok), ms(at), message, cache, slug)
	return err
}

// DeleteProvider removes a provider; the identities of its people stay.
func DeleteProvider(ctx context.Context, q Q, slug string) error {
	res, err := q.Exec(ctx, `DELETE FROM sign_in_providers WHERE slug = ?`, slug)
	return oneRow(res, err)
}

// ProviderPeople counts the accounts with an identity of each issuer.
func ProviderPeople(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT issuer, COUNT(DISTINCT user_id) FROM identities GROUP BY issuer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var iss string
		var n int
		if err := rows.Scan(&iss, &n); err != nil {
			return nil, err
		}
		out[iss] = n
	}
	return out, rows.Err()
}

// UsersWithOnlyIssuer counts the accounts that have an identity of this issuer
// and no other way to sign in: no password, no passkey, no identity of another
// issuer.
func UsersWithOnlyIssuer(ctx context.Context, q Q, issuer string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(DISTINCT i.user_id) FROM identities i
		WHERE i.issuer = ?
		AND NOT EXISTS (SELECT 1 FROM passwords p WHERE p.user_id = i.user_id)
		AND NOT EXISTS (SELECT 1 FROM passkeys k WHERE k.user_id = i.user_id)
		AND NOT EXISTS (SELECT 1 FROM identities o WHERE o.user_id = i.user_id AND o.issuer <> ?)`, issuer, issuer).Scan(&n)
	return n, err
}

// UserHasIdentityOf reports whether the user has an identity of the issuer.
func UserHasIdentityOf(ctx context.Context, q Q, userID, issuer string) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM identities WHERE user_id = ? AND issuer = ? LIMIT 1`, userID, issuer).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// --- site settings -----------------------------------------------------------

// GetSetting returns a value of site_settings, its update time (the revision
// of the row), or ErrNotFound.
func GetSetting(ctx context.Context, q Q, key string) (value string, updated int64, err error) {
	err = q.QueryRow(ctx, `SELECT value, updated FROM site_settings WHERE key = ?`, key).Scan(&value, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return
}

// SettingRevision is the update time of a setting, 0 when there is none: one
// small query, for noticing a change made by another process.
func SettingRevision(ctx context.Context, q Q, key string) (int64, error) {
	var rev int64
	err := q.QueryRow(ctx, `SELECT updated FROM site_settings WHERE key = ?`, key).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// PutSetting stores a value; the revision is the larger of now and the
// previous revision plus one, so it always grows.
func PutSetting(ctx context.Context, q Q, key, value string, now time.Time) (int64, error) {
	var old int64
	err := q.QueryRow(ctx, `SELECT updated FROM site_settings WHERE key = ?`, key).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	rev := max(ms(now), old+1)
	_, err = q.Exec(ctx, `INSERT INTO site_settings(key, value, updated) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated = excluded.updated`, key, value, rev)
	return rev, err
}

// --- passkeys ----------------------------------------------------------------

// PasskeyRow is a stored passkey.
type PasskeyRow struct {
	ID, UserID, CredentialID, Credential, Name, RPID string
	Synced                                           bool
	Created, LastUsed                                time.Time
}

const passkeyCols = `id, user_id, credential_id, credential, name, rp_id, synced, created, last_used`

func scanPasskey(sc interface{ Scan(...any) error }) (*PasskeyRow, error) {
	var p PasskeyRow
	var synced, cr, lu int64
	if err := sc.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Credential, &p.Name, &p.RPID, &synced, &cr, &lu); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Synced, p.Created = synced != 0, fromMS(cr)
	if lu != 0 {
		p.LastUsed = fromMS(lu)
	}
	return &p, nil
}

// InsertPasskey adds a passkey; a credential id registered before is a unique violation.
func InsertPasskey(ctx context.Context, q Q, p PasskeyRow) error {
	_, err := q.Exec(ctx, `INSERT INTO passkeys(`+passkeyCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.UserID, p.CredentialID, p.Credential, p.Name, p.RPID, b2i(p.Synced), ms(p.Created), zeroMS(p.LastUsed))
	return err
}

// PasskeyByCredential finds a passkey by its credential id: the one query of a passkey sign-in.
func PasskeyByCredential(ctx context.Context, q Q, credID string) (*PasskeyRow, error) {
	return scanPasskey(q.QueryRow(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE credential_id = ?`, credID))
}

// ListPasskeys returns a user's passkeys, oldest first.
func ListPasskeys(ctx context.Context, q Q, userID string) ([]PasskeyRow, error) {
	rows, err := q.Query(ctx, `SELECT `+passkeyCols+` FROM passkeys WHERE user_id = ? ORDER BY created, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PasskeyRow
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CountPasskeys counts a user's passkeys.
func CountPasskeys(ctx context.Context, q Q, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id = ?`, userID).Scan(&n)
	return
}

// CountPasskeysForRP counts a user's passkeys made for the relying party
// rpID: the ones that can be used on this host.
func CountPasskeysForRP(ctx context.Context, q Q, userID, rpID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM passkeys WHERE user_id = ? AND rp_id = ?`, userID, rpID).Scan(&n)
	return
}

// PasskeyUse records a sign-in with a passkey: the credential (with its new
// counter) and the time.
func PasskeyUse(ctx context.Context, q Q, credID, credential string, synced bool, at time.Time) error {
	res, err := q.Exec(ctx, `UPDATE passkeys SET credential = ?, synced = ?, last_used = ? WHERE credential_id = ?`, credential, b2i(synced), ms(at), credID)
	return oneRow(res, err)
}

// RenamePasskey renames one of the user's passkeys.
func RenamePasskey(ctx context.Context, q Q, id, userID, name string) error {
	res, err := q.Exec(ctx, `UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	return oneRow(res, err)
}

// DeletePasskey removes one of the user's passkeys.
func DeletePasskey(ctx context.Context, q Q, id, userID string) error {
	res, err := q.Exec(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	return oneRow(res, err)
}

// PasskeyCounts returns how many passkeys each given user has (users with
// none are absent).
func PasskeyCounts(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT user_id, COUNT(*) FROM passkeys GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// IdentityCounts returns how many identities each user has.
func IdentityCounts(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT user_id, COUNT(*) FROM identities GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PasskeysOfOtherHost counts the passkeys made for a host other than rpID.
func PasskeysOfOtherHost(ctx context.Context, q Q, rpID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM passkeys WHERE rp_id <> ?`, rpID).Scan(&n)
	return
}

// UsersWithoutPasskey counts the users matched by want (a role, or a flag on
// the user) that have no passkey. roles may be empty.
func CountPasskeyRequired(ctx context.Context, q Q, roles []string) (required, without int, err error) {
	where := `u.passkey_required = 1`
	args := []any{}
	if len(roles) > 0 {
		ph := make([]string, len(roles))
		for i, r := range roles {
			ph[i] = "?"
			args = append(args, r)
		}
		where += ` OR u.role IN (` + strings.Join(ph, ",") + `)`
	}
	err = q.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN NOT EXISTS (SELECT 1 FROM passkeys k WHERE k.user_id = u.id) THEN 1 ELSE 0 END), 0)
		FROM users u WHERE u.deleted_at IS NULL AND u.disabled = 0 AND (`+where+`)`, args...).Scan(&required, &without)
	return
}

// --- audit log ---------------------------------------------------------------

// AuditRow is one row of the audit log.
type AuditRow struct {
	ID       string
	At       time.Time
	Kind     string
	Actor    string
	UserID   string
	Username string
	IP       string
	Detail   string
	// TenantID is the tenant the row belongs to; "" when the writer did not say,
	// which InsertAudit fills in from the user the row is about, else the actor,
	// else the server's own (""); "-" says the server's own outright, for an
	// event of the site's layer made by someone who has a tenant.
	TenantID string
}

// SiteAuditKinds are the kinds of the server's own doings, which a tenant
// administrator does not see (even in the rows written before tenants, which
// all sit in the default tenant).
var SiteAuditKinds = []string{"security_changed", "provider_created", "provider_changed", "provider_deleted",
	"mail_changed", "mail_test", "connection_provider_changed", "tenant_created", "tenant_changed", "tenant_deleted", "dropped", "rate_limited", "lockout"}

// InsertAudit writes rows (a repeated id is ignored).
func InsertAudit(ctx context.Context, q Q, rows []AuditRow) error {
	for _, r := range rows {
		if _, err := q.Exec(ctx, `INSERT INTO audit_log(id, at, kind, actor, user_id, username, ip, detail, tenant_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? = '-' THEN '' ELSE COALESCE(NULLIF(?, ''), (SELECT tenant_id FROM users WHERE id = ?), (SELECT tenant_id FROM users WHERE id = ?), '') END)
			ON CONFLICT (id) DO NOTHING`, r.ID, ms(r.At), r.Kind, r.Actor, r.UserID, r.Username, r.IP, r.Detail, r.TenantID, r.TenantID, r.UserID, r.Actor); err != nil {
			return err
		}
	}
	return nil
}

// ListAudit returns one page of the log, newest first: the rows with an id
// below cursor ("" for the first page), at most limit+1 (the caller cuts it).
//
// Only the rows of the scope's tenants are read; hideSite also leaves out the
// kinds of SiteAuditKinds (what a tenant administrator may not see).
func ListAudit(ctx context.Context, q Q, sc Scope, hideSite bool, kind, userID, cursor string, limit int) ([]AuditRow, error) {
	var conds []string
	var args []any
	sCond, sArgs := sc.cond("tenant_id")
	conds, args = append(conds, sCond), append(args, sArgs...)
	if hideSite {
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(SiteAuditKinds)), ", ")
		conds = append(conds, `kind NOT IN (`+ph+`)`)
		for _, k := range SiteAuditKinds {
			args = append(args, k)
		}
	}
	if kind != "" {
		conds = append(conds, `kind = ?`)
		args = append(args, kind)
	}
	if userID != "" {
		conds = append(conds, `user_id = ?`)
		args = append(args, userID)
	}
	if cursor != "" {
		conds = append(conds, `id < ?`)
		args = append(args, cursor)
	}
	where := ""
	if len(conds) > 0 {
		where = ` WHERE ` + strings.Join(conds, ` AND `)
	}
	args = append(args, limit+1)
	rows, err := q.Query(ctx, `SELECT id, at, kind, actor, user_id, username, ip, detail, tenant_id FROM audit_log`+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var at int64
		if err := rows.Scan(&r.ID, &at, &r.Kind, &r.Actor, &r.UserID, &r.Username, &r.IP, &r.Detail, &r.TenantID); err != nil {
			return nil, err
		}
		r.At = fromMS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditTrimBatch is how many rows TrimAudit deletes in one statement.
const AuditTrimBatch = 5000

// AuditIDBefore is the smallest audit id of a row written at t or later: an id
// is the time in Unix milliseconds as 12 hexadecimal digits, then a sequence
// number (see InsertAudit's callers), so the ids below it are the rows older
// than t.
func AuditIDBefore(t time.Time) string { return fmt.Sprintf("%012x", ms(t)) }

// TrimAudit deletes rows older than before and, beyond that, all but the
// newest keep rows. It deletes a range of ids (see AuditIDBefore) a batch of
// AuditTrimBatch rows at a time, each batch its own statement: on q the
// database (not a transaction) each is its own transaction, so a large trim
// never holds SQLite's one write connection, nor PostgreSQL's locks, for long.
func TrimAudit(ctx context.Context, q Q, before time.Time, keep int) (int64, error) {
	total, err := trimAuditWhere(ctx, q, `id < ?`, AuditIDBefore(before))
	if err != nil {
		return total, err
	}
	var cut string
	err = q.QueryRow(ctx, `SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?`, keep).Scan(&cut)
	if errors.Is(err, sql.ErrNoRows) {
		return total, nil
	}
	if err != nil {
		return total, err
	}
	n, err := trimAuditWhere(ctx, q, `id <= ?`, cut)
	return total + n, err
}

// trimAuditWhere deletes the rows whose id is in the range cond says (id < ?
// or id <= ?), the oldest first, in batches.
func trimAuditWhere(ctx context.Context, q Q, cond, bound string) (int64, error) {
	var total int64
	for {
		res, err := q.Exec(ctx, `DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE `+cond+` ORDER BY id LIMIT ?)`, bound, AuditTrimBatch)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < AuditTrimBatch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}
