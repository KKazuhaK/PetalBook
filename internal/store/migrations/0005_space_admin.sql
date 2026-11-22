-- What the administrators of a server need to know about a space. A disabled
-- space stays in its members' lists but cannot be opened. The size is not
-- measured when the list is asked for (that would walk every directory of
-- every space): a background job stores it, with when, and the list reads the
-- stored value; both are NULL until the first measurement.

ALTER TABLE spaces ADD COLUMN disabled BIGINT NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN size_bytes BIGINT;
ALTER TABLE spaces ADD COLUMN size_at BIGINT;
CREATE INDEX spaces_size_at ON spaces(size_at);
