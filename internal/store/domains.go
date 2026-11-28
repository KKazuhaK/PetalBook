package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The email domains tenants claim (3.6 d2; table tenant_domains). The rules of
// claiming and verifying are internal/domains'; this is their rows.

// ErrDomainClaimed: another tenant holds the domain verified.
var ErrDomainClaimed = errors.New("another tenant holds the domain verified")

// DomainRow is one tenant's claim of a domain. A zero time is 0 in the row.
type DomainRow struct {
	TenantID     string
	Domain       string
	CodeHash     string
	CodeExpires  time.Time
	VerifiedAt   time.Time
	LapsedAt     time.Time
	CheckedAt    time.Time
	MissingSince time.Time
	// LastCheck is the latest check as JSON ("" before the first).
	LastCheck    string
	NextCheckAt  time.Time
	ProviderID   string
	JoinMode     string
	RestrictOrgs bool
	CreatedBy    string
	Created      time.Time
	Updated      time.Time
}

// Verified says whether the claim holds the domain.
func (r DomainRow) Verified() bool { return !r.VerifiedAt.IsZero() }

const domainCols = `tenant_id, domain, code_hash, code_expires, verified_at, lapsed_at, checked_at, missing_since, last_check,
	next_check_at, provider_id, join_mode, restrict_orgs, created_by, created, updated`

func optMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

func optFromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return fromMS(v)
}

func scanDomain(sc interface{ Scan(...any) error }) (DomainRow, error) {
	var r DomainRow
	var ce, va, la, ca, mi, nx, cr, up, ro int64
	err := sc.Scan(&r.TenantID, &r.Domain, &r.CodeHash, &ce, &va, &la, &ca, &mi, &r.LastCheck, &nx, &r.ProviderID, &r.JoinMode, &ro, &r.CreatedBy, &cr, &up)
	r.CodeExpires, r.VerifiedAt, r.LapsedAt, r.CheckedAt, r.MissingSince, r.NextCheckAt = optFromMS(ce), optFromMS(va), optFromMS(la), optFromMS(ca), optFromMS(mi), optFromMS(nx)
	r.Created, r.Updated, r.RestrictOrgs = fromMS(cr), fromMS(up), ro != 0
	return r, err
}

