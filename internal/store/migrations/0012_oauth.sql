-- The authorization server for MCP clients (milestone 2.6, "OAuth for MCP
-- clients" in docs/api.md). Times are Unix milliseconds.
--
-- oauth_keys holds the ES256 key that signs access tokens, sealed with the
-- server key like the other secrets; one row, made at the first start by
-- whichever server gets there first.
--
-- oauth_clients are the clients that registered themselves (RFC 7591). A client
-- named by a metadata document is never stored. fingerprint is the SHA-256 of
-- the name and the redirect URIs, so a client that registers again with the
-- same details finds its row instead of making another.
--
-- A grant is one approval of one client by one person: the access it gives, the
-- hash of the one refresh token that works now (refresh_hash) and of the few
-- replaced ones (oauth_retired), never a token. Revoking a grant deletes it.
-- Like a token's spaces, oauth_grant_spaces are not foreign keys: a space that
-- is deleted stays named and does nothing.

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
