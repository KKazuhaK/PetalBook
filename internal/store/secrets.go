package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The scopes of a secret.
const (
	SecretSite   = "site"
	SecretTenant = "tenant"
)

// SecretRow is one secret: the sealed value and when and by whom it was set.
// TenantID is "" for the site's.
type SecretRow struct {
	TenantID  string
	Name      string
	Sealed    string
	Updated   time.Time
	UpdatedBy string
}

func secretScope(tenantID string) string {
	if tenantID == "" {
		return SecretSite
	}
	return SecretTenant
}

// GetSecret returns the sealed secret of a scope, or ErrNotFound.
func GetSecret(ctx context.Context, q Q, tenantID, name string) (SecretRow, error) {
	r := SecretRow{TenantID: tenantID, Name: name}
	var up int64
	err := q.QueryRow(ctx, `SELECT value, updated, updated_by FROM secrets WHERE scope = ? AND tenant_id = ? AND name = ?`,
		secretScope(tenantID), tenantID, name).Scan(&r.Sealed, &up, &r.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.Updated = fromMS(up)
	return r, err
}

// PutSecret stores a sealed secret, replacing the one of that name.
func PutSecret(ctx context.Context, q Q, r SecretRow) error {
	_, err := q.Exec(ctx, `INSERT INTO secrets(scope, tenant_id, name, value, updated, updated_by) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (scope, tenant_id, name) DO UPDATE SET value = excluded.value, updated = excluded.updated, updated_by = excluded.updated_by`,
		secretScope(r.TenantID), r.TenantID, r.Name, r.Sealed, ms(r.Updated), r.UpdatedBy)
	return err
}

// DeleteSecret removes a secret; there being none is not an error.
func DeleteSecret(ctx context.Context, q Q, tenantID, name string) error {
	_, err := q.Exec(ctx, `DELETE FROM secrets WHERE scope = ? AND tenant_id = ? AND name = ?`, secretScope(tenantID), tenantID, name)
	return err
}

// SecretMeta is what is told about a secret: never its value.
type SecretMeta struct {
	Name      string
	Updated   time.Time
	UpdatedBy string
}

// ListSecrets returns the names of a scope's secrets with when they changed.
func ListSecrets(ctx context.Context, q Q, tenantID string) ([]SecretMeta, error) {
	rows, err := q.Query(ctx, `SELECT name, updated, updated_by FROM secrets WHERE scope = ? AND tenant_id = ? ORDER BY name`, secretScope(tenantID), tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretMeta
	for rows.Next() {
		var m SecretMeta
		var up int64
		if err := rows.Scan(&m.Name, &up, &m.UpdatedBy); err != nil {
			return nil, err
		}
		m.Updated = fromMS(up)
		out = append(out, m)
	}
	return out, rows.Err()
}
