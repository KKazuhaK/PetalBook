-- The GitHub App, users' GitHub authorisations and shared installations.
-- 0011 is left to the API tokens of the MCP milestone (feat/mcp); migrations
-- are applied by number in any order, so the two merge in either order.
--
-- A user's access and refresh tokens are stored sealed (see the secret
-- package). version counts replacements: a renewal replaces the pair it was
-- made from and nothing newer. The App's own settings are in site_settings.

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
CREATE INDEX github_authorizations_renew ON github_authorizations(state, refresh_expires);

CREATE TABLE github_installations (
  id           BIGINT PRIMARY KEY,
  account      TEXT NOT NULL,
  account_type TEXT NOT NULL DEFAULT 'Organization',
  shared       BIGINT NOT NULL DEFAULT 0,
  updated      BIGINT NOT NULL
);
CREATE INDEX github_installations_shared ON github_installations(shared, id);

ALTER TABLE remotes ADD COLUMN gh_via TEXT NOT NULL DEFAULT '';
ALTER TABLE remotes ADD COLUMN gh_installation_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE remotes ADD COLUMN gh_repo_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE remotes ADD COLUMN gh_user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX remotes_github_installation ON remotes(gh_installation_id);
CREATE INDEX remotes_github_user ON remotes(gh_user_id);
