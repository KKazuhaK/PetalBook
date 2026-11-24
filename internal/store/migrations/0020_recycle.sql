-- Milestone 3.5: the recycle bin, the people who were deleted, and the exports
-- of what a space holds. (0019 is left to phase 4.) This exists so that
-- development and demonstration databases open; it promises no upgrade that
-- keeps data, and the new columns start empty.

-- One row per space or person that went into the recycle bin. A space's rows
-- (the space, its members, groups, remotes, attachment remote, favourites,
-- drafts, daily settings and folder moves) are kept whole in records (JSON) and
-- are gone from the live tables, so no query of those tables can see it; its
-- directories are DATA/recycle/<id>/space and DATA/recycle/<id>/cache. dirs
-- lists those that were moved (a JSON list), state is where the item is in its
-- life: moving (the rows are gone, the directories are being moved),
-- ready, restoring, purging; parent is the item of the person a space was
-- recycled with ('' for none).
CREATE TABLE recycle_items (
  id              TEXT PRIMARY KEY,
  tenant_id       TEXT NOT NULL,
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
-- The list of a tenant, newest first, and the purge job's look at the oldest.
CREATE INDEX recycle_items_tenant ON recycle_items(tenant_id, deleted_at DESC, id DESC);
CREATE INDEX recycle_items_purge ON recycle_items(deleted_at);
CREATE INDEX recycle_items_parent ON recycle_items(parent);
CREATE INDEX recycle_items_space ON recycle_items(space_id);
-- Items that are not at rest (a crash leaves them): a few rows, found at start.
CREATE INDEX recycle_items_busy ON recycle_items(state) WHERE state <> 'ready';

-- A person who was deleted: deleted_at is when (NULL for everyone else), and
-- recycle_item the item that holds what was taken from them. The lists of
-- people read the partial index, which holds only those who are not deleted.
ALTER TABLE users ADD COLUMN deleted_at BIGINT;
ALTER TABLE users ADD COLUMN recycle_item TEXT NOT NULL DEFAULT '';
-- A deletion of the person under way (JSON: whether they were disabled before
-- its first attempt, and the transfers and memberships removed so far), so that
-- asking again after a failure records what the first attempt knew. '' when
-- none is.
ALTER TABLE users ADD COLUMN delete_pending TEXT NOT NULL DEFAULT '';
CREATE INDEX users_live ON users(tenant_id, created, id) WHERE deleted_at IS NULL;

-- A zip of what some spaces hold, made on request (state: queued, running,
-- ready, failed) and kept until expires. site_only marks an export a site
-- administrator made: only site administrators see it, whatever tenant_id says
-- (it may hold a personal space, a history bundle, or several tenants' spaces).
CREATE TABLE exports (
  id           TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL,
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

-- Every username a purge has freed, case folded. Read only when
-- users.reuseUsernames is "never", but written by every purge, so that switching
-- to "never" later also protects the names purged before.
CREATE TABLE retired_usernames (
  name      TEXT PRIMARY KEY,
  purged_at BIGINT NOT NULL
);
-- The daily sweep of tombstones asks whether the audit log still names a person.
CREATE INDEX audit_log_actor ON audit_log(actor);

-- A bulk action over many spaces, people or items, run in the background one
-- item at a time. failed is a JSON list of what did not work and why; a job that
-- was running when the server stopped reads "interrupted" at the next start.
-- site_only marks a job a site administrator started: it is the site's, as
-- such an export is.
CREATE TABLE bulk_jobs (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  made_by    TEXT NOT NULL,
  action     TEXT NOT NULL,
  total      BIGINT NOT NULL,
  done       BIGINT NOT NULL DEFAULT 0,
  failed     TEXT NOT NULL DEFAULT '[]',
  state      TEXT NOT NULL DEFAULT 'queued',
  site_only  BIGINT NOT NULL DEFAULT 0,
  created    BIGINT NOT NULL,
  updated    BIGINT NOT NULL
);
CREATE INDEX bulk_jobs_state ON bulk_jobs(state) WHERE state IN ('queued', 'running');

-- Part 4: storage management lists and summaries. Every number a list or the
-- summary shows is read from a column, never found by walking a disk or a
-- repository on a request.
--
-- What a space is, where it lives, and what its remote says, kept with the space
-- so that each filter of the list is a column. location says where the bytes
-- live: server (only here), own (a remote that belongs to the person), tenant
-- and site (3.6; the quota counts server and site, never own and tenant).
-- remote_via is '' without a remote, the provider of a connection, or 'git' for
-- a plain remote; remote_state is the remote's state ('' without one).
ALTER TABLE spaces ADD COLUMN location TEXT NOT NULL DEFAULT 'server';
ALTER TABLE spaces ADD COLUMN remote_via TEXT NOT NULL DEFAULT '';
ALTER TABLE spaces ADD COLUMN remote_state TEXT NOT NULL DEFAULT '';
-- A database that has spaces already gets the columns worked out here, from
-- what 0003 and 0013 knew (a connection is GitHub's, the only provider until
-- 3.8; the daily recount works them out again from the remotes' own provider).
UPDATE spaces SET
  remote_via = COALESCE((SELECT CASE WHEN r.cred_type = 'connection' THEN 'github' ELSE 'git' END FROM remotes r WHERE r.space_id = spaces.id AND r.url <> ''), ''),
  remote_state = COALESCE((SELECT r.state FROM remotes r WHERE r.space_id = spaces.id AND r.url <> ''), ''),
  location = CASE WHEN EXISTS (SELECT 1 FROM remotes r WHERE r.space_id = spaces.id AND r.url <> '') THEN 'own' ELSE 'server' END;
UPDATE spaces SET usage_bytes = 0 WHERE location NOT IN ('server', 'site');

-- When a person last used the server (a session or a token, written at most once
-- an hour) and when they first changed anything in a space; NULL until then.
ALTER TABLE users ADD COLUMN last_active BIGINT;
ALTER TABLE users ADD COLUMN first_write BIGINT;

-- The usage rows gain what the people and the summary list: what the server
-- holds for the spaces counted for the row (held_bytes), how many of them
-- follow a remote, are partial clones, are in their grace period or past it,
-- are evicted, archived or pinned, whether the person has a connection, and the
-- stored flag dormant (never used, never connected, no remote). A tenant's row
-- holds the sums over its people and spaces. created and last_active copy the
-- person's, so that a list of people is one index. (last_active 0: never.)
ALTER TABLE usage ADD COLUMN held_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN remote_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN partial_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN grace_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN overdue_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN evicted_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN archived_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN pinned_spaces BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN connected BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN dormant BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN created BIGINT NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN last_active BIGINT NOT NULL DEFAULT 0;

-- What the rows hold now, for a database that has some: the same as the daily
-- recount works out (the connection flag starts at 0; the recount sets it).
UPDATE usage SET
  held_bytes = COALESCE((SELECT SUM(CASE WHEN s.cache_state IN ('evicted', 'archived') THEN 0 ELSE COALESCE(s.size_bytes, 0) END + CASE WHEN s.cache_state = 'archived' THEN 0 ELSE s.index_bytes END)
    FROM spaces s WHERE CASE WHEN usage.kind = 'user' THEN s.usage_user = usage.id ELSE s.tenant_id = usage.id END), 0),
  remote_spaces = (SELECT COUNT(*) FROM spaces s WHERE s.remote_via <> '' AND CASE WHEN usage.kind = 'user' THEN s.usage_user = usage.id ELSE s.tenant_id = usage.id END),
  partial_spaces = (SELECT COUNT(*) FROM spaces s WHERE s.remote_via <> '' AND s.cache_state = 'partial' AND CASE WHEN usage.kind = 'user' THEN s.usage_user = usage.id ELSE s.tenant_id = usage.id END),
  evicted_spaces = (SELECT COUNT(*) FROM spaces s WHERE s.remote_via <> '' AND s.cache_state = 'evicted' AND CASE WHEN usage.kind = 'user' THEN s.usage_user = usage.id ELSE s.tenant_id = usage.id END),
  archived_spaces = (SELECT COUNT(*) FROM spaces s WHERE s.remote_via <> '' AND s.cache_state = 'archived' AND CASE WHEN usage.kind = 'user' THEN s.usage_user = usage.id ELSE s.tenant_id = usage.id END),
  pinned_spaces = (SELECT COUNT(*) FROM spaces s WHERE s.pinned <> 0 AND CASE WHEN usage.kind = 'user' THEN s.usage_user = usage.id ELSE s.tenant_id = usage.id END);
UPDATE usage SET created = COALESCE((SELECT u.created FROM users u WHERE u.id = usage.id), 0) WHERE kind = 'user';
UPDATE usage SET dormant = 1 WHERE kind = 'user' AND remote_spaces = 0 AND EXISTS (SELECT 1 FROM users u WHERE u.id = usage.id AND u.deleted_at IS NULL);
UPDATE usage SET dormant = (SELECT COUNT(*) FROM usage p WHERE p.kind = 'user' AND p.tenant_id = usage.id AND p.dormant = 1) WHERE kind = 'tenant';

-- The lists of people: one index for each sort, within a tenant and across all
-- of them (a site administrator's), and the dormant filter as a range. The sort
-- by bytes is usage_rank and usage_rank_all (0018).
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

-- The lists of spaces: one index for each sort, within a tenant and across all of
-- them; the filters that pick a few rows (a pin, an exemption, a remote that
-- needs attention) or a column (where the remote stands) have their own.
-- (0018's spaces_tenant on (tenant_id, created) is this with the id, which the keyset needs.)
DROP INDEX spaces_tenant;
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
