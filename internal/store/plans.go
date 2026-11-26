package store

import (
	"context"
	"database/sql"
	"errors"
)

// Plan is a named bundle of rules. The rules are rows of layered_settings with
// layer "plan" and layer_id the plan's id (see LayeredPlan).
type Plan struct {
	ID, Slug, Name, Description string
}

const planCols = `id, slug, name, description`

func scanPlan(sc interface{ Scan(...any) error }) (Plan, error) {
	var p Plan
	err := sc.Scan(&p.ID, &p.Slug, &p.Name, &p.Description)
	return p, err
}

// InsertPlan makes a plan; its slug is unique (ErrExists otherwise).
func InsertPlan(ctx context.Context, q Q, p Plan) error {
	_, err := q.Exec(ctx, `INSERT INTO plans(`+planCols+`) VALUES (?, ?, ?, ?)`, p.ID, p.Slug, p.Name, p.Description)
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

// UpdatePlan writes a plan's name and description (ErrNotFound when there is none).
func UpdatePlan(ctx context.Context, q Q, p Plan) error {
	res, err := q.Exec(ctx, `UPDATE plans SET name = ?, description = ? WHERE id = ?`, p.Name, p.Description, p.ID)
	return oneRow(res, err)
}

// DeletePlan removes a plan no tenant uses (ErrExists when one does), and its
// rules.
func DeletePlan(ctx context.Context, q Q, id string) error {
	var n int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM tenants WHERE plan_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrExists
	}
	res, err := q.Exec(ctx, `DELETE FROM plans WHERE id = ?`, id)
	if err := oneRow(res, err); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `DELETE FROM layered_settings WHERE layer = ? AND layer_id = ?`, LayeredPlan, id)
	return err
}

// SetTenantPlan sets a tenant's plan; "" for none. The caller calls
// Store.TenantStateReload after the commit: the plan is part of TenantRef.
func SetTenantPlan(ctx context.Context, q Q, tenant, planID string) error {
	var v any
	if planID != "" {
		v = planID
	}
	res, err := q.Exec(ctx, `UPDATE tenants SET plan_id = ? WHERE id = ?`, v, tenant)
	return oneRow(res, err)
}
