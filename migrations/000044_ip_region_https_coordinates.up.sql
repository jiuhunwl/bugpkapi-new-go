-- Upgrade the built-in IP location defaults to an HTTPS source that returns
-- coordinates. Only untouched default rows are changed; custom providers and
-- administrator-edited mappings remain intact.
ALTER TABLE `bp_ip_config`
  MODIFY COLUMN `parse_rule` varchar(512) NOT NULL DEFAULT 'country=$.country, province=$.region, city=$.city, isp=$.connection.isp, latitude=$.latitude, longitude=$.longitude';

UPDATE `bp_ip_config`
SET `parse_rule` = 'country=$.country, province=$.region, city=$.city, isp=$.connection.isp, latitude=$.latitude, longitude=$.longitude',
    `remark` = '内置默认源：ipwho.is（HTTPS，含经纬度）；失败时自动回退国家级 HTTPS 数据源'
WHERE `id` = 1
  AND LOWER(TRIM(`data_source_name`)) = 'default'
  AND TRIM(`api_url`) = ''
  AND TRIM(`parse_rule`) = 'country=$.country, province=$.regionName, city=$.city, isp=$.isp'
  AND (
    TRIM(COALESCE(`remark`, '')) = ''
    OR TRIM(`remark`) = '内置默认源：ip-api.com（http://ip-api.com/json/{ip}?lang=zh-CN）'
  );
