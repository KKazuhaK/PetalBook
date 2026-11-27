package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// --- sign-in providers -------------------------------------------------------

// ProviderSite is the owner of a provider the whole server shares; any other
// owner is a tenant's id.
const ProviderSite = "site"

// ProviderRow is a provider used to sign in, as stored in providers
// (signin = 1). Config is JSON with nothing secret in it; SecretEnc is sealed.
type ProviderRow struct {
	// ID is the row's own id: the slug for a site provider, a generated id for
	// a tenant's. Accounts are linked by it, never by Issuer.
	ID string
	// Owner is ProviderSite or the id of the tenant that owns the row.
	Owner                  string
	Slug, Kind, Name, Icon string
	// Class is "social" or "sso", and Template the preset the row was made
	// from ("" for none): both are written when the row is made and never
	// again (UpdateProvider leaves them).
	Class, Template string
	Enabled         bool
	Position        int
	AutoCreate      bool
	// MergeVerifiedEmail is trust_email_optin: the administrator's word that
	// the addresses this (site-owned, custom) provider gives are verified.
	MergeVerifiedEmail bool
	// Issuer is what the administrator typed (display and discovery only).
	Issuer           string
	Config           string
	SecretEnc        string
	Cache            string
	StatusOK         bool
	StatusChecked    time.Time
	StatusMessage    string
	Created, Updated time.Time
	// GroupsVersion is bumped by UpdateProvider (see LockProviderGroups).
	GroupsVersion int64
	// TenantID is where autoCreate puts new accounts (jit_tenant).
	TenantID string
	// Repos: the provider also holds repositories (the GitHub row once its App is
	// set up). Read-only here: the GitHub App's own writes set it.
	Repos bool
}

// providerRead is what a read of a provider selects: the columns that are
// written with it, and repos.
const providerRead = providerCols + `, repos`

const providerCols = `id, owner, slug, kind, class, template, name, icon, enabled, position, auto_create, trust_email_optin, issuer, config, secret_enc, cache, status_ok, status_checked, status_message, created, updated, groups_version, jit_tenant`

func scanProvider(sc interface{ Scan(...any) error }) (*ProviderRow, error) {
	var p ProviderRow
	var en, pos, ac, mv, ok, chk, cr, up, repos int64
	if err := sc.Scan(&p.ID, &p.Owner, &p.Slug, &p.Kind, &p.Class, &p.Template, &p.Name, &p.Icon, &en, &pos, &ac, &mv, &p.Issuer, &p.Config, &p.SecretEnc, &p.Cache, &ok, &chk, &p.StatusMessage, &cr, &up, &p.GroupsVersion, &p.TenantID, &repos); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Enabled, p.Position, p.AutoCreate, p.MergeVerifiedEmail, p.StatusOK = en != 0, int(pos), ac != 0, mv != 0, ok != 0
	p.Repos = repos != 0
	p.StatusChecked, p.Created, p.Updated = fromMS(chk), fromMS(cr), fromMS(up)
	if chk == 0 {
		p.StatusChecked = time.Time{}
	}
	return &p, nil
}

