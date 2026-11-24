UPDATE `bp_site_config`
SET
  `site_name` = REPLACE('BugPk-Api', '-', ' '),
  `description` = CONCAT(REPLACE('BugPk-Api', '-', ' '), ' service platform'),
  `copyright` = CONCAT('Copyright ', REPLACE('BugPk-Api', '-', ' '))
WHERE `id` = 1
  AND `site_name` = 'BugPk-Api'
  AND `description` = 'BugPk-Api service platform'
  AND `copyright` = 'Copyright BugPk-Api';
