-- A person's favourites in a space: pages, folders and saved views, in the
-- order the person put them. They belong to the person and the space and are
-- not part of the space's repository. A favourite is kept when the person
-- leaves the space (it is not shown, and is there again when they come back);
-- deleting the space or the user deletes it.
--
-- kind is page (target is the page id), folder (the folder path) or view
-- (folder:<path>#<view id> or tag:<tag>#<view id>). Whether the target still
-- exists is not stored: it is looked at when the list is read.

CREATE TABLE favorites (
  user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  kind     TEXT NOT NULL,
  target   TEXT NOT NULL,
  position BIGINT NOT NULL,
  created  BIGINT NOT NULL,
  PRIMARY KEY (user_id, space_id, kind, target)
);
CREATE INDEX favorites_order ON favorites(user_id, space_id, position);
CREATE INDEX favorites_space ON favorites(space_id);
