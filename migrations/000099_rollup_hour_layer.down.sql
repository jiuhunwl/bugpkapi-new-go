-- The hour layer is a derived projection: every row can be rebuilt from the
-- minute projections or the append-only call log, so dropping the tables loses
-- no source of truth. Roll back only after readers have been pointed back at
-- the minute layer, otherwise wide windows will fail to resolve a table.
DROP TABLE IF EXISTS `bp_call_api_metric_hour`;
DROP TABLE IF EXISTS `bp_call_user_metric_hour`;
DROP TABLE IF EXISTS `bp_call_metric_hour`;
