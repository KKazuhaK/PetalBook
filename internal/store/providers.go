package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The providers of milestone 3.8: an OAuth application on a GitLab or a Gitea
// host. Their rows are rows of providers (repos = 1, the id is the slug, state
// "ok" or "error" is status_ok); a person's connection to an account there is a
// row of linked_accounts (linktokens.go), the reminders of empty repositories
// rows of repo_leftovers. GitHub keeps its own tables until 5c (github.go).

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

const connProviderCols = `id, kind, host, scheme, name, owner, client_id, secret_enc, status_ok, platform_version, partial_sync, partial_checked, status_checked, updated_by, created, updated`

func scanConnProvider(row interface{ Scan(...any) error }) (*ConnProviderRow, error) {
	var p ConnProviderRow
	var ok, pc, ck, cr, up int64
	if err := row.Scan(&p.ID, &p.Type, &p.Host, &p.Scheme, &p.Name, &p.Owner, &p.ClientID, &p.SecretEnc, &ok, &p.PlatformVersion,
		&p.PartialSync, &pc, &ck, &p.UpdatedBy, &cr, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.State = connStateOf(ok != 0)
	if pc > 0 {
		p.PartialChecked = fromMS(pc)
	}
	if ck > 0 {
		p.Checked = fromMS(ck)
	}
	p.Created, p.Updated = fromMS(cr), fromMS(up)
	return &p, nil
}

// connStateOf and connOK map a forge provider's state ("ok" or "error") to the
// status_ok column and back.
func connStateOf(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

func connOK(state string) int64 { return b2i(state == "ok") }

// ListConnProviderRows returns every provider, by id.
func ListConnProviderRows(ctx context.Context, q Q) ([]ConnProviderRow, error) {
	rows, err := q.Query(ctx, `SELECT `+connProviderCols+` FROM providers WHERE repos = 1 AND kind IN ('gitlab', 'gitea') ORDER BY id`)
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
	return scanConnProvider(q.QueryRow(ctx, `SELECT `+connProviderCols+` FROM providers WHERE id = ? AND repos = 1 AND kind IN ('gitlab', 'gitea')`, id))
}

// CountConnProviderRows is how many providers an owner ("site" or a tenant id) has.
func CountConnProviderRows(ctx context.Context, q Q, owner string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM providers WHERE owner = ? AND repos = 1 AND kind IN ('gitlab', 'gitea')`, owner).Scan(&n)
	return
}

// InsertConnProviderRow adds a provider. ErrExists when the id, or the owner's
// host, is taken.
func InsertConnProviderRow(ctx context.Context, q Q, p ConnProviderRow) error {
	_, err := q.Exec(ctx, `INSERT INTO providers(`+connProviderCols+`, slug, repos, enabled) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, ?, 1, 1)`,
		p.ID, p.Type, p.Host, p.Scheme, p.Name, p.Owner, p.ClientID, p.SecretEnc, connOK(p.State), p.PlatformVersion, p.PartialSync,
		syncOrZero(p.PartialChecked), syncOrZero(p.Checked), p.UpdatedBy, ms(p.Created), ms(p.Updated), p.ID)
	if _, ok := UniqueOn(err); ok {
		return ErrExists
	}
	return err
}

// UpdateConnProviderRow replaces what an administrator can change of a provider
// (its id, type, host and owner are fixed) and what a check found.
func UpdateConnProviderRow(ctx context.Context, q Q, p ConnProviderRow) error {
	res, err := q.Exec(ctx, `UPDATE providers SET name = ?, client_id = ?, secret_enc = ?, status_ok = ?, platform_version = ?, status_checked = ?, updated_by = ?, updated = ? WHERE id = ? AND repos = 1`,
		p.Name, p.ClientID, p.SecretEnc, connOK(p.State), p.PlatformVersion, syncOrZero(p.Checked), p.UpdatedBy, ms(p.Updated), p.ID)
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
	res, err := q.Exec(ctx, `UPDATE providers SET status_ok = ?, platform_version = CASE WHEN ? = '' THEN platform_version ELSE ? END, status_checked = ?
		WHERE id = ? AND repos = 1 AND client_id = ? AND secret_enc = ?`, connOK(state), version, version, syncOrZero(checked), id, clientID, secretEnc)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetConnProviderPartial records whether the provider's host can filter a fetch.
func SetConnProviderPartial(ctx context.Context, q Q, id, partial string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE providers SET partial_sync = ?, partial_checked = ? WHERE id = ? AND repos = 1`, partial, ms(at), id)
	return err
}

// DeleteConnProviderRow removes a provider; its links and reminders go with it.
func DeleteConnProviderRow(ctx context.Context, q Q, id string) error {
	return deleteProviderRows(ctx, q, `id = ? AND repos = 1`, id)
}

// DeleteUnusedConnProviderRow removes a provider of a host only while nobody is
// connected to it and no space syncs through it, counted with its row locked
// in the deleting transaction (ProviderInUseError otherwise): a connection or
// a remote made since an earlier count keeps it.
func DeleteUnusedConnProviderRow(ctx context.Context, q Q, id string) error {
	return atomically(ctx, q, func(q Q) error {
		if err := LockProvider(ctx, q, id); err != nil {
			if errors.Is(err, ErrProviderGone) {
				return ErrNotFound
			}
			return err
		}
		n, err := CountProviderLinks(ctx, q, id)
		if err != nil {
			return err
		}
		sp, err := CountProviderSpaces(ctx, q, id)
		if err != nil {
			return err
		}
		if n > 0 || sp > 0 {
			return &ProviderInUseError{Spaces: sp, Connections: n}
		}
		return deleteProviderRows(ctx, q, `id = ? AND repos = 1`, id)
	})
}

// DeleteTenantConnProviderRows removes the providers of a tenant (it is being deleted).
func DeleteTenantConnProviderRows(ctx context.Context, q Q, tenantID string) error {
	return deleteProviderRows(ctx, q, `owner = ?`, tenantID)
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
