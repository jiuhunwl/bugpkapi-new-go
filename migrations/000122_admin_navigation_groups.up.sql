-- 侧边栏分组重排：顶级菜单原本按「谁先建谁在前」堆积（order 混用 -1/3/4/5/9995/9996/9997
-- 与 2147483647），支付藏在「套餐管理」里、菜单管理藏在「系统管理」里、站内信群发也埋在
-- 系统管理下，同级还有 60/84 同为 order 3、230/176 同为 order 7 这类并列值，排序只能靠
-- id 兜底。这里按业务动线重排成 11 组，并把同级 order 摊成 10/20/30 留插队余量。
--
-- 只动 pid 与 meta.order/title/component：菜单权限码（auth_code）与 bp_role_menu 授权关系
-- 一律不改，已被单独授权子页的角色会由 UserMenus 的祖先回溯自动看到新父级，无需重新授权。
--
-- 另修两处真实错位：
--   1. 账单记录（83）的 component 直接写成接口日志页 /interface/log，与「调用日志」共用
--      同一条组件指针；改成 /package/billing-records 后仍由前端别名表解析到同一页面，
--      而该页依据路由路径（…/billing-records）判定「仅计费流水」模式，行为不变、来源可辨。
--   2. 16 行 meta.title 是历史错误字符集导入留下的字面「?」（迁移源文件本身是 UTF-8），
--      侧边栏与菜单管理页都会显示成问号。这里改成前端已有的 i18n 键，中英文各一份。

-- ── 1. 顶级分组：顺序 + 标题 ────────────────────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.interfaceGateway',
  '$.order', 20
)
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.order', 30,
  '$.adminOrder', 30,
  '$.userOrder', 3
)
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 70 AND `name` = 'PackageManagement';

-- 支付原本是套餐管理的子目录，续费/对账与套餐配置是两条独立动线，提升为顶级。
UPDATE `bp_menu`
SET `pid` = 0,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'navigation.admin.payment',
      '$.order', 50
    )
