package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SetAwaitingSSO marks an account an administrator made for a tenant that
// requires its single sign-on: it waits for that tenant's SSO to claim it.
func SetAwaitingSSO(ctx context.Context, q Q, userID, tenantID string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO awaiting_sso(user_id, tenant_id, created) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET tenant_id = excluded.tenant_id, created = excluded.created`, userID, tenantID, ms(now))
	return err
}

// AwaitingSSO is the tenant the account waits in ("" when it waits for nothing).
func AwaitingSSO(ctx context.Context, q Q, userID string) (string, error) {
	var tenant string
	err := q.QueryRow(ctx, `SELECT tenant_id FROM awaiting_sso WHERE user_id = ?`, userID).Scan(&tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return tenant, err
}

// ClearAwaitingSSO ends the wait; false when it was not waiting (someone
// claimed it first).
func ClearAwaitingSSO(ctx context.Context, q Q, userID string) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM awaiting_sso WHERE user_id = ?`, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
