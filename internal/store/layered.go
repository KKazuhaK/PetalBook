package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The layers of layered_settings (see internal/settings).
const (
	LayeredSite   = "site"
	LayeredTenant = "tenant"
	LayeredPlan   = "plan"
)

// The sources of a rule: a site administrator's, the billing module's, the
// tenant's plan's. The site's own row and a tenant's own row are manual.
const (
	SourceManual  = "manual"
	SourceBilling = "billing"
	SourcePlan    = "plan"
)

// LayeredRow is one layer's record of one setting (see internal/settings):
// the value as JSON ("" follows the layer above) and what the layer lets the
// layers below do, as JSON ("" is the key's default). layer is "site",
// "tenant" or "plan"; layerID is "" for the site, the tenant's id for a tenant
// and for the site's rule for that tenant, and the plan's id for a plan.
//
// Source is where a rule comes from (the Source constants; "" is manual).
// Starts and Ends are the window of a rule for a tenant: it counts from
// Starts up to, not including, Ends; the zero time is none.
type LayeredRow struct {
	Layer, LayerID, Key, Source string
	Value, Lower                string
	Starts, Ends                time.Time
	SetBy                       string
	Updated                     time.Time
}

const layeredCols = `layer, layer_id, key, source, value, lower, starts, ends, set_by, updated`

func scanLayered(sc interface{ Scan(...any) error }) (LayeredRow, error) {
	var r LayeredRow
	var st, en sql.NullInt64
	var up int64
	err := sc.Scan(&r.Layer, &r.LayerID, &r.Key, &r.Source, &r.Value, &r.Lower, &st, &en, &r.SetBy, &up)
	if st.Valid {
		r.Starts = fromMS(st.Int64)
	}
	if en.Valid {
		r.Ends = fromMS(en.Int64)
	}
	r.Updated = fromMS(up)
	return r, err
}

func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ms(t)
}

func sourceOf(r LayeredRow) string {
	if r.Source == "" {
		return SourceManual
	}
	return r.Source
}

// ListLayered returns every record of a layer: all sources of every key.
func ListLayered(ctx context.Context, q Q, layer, layerID string) ([]LayeredRow, error) {
	return queryLayered(ctx, q, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ? AND layer_id = ?`, layer, layerID)
}

// ListLayeredLayer returns the records of every layer id of a layer (every
// rule of every plan).
func ListLayeredLayer(ctx context.Context, q Q, layer string) ([]LayeredRow, error) {
	return queryLayered(ctx, q, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ?`, layer)
}

