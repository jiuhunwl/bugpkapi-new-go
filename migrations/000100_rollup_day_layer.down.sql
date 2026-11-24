-- The day layer is a derived projection: every row is rebuildable from the
-- minute projections or the append-only call log, so dropping the tables loses
-- no source of truth. Roll back only after readers have been pointed back at
-- the minute layer, otherwise the 7-day trend and the active-user ranking will
-- fail to resolve a table.
DROP TABLE IF EXISTS `bp_call_user_metric_day`;
DROP TABLE IF EXISTS `bp_call_metric_day`;
