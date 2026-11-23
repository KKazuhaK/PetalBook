-- Milestone 3.3: tenants. Every user, space, group, invite and audit row
-- belongs to one tenant; the default tenant (0000000000, slug default) holds
-- everything that existed before, and every existing administrator (and invite
-- of one) becomes a site administrator. A constant DEFAULT on ADD COLUMN
-- rewrites no row on SQLite or on PostgreSQL 11 and later.
--
-- The tenants row of the default tenant, the groups table (name unique per
-- tenant) and the check at the end are made by the step of this version (see
-- tenants.go and migrate.go): rebuilding a table on SQLite needs foreign keys
-- off, which cannot be done inside a transaction.

CREATE TABLE tenants (
  id       TEXT PRIMARY KEY,
  slug     TEXT NOT NULL UNIQUE,
  name     TEXT NOT NULL,
  name_key TEXT NOT NULL DEFAULT '',
  disabled BIGINT NOT NULL DEFAULT 0,
  quota    TEXT NOT NULL DEFAULT '{}',
  created  BIGINT NOT NULL,
  updated  BIGINT NOT NULL
);

ALTER TABLE users ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '0000000000';
ALTER TABLE users ADD COLUMN site_admin BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '0000000000';
ALTER TABLE invites ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '0000000000';
ALTER TABLE invites ADD COLUMN site_admin BIGINT NOT NULL DEFAULT 0;
-- Where a provider's autoCreate puts new accounts.
ALTER TABLE sign_in_providers ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '0000000000';
ALTER TABLE audit_log ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '0000000000';
-- Who vouched for a verified address: '' is the person, a provider or a site
-- administrator (everyone who could before 3.3); 'tenant_admin' is a tenant
-- administrator alone, which does not count for merging a sign-in by address.
ALTER TABLE users ADD COLUMN email_verified_by TEXT NOT NULL DEFAULT '';

UPDATE users SET site_admin = 1 WHERE role = 'admin';
-- An administrator's invite made before keeps its promise: a site administrator.
UPDATE invites SET site_admin = 1 WHERE role = 'admin';

CREATE INDEX users_tenant ON users(tenant_id, created);
CREATE INDEX spaces_tenant ON spaces(tenant_id, created);
CREATE INDEX invites_tenant ON invites(tenant_id);
CREATE INDEX audit_log_tenant ON audit_log(tenant_id, id);

-- The settings of the layers site and tenant (see internal/settings). A row is
-- one layer's value of one key (value: JSON, NULL text means "follow the layer
-- above") and what that layer lets the layers below do (lower: JSON, '' is the
-- key's default). layer_id is '' for the site and the tenant's id for a tenant.
CREATE TABLE layered_settings (
  layer    TEXT NOT NULL,
  layer_id TEXT NOT NULL,
  key      TEXT NOT NULL,
  value    TEXT NOT NULL DEFAULT '',
  lower    TEXT NOT NULL DEFAULT '',
  updated  BIGINT NOT NULL,
  PRIMARY KEY (layer, layer_id, key)
);
