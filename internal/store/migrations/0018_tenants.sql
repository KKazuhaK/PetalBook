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

-- Registration, email verification and password reset (the second part of 3.3).
-- When and where a person accepted the terms (JSON: at, termsUrl, privacyUrl;
-- '' when they never were asked).
ALTER TABLE users ADD COLUMN terms_accepted TEXT NOT NULL DEFAULT '';

-- A registration waiting for its link to be followed. The password is already
-- hashed and the username is held (unique) until the row expires or is made
-- into an account; only the SHA-256 of the token is stored.
CREATE TABLE registrations (
  id            TEXT PRIMARY KEY,
  tenant_id     TEXT NOT NULL,
  username      TEXT NOT NULL,
  name          TEXT NOT NULL,
  email         TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  token_hash    TEXT NOT NULL UNIQUE,
  terms         TEXT NOT NULL DEFAULT '',
  created       BIGINT NOT NULL,
  expires       BIGINT NOT NULL
);
CREATE UNIQUE INDEX registrations_username ON registrations(username);
CREATE INDEX registrations_tenant ON registrations(tenant_id, expires);

-- Links that confirm an address ('verify') or reset a password ('reset'): the
-- token itself is never stored, only its SHA-256.
CREATE TABLE email_tokens (
  hash    TEXT PRIMARY KEY,
  kind    TEXT NOT NULL,
  user_id TEXT NOT NULL,
  email   TEXT NOT NULL,
  created BIGINT NOT NULL,
  expires BIGINT NOT NULL,
  used    BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX email_tokens_user ON email_tokens(user_id, kind);
CREATE INDEX email_tokens_expires ON email_tokens(expires);

-- Quotas and usage (the third part of 3.3).
-- A person's own limits (JSON: spaces, bytes, pages; '' follows the tenant and
-- the site).
ALTER TABLE users ADD COLUMN quota TEXT NOT NULL DEFAULT '';
-- What the background measurement found in a space: its pages (a COUNT on its
-- index) and the bytes of its files, and the person its numbers are counted
-- for now (its first owner; '' when it has none), so that moving a space moves
-- exactly what was added.
ALTER TABLE spaces ADD COLUMN page_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN usage_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN usage_user TEXT NOT NULL DEFAULT '';
CREATE INDEX spaces_usage_user ON spaces(usage_user);
-- What an invite fills in on the form that accepts it.
ALTER TABLE invites ADD COLUMN username TEXT NOT NULL DEFAULT '';
ALTER TABLE invites ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE invites ADD COLUMN email TEXT NOT NULL DEFAULT '';

-- One row per person ('user') and per tenant ('tenant'): what they use, so that
-- reading anyone's usage is one row. Kept in step by the statements that change
-- a space, a member or a measurement, and recounted from the rows themselves
-- once a day. 'over' lists the quotas the row is over (so that crossing one is
-- audited once), 'measured' is when a measurement last changed it.
CREATE TABLE usage (
  kind      TEXT NOT NULL,
  id        TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  spaces    BIGINT NOT NULL DEFAULT 0,
  bytes     BIGINT NOT NULL DEFAULT 0,
  pages     BIGINT NOT NULL DEFAULT 0,
  people    BIGINT NOT NULL DEFAULT 0,
  over      TEXT NOT NULL DEFAULT '',
  measured  BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, id)
);
-- The list of people by bytes: within a tenant, and across all of them (a site administrator's).
CREATE INDEX usage_rank ON usage(kind, tenant_id, bytes DESC, id);
CREATE INDEX usage_rank_all ON usage(kind, bytes DESC, id);

-- What the 1.7 measurement found is the first value of usage_bytes.
UPDATE spaces SET usage_bytes = size_bytes WHERE size_bytes IS NOT NULL;
