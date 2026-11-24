-- The body extraction is forward-only. The side table is safe to drop (the
-- main column still holds the historical bodies), but reverting the code while
-- the main column exists is a code-level rollback, not a data rollback. Never
-- delete the side table while the application still reads from it.
SELECT 1 WHERE 0;
