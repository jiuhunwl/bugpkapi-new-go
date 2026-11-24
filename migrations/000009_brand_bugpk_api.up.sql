-- 为全新安装补齐站点基础信息；已有自定义值的字段保持不变。
UPDATE `bp_site_config`
SET
  `site_name` = CASE WHEN TRIM(`site_name`) = '' THEN 'BugPk-Api' ELSE `site_name` END,
  `slogan` = CASE WHEN TRIM(`slogan`) = '' THEN 'Open API service platform' ELSE `slogan` END,
  `description` = CASE WHEN TRIM(`description`) = '' THEN 'BugPk-Api service platform' ELSE `description` END,
  `copyright` = CASE WHEN TRIM(`copyright`) = '' THEN 'Copyright BugPk-Api' ELSE `copyright` END
WHERE `id` = 1;