package store

import (
	"context"
	"time"
)

// The keys of tenant_usage_daily. The first four are counted as they happen
// (AddUsageDaily); the others are totals the daily recount sets
// (SetUsageDaily).
const (
	UsageKeyAPIRequests = "apiRequests"
	UsageKeyMCPRequests = "mcpRequests"
	UsageKeyMail        = "mail"
	UsageKeyOutgoing    = "outgoing"
	UsageKeyPeople      = "people"
	UsageKeySpaces      = "spaces"
	UsageKeyBytes       = "bytes"
	UsageKeyPinnedBytes = "pinnedBytes"
)

// UsageDay is the text of a day in tenant_usage_daily: the UTC date.
func UsageDay(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// UsageDailyRow is one key of one day of one tenant.
type UsageDailyRow struct {
	TenantID, Day, Key string
	Value              int64
}

// AddUsageDaily adds delta to a counter of a tenant's day, making it when
// there is none. It is one statement (no read before the write), so
// concurrent adds all count on SQLite and on PostgreSQL.
func AddUsageDaily(ctx context.Context, q Q, tenantID, day, key string, delta int64) error {
	_, err := q.Exec(ctx, `INSERT INTO tenant_usage_daily(tenant_id, day, key, value) VALUES (?, ?, ?, ?)
		ON CONFLICT (tenant_id, day, key) DO UPDATE SET value = tenant_usage_daily.value + excluded.value`,
		tenantID, day, key, delta)
	return err
}

// SetUsageDaily sets a total of a tenant's day (the daily recount's), replacing
// an earlier one.
func SetUsageDaily(ctx context.Context, q Q, tenantID, day, key string, value int64) error {
	_, err := q.Exec(ctx, `INSERT INTO tenant_usage_daily(tenant_id, day, key, value) VALUES (?, ?, ?, ?)
		ON CONFLICT (tenant_id, day, key) DO UPDATE SET value = excluded.value`,
		tenantID, day, key, value)
	return err
}

// ListUsageDaily returns a tenant's rows for the days from through to (both
// included, as UsageDay texts), in the order of the usage export: by day, then
// key.
func ListUsageDaily(ctx context.Context, q Q, tenantID, from, to string) ([]UsageDailyRow, error) {
	rows, err := q.Query(ctx, `SELECT tenant_id, day, key, value FROM tenant_usage_daily
		WHERE tenant_id = ? AND day >= ? AND day <= ? ORDER BY day, key`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDailyRow
	for rows.Next() {
		var r UsageDailyRow
		if err := rows.Scan(&r.TenantID, &r.Day, &r.Key, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
