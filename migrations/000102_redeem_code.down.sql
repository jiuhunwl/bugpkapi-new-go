-- Redeem codes are self-contained feature tables plus their menu seeds; none of
-- them is referenced by an existing table, so the rollback is a straight drop
-- in dependency order (records first, then the templates they point at).
--
-- The bp_menu / bp_role_menu deletes mirror the seeds in the up migration by id
-- range so a rollback leaves no orphan console entries behind.
DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 220 AND 228;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 220 AND 228;

DROP TABLE IF EXISTS `bp_redeem_ip_counter`;
DROP TABLE IF EXISTS `bp_redeem_generation_batch`;
DROP TABLE IF EXISTS `bp_redeem_record`;
DROP TABLE IF EXISTS `bp_redeem_code`;
DROP TABLE IF EXISTS `bp_redeem_code_type`;
DROP TABLE IF EXISTS `bp_redeem_guard_config`;
