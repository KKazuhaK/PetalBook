-- The schema of PetalBook's state database, for SQLite and PostgreSQL alike.
-- It replaces the migrations 0001 to 0020 of the milestones up to 3.5: nothing
-- was ever deployed, so there is no upgrade path from them, and a development
-- or demonstration database made by them must be made again (docs/design.md,
-- "数据库基线（2026-11-24）"). The next change of the schema is 0002.
--
-- One schema for both databases: TEXT and BIGINT columns only, times as Unix
-- milliseconds, booleans as 0/1. Statements end with a semicolon at the end of
-- a line; keep semicolons out of everything else in this file. A comment line
-- "-- dialect: postgres" (or sqlite) right before a statement runs it on that
-- database only (see migrate.go).

-- Tenants (3.3). Every user, space, group, invite, registration, usage row,
-- recycle item, export and bulk job belongs to one, by a foreign key. The
-- default tenant (0000000000, slug default) is made by the Go step of this
-- version (migrate.go), with the time it was made.
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

-- People. username is stored in lower case and unique on the whole server.
-- email_verified_by says who vouched for a verified address ('' the person, a
-- provider or a site administrator; 'tenant_admin' a tenant administrator
-- alone, which does not count for merging a sign-in by address).
-- terms_accepted is JSON (at, termsUrl, privacyUrl; '' when never asked), quota
-- a person's own limits (JSON; '' follows the tenant and the site).
-- deleted_at is when the person was deleted (NULL for everyone else) and
-- recycle_item the item that holds what was taken from them; delete_pending is
-- a deletion under way (JSON, '' when none is). last_active (a session or a
-- token, written at most once an hour) and first_write (the first change in a
-- space) are NULL until then.
CREATE TABLE users (
  id                TEXT PRIMARY KEY,
  tenant_id         TEXT NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  username          TEXT NOT NULL UNIQUE,
  name              TEXT NOT NULL,
  email             TEXT NOT NULL DEFAULT '',
  role              TEXT NOT NULL,
  disabled          BIGINT NOT NULL DEFAULT 0,
  created           BIGINT NOT NULL,
  email_verified    BIGINT NOT NULL DEFAULT 0,
  email_verified_by TEXT NOT NULL DEFAULT '',
  passkey_required  BIGINT NOT NULL DEFAULT 0,
  remote_exempt     BIGINT NOT NULL DEFAULT 0,
  site_admin        BIGINT NOT NULL DEFAULT 0,
  terms_accepted    TEXT NOT NULL DEFAULT '',
  quota             TEXT NOT NULL DEFAULT '',
  deleted_at        BIGINT,
  recycle_item      TEXT NOT NULL DEFAULT '',
  delete_pending    TEXT NOT NULL DEFAULT '',
  last_active       BIGINT,
  first_write       BIGINT
);
CREATE INDEX users_created ON users(created);
CREATE INDEX users_role ON users(role, disabled);
CREATE INDEX users_tenant ON users(tenant_id, created);
-- The lists of people read this partial index, which holds only those who are
-- not deleted.
CREATE INDEX users_live ON users(tenant_id, created, id) WHERE deleted_at IS NULL;

CREATE TABLE passwords (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  hash    TEXT NOT NULL,
  updated BIGINT NOT NULL
);

CREATE TABLE identities (
  id      TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  issuer  TEXT NOT NULL,
  subject TEXT NOT NULL,
  label   TEXT NOT NULL DEFAULT '',
  created BIGINT NOT NULL,
  UNIQUE (issuer, subject)
);
CREATE INDEX identities_user ON identities(user_id);

