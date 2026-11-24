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
CREATE INDEX users_live ON users(tenant_id, created, id) WHERE deleted_at IS NULL;

-- A zip of what some spaces hold, made on request (state: queued, running,
-- ready, failed) and kept until expires.
CREATE TABLE exports (
  id           TEXT PRIMARY KEY,
  tenant_id    TEXT NOT NULL,
  made_by      TEXT NOT NULL,
  made_by_name TEXT NOT NULL DEFAULT '',
  spaces       TEXT NOT NULL DEFAULT '[]',
  history      BIGINT NOT NULL DEFAULT 0,
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
CREATE TABLE bulk_jobs (
  id         TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  made_by    TEXT NOT NULL,
  action     TEXT NOT NULL,
  total      BIGINT NOT NULL,
  done       BIGINT NOT NULL DEFAULT 0,
  failed     TEXT NOT NULL DEFAULT '[]',
  state      TEXT NOT NULL DEFAULT 'queued',
  created    BIGINT NOT NULL,
  updated    BIGINT NOT NULL
);
CREATE INDEX bulk_jobs_state ON bulk_jobs(state) WHERE state IN ('queued', 'running');
