-- Why a session ended (the sign-in page says so when someone comes back to it).
-- When a session ends without the person signing out (it ran out, it was
-- signed out from another device, the password changed, the account was
-- disabled), one row is kept with the SHA-256 of its cookie value, the reason
-- and the time. A row is looked up only by the hash of a cookie the browser
-- sent, so it tells nothing to a cookie that was never a session. The rows are
-- swept after 30 days.
--
-- No user column and no foreign key: nothing needs to know whose it was, and
-- deleting the user must not bring the row back as a question.
--
-- The number is the next free one when this was written; renumber it if
-- another change took it.
CREATE TABLE session_ends (
  id     TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  ended  BIGINT NOT NULL
);
CREATE INDEX session_ends_ended ON session_ends(ended);
