-- 000142: 套餐续费顺延的回滚。
--
-- 注意顺序：先删流水表再删开关列。
-- 已发生的顺延（end_at 被加了天数）不会自动撤销——那是业务数据，
-- 需要按 bp_package_renewal_log 逐条减回；本文件的 DROP TABLE 之前请先导出该表。
-- 若直接回滚，行为退回「每次购买新建一条权益」，与升级前一致，不会报错。

DROP TABLE IF EXISTS `bp_package_renewal_log`;

ALTER TABLE `bp_package_interface_spec`
  DROP COLUMN `renewal_enabled`;

ALTER TABLE `bp_package_global`
  DROP COLUMN `renewal_enabled`;
