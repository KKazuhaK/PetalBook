-- The author of a line of the daily text the administrators gave (the line's
-- source, where it is from, is the column that was already there).
ALTER TABLE daily_lines ADD COLUMN author TEXT NOT NULL DEFAULT '';
