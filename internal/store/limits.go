package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SettingCapabilities is the site_settings key of the site's default values of
// the limits sheet (JSON, only the keys that differ from the built-in default).
const SettingCapabilities = "capabilities"

// Sources of a tenant's override of a limit (the others, site and plan, are not
// rows of tenant_limits).
const (
	LimitManual  = "manual"
	LimitBilling = "billing"
)

// Plan is a named set of values of the limits sheet; Values is JSON.
type Plan struct {
	ID, Slug, Name, Description, Values string
}

const planCols = `id, slug, name, description, settings`

func scanPlan(sc interface{ Scan(...any) error }) (Plan, error) {
	var p Plan
	err := sc.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.Values)
	return p, err
}

// InsertPlan makes a plan; its slug is unique (ErrExists otherwise).
func InsertPlan(ctx context.Context, q Q, p Plan) error {
	_, err := q.Exec(ctx, `INSERT INTO plans(`+planCols+`) VALUES (?, ?, ?, ?, ?)`, p.ID, p.Slug, p.Name, p.Description, p.Values)
	if IsUnique(err) {
		return ErrExists
	}
	return err
}

// GetPlanBySlug returns a plan, or ErrNotFound.
func GetPlanBySlug(ctx context.Context, q Q, slug string) (*Plan, error) {
	p, err := scanPlan(q.QueryRow(ctx, `SELECT `+planCols+` FROM plans WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

// ListPlans returns every plan, by slug.
func ListPlans(ctx context.Context, q Q) ([]Plan, error) {
	rows, err := q.Query(ctx, `SELECT `+planCols+` FROM plans ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePlan writes a plan's name, description and values (ErrNotFound when
// there is none).
func UpdatePlan(ctx context.Context, q Q, p Plan) error {
	res, err := q.Exec(ctx, `UPDATE plans SET name = ?, description = ?, settings = ? WHERE id = ?`, p.Name, p.Description, p.Values, p.ID)
	return oneRow(res, err)
}

// DeletePlan removes a plan no tenant uses (ErrExists when one does).
func DeletePlan(ctx context.Context, q Q, id string) error {
	var n int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM tenants WHERE plan_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrExists
	}
	res, err := q.Exec(ctx, `DELETE FROM plans WHERE id = ?`, id)
	return oneRow(res, err)
}

// SetTenantPlan sets a tenant's plan; "" for none.
func SetTenantPlan(ctx context.Context, q Q, tenant, planID string) error {
	var v any
	if planID != "" {
		v = planID
	}
	res, err := q.Exec(ctx, `UPDATE tenants SET plan_id = ? WHERE id = ?`, v, tenant)
	return oneRow(res, err)
}

// TenantPlanIDs maps each tenant that has a plan to its id.
func TenantPlanIDs(ctx context.Context, q Q) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT id, plan_id FROM tenants WHERE plan_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var t, p string
		if err := rows.Scan(&t, &p); err != nil {
			return nil, err
		}
		out[t] = p
	}
	return out, rows.Err()
}

// TenantLimit is one override of a tenant's limits sheet. Value is JSON. Starts
// and Ends are zero for none; the value counts from Starts up to, not
// including, Ends.
type TenantLimit struct {
	Tenant, Key, Source, Value, SetBy string
	Starts, Ends                      time.Time
	Updated                           time.Time
}

const limitCols = `tenant_id, key, source, value, starts, ends, set_by, updated`

func scanLimit(sc interface{ Scan(...any) error }) (TenantLimit, error) {
	var l TenantLimit
	var st, en sql.NullInt64
	var up int64
	err := sc.Scan(&l.Tenant, &l.Key, &l.Source, &l.Value, &st, &en, &l.SetBy, &up)
	if st.Valid {
		l.Starts = fromMS(st.Int64)
	}
	if en.Valid {
		l.Ends = fromMS(en.Int64)
	}
	l.Updated = fromMS(up)
	return l, err
}

func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ms(t)
}

// ListTenantLimits returns every override of every tenant (read once at start;
// the limits service keeps them in memory).
func ListTenantLimits(ctx context.Context, q Q) ([]TenantLimit, error) {
	rows, err := q.Query(ctx, `SELECT `+limitCols+` FROM tenant_limits ORDER BY tenant_id, key, source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TenantLimit
	for rows.Next() {
		l, err := scanLimit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PutTenantLimit writes an override, replacing the one of the same tenant, key
// and source (a tenant that does not exist is a foreign-key error).
func PutTenantLimit(ctx context.Context, q Q, l TenantLimit) error {
	_, err := q.Exec(ctx, `INSERT INTO tenant_limits(`+limitCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, key, source) DO UPDATE SET value = excluded.value, starts = excluded.starts,
		ends = excluded.ends, set_by = excluded.set_by, updated = excluded.updated`,
		l.Tenant, l.Key, l.Source, l.Value, nullMS(l.Starts), nullMS(l.Ends), l.SetBy, ms(l.Updated))
	return err
}

// DeleteTenantLimit removes an override; it is not an error when there is none.
func DeleteTenantLimit(ctx context.Context, q Q, tenant, key, source string) error {
	_, err := q.Exec(ctx, `DELETE FROM tenant_limits WHERE tenant_id = ? AND key = ? AND source = ?`, tenant, key, source)
	return err
}

// DeleteTenantLimitsOf removes every override of one source of a tenant.
func DeleteTenantLimitsOf(ctx context.Context, q Q, tenant, source string) error {
	_, err := q.Exec(ctx, `DELETE FROM tenant_limits WHERE tenant_id = ? AND source = ?`, tenant, source)
	return err
}
