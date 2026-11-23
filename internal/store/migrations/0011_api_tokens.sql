-- API tokens (milestone 2.6): a person's secret for scripts and MCP clients,
-- limited to a few spaces and to read or read and write. Only the SHA-256 of
-- the secret is kept, with its first characters (prefix) to tell tokens apart.
-- Times are Unix milliseconds; expires and revoked are 0 for "never" and "not
-- revoked", last_used is 0 until the token is first used.
--
-- api_token_spaces names the spaces of a token. A space that is deleted stays
-- listed (it does nothing), so space_id is not a foreign key; the token's
-- owner leaving or being deleted takes the token with them.

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
