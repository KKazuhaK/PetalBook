-- The schema of PetalBook's state database, for SQLite and PostgreSQL alike:
-- the baseline. Until the first release it is edited in place, and nothing
-- converts a database from one version of it to another: a database records
-- the hash of the baseline it was made from (schema_baseline), and one made from
-- another is refused at start and must be made again. After the first release,
-- upgrades are code of their own in internal/store/upgrade, never edits that
-- assume an old database (docs/design/database.md, "5.3 Database baseline (2026-11-24)").
--
-- One schema for both databases: TEXT and BIGINT columns only, times as Unix
-- milliseconds, booleans as 0/1. Every TEXT column is COLLATE "C": text
-- compares and sorts by its bytes on PostgreSQL as it does on SQLite (where the
-- statements are run with COLLATE BINARY), whatever the database's locale, so
-- the two give the same order, a cursor means the same on both, and the
-- indexes serve the ORDER BYs and cursor comparisons as they are written.
--
-- Statements end with a semicolon at the end of a line; keep semicolons out of
-- everything else in this file. A comment line "-- dialect: postgres" (or
-- sqlite) right before a statement runs it on that database only (schema.go).

-- Plans (3.6): a named bundle of rules (docs/api.md, "Plans"). The rules are
-- rows of layered_settings with layer 'plan' and layer_id the plan's id. It
-- comes before tenants, which may name one.
CREATE TABLE plans (
  id          TEXT COLLATE "C" PRIMARY KEY,
  slug        TEXT COLLATE "C" NOT NULL UNIQUE,
  name        TEXT COLLATE "C" NOT NULL,
  description TEXT COLLATE "C" NOT NULL DEFAULT ''
);

-- Tenants (3.3). Every user, space, group, invite, registration, usage row,
-- recycle item, export and bulk job belongs to one, by a foreign key. The
-- default tenant (0000000000, slug default) is made with the database
-- (schema.go), with the time it was made.
CREATE TABLE tenants (
  id       TEXT COLLATE "C" PRIMARY KEY,
  slug     TEXT COLLATE "C" NOT NULL UNIQUE,
  name     TEXT COLLATE "C" NOT NULL,
  name_key TEXT COLLATE "C" NOT NULL DEFAULT '',
  disabled BIGINT NOT NULL DEFAULT 0,
  plan_id  TEXT COLLATE "C" REFERENCES plans(id),
  created  BIGINT NOT NULL,
  updated  BIGINT NOT NULL,
  -- How it came to be: with the database ('default'), by a site administrator
  -- ('admin') or by a person who registered it ('self'); created_by is that
  -- person's id ('' for the others). Not a foreign key: it outlives the person.
  origin     TEXT COLLATE "C" NOT NULL DEFAULT 'admin' CHECK (origin IN ('default', 'admin', 'self')),
  created_by TEXT COLLATE "C" NOT NULL DEFAULT ''
);
-- What a person has made (orgCreation.perPerson counts it).
CREATE INDEX tenants_created_by ON tenants(created_by) WHERE created_by <> '';

