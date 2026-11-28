package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The kinds of a tenant's integration record (table tenant_integrations). The
// record holds the configuration that is not a secret, as JSON owned by the
// code of the kind; the secrets are in secrets (scope tenant).
const (
	IntegrationSMTP    = "smtp"
	IntegrationCaptcha = "captcha"
	IntegrationDaily   = "daily"
	IntegrationGitHub  = "github"
	IntegrationGitLab  = "gitlab"
	IntegrationGitea   = "gitea"
)

// IntegrationRow is one record of a tenant's: Kind, and ID to tell apart
// several of a kind ("" for the kinds a tenant has one of), the JSON
// configuration, and who changed it last and when.
type IntegrationRow struct {
	TenantID, Kind, ID string
	Config             string
	UpdatedBy          string
	Updated            time.Time
}

const integrationCols = `tenant_id, kind, id, config, updated_by, updated`

func scanIntegration(sc interface{ Scan(...any) error }) (IntegrationRow, error) {
	var r IntegrationRow
	var up int64
	err := sc.Scan(&r.TenantID, &r.Kind, &r.ID, &r.Config, &r.UpdatedBy, &up)
	r.Updated = fromMS(up)
	return r, err
}

// GetIntegration returns one record of a tenant, or ErrNotFound.
func GetIntegration(ctx context.Context, q Q, tenantID, kind, id string) (IntegrationRow, error) {
	r, err := scanIntegration(q.QueryRow(ctx, `SELECT `+integrationCols+` FROM tenant_integrations WHERE tenant_id = ? AND kind = ? AND id = ?`, tenantID, kind, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ListIntegrations returns a tenant's records, of one kind when kind is not "".
func ListIntegrations(ctx context.Context, q Q, tenantID, kind string) ([]IntegrationRow, error) {
	query, args := `SELECT `+integrationCols+` FROM tenant_integrations WHERE tenant_id = ?`, []any{tenantID}
	if kind != "" {
		query, args = query+` AND kind = ?`, append(args, kind)
	}
	rows, err := q.Query(ctx, query+` ORDER BY kind, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntegrationRow
	for rows.Next() {
		r, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListIntegrationsOfKind returns the records of one kind of every tenant: what
// the server reads at startup into memory.
func ListIntegrationsOfKind(ctx context.Context, q Q, kind string) ([]IntegrationRow, error) {
	rows, err := q.Query(ctx, `SELECT `+integrationCols+` FROM tenant_integrations WHERE kind = ? ORDER BY tenant_id, id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntegrationRow
	for rows.Next() {
		r, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutIntegration stores a record, replacing the one of the same tenant, kind
// and id. A tenant that does not exist is refused by the foreign key.
func PutIntegration(ctx context.Context, q Q, r IntegrationRow) error {
	cfg := r.Config
	if cfg == "" {
		cfg = "{}"
	}
	_, err := q.Exec(ctx, `INSERT INTO tenant_integrations(`+integrationCols+`) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, kind, id) DO UPDATE SET config = excluded.config, updated_by = excluded.updated_by, updated = excluded.updated`,
		r.TenantID, r.Kind, r.ID, cfg, r.UpdatedBy, ms(r.Updated))
	return err
}

// DeleteIntegration removes a record; there being none is not an error.
func DeleteIntegration(ctx context.Context, q Q, tenantID, kind, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM tenant_integrations WHERE tenant_id = ? AND kind = ? AND id = ?`, tenantID, kind, id)
	return err
}
