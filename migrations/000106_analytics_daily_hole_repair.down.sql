-- Forward recovery intent has no destructive rollback. Never rewind the
-- committed checkpoint or resurrect a half-cleared projection on downgrade.
SELECT 1 WHERE 0;
