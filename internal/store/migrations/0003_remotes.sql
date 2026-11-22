-- The remote repository a space follows, and its credential. A row with an
-- empty url is a space that only has an SSH key pair so far (made before
-- connecting, so the public key can be added to the repository first).
-- Tokens and private keys are stored encrypted (see the secret package);
-- the other columns hold nothing secret.

CREATE TABLE remotes (
  space_id    TEXT PRIMARY KEY REFERENCES spaces(id) ON DELETE CASCADE,
  url         TEXT NOT NULL DEFAULT '',
  branch      TEXT NOT NULL DEFAULT 'main',
  cred_type   TEXT NOT NULL DEFAULT '',
  username    TEXT NOT NULL DEFAULT '',
  token_enc   TEXT NOT NULL DEFAULT '',
  privkey_enc TEXT NOT NULL DEFAULT '',
  pubkey      TEXT NOT NULL DEFAULT '',
  last_good   TEXT NOT NULL DEFAULT '',
  state       TEXT NOT NULL DEFAULT 'local',
  error_kind  TEXT NOT NULL DEFAULT '',
  last_error  TEXT NOT NULL DEFAULT '',
  remote_tip  TEXT NOT NULL DEFAULT '',
  last_sync   BIGINT NOT NULL DEFAULT 0,
  updated     BIGINT NOT NULL
);
