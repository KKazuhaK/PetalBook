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
	Updated             time.Time
}

// ListLayered returns every record of a layer.
func ListLayered(ctx context.Context, q Q, layer, layerID string) ([]LayeredRow, error) {
	rows, err := q.Query(ctx, `SELECT layer, layer_id, key, value, lower, updated FROM layered_settings WHERE layer = ? AND layer_id = ?`, layer, layerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LayeredRow
	for rows.Next() {
		var r LayeredRow
		var up int64
		if err := rows.Scan(&r.Layer, &r.LayerID, &r.Key, &r.Value, &r.Lower, &up); err != nil {
			return nil, err
		}
		r.Updated = fromMS(up)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetLayered returns one record, or ErrNotFound.
func GetLayered(ctx context.Context, q Q, layer, layerID, key string) (*LayeredRow, error) {
	var r LayeredRow
	var up int64
	err := q.QueryRow(ctx, `SELECT layer, layer_id, key, value, lower, updated FROM layered_settings WHERE layer = ? AND layer_id = ? AND key = ?`,
		layer, layerID, key).Scan(&r.Layer, &r.LayerID, &r.Key, &r.Value, &r.Lower, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.Updated = fromMS(up)
	return &r, err
}

// PutLayered writes a record, replacing the layer's earlier one of the key.
func PutLayered(ctx context.Context, q Q, r LayeredRow) error {
	_, err := q.Exec(ctx, `INSERT INTO layered_settings(layer, layer_id, key, value, lower, updated) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (layer, layer_id, key) DO UPDATE SET value = excluded.value, lower = excluded.lower, updated = excluded.updated`,
		r.Layer, r.LayerID, r.Key, r.Value, r.Lower, ms(r.Updated))
	return err
}

// LayeredOfKey returns the records of a key in every layer id of a layer: the
// tenants that have a value or a rule for it.
func LayeredOfKey(ctx context.Context, q Q, layer, key string) ([]LayeredRow, error) {
	rows, err := q.Query(ctx, `SELECT layer, layer_id, key, value, lower, updated FROM layered_settings WHERE layer = ? AND key = ?`, layer, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LayeredRow
	for rows.Next() {
		var r LayeredRow
		var up int64
		if err := rows.Scan(&r.Layer, &r.LayerID, &r.Key, &r.Value, &r.Lower, &up); err != nil {
			return nil, err
		}
		r.Updated = fromMS(up)
		out = append(out, r)
	}
	return out, rows.Err()
}