// ListProviders returns every sign-in provider (of every owner) in the
// administrators' order.
func ListProviders(ctx context.Context, q Q) ([]ProviderRow, error) {
	rows, err := q.Query(ctx, `SELECT `+providerRead+` FROM providers WHERE signin = 1 ORDER BY position, name, id`)
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

// GetProvider returns one sign-in provider by its id or ErrNotFound.
func GetProvider(ctx context.Context, q Q, id string) (*ProviderRow, error) {
	return scanProvider(q.QueryRow(ctx, `SELECT `+providerRead+` FROM providers WHERE id = ? AND signin = 1`, id))
}

// InsertProvider adds a sign-in provider; a taken id, or a slug taken by any
// provider of the server, is a unique violation.
func InsertProvider(ctx context.Context, q Q, p ProviderRow) error {
	owner := p.Owner
	if owner == "" {
		owner = ProviderSite
	}
	if p.Class == "" {
		// A row made without one (tests, the GitHub row) gets its kind's:
		// GitHub is social, anything else enterprise sign-in.
		p.Class = "sso"
		if p.Kind == "github" {
			p.Class = "social"
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO providers(`+providerCols+`, signin) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		p.ID, owner, p.Slug, p.Kind, p.Class, p.Template, p.Name, p.Icon, b2i(p.Enabled), p.Position, b2i(p.AutoCreate), b2i(p.MergeVerifiedEmail), p.Issuer,
		p.Config, p.SecretEnc, p.Cache, b2i(p.StatusOK), zeroMS(p.StatusChecked), p.StatusMessage, ms(p.Created), ms(p.Updated), p.GroupsVersion, tenantOrDefault(p.TenantID))
	return err
}

// UpdateProvider replaces everything but the id, the owner, the slug, the kind,
// the class, the template and the creation time, and bumps the groups version. What the GitHub App
// owns in a config (its record and its client id: see PutGitHubApp) is kept as
// it is stored now, not as the caller read it: the one who sets the App up is
// the only one who changes them.
func UpdateProvider(ctx context.Context, q Q, p ProviderRow) error {
	return atomically(ctx, q, func(q Q) error {
		if p.Kind == "github" {
			if _, err := q.Exec(ctx, `UPDATE providers SET id = id WHERE id = ?`, p.ID); err != nil {
				return err
			}
			var cur string
			if err := q.QueryRow(ctx, `SELECT config FROM providers WHERE id = ?`, p.ID).Scan(&cur); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			merged, err := keepAppConfig(cur, p.Config)
			if err != nil {
				return err
			}
			// The base URL moves only while nobody is linked (refuseGitHubRehost).
			have, _ := configMap(cur)
			next, err := configMap(merged)
			if err != nil {
				return err
			}
			if err := refuseGitHubRehost(ctx, q, p.ID, baseURLOf(have), baseURLOf(next)); err != nil {
				return err
			}
			p.Config = merged
		}
		res, err := q.Exec(ctx, `UPDATE providers SET name = ?, icon = ?, enabled = ?, position = ?, auto_create = ?, trust_email_optin = ?, issuer = ?,
			config = ?, secret_enc = ?, cache = ?, status_ok = ?, status_checked = ?, status_message = ?, updated = ?, jit_tenant = ?, groups_version = groups_version + 1 WHERE id = ? AND signin = 1`,
			p.Name, p.Icon, b2i(p.Enabled), p.Position, b2i(p.AutoCreate), b2i(p.MergeVerifiedEmail), p.Issuer,
			p.Config, p.SecretEnc, p.Cache, b2i(p.StatusOK), zeroMS(p.StatusChecked), p.StatusMessage, ms(p.Updated), tenantOrDefault(p.TenantID), p.ID)
		return oneRow(res, err)
	})
}

// LockProviderGroups locks a provider's row for the rest of the transaction if
// its groups version is still version. It reports false, locking nothing, when
// the provider has changed since or is gone: a write of the provider holds the
// row until it commits, so this waits for it and then sees the new version.
func LockProviderGroups(ctx context.Context, q Q, id string, version int64) (bool, error) {
	err := oneRow(q.Exec(ctx, `UPDATE providers SET id = id WHERE id = ? AND groups_version = ?`, id, version))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// SetProviderStatus records the result of a discovery or metadata fetch (and
// the document, when it succeeded).
func SetProviderStatus(ctx context.Context, q Q, id string, ok bool, at time.Time, message, cache string, keepCache bool) error {
	if keepCache {
		_, err := q.Exec(ctx, `UPDATE providers SET status_ok = ?, status_checked = ?, status_message = ? WHERE id = ?`, b2i(ok), ms(at), message, id)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE providers SET status_ok = ?, status_checked = ?, status_message = ?, cache = ? WHERE id = ?`, b2i(ok), ms(at), message, cache, id)
	return err
}

// DeleteProvider removes a sign-in provider and, by cascade, the accounts its
// people linked to it (and what they connected there: tokens, reminders).
func DeleteProvider(ctx context.Context, q Q, id string) error {
	return atomically(ctx, q, func(q Q) error {
		if err := lockUnusedProvider(ctx, q, id); err != nil {
			return err
		}
		var one int
		if err := q.QueryRow(ctx, `SELECT 1 FROM providers WHERE id = ? AND signin = 1`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return deleteProviderRows(ctx, q, `id = ? AND signin = 1`, id)
	})
}

// ProviderPeople counts the accounts linked to each provider (by provider id).
func ProviderPeople(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT provider_id, COUNT(*) FROM linked_accounts WHERE signin = 1 GROUP BY provider_id`)
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

// UsersWithOnlyProvider counts the accounts that have a sign-in link to this
// provider and no other way to sign in: no password, no passkey, no sign-in
// link to another provider.
func UsersWithOnlyProvider(ctx context.Context, q Q, providerID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM linked_accounts i
		WHERE i.provider_id = ? AND i.signin = 1
		AND NOT EXISTS (SELECT 1 FROM passwords p WHERE p.user_id = i.user_id)
		AND NOT EXISTS (SELECT 1 FROM passkeys k WHERE k.user_id = i.user_id)
		AND NOT EXISTS (SELECT 1 FROM linked_accounts o WHERE o.user_id = i.user_id AND o.signin = 1 AND o.provider_id <> ?)`, providerID, providerID).Scan(&n)
	return n, err
}

// --- site settings -----------------------------------------------------------

// GetRecord returns a value of site_records, its update time (the revision
// of the row), or ErrNotFound.
func GetRecord(ctx context.Context, q Q, key string) (value string, updated int64, err error) {
	err = q.QueryRow(ctx, `SELECT value, updated FROM site_records WHERE key = ?`, key).Scan(&value, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return
}

// RecordRevision is the update time of a setting, 0 when there is none: one
// small query, for noticing a change made by another process.
func RecordRevision(ctx context.Context, q Q, key string) (int64, error) {
	var rev int64
	err := q.QueryRow(ctx, `SELECT updated FROM site_records WHERE key = ?`, key).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// PutRecord stores a value; the revision is the larger of now and the
// previous revision plus one, so it always grows.
func PutRecord(ctx context.Context, q Q, key, value string, now time.Time) (int64, error) {
	var old int64
	err := q.QueryRow(ctx, `SELECT updated FROM site_records WHERE key = ?`, key).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	rev := max(ms(now), old+1)
	_, err = q.Exec(ctx, `INSERT INTO site_records(key, value, updated) VALUES (?, ?, ?)
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

// LinkCounts returns how many sign-in links each user has.
func LinkCounts(ctx context.Context, q Q) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT user_id, COUNT(*) FROM linked_accounts WHERE signin = 1 GROUP BY user_id`)
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
