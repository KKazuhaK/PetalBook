-- Server storage (milestone 3.1): when a space was last used, what its copy on
-- the server is (a full clone, a partial one, or evicted), whether an
-- administrator keeps it (pinned) or leaves it out of the requirement of a
-- remote (exempt), and the size of its index. The site's policy is in
-- site_settings ("storage"). A user can be exempt from the requirement too.
--
-- 0011 and 0012 are the MCP milestone's, 0013 is the GitHub App's and 0014 is
-- the sign-in milestone's; each migration is recorded by its own number.

ALTER TABLE spaces ADD COLUMN last_used BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN cache_state TEXT NOT NULL DEFAULT '';
ALTER TABLE spaces ADD COLUMN pinned BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN exempt BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN index_bytes BIGINT NOT NULL DEFAULT 0;
CREATE INDEX spaces_last_used ON spaces(last_used);
ALTER TABLE users ADD COLUMN remote_exempt BIGINT NOT NULL DEFAULT 0;
