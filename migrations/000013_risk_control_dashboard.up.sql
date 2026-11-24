ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_interface_call_log_client_ip_time` (`client_ip`, `created_at`),
  ADD KEY `idx_bp_interface_call_log_created_id` (`created_at`, `id`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (170, 90, 'RiskControlDashboard', '/risk-control/dashboard', '/risk-control/dashboard', NULL, 'menu', 'risk-control:view', JSON_OBJECT('title', 'system.riskControl.dashboardTitle', 'icon', 'lucide:shield-alert', 'order', 3), 1),
  (171, 90, 'RiskControlEarth', '/risk-control/earth', '/risk-control/earth', NULL, 'menu', 'risk-control:earth:view', JSON_OBJECT('title', 'system.riskControl.earthTitle', 'icon', 'lucide:globe-2', 'order', 4), 1),
  (172, 170, 'RiskControlEventList', '', NULL, NULL, 'button', 'risk-control:event:list', JSON_OBJECT('title', '风控事件列表', 'order', 1), 1),
  (173, 171, 'RiskControlEarthView', '', NULL, NULL, 'button', 'risk-control:earth:view', JSON_OBJECT('title', '地球视角监控', 'order', 1), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 170 AND 173;
