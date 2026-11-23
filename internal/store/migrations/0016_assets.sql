-- The attachment repository of a space (milestone 3.1): a second remote that
-- holds the space's attachments, with the same columns as the space's own
-- remote, a mark for "an attachment has gone to it" (after that it cannot be
-- taken away) and the progress of moving the attachments of the space into it.
-- At most one row per space. The credential is stored as in remotes.
--
-- 0014 is the sign-in milestone's and 0015 the storage policy's; each
-- migration is recorded by its own number.

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
