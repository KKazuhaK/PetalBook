-- Every stored draft gets a new opaque version, so two devices of one person
-- cannot silently replace each other's draft. Drafts that already exist get a
-- distinct version of their own.

ALTER TABLE drafts ADD COLUMN version TEXT NOT NULL DEFAULT '';
UPDATE drafts SET version = 'v-' || space_id || '-' || page_id || '-' || user_id;