func queryDomains(ctx context.Context, q Q, query string, args ...any) ([]DomainRow, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainRow{}
	for rows.Next() {
		r, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertDomain stores a new claim. A claim of the domain the tenant has
// already is ErrExists.
func InsertDomain(ctx context.Context, q Q, r DomainRow) error {
	_, err := q.Exec(ctx, `INSERT INTO tenant_domains(`+domainCols+`) VALUES (?, ?, ?, ?, 0, 0, 0, 0, '', 0, '', '', ?, ?, ?, ?)`,
		r.TenantID, r.Domain, r.CodeHash, optMS(r.CodeExpires), b2i(r.RestrictOrgs), r.CreatedBy, ms(r.Created), ms(r.Updated))
	if IsUnique(err) {
		return ErrExists
	}
	return err
}

// GetDomain returns a tenant's claim, or ErrNotFound.
func GetDomain(ctx context.Context, q Q, tenantID, domain string) (DomainRow, error) {
	r, err := scanDomain(q.QueryRow(ctx, `SELECT `+domainCols+` FROM tenant_domains WHERE tenant_id = ? AND domain = ?`, tenantID, domain))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// TenantDomains lists a tenant's claims by domain (the primary key's order).
func TenantDomains(ctx context.Context, q Q, tenantID string) ([]DomainRow, error) {
	return queryDomains(ctx, q, `SELECT `+domainCols+` FROM tenant_domains WHERE tenant_id = ? ORDER BY tenant_id, domain`, tenantID)
}

// CountTenantDomains counts a tenant's claims.
func CountTenantDomains(ctx context.Context, q Q, tenantID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM tenant_domains WHERE tenant_id = ?`, tenantID).Scan(&n)
	return n, err
}

// DomainUse is how many claims, and verified ones, a tenant has.
type DomainUse struct {
	TenantID         string
	Claims, Verified int
}

// DomainUseByTenant counts every tenant's claims (the preview of turning
// features.domains off: an administrator's rare action, not a list).
func DomainUseByTenant(ctx context.Context, q Q) ([]DomainUse, error) {
	rows, err := q.Query(ctx, `SELECT tenant_id, COUNT(*), SUM(CASE WHEN verified_at > 0 THEN 1 ELSE 0 END) FROM tenant_domains GROUP BY tenant_id ORDER BY tenant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainUse
	for rows.Next() {
		var u DomainUse
		if err := rows.Scan(&u.TenantID, &u.Claims, &u.Verified); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DomainFilter is a page of the site's list: by domain, then tenant, after
// the cursor (domain and tenant of the last row shown).
type DomainFilter struct {
	AfterDomain, AfterTenant string
	VerifiedOnly             bool
	Limit                    int
}

// ListDomains is the site's list of every claim, by domain.
func ListDomains(ctx context.Context, q Q, f DomainFilter) ([]DomainRow, error) {
	var where []string
	var args []any
	if f.AfterDomain != "" {
		// A row value, so both databases seek the index to the cursor.
		where = append(where, `(domain, tenant_id) > (?, ?)`)
		args = append(args, f.AfterDomain, f.AfterTenant)
	}
	if f.VerifiedOnly {
		where = append(where, `verified_at > 0`)
	}
	w := ""
	if len(where) > 0 {
		w = ` WHERE ` + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit)
	return queryDomains(ctx, q, `SELECT `+domainCols+` FROM tenant_domains`+w+` ORDER BY domain, tenant_id LIMIT ?`, args...)
}

// VerifiedDomains is every verified claim: the in-memory map is made of it.
func VerifiedDomains(ctx context.Context, q Q) ([]DomainRow, error) {
	return queryDomains(ctx, q, `SELECT `+domainCols+` FROM tenant_domains WHERE verified_at > 0 ORDER BY domain`)
}

// VerifiedHolder returns the claim that holds the domain verified, or ErrNotFound.
func VerifiedHolder(ctx context.Context, q Q, domain string) (DomainRow, error) {
	r, err := scanDomain(q.QueryRow(ctx, `SELECT `+domainCols+` FROM tenant_domains WHERE domain = ? AND verified_at > 0`, domain))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SetDomainCode gives a claim that is not verified a new code. It reports
// false when there is no such claim, or it is verified.
func SetDomainCode(ctx context.Context, q Q, tenantID, domain, hash string, expires, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE tenant_domains SET code_hash = ?, code_expires = ?, updated = ? WHERE tenant_id = ? AND domain = ? AND verified_at = 0`,
		hash, ms(expires), ms(now), tenantID, domain)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetDomainRestrict sets whether people may make organisations on the domain.
func SetDomainRestrict(ctx context.Context, q Q, tenantID, domain string, restrict bool, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE tenant_domains SET restrict_orgs = ?, updated = ? WHERE tenant_id = ? AND domain = ?`, b2i(restrict), ms(now), tenantID, domain)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecordDomainCheck stores a check by hand: what it saw, and, when it found
// the record of a verified claim, that it is not missing. It changes nothing
// of the recheck's schedule.
func RecordDomainCheck(ctx context.Context, q Q, tenantID, domain, lastCheck string, at time.Time, found bool) error {
	_, err := q.Exec(ctx, `UPDATE tenant_domains SET last_check = ?, checked_at = ?, updated = ?,
		missing_since = CASE WHEN ? = 1 THEN 0 ELSE missing_since END WHERE tenant_id = ? AND domain = ?`,
		lastCheck, ms(at), ms(at), b2i(found), tenantID, domain)
	return err
}

// VerifyDomain marks a claim verified, if its code is still valid and no other
// tenant holds the domain verified: one statement, and the unique index
// tenant_domains_verified settles two tenants that race (one of the two
// statements fails). ErrDomainClaimed when another holds it; false when the
// claim is gone, verified already, or its code expired.
func VerifyDomain(ctx context.Context, q Q, tenantID, domain, lastCheck string, now, next time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE tenant_domains SET verified_at = ?, lapsed_at = 0, missing_since = 0, checked_at = ?, last_check = ?, next_check_at = ?, updated = ?
		WHERE tenant_id = ? AND domain = ? AND verified_at = 0 AND code_expires > ?
		AND NOT EXISTS (SELECT 1 FROM tenant_domains o WHERE o.domain = ? AND o.verified_at > 0)`,
		ms(now), ms(now), lastCheck, ms(next), ms(now), tenantID, domain, ms(now), domain)
	if IsUnique(err) {
		return false, ErrDomainClaimed
	}
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, err
	}
	if h, err := VerifiedHolder(ctx, q, domain); err == nil && h.TenantID != tenantID {
		return false, ErrDomainClaimed
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	return false, nil
}

// DueDomains are the verified claims whose recheck is due at now, soonest
// first (tenant_domains_due).
func DueDomains(ctx context.Context, q Q, now time.Time, limit int) ([]DomainRow, error) {
	return queryDomains(ctx, q, `SELECT `+domainCols+` FROM tenant_domains WHERE verified_at > 0 AND next_check_at <= ?
		ORDER BY next_check_at, tenant_id, domain LIMIT ?`, ms(now), limit)
}

// NextDomainCheck is when the soonest recheck is due; false when no claim is verified.
func NextDomainCheck(ctx context.Context, q Q) (time.Time, bool, error) {
	var next sql.NullInt64
	err := q.QueryRow(ctx, `SELECT MIN(next_check_at) FROM tenant_domains WHERE verified_at > 0`).Scan(&next)
	if err != nil || !next.Valid {
		return time.Time{}, false, err
	}
	return fromMS(next.Int64), true, nil
}

// RecheckDomain stores what a recheck found, if the claim is still the one it
// read (verified at the same time): a claim removed or reassigned meanwhile is
// left alone. It reports whether it wrote.
func RecheckDomain(ctx context.Context, q Q, r DomainRow, lastCheck string, at, missingSince, next time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE tenant_domains SET last_check = ?, checked_at = ?, missing_since = ?, next_check_at = ?, updated = ?
		WHERE tenant_id = ? AND domain = ? AND verified_at = ?`,
		lastCheck, ms(at), optMS(missingSince), ms(next), ms(at), r.TenantID, r.Domain, ms(r.VerifiedAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// LapseDomain cancels a verified claim whose record has been missing too long,
// if it is still the one read. The row stays, lapsed, so its tenant sees why.
func LapseDomain(ctx context.Context, q Q, r DomainRow, lastCheck string, at time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE tenant_domains SET verified_at = 0, lapsed_at = ?, next_check_at = 0, last_check = ?, checked_at = ?, updated = ?
		WHERE tenant_id = ? AND domain = ? AND verified_at = ?`,
		ms(at), lastCheck, ms(at), ms(at), r.TenantID, r.Domain, ms(r.VerifiedAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PostponeDomainCheck moves a verified claim's recheck to next (its tenant's
// outbound share was spent, or the feature is off for it).
func PostponeDomainCheck(ctx context.Context, q Q, r DomainRow, next time.Time) error {
	_, err := q.Exec(ctx, `UPDATE tenant_domains SET next_check_at = ? WHERE tenant_id = ? AND domain = ? AND verified_at = ?`,
		ms(next), r.TenantID, r.Domain, ms(r.VerifiedAt))
	return err
}

// DeleteDomain removes a tenant's claim; false when it had none.
func DeleteDomain(ctx context.Context, q Q, tenantID, domain string) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM tenant_domains WHERE tenant_id = ? AND domain = ?`, tenantID, domain)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AssignDomain moves a verified domain to another tenant (a site
// administrator's transfer), in one transaction: the holder's claim goes, and
// the target's becomes the verified one, with the holder's code, so the record
// in DNS stays good. It returns the tenant that held it; ErrNotFound when none
// holds it verified. Assigning it to its holder changes nothing.
func AssignDomain(ctx context.Context, q Q, domain, to, by string, now time.Time) (from string, err error) {
	err = atomically(ctx, q, func(q Q) error {
		h, err := VerifiedHolder(ctx, q, domain)
		if err != nil {
			return err
		}
		from = h.TenantID
		if from == to {
			return nil
		}
		if _, err := DeleteDomain(ctx, q, from, domain); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO tenant_domains(`+domainCols+`) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, '', '', 0, ?, ?, ?)
			ON CONFLICT (tenant_id, domain) DO UPDATE SET code_hash = excluded.code_hash, code_expires = excluded.code_expires,
			verified_at = excluded.verified_at, lapsed_at = 0, checked_at = excluded.checked_at, missing_since = excluded.missing_since,
			last_check = excluded.last_check, next_check_at = excluded.next_check_at, updated = excluded.updated`,
			to, domain, h.CodeHash, optMS(h.CodeExpires), ms(now), optMS(h.CheckedAt), optMS(h.MissingSince), h.LastCheck, optMS(h.NextCheckAt), by, ms(now), ms(now))
		return err
	})
	return from, err
}
