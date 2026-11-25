package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LayeredRow is one layer's record of one setting (see internal/settings):
// the value as JSON ("" follows the layer above) and what the layer lets the
// layers below do, as JSON ("" is the key's default). layer is "site" or
// "tenant"; layerID is "" for the site and the tenant's id for a tenant.
type LayeredRow struct {
	Layer, LayerID, Key string
	Value, Lower        string
	// SiteLocked is a tenant's row whose value a site administrator set and
	// locked for that tenant: the tenant's own administrators cannot change it.
	SiteLocked bool
	Updated    time.Time
}

const layeredCols = `layer, layer_id, key, value, lower, site_locked, updated`

func scanLayered(sc interface{ Scan(...any) error }) (LayeredRow, error) {
	var r LayeredRow
	var locked, up int64
	err := sc.Scan(&r.Layer, &r.LayerID, &r.Key, &r.Value, &r.Lower, &locked, &up)
	r.SiteLocked, r.Updated = locked != 0, fromMS(up)
	return r, err
}

// ListLayered returns every record of a layer.
func ListLayered(ctx context.Context, q Q, layer, layerID string) ([]LayeredRow, error) {
	rows, err := q.Query(ctx, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ? AND layer_id = ?`, layer, layerID)
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
func GetLayered(ctx context.Context, q Q, layer, layerID, key string) (*LayeredRow, error) {
	r, err := scanLayered(q.QueryRow(ctx, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ? AND layer_id = ? AND key = ?`,
		layer, layerID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// PutLayered writes a record, replacing the layer's earlier one of the key.
func PutLayered(ctx context.Context, q Q, r LayeredRow) error {
	_, err := q.Exec(ctx, `INSERT INTO layered_settings(layer, layer_id, key, value, lower, site_locked, updated) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (layer, layer_id, key) DO UPDATE SET value = excluded.value, lower = excluded.lower, site_locked = excluded.site_locked, updated = excluded.updated`,
		r.Layer, r.LayerID, r.Key, r.Value, r.Lower, b2i(r.SiteLocked), ms(r.Updated))
	return err
}

// PutLayeredUnlocked writes a tenant administrator's change to a tenant's
// record: the value when setValue, the rule when setLower, never site_locked.
// It is one statement, so a site lock committed by another transaction at any
// moment is seen: an existing record is changed only while it is not locked,
// and a new one starts unlocked (a site lock on a missing record inserts it too,
// so the two meet on the conflict). It reports false, writing nothing, when the
// record is locked.
func PutLayeredUnlocked(ctx context.Context, q Q, r LayeredRow, setValue, setLower bool) (bool, error) {
	set := `updated = excluded.updated`
	if setValue {
		set += `, value = excluded.value`
	}
	if setLower {
		set += `, lower = excluded.lower`
	}
	res, err := q.Exec(ctx, `INSERT INTO layered_settings(layer, layer_id, key, value, lower, site_locked, updated) VALUES (?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT (layer, layer_id, key) DO UPDATE SET `+set+` WHERE layered_settings.site_locked = 0`,
		r.Layer, r.LayerID, r.Key, r.Value, r.Lower, ms(r.Updated))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// LayeredOfKey returns the records of a key in every layer id of a layer: the
// tenants that have a value or a rule for it.
func LayeredOfKey(ctx context.Context, q Q, layer, key string) ([]LayeredRow, error) {
	rows, err := q.Query(ctx, `SELECT `+layeredCols+` FROM layered_settings WHERE layer = ? AND key = ?`, layer, key)
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
