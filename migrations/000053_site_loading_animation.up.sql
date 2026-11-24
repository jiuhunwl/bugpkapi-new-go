-- Optional custom startup animation. The application always keeps its built-in
-- lightweight CSS loader as a safe fallback when this option is disabled.
ALTER TABLE `bp_site_config`
  ADD COLUMN `loading_animation_enabled` tinyint NOT NULL DEFAULT 0
    COMMENT '1=use configured startup animation image, 0=use built-in CSS animation'
    AFTER `noindex`,
  ADD COLUMN `loading_animation_url` varchar(1024) NOT NULL DEFAULT ''
    COMMENT 'Uploaded or external startup animation image URL'
    AFTER `loading_animation_enabled`;

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (189,130,'SiteLoadingAnimationUpload','',NULL,NULL,'button','site:upload-loading-animation',JSON_OBJECT('title','上传启动动画','order',2),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`) VALUES (1,189);