WHERE `id` = 100 AND `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.userPermissions',
  '$.order', 60
)
WHERE `id` = 10 AND `name` = 'UserManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 70)
WHERE `id` = 120 AND `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 80)
WHERE `id` = 90 AND `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 100)
WHERE `id` = 30 AND `name` = 'System';

-- ── 2. 新增「工单与支持」分组，收拢工单管理与站内信群发 ────────────────────
-- 204 是页面（type=menu），不能直接当容器；新建目录承接，原路径与组件保持不变。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
SELECT
  900100, 0, 'TicketSupportHub', '/admin/support', NULL, '/admin/tickets', 'catalog', NULL,
  JSON_OBJECT('title', 'navigation.admin.ticketsSupport', 'icon', 'lucide:life-buoy', 'order', 90, 'audience', 'admin'),
  1
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `bp_menu` m WHERE m.`name` = 'TicketSupportHub');

UPDATE `bp_menu`
SET `pid` = 900100,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 204 AND `name` = 'AdminTicketManagement';

UPDATE `bp_menu`
SET `pid` = 900100,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.order', 20,
      '$.audience', 'admin'
    )
WHERE `id` = 241 AND `name` = 'SystemInboxBroadcast';

-- ── 3. 子页归位 ────────────────────────────────────────────────────────────
-- 菜单管理属于「谁能看到什么」，与用户/角色同组，不再是系统管理的内部配置。
UPDATE `bp_menu`
SET `pid` = 10,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 31 AND `name` = 'SystemMenu';

-- 兑换码原本是独立顶级目录，实际与套餐/账单同属一条售卖动线。
-- 221-223 直接挂到套餐与账单下，220 保留成隐藏别名路由，旧书签仍可跳转。
UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 60)
WHERE `id` = 221 AND `name` = 'RedeemTypes';

UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 70)
WHERE `id` = 222 AND `name` = 'RedeemCodes';

UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 80)
WHERE `id` = 223 AND `name` = 'RedeemRecords';

UPDATE `bp_menu`
SET `redirect` = '/redeem/codes',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000122-redeem-into-packages'
    )
WHERE `id` = 220 AND `name` = 'RedeemCode';

-- 操作日志与日志清理是数据侧运维，归「数据与风控」，不留在系统设置里。
UPDATE `bp_menu`
SET `pid` = 90,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 211 AND `name` = 'TicketAdminAttachment';

UPDATE `bp_menu`
SET `pid` = 90,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 160 AND `name` = 'SystemLogDelete';

-- ── 4. 同级 order 摊开为 10/20/30，消除并列 ─────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 71 AND `name` = 'GlobalPackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 75 AND `name` = 'InterfacePackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 79 AND `name` = 'PointPackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 243 AND `name` = 'UserPackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 50)
WHERE `id` = 83 AND `name` = 'BillingRecordList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 101 AND `name` = 'PaymentConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 103 AND `name` = 'RechargePresetList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 107 AND `name` = 'PaymentOrderList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 11 AND `name` = 'SystemUser';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 20 AND `name` = 'SystemRole';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 121 AND `name` = 'NoticeList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 130 AND `name` = 'SiteConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 138 AND `name` = 'FriendLinkCenter';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 181 AND `name` = 'AdvertisementList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 50)
WHERE `id` = 190 AND `name` = 'DonationConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 170 AND `name` = 'RiskControlDashboard';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 150 AND `name` = 'SystemRegisterOpenConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 152 AND `name` = 'SystemRegisterEmailConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 155 AND `name` = 'SystemRegisterMobileConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 158 AND `name` = 'SystemOAuthLoginConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 50)
WHERE `id` = 176 AND `name` = 'SystemIpSourceConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 60)
WHERE `id` = 179 AND `name` = 'SystemCaptchaConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 70)
WHERE `id` = 185 AND `name` = 'SystemCustomCode';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 80)
WHERE `id` = 230 AND `name` = 'SystemOpenApiConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 90)
WHERE `id` = 232 AND `name` = 'SystemOpenApiKeys';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 100)
WHERE `id` = 250 AND `name` = 'SystemTranslation';

-- 用户侧顶级顺序同时去并列：套餐与资产、服务支持、个人设置依次排在注入的
-- 接口服务(2)/调用日志(2.1)/我的密钥(3)/我的友链(4) 之后。
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 5)
WHERE `id` = 84 AND `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 6)
WHERE `id` = 210 AND `name` = 'UserServiceSupport';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 7)
WHERE `id` = 203 AND `name` = 'Profile';

-- ── 5. 账单记录组件指针 ─────────────────────────────────────────────────────
UPDATE `bp_menu`
SET `component` = '/package/billing-records'
WHERE `id` = 83 AND `name` = 'BillingRecordList';

-- ── 6. 问号标题改回 i18n 键（沿用前端各功能页已有文案）─────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'ipConfig.title')
WHERE `id` = 176 AND `name` = 'SystemIpSourceConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'ipConfig.save')
WHERE `id` = 177 AND `name` = 'SystemIpSourceConfigUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'captchaConfig.title')
WHERE `id` = 179 AND `name` = 'SystemCaptchaConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'captchaConfig.save')
WHERE `id` = 180 AND `name` = 'SystemCaptchaConfigUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.title')
WHERE `id` = 181 AND `name` = 'AdvertisementList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.create')
WHERE `id` = 182 AND `name` = 'AdvertisementCreate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.editor.edit')
WHERE `id` = 183 AND `name` = 'AdvertisementUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.delete.action')
WHERE `id` = 184 AND `name` = 'AdvertisementDelete';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'customCode.title')
WHERE `id` = 185 AND `name` = 'SystemCustomCode';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'customCode.save')
WHERE `id` = 186 AND `name` = 'SystemCustomCodeUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'deliveryPolicy.title')
WHERE `id` = 187 AND `name` = 'InterfaceDeliveryPolicy';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'requestValidation.title')
WHERE `id` = 188 AND `name` = 'InterfaceRequestValidation';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'siteSettings.loading.fields.upload'
)
WHERE `id` = 189 AND `name` = 'SiteLoadingAnimationUpload';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'donationSettings.title')
WHERE `id` = 190 AND `name` = 'DonationConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'donationSettings.actions.save')
WHERE `id` = 191 AND `name` = 'DonationConfigUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'donationSettings.actions.uploadQrCode')
WHERE `id` = 192 AND `name` = 'DonationQRCodeUpload';