-- People. username is stored in lower case and unique on the whole server.
-- name_key is the name folded by Go (FoldName): every write of the name writes
-- it too, so a search reads a column instead of calling a function on every
-- row. email, email_key, email_verified and email_verified_by are a copy of
-- the primary row of user_emails (below), written with it in one transaction
-- (SetPrimaryEmail), so the lists and the search need no join; nothing looks
-- an account up by them.
-- email_verified_by says who vouched for a verified address ('' the person or
-- a site administrator; 'provider:<slug>' a trusted sign-in; 'tenant_admin' a
-- tenant administrator
-- alone, which does not count for merging a sign-in by address).
-- terms_accepted is JSON (at, termsUrl, privacyUrl; '' when never asked), quota
-- a person's own limits (JSON; '' follows the tenant and the site).
-- deleted_at is when the person was deleted (NULL for everyone else) and
-- recycle_item the item that holds what was taken from them; delete_pending is
-- a deletion under way (JSON, '' when none is). last_active (a session or a
-- token, written at most once an hour) and first_write (the first change in a
-- space) are NULL until then.
CREATE TABLE users (
  id                TEXT COLLATE "C" PRIMARY KEY,
  tenant_id         TEXT COLLATE "C" NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  username          TEXT COLLATE "C" NOT NULL UNIQUE,
  name              TEXT COLLATE "C" NOT NULL,
  name_key          TEXT COLLATE "C" NOT NULL DEFAULT '',
  email             TEXT COLLATE "C" NOT NULL DEFAULT '',
  email_key         TEXT COLLATE "C" NOT NULL DEFAULT '',
  role              TEXT COLLATE "C" NOT NULL,
  disabled          BIGINT NOT NULL DEFAULT 0,
  created           BIGINT NOT NULL,
  email_verified    BIGINT NOT NULL DEFAULT 0,
  email_verified_by TEXT COLLATE "C" NOT NULL DEFAULT '',
  passkey_required  BIGINT NOT NULL DEFAULT 0,
  remote_exempt     BIGINT NOT NULL DEFAULT 0,
  site_admin        BIGINT NOT NULL DEFAULT 0,
  terms_accepted    TEXT COLLATE "C" NOT NULL DEFAULT '',
  quota             TEXT COLLATE "C" NOT NULL DEFAULT '',
  deleted_at        BIGINT,
  recycle_item      TEXT COLLATE "C" NOT NULL DEFAULT '',
  delete_pending    TEXT COLLATE "C" NOT NULL DEFAULT '',
  last_active       BIGINT,
  first_write       BIGINT
);
CREATE INDEX users_created ON users(created);
CREATE INDEX users_role ON users(role, disabled);
-- The lists of people read this partial index, which holds only those who are
-- not deleted.
CREATE INDEX users_live ON users(tenant_id, created, id) WHERE deleted_at IS NULL;
-- The administration's list of people (3.10) pages by keyset over one of these
-- for each sort; the *_all ones serve an administrator of the whole server.
CREATE INDEX users_live_all ON users(created, id) WHERE deleted_at IS NULL;
-- The administrators of one tenant, for its overview: a tenant's few
-- administrators are read without walking its people.
CREATE INDEX users_admins ON users(tenant_id, created, id) WHERE role = 'admin' AND deleted_at IS NULL;
CREATE INDEX users_live_name ON users(tenant_id, name_key, id) WHERE deleted_at IS NULL;
CREATE INDEX users_live_name_all ON users(name_key, id) WHERE deleted_at IS NULL;
CREATE INDEX users_live_active ON users(tenant_id, last_active DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX users_live_active_all ON users(last_active DESC, id) WHERE deleted_at IS NULL;

-- The addresses of people (3.6). An address belongs to one account on the
-- whole server, verified or not, and also to a deleted one until it is purged
-- (the purge deletes its rows). email_key is the address as addresses are
-- compared (EmailKey: the domain as IDNA punycode, the local part NFC and case
-- folded; '' for one that has no key, which matches nothing). One primary
-- address per account, its UPN, copied to users.email*. Signing in and
-- resetting by address read a verified row; there may be several rows per
-- account (the others are not primary).
-- verified_by as users.email_verified_by.
CREATE TABLE user_emails (
  user_id     TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email       TEXT COLLATE "C" NOT NULL,
  email_key   TEXT COLLATE "C" NOT NULL,
  is_primary  BIGINT NOT NULL DEFAULT 0,
  verified    BIGINT NOT NULL DEFAULT 0,
  verified_by TEXT COLLATE "C" NOT NULL DEFAULT '',
  created     BIGINT NOT NULL,
  PRIMARY KEY (user_id, email)
);
CREATE UNIQUE INDEX user_emails_key ON user_emails(email_key) WHERE email_key <> '';
CREATE UNIQUE INDEX user_emails_primary ON user_emails(user_id) WHERE is_primary = 1;

CREATE TABLE passwords (
  user_id TEXT COLLATE "C" PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  hash    TEXT COLLATE "C" NOT NULL,
  updated BIGINT NOT NULL,
  -- 1 for a temporary password an administrator set: the next sign-in with it
  -- is held until the person chooses their own. Any other way of setting the
  -- password clears it.
  must_change BIGINT NOT NULL DEFAULT 0
);


-- A held session may only register a passkey and sign out.
CREATE TABLE sessions (
  id        TEXT COLLATE "C" PRIMARY KEY,
  user_id   TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created   BIGINT NOT NULL,
  last_used BIGINT NOT NULL,
  held      BIGINT NOT NULL DEFAULT 0,
  -- What the administrator's list of a person's sessions shows: the browser's
  -- own name for itself (cut to 200 bytes) and how they signed in ("password",
  -- "passkey", "sso:<provider slug>", "invite", "registration", "reset").
  user_agent TEXT COLLATE "C" NOT NULL DEFAULT '',
  method     TEXT COLLATE "C" NOT NULL DEFAULT '',
  -- Reserved for 3.7 (several organizations per account). current_tenant is the
  -- context the session is in ('' is the person's own). proofs is JSON: a map
  -- from tenant id to {method, provider, at, seen}, the verification each
  -- organization holds in this session (method sso|domain|password|passkey|
  -- social; at and seen are epoch milliseconds; a proof lives while its
  -- organization's site-set sliding lifetime does). client picks that lifetime
  -- (web|app).
  current_tenant TEXT COLLATE "C" NOT NULL DEFAULT '',
  proofs         TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  client         TEXT COLLATE "C" NOT NULL DEFAULT 'web'
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
  id      TEXT COLLATE "C" PRIMARY KEY,
  user_id TEXT COLLATE "C" NOT NULL,
  reason  TEXT COLLATE "C" NOT NULL,
  ended   BIGINT NOT NULL
);
CREATE INDEX session_ends_ended ON session_ends(ended);
CREATE INDEX session_ends_user ON session_ends(user_id);

-- username, name and email are what an invite fills in on the form that
-- accepts it; site_admin makes the account a site administrator.
CREATE TABLE invites (
  id         TEXT COLLATE "C" PRIMARY KEY,
  tenant_id  TEXT COLLATE "C" NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  token_hash TEXT COLLATE "C" NOT NULL UNIQUE,
  role       TEXT COLLATE "C" NOT NULL,
  site_admin BIGINT NOT NULL DEFAULT 0,
  created_by TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created    BIGINT NOT NULL,
  expires    BIGINT NOT NULL,
  used       BIGINT NOT NULL DEFAULT 0,
  used_by    TEXT COLLATE "C" NOT NULL DEFAULT '',
  username   TEXT COLLATE "C" NOT NULL DEFAULT '',
  name       TEXT COLLATE "C" NOT NULL DEFAULT '',
  email      TEXT COLLATE "C" NOT NULL DEFAULT '',
  -- The ids of the local groups of the tenant the account joins when the invite
  -- is accepted (a batch of new people by file names them), comma separated.
  groups     TEXT COLLATE "C" NOT NULL DEFAULT '',
  -- email_key is email in EmailKey form ('' without one): a batch of new
  -- people does not invite an address that has an open invite of the tenant.
  email_key  TEXT COLLATE "C" NOT NULL DEFAULT ''
);
CREATE INDEX invites_expires ON invites(expires);
CREATE INDEX invites_tenant ON invites(tenant_id);
CREATE INDEX invites_email ON invites(tenant_id, email_key) WHERE email_key <> '';
-- Deleting a person deletes their invites (the foreign key's cascade).
CREATE INDEX invites_created_by ON invites(created_by);

-- Spaces. name_key is the name folded by Go (FoldName), so that both databases
-- search and sort the same. inbox is the folder quick notes go to. size_bytes
-- is the background measurement (NULL until the first one; when it was made is
-- in space_measure).
-- last_used, cache_state, pinned, exempt and index_bytes are the server storage
-- of 3.1. page_count and usage_bytes are what the quotas count, usage_user the
-- person the space is counted for (its first owner, '' when none). location
-- says where the bytes live (server, own, tenant, site), remote_via is '' without
-- a remote, the provider of a connection or 'git', remote_state the remote's
-- state ('' without one). remote_lost_at is when the space last lost the GitHub
-- access its remote synced through (0: never): the
-- storage requirement's grace period starts again from then.
CREATE TABLE spaces (
  id           TEXT COLLATE "C" PRIMARY KEY,
  tenant_id    TEXT COLLATE "C" NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  name         TEXT COLLATE "C" NOT NULL,
  name_key     TEXT COLLATE "C" NOT NULL DEFAULT '',
  personal_of  TEXT COLLATE "C" UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  created      BIGINT NOT NULL,
  inbox        TEXT COLLATE "C" NOT NULL DEFAULT 'Inbox',
  disabled     BIGINT NOT NULL DEFAULT 0,
  size_bytes   BIGINT,
  last_used    BIGINT NOT NULL DEFAULT 0,
  cache_state  TEXT COLLATE "C" NOT NULL DEFAULT '',
  pinned       BIGINT NOT NULL DEFAULT 0,
  exempt       BIGINT NOT NULL DEFAULT 0,
  index_bytes  BIGINT NOT NULL DEFAULT 0,
  page_count   BIGINT NOT NULL DEFAULT 0,
  usage_bytes  BIGINT NOT NULL DEFAULT 0,
  usage_user   TEXT COLLATE "C" NOT NULL DEFAULT '',
  location     TEXT COLLATE "C" NOT NULL DEFAULT 'server',
  remote_via   TEXT COLLATE "C" NOT NULL DEFAULT '',
  remote_state TEXT COLLATE "C" NOT NULL DEFAULT '',
  remote_lost_at BIGINT NOT NULL DEFAULT 0
);
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
CREATE INDEX spaces_lost ON spaces(remote_lost_at, id) WHERE remote_lost_at <> 0;
CREATE INDEX spaces_lost_t ON spaces(tenant_id, remote_lost_at, id) WHERE remote_lost_at <> 0;
CREATE INDEX spaces_exempt ON spaces(created, id) WHERE exempt <> 0;
CREATE INDEX spaces_owner ON spaces(usage_user, created, id);

-- The queue of the background measurement: when each space was last measured
-- (NULL: never). It is a table of its own so that the measurement, which
-- visits every space again and again, writes this narrow row and leaves the
-- space's wide, many-indexed row alone unless what it found changed.
-- size_at is when the space was last measured or tried (a failing space goes
-- to the back of the queue). measured_at is when it was last measured, set only
-- by a success (NULL: never), and is what the tenant's measuredAt shows.
-- tenant_id is the space's (spaces do not move between tenants), so that when
-- a tenant was last measured is one index lookup (its newest measured_at).
CREATE TABLE space_measure (
  space_id  TEXT COLLATE "C" PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  tenant_id TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  size_at   BIGINT,
  measured_at BIGINT
);
CREATE INDEX space_measure_due ON space_measure(size_at, space_id);
CREATE INDEX space_measure_tenant ON space_measure(tenant_id, measured_at);

CREATE TABLE space_members (
  space_id TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  user_id  TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role     TEXT COLLATE "C" NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (space_id, user_id)
);
CREATE INDEX space_members_user ON space_members(user_id);

-- Every stored draft has an opaque version, so two devices of one person
-- cannot silently replace each other's draft.
CREATE TABLE drafts (
  space_id TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  page_id  TEXT COLLATE "C" NOT NULL,
  user_id  TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  base_rev TEXT COLLATE "C" NOT NULL,
  meta     TEXT COLLATE "C" NOT NULL,
  body     TEXT COLLATE "C" NOT NULL,
  version  TEXT COLLATE "C" NOT NULL DEFAULT '',
  created  BIGINT NOT NULL,
  updated  BIGINT NOT NULL,
  held     BIGINT NOT NULL DEFAULT 0,
  due      BIGINT NOT NULL,
  PRIMARY KEY (space_id, page_id, user_id)
);
CREATE INDEX drafts_due ON drafts(held, due);
-- Deleting a person deletes their drafts (the foreign key's cascade).
CREATE INDEX drafts_user ON drafts(user_id);

-- The remote repositories of a space, one row per role, and their credentials.
-- role 'content' is the repository the space follows (a row with an empty url
-- is a space that only has an SSH key pair so far); role 'assets' is its
-- attachment repository (3.1). Tokens and private keys are stored encrypted
-- (see the secret package). provider is the provider a remote goes through:
-- 'git' for a plain address with a key or a token, otherwise a connection's
-- provider; via, installation_id, repo_id and cred_user_id say which of that
-- provider's credentials syncs it (via 'app': the installation; via 'user':
-- the person whose link it is). target_id is reserved for the storage target
-- (3.6) that made the repository. used (role 'assets'): 1 once an attachment
-- has gone to it, 2 once the space's own attachments were moved into it (its
-- repository ignores assets/ from then on). job_id (role 'assets'): the last
-- move of the space's attachments into it, a row of jobs (pruned with it).
CREATE TABLE remotes (
  space_id        TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  role            TEXT COLLATE "C" NOT NULL CHECK (role IN ('content', 'assets')),
  url             TEXT COLLATE "C" NOT NULL DEFAULT '',
  branch          TEXT COLLATE "C" NOT NULL DEFAULT 'main',
  cred_type       TEXT COLLATE "C" NOT NULL DEFAULT '',
  username        TEXT COLLATE "C" NOT NULL DEFAULT '',
  token_enc       TEXT COLLATE "C" NOT NULL DEFAULT '',
  privkey_enc     TEXT COLLATE "C" NOT NULL DEFAULT '',
  pubkey          TEXT COLLATE "C" NOT NULL DEFAULT '',
  last_good       TEXT COLLATE "C" NOT NULL DEFAULT '',
  state           TEXT COLLATE "C" NOT NULL DEFAULT 'local',
  error_kind      TEXT COLLATE "C" NOT NULL DEFAULT '',
  last_error      TEXT COLLATE "C" NOT NULL DEFAULT '',
  remote_tip      TEXT COLLATE "C" NOT NULL DEFAULT '',
  last_sync       BIGINT NOT NULL DEFAULT 0,
  updated         BIGINT NOT NULL,
  provider        TEXT COLLATE "C" NOT NULL DEFAULT 'git',
  via             TEXT COLLATE "C" NOT NULL DEFAULT '',
  installation_id BIGINT NOT NULL DEFAULT 0,
  repo_id         BIGINT NOT NULL DEFAULT 0,
  cred_user_id    TEXT COLLATE "C" NOT NULL DEFAULT '',
  target_id       TEXT COLLATE "C" NOT NULL DEFAULT '',
  used            BIGINT NOT NULL DEFAULT 0,
  job_id          TEXT COLLATE "C" NOT NULL DEFAULT '',
  PRIMARY KEY (space_id, role)
);
-- "The spaces using a connection": one index scan for both roles.
CREATE INDEX remotes_installation ON remotes(installation_id) WHERE installation_id <> 0;
CREATE INDEX remotes_cred_user ON remotes(cred_user_id) WHERE cred_user_id <> '';

-- Server-wide records as text by key (most are JSON): what is not a setting.
-- The daily text's interfaces and providers, the captcha's site key, the
-- storage policy's 'since', the mail sender, the
-- marker 'passkey_fake_key', and 'first_user', the time
-- the first account was made (MarkFirstUser). Policy is in layered_settings and
-- every secret in secrets; nothing here is a setting or a secret.
CREATE TABLE site_records (
  key     TEXT COLLATE "C" PRIMARY KEY,
  value   TEXT COLLATE "C" NOT NULL,
  updated BIGINT NOT NULL
);

-- The layered settings (see internal/settings). A row is one layer's value of
-- one key and what that layer lets the layers below do.
--   layer 'site', layer_id '':       the site's own value and lower.
--   layer 'site', layer_id a tenant: the site's rule for that tenant. It has a
--                                    source (manual, billing) and may have a
--                                    window: starts and ends (milliseconds,
--                                    NULL for none; it counts from starts up to,
--                                    not including, ends).
--   layer 'tenant', layer_id a tenant: the tenant's own value and lower.
--   layer 'plan', layer_id a plan:   a rule of the plan (source 'plan').
-- One row per (layer, layer_id, key, source). There is no foreign key on the
-- shared layer_id column: DeleteTenant and DeletePlan delete their rows.
CREATE TABLE layered_settings (
  layer    TEXT COLLATE "C" NOT NULL,
  layer_id TEXT COLLATE "C" NOT NULL,
  key      TEXT COLLATE "C" NOT NULL,
  source   TEXT COLLATE "C" NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'billing', 'plan')),
  value    TEXT COLLATE "C" NOT NULL DEFAULT '',
  lower    TEXT COLLATE "C" NOT NULL DEFAULT '',
  starts   BIGINT,
  ends     BIGINT,
  set_by   TEXT COLLATE "C" NOT NULL DEFAULT '',
  updated  BIGINT NOT NULL,
  PRIMARY KEY (layer, layer_id, key, source),
  CHECK (ends IS NULL OR starts IS NULL OR ends > starts)
);
CREATE INDEX layered_settings_starts ON layered_settings(starts) WHERE starts IS NOT NULL;
CREATE INDEX layered_settings_ends ON layered_settings(ends) WHERE ends IS NOT NULL;

-- Every secret the server keeps for the site or for a tenant, sealed with the
-- server's key (internal/secret) and bound to its scope and name. The API only
-- ever says whether one is set and when it changed; the value goes to the code
-- that uses it, nowhere else. There is no foreign key on the shared tenant_id
-- column: DeleteTenant deletes a tenant's rows.
--   scope 'site',   tenant_id '':        a secret of the site.
--   scope 'tenant', tenant_id a tenant:  a secret of that tenant.
CREATE TABLE secrets (
  scope      TEXT COLLATE "C" NOT NULL CHECK (scope IN ('site', 'tenant')),
  tenant_id  TEXT COLLATE "C" NOT NULL DEFAULT '',
  name       TEXT COLLATE "C" NOT NULL,
  value      TEXT COLLATE "C" NOT NULL,
  updated    BIGINT NOT NULL,
  updated_by TEXT COLLATE "C" NOT NULL DEFAULT '',
  PRIMARY KEY (scope, tenant_id, name),
  CHECK ((scope = 'site') = (tenant_id = ''))
);

-- One row: every write of layered_settings adds one to rev in its transaction,
-- so a process that did not make the write (the CLI, a standby server) is seen
-- by one point read.
CREATE TABLE settings_rev (
  id  BIGINT PRIMARY KEY CHECK (id = 1),
  rev BIGINT NOT NULL
);

-- The daily text: the lines an administrator pasted or uploaded, a space's
-- choice of the server's sources, and what the external interfaces said
-- (kept seven days).
CREATE TABLE daily_lines (
  position BIGINT PRIMARY KEY,
  lang     TEXT COLLATE "C" NOT NULL DEFAULT '',
  body     TEXT COLLATE "C" NOT NULL,
  author   TEXT COLLATE "C" NOT NULL DEFAULT '',
  source   TEXT COLLATE "C" NOT NULL DEFAULT ''
);
CREATE INDEX daily_lines_lang ON daily_lines(lang, position);

CREATE TABLE space_daily (
  space_id TEXT COLLATE "C" PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  inherit  BIGINT NOT NULL DEFAULT 1,
  apis     TEXT COLLATE "C" NOT NULL DEFAULT '[]'
);

CREATE TABLE daily_cache (
  api_key TEXT COLLATE "C" NOT NULL,
  lang    TEXT COLLATE "C" NOT NULL,
  period  TEXT COLLATE "C" NOT NULL,
  skip    BIGINT NOT NULL,
  body    TEXT COLLATE "C" NOT NULL,
  fetched BIGINT NOT NULL,
  PRIMARY KEY (api_key, lang, period, skip)
);
CREATE INDEX daily_cache_latest ON daily_cache(api_key, lang, fetched);
CREATE INDEX daily_cache_fetched ON daily_cache(fetched);
CREATE INDEX daily_cache_api ON daily_cache(api_key, fetched);

-- A person's favourites in a space: pages, folders and saved views, in the
-- order the person put them. kind is page, folder or view.
CREATE TABLE favorites (
  user_id  TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  space_id TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  kind     TEXT COLLATE "C" NOT NULL,
  target   TEXT COLLATE "C" NOT NULL,
  position BIGINT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (user_id, space_id, kind, target)
);
CREATE INDEX favorites_order ON favorites(user_id, space_id, position);
CREATE INDEX favorites_space ON favorites(space_id);

-- The notification centre (3.10 part 4). A row is something that happened to one
-- person: kind says what (see store/notifications.go for the registry), ref the
-- thing it is about (a space id), payload the facts as JSON (the web app writes
-- the sentence, in the person's language). id is the time in Unix milliseconds
-- as 12 hexadecimal digits and a sequence, so ids sort by time. read_at is NULL
-- while unread. Space-wide events (the problems of a space) are not rows: they
-- are read from the index when the list is asked for, and notification_seen
-- keeps, for each of them, what the person last read.
CREATE TABLE notifications (
  id        TEXT COLLATE "C" PRIMARY KEY,
  user_id   TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  kind      TEXT COLLATE "C" NOT NULL,
  ref       TEXT COLLATE "C" NOT NULL DEFAULT '',
  payload   TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  created   BIGINT NOT NULL,
  read_at   BIGINT
);
CREATE INDEX notifications_user ON notifications(user_id, id DESC);
CREATE INDEX notifications_unread ON notifications(user_id) WHERE read_at IS NULL;
CREATE INDEX notifications_tenant ON notifications(tenant_id);

CREATE TABLE notification_seen (
  user_id TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT COLLATE "C" NOT NULL,
  sig     TEXT COLLATE "C" NOT NULL,
  seen    BIGINT NOT NULL,
  PRIMARY KEY (user_id, key)
);

-- Folder moves whose favourites have not been made to follow yet.
CREATE TABLE folder_moves (
  space_id  TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  from_path TEXT COLLATE "C" NOT NULL,
  to_path   TEXT COLLATE "C" NOT NULL,
  created   BIGINT NOT NULL,
  PRIMARY KEY (space_id, from_path, to_path)
);

-- API tokens (2.6). Only the SHA-256 of the secret is kept, with its first
-- characters. A token's spaces are not foreign keys: a deleted space stays
-- listed and does nothing.
CREATE TABLE api_tokens (
  id        TEXT COLLATE "C" PRIMARY KEY,
  user_id   TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name      TEXT COLLATE "C" NOT NULL,
  prefix    TEXT COLLATE "C" NOT NULL,
  hash      TEXT COLLATE "C" NOT NULL,
  access    TEXT COLLATE "C" NOT NULL,
  created   BIGINT NOT NULL,
  expires   BIGINT NOT NULL DEFAULT 0,
  last_used BIGINT NOT NULL DEFAULT 0,
  revoked   BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX api_tokens_hash ON api_tokens(hash);
CREATE INDEX api_tokens_user ON api_tokens(user_id, created);
CREATE INDEX api_tokens_created ON api_tokens(created, id);

CREATE TABLE api_token_spaces (
  token_id TEXT COLLATE "C" NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
  space_id TEXT COLLATE "C" NOT NULL,
  PRIMARY KEY (token_id, space_id)
);

-- The authorization server for MCP clients (2.6): the signing key, the clients
-- that registered themselves, and the grants (one approval of one client by
-- one person, with the hash of its refresh token and of the few replaced ones).
CREATE TABLE oauth_keys (
  id      TEXT COLLATE "C" PRIMARY KEY,
  sealed  TEXT COLLATE "C" NOT NULL,
  created BIGINT NOT NULL
);

CREATE TABLE oauth_clients (
  id            TEXT COLLATE "C" PRIMARY KEY,
  name          TEXT COLLATE "C" NOT NULL,
  redirect_uris TEXT COLLATE "C" NOT NULL,
  fingerprint   TEXT COLLATE "C" NOT NULL,
  created       BIGINT NOT NULL,
  last_used     BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX oauth_clients_fingerprint ON oauth_clients(fingerprint);
CREATE INDEX oauth_clients_created ON oauth_clients(created);

CREATE TABLE oauth_grants (
  id           TEXT COLLATE "C" PRIMARY KEY,
  user_id      TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id    TEXT COLLATE "C" NOT NULL,
  client_name  TEXT COLLATE "C" NOT NULL,
  access       TEXT COLLATE "C" NOT NULL,
  resource     TEXT COLLATE "C" NOT NULL,
  created      BIGINT NOT NULL,
  last_used    BIGINT NOT NULL,
  refresh_hash TEXT COLLATE "C" NOT NULL
);
CREATE INDEX oauth_grants_user ON oauth_grants(user_id, created);
CREATE INDEX oauth_grants_created ON oauth_grants(created, id);
CREATE INDEX oauth_grants_client ON oauth_grants(client_id);
CREATE INDEX oauth_grants_used ON oauth_grants(last_used);

CREATE TABLE oauth_grant_spaces (
  grant_id TEXT COLLATE "C" NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
  space_id TEXT COLLATE "C" NOT NULL,
  PRIMARY KEY (grant_id, space_id)
);

CREATE TABLE oauth_retired (
  grant_id TEXT COLLATE "C" NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
  hash     TEXT COLLATE "C" NOT NULL,
  at       BIGINT NOT NULL,
  PRIMARY KEY (grant_id, hash)
);

-- credential is the JSON of the WebAuthn credential; rp_id is the host the
-- passkey was made for.
CREATE TABLE passkeys (
  id            TEXT COLLATE "C" PRIMARY KEY,
  user_id       TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id TEXT COLLATE "C" NOT NULL UNIQUE,
  credential    TEXT COLLATE "C" NOT NULL,
  name          TEXT COLLATE "C" NOT NULL,
  rp_id         TEXT COLLATE "C" NOT NULL DEFAULT '',
  synced        BIGINT NOT NULL DEFAULT 0,
  created       BIGINT NOT NULL,
  last_used     BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX passkeys_user ON passkeys(user_id);

-- The audit log. tenant_id is a plain column, not a foreign key: the record of
-- what happened in a tenant outlives the tenant. id is 12 hexadecimal digits of
-- the time in Unix milliseconds and a sequence number, so the ids sort by time:
-- the trim deletes a range of ids, and no index of `at` is needed.
CREATE TABLE audit_log (
  id        TEXT COLLATE "C" PRIMARY KEY,
  tenant_id TEXT COLLATE "C" NOT NULL DEFAULT '0000000000',
  at        BIGINT NOT NULL,
  kind      TEXT COLLATE "C" NOT NULL,
  actor     TEXT COLLATE "C" NOT NULL DEFAULT '',
  user_id   TEXT COLLATE "C" NOT NULL DEFAULT '',
  username  TEXT COLLATE "C" NOT NULL DEFAULT '',
  ip        TEXT COLLATE "C" NOT NULL DEFAULT '',
  detail    TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  -- Written from the registry of kinds (internal/auditlog) when the row is
  -- inserted, not worked out when it is read: a kind that moves to another
  -- category keeps its old rows' category, and the filter and the trim use the
  -- index. capacity is reserved for acting as a tenant's administrator ('',
  -- site_admin or tenant_admin).
  category  TEXT COLLATE "C" NOT NULL DEFAULT 'admin',
  capacity  TEXT COLLATE "C" NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_kind ON audit_log(kind, id);
CREATE INDEX audit_log_user ON audit_log(user_id, id);
CREATE INDEX audit_log_tenant ON audit_log(tenant_id, id);
-- A tenant's log filtered by kind (a tenant administrator's view).
CREATE INDEX audit_log_tenant_kind ON audit_log(tenant_id, kind, id);
-- A tenant's log filtered by category, and the site's log or the trim of the
-- optional categories' rows by category.
CREATE INDEX audit_log_tenant_cat ON audit_log(tenant_id, category, id);
CREATE INDEX audit_log_cat ON audit_log(category, id);
-- Only the rows written in a capacity: the filter by capacity reads them alone.
CREATE INDEX audit_log_capacity ON audit_log(capacity, id) WHERE capacity <> '';
CREATE INDEX audit_log_tenant_capacity ON audit_log(tenant_id, capacity, id) WHERE capacity <> '';
-- A filter on the actor pages by id; the daily sweep of tombstones asks whether
-- the audit log still names a person.
CREATE INDEX audit_log_actor ON audit_log(actor, id);

-- Groups (3.2): a set of people that can be a member of a shared space with a
-- role. source is 'local' or 'synced'; name_key is the folded name, unique
-- within a tenant. A synced membership says where it came from (provider and
-- claim value; both '' for a local group).
CREATE TABLE groups (
  id          TEXT COLLATE "C" PRIMARY KEY,
  tenant_id   TEXT COLLATE "C" NOT NULL DEFAULT '0000000000' REFERENCES tenants(id),
  name        TEXT COLLATE "C" NOT NULL,
  name_key    TEXT COLLATE "C" NOT NULL,
  description TEXT COLLATE "C" NOT NULL DEFAULT '',
  source      TEXT COLLATE "C" NOT NULL,
  created     BIGINT NOT NULL,
  updated     BIGINT NOT NULL,
  UNIQUE (tenant_id, name_key)
);

CREATE TABLE group_members (
  group_id TEXT COLLATE "C" NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id  TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT COLLATE "C" NOT NULL DEFAULT '',
  value    TEXT COLLATE "C" NOT NULL DEFAULT '',
  created  BIGINT NOT NULL,
  PRIMARY KEY (group_id, user_id, provider, value)
);
CREATE INDEX group_members_user ON group_members(user_id);

CREATE TABLE space_groups (
  space_id TEXT COLLATE "C" NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  group_id TEXT COLLATE "C" NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  role     TEXT COLLATE "C" NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (space_id, group_id)
);
CREATE INDEX space_groups_group ON space_groups(group_id);

-- Custom roles (3.6, reserved: nothing makes them yet but the store's own
-- functions). The built-in roles are code (internal/authz), not rows. scope is
-- 'site', 'tenant' or 'space'; tenant_id is '' for a role of the site. keys is
-- a JSON list of key names; a renamed key keeps its old name as an alias, so a
-- stored list stays good. A tenant's roles go with it (DeleteTenant).
CREATE TABLE roles (
  id        TEXT COLLATE "C" PRIMARY KEY,
  tenant_id TEXT COLLATE "C" NOT NULL DEFAULT '',
  scope     TEXT COLLATE "C" NOT NULL CHECK (scope IN ('site', 'tenant', 'space')),
  name      TEXT COLLATE "C" NOT NULL,
  keys      TEXT COLLATE "C" NOT NULL DEFAULT '[]',
  created   BIGINT NOT NULL,
  updated   BIGINT NOT NULL,
  UNIQUE (tenant_id, name)
);

-- A role held by a person or a group: across the site (tenant_id ''), in one
-- tenant, or in one space (space_id; the space level's, reserved). The
-- subject is not a foreign key (it is either table's): a group's grants go
-- with the group (DeleteGroup), a person's stay with their tombstone. The
-- session cache reads a person's grants by the index on the subject, and only
-- while some role exists (Store.CustomRoles).
CREATE TABLE role_grants (
  role_id      TEXT COLLATE "C" NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  subject_kind TEXT COLLATE "C" NOT NULL CHECK (subject_kind IN ('user', 'group')),
  subject_id   TEXT COLLATE "C" NOT NULL,
  tenant_id    TEXT COLLATE "C" NOT NULL DEFAULT '',
  space_id     TEXT COLLATE "C" NOT NULL DEFAULT '',
  created      BIGINT NOT NULL,
  PRIMARY KEY (role_id, subject_kind, subject_id, tenant_id, space_id)
);
CREATE INDEX role_grants_subject ON role_grants(subject_kind, subject_id);

-- A registration waiting for its link to be followed. The password is already
-- hashed and the username is held (unique) until the row expires or is made
-- into an account; only the SHA-256 of the token is stored.
CREATE TABLE registrations (
  id            TEXT COLLATE "C" PRIMARY KEY,
  tenant_id     TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  username      TEXT COLLATE "C" NOT NULL,
  name          TEXT COLLATE "C" NOT NULL,
  email         TEXT COLLATE "C" NOT NULL,
  password_hash TEXT COLLATE "C" NOT NULL,
  token_hash    TEXT COLLATE "C" NOT NULL UNIQUE,
  terms         TEXT COLLATE "C" NOT NULL DEFAULT '',
  created       BIGINT NOT NULL,
  expires       BIGINT NOT NULL,
  -- A registration that makes an organisation (docs/design/3-6-c.md, 2) names
  -- it; org_slug <> '' marks it, and holds the slug (unique) until the row ends.
  -- Its tenant_id is the default tenant, which keeps the foreign key.
  org_name      TEXT COLLATE "C" NOT NULL DEFAULT '',
  org_slug      TEXT COLLATE "C" NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX registrations_username ON registrations(username);
CREATE UNIQUE INDEX registrations_org_slug ON registrations(org_slug) WHERE org_slug <> '';
CREATE INDEX registrations_tenant ON registrations(tenant_id, expires);

-- Links that confirm an address ('verify') or reset a password ('reset'): only
-- the SHA-256 of the token is stored.
CREATE TABLE email_tokens (
  hash    TEXT COLLATE "C" PRIMARY KEY,
  kind    TEXT COLLATE "C" NOT NULL,
  user_id TEXT COLLATE "C" NOT NULL,
  email   TEXT COLLATE "C" NOT NULL,
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
  kind            TEXT COLLATE "C" NOT NULL,
  id              TEXT COLLATE "C" NOT NULL,
  tenant_id       TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  spaces          BIGINT NOT NULL DEFAULT 0,
  bytes           BIGINT NOT NULL DEFAULT 0,
  pages           BIGINT NOT NULL DEFAULT 0,
  people          BIGINT NOT NULL DEFAULT 0,
  over            TEXT COLLATE "C" NOT NULL DEFAULT '',
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
-- of them (a site administrator's). They hold the people's rows only (kind
-- 'user'): the tenants' rows are read by their key. The indexes across all
-- tenants keep kind in front, which SQLite's planner needs to take them for the
-- ORDER BY: without an equality on a leading column it costs the partial index
-- as a scan and prefers the primary key and a sort.
CREATE INDEX usage_rank ON usage(tenant_id, bytes DESC, id) WHERE kind = 'user';
CREATE INDEX usage_rank_all ON usage(kind, bytes DESC, id) WHERE kind = 'user';
CREATE INDEX usage_held ON usage(tenant_id, held_bytes DESC, id) WHERE kind = 'user';
CREATE INDEX usage_held_all ON usage(kind, held_bytes DESC, id) WHERE kind = 'user';
CREATE INDEX usage_active ON usage(tenant_id, last_active, id) WHERE kind = 'user';
CREATE INDEX usage_active_all ON usage(kind, last_active, id) WHERE kind = 'user';
CREATE INDEX usage_created ON usage(tenant_id, created, id) WHERE kind = 'user';
CREATE INDEX usage_created_all ON usage(kind, created, id) WHERE kind = 'user';
CREATE INDEX usage_dormant ON usage(tenant_id, dormant, created, id) WHERE kind = 'user';
CREATE INDEX usage_dormant_all ON usage(kind, dormant, created, id) WHERE kind = 'user';
CREATE INDEX usage_nspaces ON usage(tenant_id, spaces DESC, id) WHERE kind = 'user';
CREATE INDEX usage_nspaces_all ON usage(kind, spaces DESC, id) WHERE kind = 'user';
-- The people with a deadline running or passed are few: a partial index each.
CREATE INDEX usage_grace ON usage(tenant_id, id) WHERE kind = 'user' AND grace_spaces > 0;
CREATE INDEX usage_overdue ON usage(tenant_id, id) WHERE kind = 'user' AND overdue_spaces > 0;

-- Every outside system a person can sign in with or connect repositories to
-- (consolidation 5). owner is 'site' or the id of the tenant that owns the row
-- (org SSO); slug is the URL name, unique on the whole server (it is in the
-- callback addresses registered at each identity provider); for a site row the
-- id is the slug, for a tenant's row a generated id. class is a sign-in row's
-- 'social' or 'sso', chosen when it is made and never written again ('' for a
-- row that only holds repositories); template is the preset a site social row
-- was made from (google, microsoft, apple, gitlab-com; '' for none), fixed too,
-- and the trust rules of its addresses are the template's. kind 'oauth2' (a
-- plain OAuth 2.0 provider) is reserved. signin and repos say what the row
-- may be used for. issuer is what an administrator typed (an OIDC iss, a SAML
-- entity id) and only for display and discovery: no account is ever found by
-- it, only by the row's own id (linked_accounts). config is JSON with nothing
-- secret in it, secret_enc the sealed client secret (or SAML signing key),
-- cache the discovery document or the identity provider's metadata. jit_tenant
-- is where autoCreate puts new accounts; trust_email_optin is a site-owned
-- custom OIDC or SAML provider's "its address is verified" switch; groups_version
-- is bumped by every change of the row. The GitLab and Gitea rows (repos = 1)
-- keep their state in status_ok and status_checked.
CREATE TABLE providers (
  id                TEXT COLLATE "C" PRIMARY KEY,
  owner             TEXT COLLATE "C" NOT NULL DEFAULT 'site',
  slug              TEXT COLLATE "C" NOT NULL,
  kind              TEXT COLLATE "C" NOT NULL CHECK (kind IN ('oidc', 'oauth2', 'saml', 'github', 'gitlab', 'gitea')),
  class             TEXT COLLATE "C" NOT NULL DEFAULT '' CHECK (class IN ('', 'social', 'sso')),
  template          TEXT COLLATE "C" NOT NULL DEFAULT '',
  name              TEXT COLLATE "C" NOT NULL,
  icon              TEXT COLLATE "C" NOT NULL DEFAULT '',
  signin            BIGINT NOT NULL DEFAULT 0,
  repos             BIGINT NOT NULL DEFAULT 0,
  enabled           BIGINT NOT NULL DEFAULT 0,
  position          BIGINT NOT NULL DEFAULT 0,
  issuer            TEXT COLLATE "C" NOT NULL DEFAULT '',
  host              TEXT COLLATE "C" NOT NULL DEFAULT '',
  scheme            TEXT COLLATE "C" NOT NULL DEFAULT 'https',
  client_id         TEXT COLLATE "C" NOT NULL DEFAULT '',
  secret_enc        TEXT COLLATE "C" NOT NULL DEFAULT '',
  config            TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  jit_tenant        TEXT COLLATE "C" NOT NULL DEFAULT '',
  auto_create       BIGINT NOT NULL DEFAULT 0,
  trust_email_optin BIGINT NOT NULL DEFAULT 0,
  cache             TEXT COLLATE "C" NOT NULL DEFAULT '',
  status_ok         BIGINT NOT NULL DEFAULT 0,
  status_checked    BIGINT NOT NULL DEFAULT 0,
  status_message    TEXT COLLATE "C" NOT NULL DEFAULT '',
  platform_version  TEXT COLLATE "C" NOT NULL DEFAULT '',
  partial_sync      TEXT COLLATE "C" NOT NULL DEFAULT 'unknown',
  partial_checked   BIGINT NOT NULL DEFAULT 0,
  groups_version    BIGINT NOT NULL DEFAULT 0,
  updated_by        TEXT COLLATE "C" NOT NULL DEFAULT '',
  created           BIGINT NOT NULL,
  updated           BIGINT NOT NULL,
  CHECK (signin = 0 OR class <> ''),
  CHECK (template = '' OR (kind = 'oidc' AND class = 'social' AND owner = 'site'))
);
CREATE UNIQUE INDEX providers_slug ON providers(slug);
CREATE UNIQUE INDEX providers_owner_host ON providers(owner, kind, host) WHERE host <> '';

-- A person's account at a provider: one per (provider, person), and one person
-- per (provider, subject). subject is the OIDC sub, the SAML NameID, or the
-- numeric id on GitHub, GitLab or Gitea. An account is found by the provider's
-- own id and the subject, never by an issuer string. The token columns are for
-- repository links (GitHub, GitLab, Gitea; sealed, bound to the provider and the
-- person); a sign-in link has none. version counts replacements of the tokens.
-- state is ok, or expired: the tokens are gone and the person must authorise
-- again. renew_at is when the one renewal loop next renews the refresh token (0:
-- never); the partial index serves "the next one due" and "all due now".
-- renewing_until is the lease of the one process asking the host for a new pair
-- (0: none): a refresh token used twice is dead at some hosts, so a renewal
-- claims the row first, and a lease left by a process that died ends by itself.
CREATE TABLE linked_accounts (
  id              TEXT COLLATE "C" PRIMARY KEY,
  provider_id     TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id         TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject         TEXT COLLATE "C" NOT NULL,
  login           TEXT COLLATE "C" NOT NULL DEFAULT '',
  email           TEXT COLLATE "C" NOT NULL DEFAULT '',
  email_trusted   BIGINT NOT NULL DEFAULT 0,
  signin          BIGINT NOT NULL DEFAULT 0,
  access_enc      TEXT COLLATE "C" NOT NULL DEFAULT '',
  refresh_enc     TEXT COLLATE "C" NOT NULL DEFAULT '',
  access_expires  BIGINT NOT NULL DEFAULT 0,
  refresh_expires BIGINT NOT NULL DEFAULT 0,
  scopes          TEXT COLLATE "C" NOT NULL DEFAULT '',
  renew_at        BIGINT NOT NULL DEFAULT 0,
  renewing_until  BIGINT NOT NULL DEFAULT 0,
  state           TEXT COLLATE "C" NOT NULL DEFAULT 'ok',
  version         BIGINT NOT NULL DEFAULT 1,
  is_admin        BIGINT NOT NULL DEFAULT 0,
  label           TEXT COLLATE "C" NOT NULL DEFAULT '',
  created         BIGINT NOT NULL,
  last_used       BIGINT NOT NULL DEFAULT 0,
  updated         BIGINT NOT NULL,
  UNIQUE (provider_id, subject),
  UNIQUE (provider_id, user_id)
);
CREATE INDEX linked_accounts_user ON linked_accounts(user_id);
CREATE INDEX linked_accounts_renew ON linked_accounts(renew_at) WHERE refresh_enc <> '' AND state = 'ok';

-- The installations of an App-kind provider (GitHub), each connected to its
-- one owner on this server: a person (owner_user: their own GitHub account) or
-- a tenant (owner_tenant: a GitHub organisation), never both and never two.
-- An owner claims an installation on the App's setup callback, after GitHub
-- showed with the claimer's own token that they reach it. A person's spaces
-- sync only through their own installation, a tenant's only through its own.
-- account_id is GitHub's immutable id of the account; claimed_by who claimed it.
CREATE TABLE provider_installations (
  provider_id  TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  id           BIGINT NOT NULL,
  account      TEXT COLLATE "C" NOT NULL,
  account_id   BIGINT NOT NULL DEFAULT 0,
  account_type TEXT COLLATE "C" NOT NULL DEFAULT 'Organization',
  owner_user   TEXT COLLATE "C" REFERENCES users(id) ON DELETE CASCADE,
  owner_tenant TEXT COLLATE "C" REFERENCES tenants(id) ON DELETE CASCADE,
  claimed_by   TEXT COLLATE "C" NOT NULL DEFAULT '',
  created      BIGINT NOT NULL,
  updated      BIGINT NOT NULL,
  PRIMARY KEY (provider_id, id),
  CHECK ((owner_user IS NULL AND owner_tenant IS NOT NULL) OR (owner_user IS NOT NULL AND owner_tenant IS NULL))
);
CREATE INDEX provider_installations_user ON provider_installations(owner_user) WHERE owner_user IS NOT NULL;
CREATE INDEX provider_installations_tenant ON provider_installations(owner_tenant) WHERE owner_tenant IS NOT NULL;

-- Who installed each installation of the GitHub App, as GitHub's signed
-- installation webhook (action created) said: sender_id is the GitHub user who
-- installed it (on an organisation, an owner: only they install, or approve a
-- member's request). An organisation's installation is claimed only by that
-- user. Written again, the same, when GitHub redelivers; deleted with the
-- installation (action deleted).
CREATE TABLE github_installers (
  installation_id BIGINT PRIMARY KEY,
  account_id      BIGINT NOT NULL,
  account         TEXT COLLATE "C" NOT NULL,
  account_type    TEXT COLLATE "C" NOT NULL,
  sender_id       BIGINT NOT NULL,
  sender          TEXT COLLATE "C" NOT NULL,
  created         BIGINT NOT NULL
);

-- An organisation's claim of an installation made on the setup callback before
-- GitHub's webhook said who installed it: the webhook completes it when its
-- sender is the claimer's GitHub user (claimer_gh). state is '' while it
-- waits, 'claiming' while one request completes it, else the code it failed
-- with (shown until it expires). One per installation and claimer.
CREATE TABLE github_pending_claims (
  installation_id BIGINT NOT NULL,
  user_id         TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  owner_user      TEXT COLLATE "C" REFERENCES users(id) ON DELETE CASCADE,
  owner_tenant    TEXT COLLATE "C" REFERENCES tenants(id) ON DELETE CASCADE,
  claimer_gh      BIGINT NOT NULL,
  account_id      BIGINT NOT NULL,
  account         TEXT COLLATE "C" NOT NULL,
  account_type    TEXT COLLATE "C" NOT NULL,
  state           TEXT COLLATE "C" NOT NULL DEFAULT '',
  created         BIGINT NOT NULL,
  expires         BIGINT NOT NULL,
  PRIMARY KEY (installation_id, user_id),
  CHECK ((owner_user IS NULL AND owner_tenant IS NOT NULL) OR (owner_user IS NOT NULL AND owner_tenant IS NULL))
);
CREATE INDEX github_pending_claims_tenant ON github_pending_claims(owner_tenant) WHERE owner_tenant IS NOT NULL;
CREATE INDEX github_pending_claims_user ON github_pending_claims(owner_user) WHERE owner_user IS NOT NULL;
CREATE INDEX github_pending_claims_expires ON github_pending_claims(expires);

-- A site provider shared with a tenant (reserved for org sign-in, 3.6 d): a
-- tenant reaches only what is shared with it. GitHub installations are never
-- shared; each has its own owner (provider_installations).
CREATE TABLE provider_shares (
  provider_id     TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  tenant_id       TEXT COLLATE "C" NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  PRIMARY KEY (provider_id, tenant_id)
);
CREATE INDEX provider_shares_tenant ON provider_shares(tenant_id);

-- Where the spaces of an owner ('site' or a tenant id) get repositories made automatically.
CREATE TABLE storage_targets (
  id              TEXT COLLATE "C" PRIMARY KEY,
  owner           TEXT COLLATE "C" NOT NULL,
  provider_id     TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  installation_id BIGINT NOT NULL DEFAULT 0,
  namespace       TEXT COLLATE "C" NOT NULL,
  namespace_id    TEXT COLLATE "C" NOT NULL DEFAULT '',
  created         BIGINT NOT NULL,
  updated         BIGINT NOT NULL
);

-- An identity provider's group claim value mapped to a PetalBook group.
CREATE TABLE group_mappings (
  provider_id TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  value       TEXT COLLATE "C" NOT NULL,
  group_id    TEXT COLLATE "C" NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  PRIMARY KEY (provider_id, value, group_id)
);

-- A reminder that a failed attempt to make a space with a new repository left
-- an empty one on a provider. repo_id is the platform's own id of it, as text.
-- Deleting a person deletes their reminders (the foreign key's cascade).
CREATE TABLE repo_leftovers (
  provider_id TEXT COLLATE "C" NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  user_id     TEXT COLLATE "C" NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id     TEXT COLLATE "C" NOT NULL,
  full_name   TEXT COLLATE "C" NOT NULL,
  html_url    TEXT COLLATE "C" NOT NULL,
  space_name  TEXT COLLATE "C" NOT NULL DEFAULT '',
  created     BIGINT NOT NULL,
  PRIMARY KEY (provider_id, user_id, repo_id)
);
CREATE INDEX repo_leftovers_user ON repo_leftovers(user_id);

-- The recycle bin (3.5): one row per space or person that went into it. A
-- space's rows are kept whole in records (JSON) and are gone from the live
-- tables; dirs lists the directories that were moved; job_id is the job that
-- holds the item (recycle.move, recycle.restore or recycle.purge: what the
-- item is doing) and '' for one at rest; parent is the item of the person a
-- space was recycled with ('' for none).
CREATE TABLE recycle_items (
  id              TEXT COLLATE "C" PRIMARY KEY,
  tenant_id       TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  kind            TEXT COLLATE "C" NOT NULL,
  space_id        TEXT COLLATE "C" NOT NULL DEFAULT '',
  user_id         TEXT COLLATE "C" NOT NULL DEFAULT '',
  parent          TEXT COLLATE "C" NOT NULL DEFAULT '',
  name            TEXT COLLATE "C" NOT NULL DEFAULT '',
  name_key        TEXT COLLATE "C" NOT NULL DEFAULT '',
  personal        BIGINT NOT NULL DEFAULT 0,
  records         TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  dirs            TEXT COLLATE "C" NOT NULL DEFAULT '[]',
  bytes           BIGINT NOT NULL DEFAULT 0,
  deleted_at      BIGINT NOT NULL,
  deleted_by      TEXT COLLATE "C" NOT NULL DEFAULT '',
  deleted_by_name TEXT COLLATE "C" NOT NULL DEFAULT '',
  reason          TEXT COLLATE "C" NOT NULL DEFAULT '',
  job_id          TEXT COLLATE "C" NOT NULL DEFAULT ''
);
CREATE INDEX recycle_items_tenant ON recycle_items(tenant_id, deleted_at DESC, id DESC);
CREATE INDEX recycle_items_purge ON recycle_items(deleted_at);
CREATE INDEX recycle_items_parent ON recycle_items(parent);
CREATE INDEX recycle_items_space ON recycle_items(space_id);
-- Items that are not at rest (a job holds them; one a crash or a failure left
-- is found again by the hourly look): a few rows.
CREATE INDEX recycle_items_busy ON recycle_items(job_id) WHERE job_id <> '';

-- Every username a purge has freed, case folded.
CREATE TABLE retired_usernames (
  name      TEXT COLLATE "C" PRIMARY KEY,
  purged_at BIGINT NOT NULL
);

-- Background work, one row per job whatever its kind (internal/jobs): a bulk
-- action over many spaces, people or items now; exports, recycle actions,
-- attachment and storage moves, rebuilds, invite sending and group sync are the
-- kinds reserved for it (docs/design/consolidation-2.md, section 4). params is
-- the request (JSON), failed a JSON list of the items that did not work and why,
-- result what the job made. lock_key, when set, allows one active job per key
-- (jobs_lock). capacity is the acting scope the job was asked in (site_admin,
-- tenant_admin or ''), site_only marks a job only site administrators see.
-- runner and lease_until name the process that claimed it; leasing (a standby
-- server) is reserved: today a start recovers every active row.
CREATE TABLE jobs (
  id           TEXT COLLATE "C" PRIMARY KEY,
  tenant_id    TEXT COLLATE "C" NOT NULL REFERENCES tenants(id),
  kind         TEXT COLLATE "C" NOT NULL,
  subject      TEXT COLLATE "C" NOT NULL DEFAULT '',
  lock_key     TEXT COLLATE "C" NOT NULL DEFAULT '',
  made_by      TEXT COLLATE "C" NOT NULL DEFAULT '',
  made_by_name TEXT COLLATE "C" NOT NULL DEFAULT '',
  capacity     TEXT COLLATE "C" NOT NULL DEFAULT '',
  site_only    BIGINT NOT NULL DEFAULT 0,
  params       TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  state        TEXT COLLATE "C" NOT NULL DEFAULT 'queued',
  total        BIGINT NOT NULL DEFAULT 0,
  done         BIGINT NOT NULL DEFAULT 0,
  bytes_total  BIGINT NOT NULL DEFAULT 0,
  bytes_done   BIGINT NOT NULL DEFAULT 0,
  failed       TEXT COLLATE "C" NOT NULL DEFAULT '[]',
  error        TEXT COLLATE "C" NOT NULL DEFAULT '',
  result       TEXT COLLATE "C" NOT NULL DEFAULT '{}',
  attempt      BIGINT NOT NULL DEFAULT 0,
  runner       TEXT COLLATE "C" NOT NULL DEFAULT '',
  lease_until  BIGINT NOT NULL DEFAULT 0,
  created      BIGINT NOT NULL,
  started      BIGINT NOT NULL DEFAULT 0,
  updated      BIGINT NOT NULL,
  expires      BIGINT NOT NULL DEFAULT 0
);
-- The runner's scans (at start, and when it is woken) read the active jobs only.
CREATE INDEX jobs_active ON jobs(state, created, id) WHERE state IN ('queued', 'running');
CREATE UNIQUE INDEX jobs_lock ON jobs(lock_key) WHERE lock_key <> '' AND state IN ('queued', 'running');
CREATE INDEX jobs_tenant ON jobs(tenant_id, kind, created DESC, id DESC);
CREATE INDEX jobs_expires ON jobs(expires) WHERE expires > 0;
-- The daily pass deletes the jobs that ended long ago (PruneJobs).
CREATE INDEX jobs_updated ON jobs(updated);

-- PostgreSQL: the tables whose rows are rewritten often (usage counters,
-- measurements of spaces, a person's last activity, a session's last use, a
-- remote's sync state) leave room in each page, so that an update that changes
-- no indexed column is a HOT update: no new index entries, and the old row is
-- reclaimed within the page. (With 80, 4891 of 5000 such updates were HOT,
-- against none at the default 100.)
-- dialect: postgres
ALTER TABLE usage SET (fillfactor = 85);
-- dialect: postgres
ALTER TABLE spaces SET (fillfactor = 85);
-- dialect: postgres
ALTER TABLE users SET (fillfactor = 85);
-- dialect: postgres
ALTER TABLE sessions SET (fillfactor = 85);
-- dialect: postgres
ALTER TABLE remotes SET (fillfactor = 85);
-- dialect: postgres
ALTER TABLE jobs SET (fillfactor = 85);
