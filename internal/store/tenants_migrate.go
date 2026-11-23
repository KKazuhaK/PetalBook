package store

import (
	"context"
	"fmt"
	"time"
)

// DefaultTenantID is the tenant every row that existed before 3.3 belongs to.
const DefaultTenantID = "0000000000"

// DefaultTenantSlug is the slug of the default tenant.
const DefaultTenantSlug = "default"

// tenantsStep is the Go part of migration 18. It runs in the transaction of
// the version, after the statements of its file, and on SQLite with foreign
// keys switched off around the transaction (see outsideSteps).
func tenantsStep(d Dialect) func(ctx context.Context, q Q) error {
	return func(ctx context.Context, q Q) error {
		now := ms(time.Now())
		if _, err := q.Exec(ctx, `INSERT INTO tenants(id, slug, name, name_key, disabled, quota, created, updated) VALUES (?, ?, 'Default', ?, 0, '{}', ?, ?)`,
			DefaultTenantID, DefaultTenantSlug, FoldName("Default"), now, now); err != nil {
			return err
		}
		switch d {
		case Postgres:
			// Group names become unique within a tenant: a dropped constraint and
			// a new index; nothing is rewritten but the catalogue.
			for _, st := range []string{
				`ALTER TABLE groups ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '0000000000'`,
				`ALTER TABLE groups DROP CONSTRAINT groups_name_key_key`,
				`CREATE UNIQUE INDEX groups_tenant_name ON groups(tenant_id, name_key)`,
			} {
				if _, err := q.Exec(ctx, st); err != nil {
					return err
				}
			}
		default:
			// SQLite cannot drop a UNIQUE constraint: rebuild the table (the
			// procedure of the SQLite documentation, "Making Other Kinds Of Table
			// Schema Changes"). Foreign keys are off, so dropping groups does not
			// cascade into group_members and space_groups, whose references name
			// the table and find the new one again.
			for _, st := range []string{
				`CREATE TABLE groups_new (
				  id          TEXT PRIMARY KEY,
				  tenant_id   TEXT NOT NULL DEFAULT '0000000000',
				  name        TEXT NOT NULL,
				  name_key    TEXT NOT NULL,
				  description TEXT NOT NULL DEFAULT '',
				  source      TEXT NOT NULL,
				  created     BIGINT NOT NULL,
				  updated     BIGINT NOT NULL,
				  UNIQUE (tenant_id, name_key)
				)`,
				`INSERT INTO groups_new(id, tenant_id, name, name_key, description, source, created, updated)
				  SELECT id, '0000000000', name, name_key, description, source, created, updated FROM groups`,
				`DROP TABLE groups`,
				`ALTER TABLE groups_new RENAME TO groups`,
			} {
				if _, err := q.Exec(ctx, st); err != nil {
					return err
				}
			}
		}
		// What everyone uses, from the rows that exist: the usage rows start
		// right (the same pass that corrects them every day).
		_, err := recountUsage(ctx, q)
		return err
	}
}

// checkForeignKeys is the check that ends a step that ran with foreign keys
// off: a row that points nowhere makes the migration fail and roll back.
func checkForeignKeys(ctx context.Context, q Q) error {
	rows, err := q.Query(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table, rowid, parent, fk any
		rows.Scan(&table, &rowid, &parent, &fk)
		return fmt.Errorf("foreign key check failed after the rebuild: a row of %v points to a missing row of %v", table, parent)
	}
	return rows.Err()
}