-- A held session may only register a passkey and sign out.
CREATE TABLE sessions (
  id        TEXT PRIMARY KEY,
  user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created   BIGINT NOT NULL,
  last_used BIGINT NOT NULL,
  held      BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX sessions_user ON sessions(user_id, last_used);
CREATE INDEX sessions_last_used ON sessions(last_used);
CREATE INDEX sessions_created ON sessions(created);

-- Why a session ended without the person signing out (the sign-in page says so
-- when they come back). One row is kept for 30 days with the SHA-256 of the
-- session cookie value (never the value), the reason and the time; the daily
-- sweep deletes by session_ends_ended. It is looked up only by the hash of a
-- cookie the browser sent. user_id is whose it was, so that enabling a person
-- (or their tenant) again can forget their `disabled` records. No foreign key:
-- the record of a deleted person goes with the daily sweep like any other.
CREATE TABLE session_ends (
  id      TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  reason  TEXT NOT NULL,
  ended   BIGINT NOT NULL
);
CREATE INDEX session_ends_ended ON session_ends(ended);
CREATE INDEX session_ends_user ON session_ends(user_id);

-- username, name and email are what an invite fills in on the form that
-- accepts it; site_admin makes the account a site administrator.
CREATE TABLE invites (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  token_hash TEXT NOT NULL UNIQUE,
  role       TEXT NOT NULL,
  site_admin BIGINT NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created    BIGINT NOT NULL,
  expires    BIGINT NOT NULL,
  used       BIGINT NOT NULL DEFAULT 0,
  used_by    TEXT NOT NULL DEFAULT '',
  username   TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  email      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX invites_expires ON invites(expires);
CREATE INDEX invites_tenant ON invites(tenant_id);

-- Spaces. name_key is the name folded by Go (FoldName), so that both databases
-- search and sort the same. inbox is the folder quick notes go to. size_bytes
-- and size_at are the background measurement (NULL until the first one).
-- last_used, cache_state, pinned, exempt and index_bytes are the server storage
-- of 3.1. page_count and usage_bytes are what the quotas count, usage_user the
-- person the space is counted for (its first owner, '' when none). location
-- says where the bytes live (server, own, tenant, site), remote_via is '' without
-- a remote, the provider of a connection or 'git', remote_state the remote's
-- state ('' without one).
CREATE TABLE spaces (
  id           TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  name         TEXT NOT NULL,
  name_key     TEXT NOT NULL DEFAULT '',
  personal_of  TEXT UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  created      BIGINT NOT NULL,
  inbox        TEXT NOT NULL DEFAULT 'Inbox',
  disabled     BIGINT NOT NULL DEFAULT 0,
  size_bytes   BIGINT,
  size_at      BIGINT,
  last_used    BIGINT NOT NULL DEFAULT 0,
  cache_state  TEXT NOT NULL DEFAULT '',
  pinned       BIGINT NOT NULL DEFAULT 0,
  exempt       BIGINT NOT NULL DEFAULT 0,
  index_bytes  BIGINT NOT NULL DEFAULT 0,
  page_count   BIGINT NOT NULL DEFAULT 0,
  usage_bytes  BIGINT NOT NULL DEFAULT 0,
  usage_user   TEXT NOT NULL DEFAULT '',
  location     TEXT NOT NULL DEFAULT 'server',
  remote_via   TEXT NOT NULL DEFAULT '',
  remote_state TEXT NOT NULL DEFAULT ''
);
CREATE INDEX spaces_size_at ON spaces(size_at);
CREATE INDEX spaces_last_used ON spaces(last_used);
CREATE INDEX spaces_usage_user ON spaces(usage_user);
-- The lists of spaces: one index for each sort, within a tenant and across all
-- of them; the filters that pick a few rows (a pin, an exemption) or a column
-- (where the remote stands) have their own.
CREATE INDEX spaces_created_t ON spaces(tenant_id, created, id);
CREATE INDEX spaces_created ON spaces(created, id);
CREATE INDEX spaces_name_t ON spaces(tenant_id, name_key, id);
CREATE INDEX spaces_name ON spaces(name_key, id);
CREATE INDEX spaces_bytes_t ON spaces(tenant_id, size_bytes DESC, id);
CREATE INDEX spaces_bytes ON spaces(size_bytes DESC, id);
CREATE INDEX spaces_used_t ON spaces(tenant_id, last_used DESC, id);
CREATE INDEX spaces_used ON spaces(last_used DESC, id);
CREATE INDEX spaces_via_t ON spaces(tenant_id, remote_via, created, id);
CREATE INDEX spaces_via ON spaces(remote_via, created, id);
CREATE INDEX spaces_rstate_t ON spaces(tenant_id, remote_state, created, id);
CREATE INDEX spaces_rstate ON spaces(remote_state, created, id);
CREATE INDEX spaces_pinned ON spaces(created, id) WHERE pinned <> 0;
CREATE INDEX spaces_exempt ON spaces(created, id) WHERE exempt <> 0;
CREATE INDEX spaces_owner ON spaces(usage_user, created, id);

CREATE TABLE space_members (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role     TEXT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (space_id, user_id)
);
CREATE INDEX space_members_user ON space_members(user_id);

-- Every stored draft has an opaque version, so two devices of one person
-- cannot silently replace each other's draft.
CREATE TABLE drafts (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  page_id  TEXT NOT NULL,
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  base_rev TEXT NOT NULL,
  meta     TEXT NOT NULL,
  body     TEXT NOT NULL,
  version  TEXT NOT NULL DEFAULT '',
  created  BIGINT NOT NULL,
  updated  BIGINT NOT NULL,
  held     BIGINT NOT NULL DEFAULT 0,
  due      BIGINT NOT NULL,
  PRIMARY KEY (space_id, page_id, user_id)
);
CREATE INDEX drafts_due ON drafts(held, due);

-- The remote repository a space follows, and its credential. A row with an
-- empty url is a space that only has an SSH key pair so far. Tokens and private
-- keys are stored encrypted (see the secret package). provider is the provider
-- a remote goes through: 'git' for a plain address with a key or a token,
-- otherwise a connection's provider.
CREATE TABLE remotes (
  space_id           TEXT PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  url                TEXT NOT NULL DEFAULT '',
  branch             TEXT NOT NULL DEFAULT 'main',
  cred_type          TEXT NOT NULL DEFAULT '',
  username           TEXT NOT NULL DEFAULT '',
  token_enc          TEXT NOT NULL DEFAULT '',
  privkey_enc        TEXT NOT NULL DEFAULT '',
  pubkey             TEXT NOT NULL DEFAULT '',
  last_good          TEXT NOT NULL DEFAULT '',
  state              TEXT NOT NULL DEFAULT 'local',
  error_kind         TEXT NOT NULL DEFAULT '',
  last_error         TEXT NOT NULL DEFAULT '',
  remote_tip         TEXT NOT NULL DEFAULT '',
  last_sync          BIGINT NOT NULL DEFAULT 0,
  updated            BIGINT NOT NULL,
  provider           TEXT NOT NULL DEFAULT 'git',
  gh_via             TEXT NOT NULL DEFAULT '',
  gh_installation_id BIGINT NOT NULL DEFAULT 0,
  gh_repo_id         BIGINT NOT NULL DEFAULT 0,
  gh_user_id         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX remotes_github_installation ON remotes(gh_installation_id);
CREATE INDEX remotes_github_user ON remotes(gh_user_id);

-- The attachment repository of a space (3.1): a second remote with the same
-- columns as the space's own, a mark for "an attachment has gone to it" and the
-- progress of moving the attachments of the space into it.
CREATE TABLE asset_remotes (
  space_id           TEXT PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  url                TEXT NOT NULL DEFAULT '',
  branch             TEXT NOT NULL DEFAULT 'main',
  cred_type          TEXT NOT NULL DEFAULT '',
  username           TEXT NOT NULL DEFAULT '',
  token_enc          TEXT NOT NULL DEFAULT '',
  privkey_enc        TEXT NOT NULL DEFAULT '',
  pubkey             TEXT NOT NULL DEFAULT '',
  last_good          TEXT NOT NULL DEFAULT '',
  state              TEXT NOT NULL DEFAULT 'ok',
  error_kind         TEXT NOT NULL DEFAULT '',
  last_error         TEXT NOT NULL DEFAULT '',
  remote_tip         TEXT NOT NULL DEFAULT '',
  last_sync          BIGINT NOT NULL DEFAULT 0,
  updated            BIGINT NOT NULL,
  provider           TEXT NOT NULL DEFAULT 'git',
  gh_via             TEXT NOT NULL DEFAULT '',
  gh_installation_id BIGINT NOT NULL DEFAULT 0,
  gh_repo_id         BIGINT NOT NULL DEFAULT 0,
  gh_user_id         TEXT NOT NULL DEFAULT '',
  used               BIGINT NOT NULL DEFAULT 0,
  move_state         TEXT NOT NULL DEFAULT '',
  move_files         BIGINT NOT NULL DEFAULT 0,
  move_files_done    BIGINT NOT NULL DEFAULT 0,
  move_bytes         BIGINT NOT NULL DEFAULT 0,
  move_bytes_done    BIGINT NOT NULL DEFAULT 0,
  move_error         TEXT NOT NULL DEFAULT '',
  move_updated       BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX asset_remotes_move ON asset_remotes(move_state);

-- Server-wide settings as text by key (most are JSON): the daily text, the
-- security settings, the storage policy, the GitHub App, and 'first_user', the
-- time the first account was made (MarkFirstUser).
CREATE TABLE site_settings (
  key     TEXT PRIMARY KEY,
  value   TEXT NOT NULL,
  updated BIGINT NOT NULL
);

-- The settings of the layers site and tenant (see internal/settings). A row is
-- one layer's value of one key and what that layer lets the layers below do.
-- layer_id is '' for the site and the tenant's id for a tenant.
CREATE TABLE layered_settings (
  layer    TEXT NOT NULL,
  layer_id TEXT NOT NULL,
  key      TEXT NOT NULL,
  value    TEXT NOT NULL DEFAULT '',
  lower    TEXT NOT NULL DEFAULT '',
  updated  BIGINT NOT NULL,
  PRIMARY KEY (layer, layer_id, key)
);

-- The daily text: the lines an administrator pasted or uploaded, a space's
-- choice of the server's sources, and what the external interfaces said
-- (kept seven days).
CREATE TABLE daily_lines (
  position BIGINT PRIMARY KEY,
  lang     TEXT NOT NULL DEFAULT '',
  body     TEXT NOT NULL,
  author   TEXT NOT NULL DEFAULT '',
  source   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX daily_lines_lang ON daily_lines(lang, position);

CREATE TABLE space_daily (
  space_id TEXT PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  inherit  BIGINT NOT NULL DEFAULT 1,
  apis     TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE daily_cache (
  api_key TEXT NOT NULL,
  lang    TEXT NOT NULL,
  period  TEXT NOT NULL,
  skip    BIGINT NOT NULL,
  body    TEXT NOT NULL,
  fetched BIGINT NOT NULL,
  PRIMARY KEY (api_key, lang, period, skip)
);
CREATE INDEX daily_cache_latest ON daily_cache(api_key, lang, fetched);
CREATE INDEX daily_cache_fetched ON daily_cache(fetched);
CREATE INDEX daily_cache_api ON daily_cache(api_key, fetched);

-- A person's favourites in a space: pages, folders and saved views, in the
-- order the person put them. kind is page, folder or view.
CREATE TABLE favorites (
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  kind     TEXT NOT NULL,
  target   TEXT NOT NULL,
  position BIGINT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (user_id, space_id, kind, target)
);
CREATE INDEX favorites_order ON favorites(user_id, space_id, position);
CREATE INDEX favorites_space ON favorites(space_id);

-- Folder moves whose favourites have not been made to follow yet.
CREATE TABLE folder_moves (
  space_id  TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  from_path TEXT NOT NULL,
  to_path   TEXT NOT NULL,
  created   BIGINT NOT NULL,
  PRIMARY KEY (space_id, from_path, to_path)
);

-- API tokens (2.6). Only the SHA-256 of the secret is kept, with its first
-- characters. A token's spaces are not foreign keys: a deleted space stays
-- listed and does nothing.
CREATE TABLE api_tokens (
  id        TEXT PRIMARY KEY,
  user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name      TEXT NOT NULL,
  prefix    TEXT NOT NULL,
  hash      TEXT NOT NULL,
  access    TEXT NOT NULL,
  created   BIGINT NOT NULL,
  expires   BIGINT NOT NULL DEFAULT 0,
  last_used BIGINT NOT NULL DEFAULT 0,
  revoked   BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX api_tokens_hash ON api_tokens(hash);
CREATE INDEX api_tokens_user ON api_tokens(user_id, created);
CREATE INDEX api_tokens_created ON api_tokens(created, id);

CREATE TABLE api_token_spaces (
  token_id TEXT NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
  space_id TEXT NOT NULL,
  PRIMARY KEY (token_id, space_id)
);

-- The authorization server for MCP clients (2.6): the signing key, the clients
-- that registered themselves, and the grants (one approval of one client by
-- one person, with the hash of its refresh token and of the few replaced ones).
CREATE TABLE oauth_keys (
  id      TEXT PRIMARY KEY,
  sealed  TEXT NOT NULL,
  created BIGINT NOT NULL
);

CREATE TABLE oauth_clients (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,
  fingerprint   TEXT NOT NULL,
  created       BIGINT NOT NULL,
  last_used     BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX oauth_clients_fingerprint ON oauth_clients(fingerprint);
CREATE INDEX oauth_clients_created ON oauth_clients(created);

CREATE TABLE oauth_grants (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id    TEXT NOT NULL,
  client_name  TEXT NOT NULL,
  access       TEXT NOT NULL,
  resource     TEXT NOT NULL,
  created      BIGINT NOT NULL,
  last_used    BIGINT NOT NULL,
  refresh_hash TEXT NOT NULL
);
CREATE INDEX oauth_grants_user ON oauth_grants(user_id, created);
CREATE INDEX oauth_grants_created ON oauth_grants(created, id);
CREATE INDEX oauth_grants_client ON oauth_grants(client_id);
CREATE INDEX oauth_grants_used ON oauth_grants(last_used);

CREATE TABLE oauth_grant_spaces (
  grant_id TEXT NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
  space_id TEXT NOT NULL,
  PRIMARY KEY (grant_id, space_id)
);

CREATE TABLE oauth_retired (
  grant_id TEXT NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
  hash     TEXT NOT NULL,
  at       BIGINT NOT NULL,
  PRIMARY KEY (grant_id, hash)
);

-- The GitHub provider (2.7): users' authorisations (tokens sealed; version
-- counts replacements), the App's installations, the tenants each is shared
-- with, and reminders of empty repositories a failed attempt left.
CREATE TABLE github_authorizations (
  user_id         TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  github_id       BIGINT NOT NULL UNIQUE,
  login           TEXT NOT NULL,
  access_enc      TEXT NOT NULL DEFAULT '',
  refresh_enc     TEXT NOT NULL DEFAULT '',
  access_expires  BIGINT NOT NULL DEFAULT 0,
  refresh_expires BIGINT NOT NULL DEFAULT 0,
  state           TEXT NOT NULL DEFAULT 'ok',
  version         BIGINT NOT NULL DEFAULT 1,
  connected       BIGINT NOT NULL,
  updated         BIGINT NOT NULL
);
CREATE INDEX github_authorizations_renew ON github_authorizations(state, refresh_expires, user_id);

CREATE TABLE github_installations (
  id           BIGINT PRIMARY KEY,
  account      TEXT NOT NULL,
  account_type TEXT NOT NULL DEFAULT 'Organization',
  updated      BIGINT NOT NULL
);

CREATE TABLE github_installation_tenants (
  installation_id BIGINT NOT NULL REFERENCES github_installations(id) ON DELETE CASCADE,
  tenant_id       TEXT NOT NULL REFERENCES tenants(id),
  PRIMARY KEY (installation_id, tenant_id)
);
CREATE INDEX github_installation_tenants_tenant ON github_installation_tenants(tenant_id, installation_id);

CREATE TABLE github_leftovers (
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id    BIGINT NOT NULL,
  full_name  TEXT NOT NULL,
  html_url   TEXT NOT NULL,
  space_name TEXT NOT NULL DEFAULT '',
  created    BIGINT NOT NULL,
  PRIMARY KEY (user_id, repo_id)
);
CREATE INDEX github_leftovers_repo ON github_leftovers(repo_id);

-- Sign-in providers (2.8). config is JSON with nothing secret in it,
-- secret_enc the sealed client secret (or SAML signing key), cache the
-- discovery document or the identity provider's metadata. issuer is checked
-- unique in code, because '' may repeat. tenant_id is where autoCreate puts new
-- accounts; groups_version is bumped by every change of the row.
CREATE TABLE sign_in_providers (
  slug                 TEXT PRIMARY KEY,
  kind                 TEXT NOT NULL,
  name                 TEXT NOT NULL,
  icon                 TEXT NOT NULL DEFAULT '',
  enabled              BIGINT NOT NULL DEFAULT 0,
  position             BIGINT NOT NULL DEFAULT 0,
  auto_create          BIGINT NOT NULL DEFAULT 0,
  merge_verified_email BIGINT NOT NULL DEFAULT 0,
  legacy_callback      BIGINT NOT NULL DEFAULT 0,
  issuer               TEXT NOT NULL DEFAULT '',
  config               TEXT NOT NULL DEFAULT '{}',
  secret_enc           TEXT NOT NULL DEFAULT '',
  cache                TEXT NOT NULL DEFAULT '',
  status_ok            BIGINT NOT NULL DEFAULT 0,
  status_checked       BIGINT NOT NULL DEFAULT 0,
  status_message       TEXT NOT NULL DEFAULT '',
  created              BIGINT NOT NULL,
  updated              BIGINT NOT NULL,
  groups_version       BIGINT NOT NULL DEFAULT 0,
  tenant_id            TEXT NOT NULL DEFAULT '0000000000' REFERENCES tenants(id)
);
CREATE INDEX sign_in_providers_issuer ON sign_in_providers(issuer);

-- credential is the JSON of the WebAuthn credential; rp_id is the host the
-- passkey was made for.
CREATE TABLE passkeys (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id TEXT NOT NULL UNIQUE,
  credential    TEXT NOT NULL,
  name          TEXT NOT NULL,
  rp_id         TEXT NOT NULL DEFAULT '',
  synced        BIGINT NOT NULL DEFAULT 0,
  created       BIGINT NOT NULL,
  last_used     BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX passkeys_user ON passkeys(user_id);

-- The audit log. tenant_id is a plain column, not a foreign key: the record of
-- what happened in a tenant outlives the tenant.
CREATE TABLE audit_log (
  id        TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL DEFAULT '0000000000',
  at        BIGINT NOT NULL,
  kind      TEXT NOT NULL,
  actor     TEXT NOT NULL DEFAULT '',
  user_id   TEXT NOT NULL DEFAULT '',
  username  TEXT NOT NULL DEFAULT '',
  ip        TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at ON audit_log(at);
CREATE INDEX audit_log_kind ON audit_log(kind, id);
CREATE INDEX audit_log_user ON audit_log(user_id, id);
CREATE INDEX audit_log_tenant ON audit_log(tenant_id, id);
-- The daily sweep of tombstones asks whether the audit log still names a person.
CREATE INDEX audit_log_actor ON audit_log(actor);

-- Groups (3.2): a set of people that can be a member of a shared space with a
-- role. source is 'local' or 'synced'; name_key is the folded name, unique
-- within a tenant. A synced membership says where it came from (provider and
-- claim value; both '' for a local group).
CREATE TABLE groups (
  id          TEXT PRIMARY KEY,
  tenant_id   TEXT NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  name        TEXT NOT NULL,
  name_key    TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL,
  created     BIGINT NOT NULL,
  updated     BIGINT NOT NULL,
  UNIQUE (tenant_id, name_key)
);

CREATE TABLE group_members (
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL DEFAULT '',
  value    TEXT NOT NULL DEFAULT '',
  created  BIGINT NOT NULL,
  PRIMARY KEY (group_id, user_id, provider, value)
);
CREATE INDEX group_members_user ON group_members(user_id);

CREATE TABLE space_groups (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  role     TEXT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (space_id, group_id)
);
CREATE INDEX space_groups_group ON space_groups(group_id);

-- A registration waiting for its link to be followed. The password is already
-- hashed and the username is held (unique) until the row expires or is made
-- into an account; only the SHA-256 of the token is stored.
CREATE TABLE registrations (
  id            TEXT PRIMARY KEY,
  tenant_id     TEXT NOT NULL REFERENCES tenants(id),
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

-- Links that confirm an address ('verify') or reset a password ('reset'): only
-- the SHA-256 of the token is stored.
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

-- One row per person ('user') and per tenant ('tenant'): what they use, so that
-- reading anyone's usage is one row. Kept in step by the statements that change
-- a space, a member or a measurement, and recounted once a day. 'over' lists the
-- quotas the row is over, 'measured' is when a measurement last changed it.
-- held_bytes and the counts of spaces are what the storage lists show; a
-- tenant's row holds the sums over its people and spaces. created and
-- last_active copy the person's, so that a list of people is one index.
CREATE TABLE usage (
  kind            TEXT NOT NULL,
  id              TEXT NOT NULL,
  tenant_id       TEXT NOT NULL REFERENCES tenants(id),
  spaces          BIGINT NOT NULL DEFAULT 0,
  bytes           BIGINT NOT NULL DEFAULT 0,
  pages           BIGINT NOT NULL DEFAULT 0,
  people          BIGINT NOT NULL DEFAULT 0,
  over            TEXT NOT NULL DEFAULT '',
  measured        BIGINT NOT NULL DEFAULT 0,
  held_bytes      BIGINT NOT NULL DEFAULT 0,
  remote_spaces   BIGINT NOT NULL DEFAULT 0,
  partial_spaces  BIGINT NOT NULL DEFAULT 0,
  grace_spaces    BIGINT NOT NULL DEFAULT 0,
  overdue_spaces  BIGINT NOT NULL DEFAULT 0,
  evicted_spaces  BIGINT NOT NULL DEFAULT 0,
  archived_spaces BIGINT NOT NULL DEFAULT 0,
  pinned_spaces   BIGINT NOT NULL DEFAULT 0,
  connected       BIGINT NOT NULL DEFAULT 0,
  dormant         BIGINT NOT NULL DEFAULT 0,
  created         BIGINT NOT NULL DEFAULT 0,
  last_active     BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, id)
);
-- The lists of people: one index for each sort, within a tenant and across all
-- of them (a site administrator's).
CREATE INDEX usage_rank ON usage(kind, tenant_id, bytes DESC, id);
CREATE INDEX usage_rank_all ON usage(kind, bytes DESC, id);
CREATE INDEX usage_held ON usage(kind, tenant_id, held_bytes DESC, id);
CREATE INDEX usage_held_all ON usage(kind, held_bytes DESC, id);
CREATE INDEX usage_active ON usage(kind, tenant_id, last_active, id);
CREATE INDEX usage_active_all ON usage(kind, last_active, id);
CREATE INDEX usage_created ON usage(kind, tenant_id, created, id);
CREATE INDEX usage_created_all ON usage(kind, created, id);
CREATE INDEX usage_dormant ON usage(kind, tenant_id, dormant, created, id);
CREATE INDEX usage_dormant_all ON usage(kind, dormant, created, id);
CREATE INDEX usage_nspaces ON usage(kind, tenant_id, spaces DESC, id);
CREATE INDEX usage_nspaces_all ON usage(kind, spaces DESC, id);
-- The people with a deadline running or passed are few: a partial index each.
CREATE INDEX usage_grace ON usage(tenant_id, id) WHERE kind = 'user' AND grace_spaces > 0;
CREATE INDEX usage_overdue ON usage(tenant_id, id) WHERE kind = 'user' AND overdue_spaces > 0;

-- Connections to GitLab and Gitea/Forgejo/Codeberg (3.8). One row of providers
-- is one OAuth application on one host. owner is 'site' or the id of the tenant
-- that owns the provider (a plain column: 'site' is no tenant).
CREATE TABLE providers (
  id               TEXT PRIMARY KEY,
  type             TEXT NOT NULL,
  host             TEXT NOT NULL,
  scheme           TEXT NOT NULL DEFAULT 'https',
  name             TEXT NOT NULL,
  owner            TEXT NOT NULL DEFAULT 'site',
  client_id        TEXT NOT NULL,
  secret_enc       TEXT NOT NULL,
  state            TEXT NOT NULL DEFAULT 'ok',
  platform_version TEXT NOT NULL DEFAULT '',
  partial_sync     TEXT NOT NULL DEFAULT 'unknown',
  partial_checked  BIGINT NOT NULL DEFAULT 0,
  checked          BIGINT NOT NULL DEFAULT 0,
  updated_by       TEXT NOT NULL DEFAULT '',
  created          BIGINT NOT NULL,
  updated          BIGINT NOT NULL
);
CREATE UNIQUE INDEX providers_owner_host ON providers(owner, host);

-- A person's connection to an account on a provider's host. One account on a
-- host links to one user, and a user has one connection per provider.
CREATE TABLE provider_authorizations (
  provider        TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id      TEXT NOT NULL,
  login           TEXT NOT NULL,
  access_enc      TEXT NOT NULL DEFAULT '',
  refresh_enc     TEXT NOT NULL DEFAULT '',
  access_expires  BIGINT NOT NULL DEFAULT 0,
  refresh_expires BIGINT NOT NULL DEFAULT 0,
  state           TEXT NOT NULL DEFAULT 'ok',
  version         BIGINT NOT NULL DEFAULT 1,
  is_admin        BIGINT NOT NULL DEFAULT 0,
  connected       BIGINT NOT NULL,
  updated         BIGINT NOT NULL,
  PRIMARY KEY (provider, user_id),
  UNIQUE (provider, account_id)
);
CREATE INDEX provider_authorizations_renew ON provider_authorizations(state, refresh_expires, provider, user_id);
CREATE INDEX provider_authorizations_user ON provider_authorizations(user_id);

CREATE TABLE provider_leftovers (
  provider   TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id    BIGINT NOT NULL,
  full_name  TEXT NOT NULL,
  html_url   TEXT NOT NULL,
  space_name TEXT NOT NULL DEFAULT '',
  created    BIGINT NOT NULL,
  PRIMARY KEY (provider, user_id, repo_id)
);

-- The recycle bin (3.5): one row per space or person that went into it. A
-- space's rows are kept whole in records (JSON) and are gone from the live
-- tables; dirs lists the directories that were moved; state is moving, ready,
-- restoring or purging; parent is the item of the person a space was recycled
-- with ('' for none).
CREATE TABLE recycle_items (
  id              TEXT PRIMARY KEY,
  tenant_id       TEXT NOT NULL REFERENCES tenants(id),
  kind            TEXT NOT NULL,
  space_id        TEXT NOT NULL DEFAULT '',
  user_id         TEXT NOT NULL DEFAULT '',
  parent          TEXT NOT NULL DEFAULT '',
  name            TEXT NOT NULL DEFAULT '',
  name_key        TEXT NOT NULL DEFAULT '',
  personal        BIGINT NOT NULL DEFAULT 0,
  records         TEXT NOT NULL DEFAULT '{}',
  dirs            TEXT NOT NULL DEFAULT '[]',
  bytes           BIGINT NOT NULL DEFAULT 0,
  deleted_at      BIGINT NOT NULL,
  deleted_by      TEXT NOT NULL DEFAULT '',
  deleted_by_name TEXT NOT NULL DEFAULT '',
  reason          TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL DEFAULT 'ready',
  state_at        BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX recycle_items_tenant ON recycle_items(tenant_id, deleted_at DESC, id DESC);
CREATE INDEX recycle_items_purge ON recycle_items(deleted_at);
CREATE INDEX recycle_items_parent ON recycle_items(parent);
CREATE INDEX recycle_items_space ON recycle_items(space_id);
-- Items that are not at rest (a crash leaves them): a few rows, found at start.
CREATE INDEX recycle_items_busy ON recycle_items(state) WHERE state <> 'ready';

-- A zip of what some spaces hold, made on request (state: queued, running,
-- ready, failed) and kept until expires. site_only marks an export only site
-- administrators see, whatever tenant_id says.
CREATE TABLE exports (
  id           TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL REFERENCES tenants(id),
  made_by      TEXT NOT NULL,
  made_by_name TEXT NOT NULL DEFAULT '',
  spaces       TEXT NOT NULL DEFAULT '[]',
  history      BIGINT NOT NULL DEFAULT 0,
  site_only    BIGINT NOT NULL DEFAULT 0,
  state        TEXT NOT NULL DEFAULT 'queued',
  error        TEXT NOT NULL DEFAULT '',
  bytes        BIGINT NOT NULL DEFAULT 0,
  path         TEXT NOT NULL DEFAULT '',
  created      BIGINT NOT NULL,
  expires      BIGINT NOT NULL
);
CREATE INDEX exports_tenant ON exports(tenant_id, created DESC, id DESC);
CREATE INDEX exports_expires ON exports(expires);

-- Every username a purge has freed, case folded.
CREATE TABLE retired_usernames (
  name      TEXT PRIMARY KEY,
  purged_at BIGINT NOT NULL
);

-- A bulk action over many spaces, people or items, run in the background one
-- item at a time. failed is a JSON list of what did not work and why.
-- site_only marks a job a site administrator started.
CREATE TABLE bulk_jobs (
  id        TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL REFERENCES tenants(id),
  made_by   TEXT NOT NULL,
  action    TEXT NOT NULL,
  total     BIGINT NOT NULL,
  done      BIGINT NOT NULL DEFAULT 0,
  failed    TEXT NOT NULL DEFAULT '[]',
  state     TEXT NOT NULL DEFAULT 'queued',
  site_only BIGINT NOT NULL DEFAULT 0,
  created   BIGINT NOT NULL,
  updated   BIGINT NOT NULL
);
CREATE INDEX bulk_jobs_state ON bulk_jobs(state) WHERE state IN ('queued', 'running');
