-- IP 归属地数据源后台配置（单点表，id=1）
-- data_source_name: default=内置 ip-api.com；custom_xxx=自定义 URL + 字段映射
CREATE TABLE IF NOT EXISTS `bp_ip_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `data_source_name` varchar(64) NOT NULL DEFAULT 'default',
  `api_url` varchar(1024) NOT NULL DEFAULT '',
  `parse_rule` varchar(512) NOT NULL DEFAULT 'country=$.country, province=$.regionName, city=$.city, isp=$.isp',
  `enabled` tinyint NOT NULL DEFAULT 1,
  `remark` varchar(255) NOT NULL DEFAULT '',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP region data source config singleton';

INSERT INTO `bp_ip_config` (`id`, `data_source_name`, `api_url`, `parse_rule`, `enabled`, `remark`)
VALUES (1, 'default', '', 'country=$.country, province=$.regionName, city=$.city, isp=$.isp', 1, '内置默认源：ip-api.com（http://ip-api.com/json/{ip}?lang=zh-CN）')
ON DUPLICATE KEY UPDATE `id` = `id`;
