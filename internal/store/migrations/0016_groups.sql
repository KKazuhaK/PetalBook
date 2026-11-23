-- Milestone 3.2: groups. A group is a set of people that can be a member of a
-- shared space with a role. source is 'local' (an administrator keeps its
-- people) or 'synced' (its people come from a sign-in provider's groups claim).
-- name_key is the case-folded name, unique like a space's.

CREATE TABLE groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  name_key    TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL,
  created     BIGINT NOT NULL,
  updated     BIGINT NOT NULL
);

-- provider and value say where a synced membership came from (the provider's
-- slug and the claim value); both are '' for a local group. A person may reach
-- one group through several values, one row each.
CREATE TABLE group_members (
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL DEFAULT '',
  value    TEXT NOT NULL DEFAULT '',
  created  BIGINT NOT NULL,
  PRIMARY KEY (group_id, user_id, provider, value)
);
CREATE INDEX group_members_user ON group_members(user_id);

CREATE TABLE space_groups (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  role     TEXT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (space_id, group_id)
);
CREATE INDEX space_groups_group ON space_groups(group_id);
