-- Milestone 2.8: sign-in providers, passkeys and the audit log. The security
-- settings live in site_settings (key 'security'). Secrets are sealed with the
-- server's key (see the secret package).

ALTER TABLE users ADD COLUMN email_verified BIGINT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN passkey_required BIGINT NOT NULL DEFAULT 0;

-- A held session may only register a passkey and sign out.
ALTER TABLE sessions ADD COLUMN held BIGINT NOT NULL DEFAULT 0;

-- config is JSON with nothing secret in it; secret_enc is the sealed client
-- secret (or the sealed SAML signing key); cache is the discovery document or
-- the identity provider's metadata, so a sign-in never fetches either. issuer is
-- the issuer of the identities the provider makes ('' while a SAML provider's
-- metadata has not been read); that two providers do not share one is checked
-- in code, because '' may repeat.
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
  updated              BIGINT NOT NULL
);
CREATE INDEX sign_in_providers_issuer ON sign_in_providers(issuer);

-- credential is the JSON of the WebAuthn credential (public key, counter,
-- flags); rp_id is the host the passkey was made for.
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

CREATE TABLE audit_log (
  id       TEXT PRIMARY KEY,
  at       BIGINT NOT NULL,
  kind     TEXT NOT NULL,
  actor    TEXT NOT NULL DEFAULT '',
  user_id  TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL DEFAULT '',
  ip       TEXT NOT NULL DEFAULT '',
  detail   TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at ON audit_log(at);
CREATE INDEX audit_log_kind ON audit_log(kind, id);
CREATE INDEX audit_log_user ON audit_log(user_id, id);
