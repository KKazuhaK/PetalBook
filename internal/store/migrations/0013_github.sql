-- The GitHub App, users' GitHub authorisations and shared installations.
-- 0011 and 0012 belong to the MCP milestone (feat/mcp: API tokens and OAuth).
-- Each migration is recorded by its own number and the missing ones run at the
-- next start, so a database migrated on this branch alone picks up 0011 and
-- 0012 after the merge; the numbers must not collide, which is why this is 0013.
--
-- A user's access and refresh tokens are stored sealed (see the secret
-- package). version counts replacements: a renewal replaces the pair it was
-- made from and nothing newer. The App's own settings are in site_settings.
--
-- An installation is shared per tenant: github_installation_tenants has a row
-- for each tenant a site administrator shared it with. tenant_id is a plain
-- column, not a foreign key: the tenants table belongs to 0018, which may run
-- before or after this file. Deleting a tenant deletes its rows in code.
--
-- github_leftovers remembers, per user, an empty repository a failed attempt
-- to make a space left on GitHub: a reminder, cleared by the person or when a
-- space connects to the repository.

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
  tenant_id       TEXT NOT NULL,
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

ALTER TABLE remotes ADD COLUMN gh_via TEXT NOT NULL DEFAULT '';
ALTER TABLE remotes ADD COLUMN gh_installation_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE remotes ADD COLUMN gh_repo_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE remotes ADD COLUMN gh_user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX remotes_github_installation ON remotes(gh_installation_id);
CREATE INDEX remotes_github_user ON remotes(gh_user_id);