func queryLayered(ctx context.Context, q Q, query string, args ...any) ([]LayeredRow, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LayeredRow
	for rows.Next() {
		r, err := scanLayered(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetLayered returns one record, or ErrNotFound.
func GetLayered(ctx context.Context, q Q, layer, layerID, key, source string) (*LayeredRow, error) {
	r, err := scanLayered(q.QueryRow(ctx, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ? AND layer_id = ? AND key = ? AND source = ?`,
		layer, layerID, key, source))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// PutLayered writes a record, replacing the earlier one of the same layer,
// layer id, key and source. The writes of layered_settings are counted once a
// transaction by its caller (BumpSettingsRev).
func PutLayered(ctx context.Context, q Q, r LayeredRow) error {
	_, err := q.Exec(ctx, `INSERT INTO layered_settings(`+layeredCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (layer, layer_id, key, source) DO UPDATE SET value = excluded.value, lower = excluded.lower,
		starts = excluded.starts, ends = excluded.ends, set_by = excluded.set_by, updated = excluded.updated`,
		r.Layer, r.LayerID, r.Key, sourceOf(r), r.Value, r.Lower, nullMS(r.Starts), nullMS(r.Ends), r.SetBy, ms(r.Updated))
	return err
}

// DeleteLayered removes a record; none is not an error.
func DeleteLayered(ctx context.Context, q Q, layer, layerID, key, source string) error {
	_, err := q.Exec(ctx, `DELETE FROM layered_settings WHERE layer = ? AND layer_id = ? AND key = ? AND source = ?`, layer, layerID, key, source)
	return err
}

// DeleteManualRules removes every manual rule of a tenant (the site's rules
// for it of source manual, dated or not; billing's stay) and answers the ones
// it removed.
func DeleteManualRules(ctx context.Context, q Q, tenant string) ([]LayeredRow, error) {
	where := ` WHERE layer = ? AND layer_id = ? AND source = ?`
	args := []any{LayeredSite, tenant, SourceManual}
	rows, err := queryLayered(ctx, q, `SELECT `+layeredCols+` FROM layered_settings`+where, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM layered_settings`+where, args...); err != nil {
		return nil, err
	}
	return rows, nil
}

// PutLayeredFields writes a tenant administrator's change to a tenant's own
// record: the value when setValue, the rule when setLower. The other field of
// an existing record is kept.
func PutLayeredFields(ctx context.Context, q Q, r LayeredRow, setValue, setLower bool) error {
	set := `updated = excluded.updated`
	if setValue {
		set += `, value = excluded.value`
	}
	if setLower {
		set += `, lower = excluded.lower`
	}
	_, err := q.Exec(ctx, `INSERT INTO layered_settings(layer, layer_id, key, source, value, lower, set_by, updated) VALUES (?, ?, ?, 'manual', ?, ?, ?, ?)
		ON CONFLICT (layer, layer_id, key, source) DO UPDATE SET `+set,
		r.Layer, r.LayerID, r.Key, r.Value, r.Lower, r.SetBy, ms(r.Updated))
	return err
}

// LayeredOfKey returns the records of a key in every layer id of a layer: the
// tenants that have a value or a rule for it.
func LayeredOfKey(ctx context.Context, q Q, layer, key string) ([]LayeredRow, error) {
	return queryLayered(ctx, q, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ? AND key = ?`, layer, key)
}

// BumpSettingsRev adds one to the settings revision and answers it. Every
// write of layered_settings makes it in its own transaction.
func BumpSettingsRev(ctx context.Context, q Q) (int64, error) {
	var rev int64
	err := q.QueryRow(ctx, `INSERT INTO settings_rev(id, rev) VALUES (1, 1)
		ON CONFLICT (id) DO UPDATE SET rev = settings_rev.rev + 1 RETURNING rev`).Scan(&rev)
	return rev, err
}

// SettingsRev is the settings revision: 0 before any write.
func SettingsRev(ctx context.Context, q Q) (int64, error) {
	var rev int64
	err := q.QueryRow(ctx, `SELECT rev FROM settings_rev WHERE id = 1`).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return rev, err
}

// NextRuleBoundary is the earliest start or end of a window after t among the
// rules for tenants (two index seeks); ok is false when none is dated.
func NextRuleBoundary(ctx context.Context, q Q, after time.Time) (next time.Time, ok bool, err error) {
	var a, b sql.NullInt64
	err = q.QueryRow(ctx, `SELECT
		(SELECT MIN(starts) FROM layered_settings WHERE starts > ?),
		(SELECT MIN(ends) FROM layered_settings WHERE ends > ?)`, ms(after), ms(after)).Scan(&a, &b)
	if err != nil {
		return time.Time{}, false, err
	}
	var m int64
	switch {
	case a.Valid && b.Valid:
		m = min(a.Int64, b.Int64)
	case a.Valid:
		m = a.Int64
	case b.Valid:
		m = b.Int64
	default:
		return time.Time{}, false, nil
	}
	return fromMS(m), true, nil
}

// RuleBoundary is a rule whose window started or ended.
type RuleBoundary struct {
	Tenant, Key, Source string
	// Started is true for the start of the window, false for its end.
	Started bool
	At      time.Time
}

// RuleBoundaries lists the starts and ends of windows in (after, upTo].
func RuleBoundaries(ctx context.Context, q Q, after, upTo time.Time) ([]RuleBoundary, error) {
	var out []RuleBoundary
	for _, col := range []string{"starts", "ends"} {
		rows, err := q.Query(ctx, `SELECT layer_id, key, source, `+col+` FROM layered_settings
			WHERE `+col+` > ? AND `+col+` <= ? AND layer = 'site' ORDER BY `+col+`, layer_id, key`, ms(after), ms(upTo))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var b RuleBoundary
			var at int64
			if err := rows.Scan(&b.Tenant, &b.Key, &b.Source, &at); err != nil {
				rows.Close()
				return nil, err
			}
			b.Started, b.At = col == "starts", fromMS(at)
			out = append(out, b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
