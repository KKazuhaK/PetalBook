-- The folder of a space that quick notes go to. It is only a name: the
-- folder is made when the first page is created in it.

ALTER TABLE spaces ADD COLUMN inbox TEXT NOT NULL DEFAULT 'Inbox';
