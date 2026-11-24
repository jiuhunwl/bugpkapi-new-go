-- ============================================================
-- 000087 down: remove configurable stop-polling code column
-- ============================================================

ALTER TABLE `bp_interface_api`
  DROP COLUMN `polling_stop_code`;