-- SEO 后台设置：bp_site_config 新增 SEO 相关列（P0 4 字段 + noindex 收录开关）
-- seo_title:     SEO 标题（空时前端回退 site_name）
-- seo_keywords:  SEO 关键词（英文逗号分隔，保存时中文逗号规范化为英文逗号）
-- og_image:      OG 分享图 URL（https?:// 绝对地址或 /system-api/upload/ 相对路径，可空）
-- site_url:      站点对外基础地址（https?:// 开头，结尾不带 /，可空；用于 sitemap/canonical）
-- noindex:       收录开关（0=允许收录，1=禁止收录，页面输出 noindex 且 sitemap 停发）
ALTER TABLE `bp_site_config`
  ADD COLUMN `seo_title` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'SEO 标题' AFTER `copyright`,
  ADD COLUMN `seo_keywords` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'SEO 关键词（英文逗号分隔）' AFTER `seo_title`,
  ADD COLUMN `og_image` varchar(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'OG 分享图 URL' AFTER `seo_keywords`,
  ADD COLUMN `site_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '站点对外基础地址（sitemap/canonical）' AFTER `og_image`,
  ADD COLUMN `noindex` tinyint(1) NOT NULL DEFAULT 0 COMMENT '收录开关：1=禁止收录' AFTER `site_url`;
