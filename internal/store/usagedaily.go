package store

import (
	"context"
	"database/sql"
	"errors"
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

// TenantTotals are the totals the daily recount sets for a tenant.
type TenantTotals struct {
	TenantID                           string
	People, Spaces, Bytes, PinnedBytes int64
}

// pinnedBytesSQL is the disk a space keeps on the server when pinned: what the
// last measurement found of its clone, else what the quotas count of it.
const pinnedBytesSQL = `COALESCE(size_bytes, usage_bytes, 0)`

// PinnedBytes is the disk the pinned spaces of a tenant take, leaving out the
// space named by except ("" for none): one indexed read.
func PinnedBytes(ctx context.Context, q Q, tenantID, except string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(`+pinnedBytesSQL+`), 0) FROM spaces WHERE pinned <> 0 AND tenant_id = ? AND id <> ?`,
		tenantID, except).Scan(&n)
	return n, err
}

// SpaceDiskBytes is what pinning a space would keep on the server's disk.
func SpaceDiskBytes(ctx context.Context, q Q, id string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT `+pinnedBytesSQL+` FROM spaces WHERE id = ?`, id).Scan(&n)
	return n, err
}

// ListTenantTotals reads the totals of every tenant: the people, spaces and
// bytes of the usage rows (a tenant with no row has none) and the disk of its
// pinned spaces. Two queries, whatever the number of tenants.
func ListTenantTotals(ctx context.Context, q Q) ([]TenantTotals, error) {
	rows, err := q.Query(ctx, `SELECT t.id, COALESCE(u.people, 0), COALESCE(u.spaces, 0), COALESCE(u.bytes, 0)
		FROM tenants t LEFT JOIN usage u ON u.kind = 'tenant' AND u.id = t.id ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	var out []TenantTotals
	for rows.Next() {
		var t TenantTotals
		if err := rows.Scan(&t.TenantID, &t.People, &t.Spaces, &t.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pin, err := q.Query(ctx, `SELECT tenant_id, COALESCE(SUM(`+pinnedBytesSQL+`), 0) FROM spaces WHERE pinned <> 0 GROUP BY tenant_id`)
	if err != nil {
		return nil, err
	}
	defer pin.Close()
	byTenant := map[string]int64{}
	for pin.Next() {
		var id string
		var n int64
		if err := pin.Scan(&id, &n); err != nil {
			return nil, err
		}
		byTenant[id] = n
	}
	if err := pin.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].PinnedBytes = byTenant[out[i].TenantID]
	}
	return out, nil
}

// SetTenantTotals sets the totals of one tenant's day in one transaction's worth
// of statements (the caller's Q).
func SetTenantTotals(ctx context.Context, q Q, day string, t TenantTotals) error {
	for _, kv := range []struct {
		key string
		v   int64
	}{{UsageKeyPeople, t.People}, {UsageKeySpaces, t.Spaces}, {UsageKeyBytes, t.Bytes}, {UsageKeyPinnedBytes, t.PinnedBytes}} {
		if err := SetUsageDaily(ctx, q, t.TenantID, day, kv.key, kv.v); err != nil {
			return err
		}
	}
	return nil
}

// UsageDailyValue is the value of a counter of a tenant's day (0 when none).
func UsageDailyValue(ctx context.Context, q Q, tenantID, day, key string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT value FROM tenant_usage_daily WHERE tenant_id = ? AND day = ? AND key = ?`, tenantID, day, key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
