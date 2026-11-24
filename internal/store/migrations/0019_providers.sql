-- Connections to GitLab and Gitea/Forgejo/Codeberg (milestone 3.8). One row of
-- providers is one OAuth application on one host, set up by an administrator;
-- GitHub keeps its own tables (0013) and is not in here.
--
-- owner is 'site' or the id of the tenant that owns the provider (3.6 lets a
-- tenant's administrators set one up; the column and the visibility rule are
-- here so that nothing about them changes later). A plain column, not a
-- foreign key: the tenants table belongs to 0018 and a tenant's rows are
-- removed in code.
--
-- secret_enc is the client secret, sealed with the provider's id in the purpose.
-- partial_sync is 'yes', 'no' or 'unknown'; partial_checked is when it was
-- last detected (0: never).
CREATE TABLE providers (
  id              TEXT PRIMARY KEY,
  type            TEXT NOT NULL,
  host            TEXT NOT NULL,
  scheme          TEXT NOT NULL DEFAULT 'https',
  name            TEXT NOT NULL,
  owner           TEXT NOT NULL DEFAULT 'site',
  client_id       TEXT NOT NULL,
  secret_enc      TEXT NOT NULL,
  state           TEXT NOT NULL DEFAULT 'ok',
  platform_version TEXT NOT NULL DEFAULT '',
  partial_sync    TEXT NOT NULL DEFAULT 'unknown',
  partial_checked BIGINT NOT NULL DEFAULT 0,
  checked         BIGINT NOT NULL DEFAULT 0,
  updated_by      TEXT NOT NULL DEFAULT '',
  created         BIGINT NOT NULL,
  updated         BIGINT NOT NULL
);
CREATE UNIQUE INDEX providers_owner_host ON providers(owner, host);

-- A person's connection to an account on a provider's host. Tokens are sealed
-- separately (access, refresh). One account on a host links to one user, and
-- a user has one connection per provider. is_admin is 1 when the host said, when
-- the person connected, that the account is an administrator of the host (Gitea
-- and Forgejo say it): the token then carries those rights.
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

-- Reminders of empty repositories a failed attempt to make a space left on a
-- provider, as github_leftovers does for GitHub.
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
