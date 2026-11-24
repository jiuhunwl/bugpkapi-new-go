UPDATE `bp_menu`
SET `component` = '/payment/recharge-presets',
    `path` = '/payment/recharge-presets'
WHERE `name` = 'RechargePresetList';

UPDATE `bp_menu`
SET `component` = '/payment/orders',
    `path` = '/payment/orders'
WHERE `name` = 'PaymentOrderList';

UPDATE `bp_menu`
SET `component` = '/site/admin/config'
WHERE `name` = 'SiteConfig';

UPDATE `bp_menu`
SET `component` = '/friend-links/admin/list'
WHERE `name` = 'FriendLinkList';

UPDATE `bp_menu`
SET `component` = '/friend-links/admin/applications'
WHERE `name` = 'FriendLinkApplications';

UPDATE `bp_menu`
SET `component` = '/friend-links/admin/config'
WHERE `name` = 'FriendLinkConfig';

UPDATE `bp_menu`
SET `component` = '/dashboard-stats/admin'
WHERE `name` = 'AdminDashboardStats';

UPDATE `bp_menu`
SET `component` = '/dashboard-stats/user'
WHERE `name` = 'UserDashboardStats';

UPDATE `bp_menu`
SET `component` = '/package/billing-records'
WHERE `name` = 'BillingRecordList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '套餐商城')
WHERE `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'Payment orders')
WHERE `name` = 'UserPaymentOrder';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '数据看板')
WHERE `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'Payment')
WHERE `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'Alipay Config')
WHERE `name` = 'PaymentConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '内容运营')
WHERE `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '公告管理')
WHERE `name` = 'NoticeList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '首页滚动公告')
WHERE `name` = 'HomeNoticeConfig';

UPDATE `bp_menu`
SET `path` = '/home',
    `component` = NULL,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '首页概览')
WHERE `name` = 'HomeOverview';

UPDATE `bp_menu`
SET `component` = '/package/global'
WHERE `name` = 'GlobalPackageList';

UPDATE `bp_menu`
SET `component` = '/package/interface'
WHERE `name` = 'InterfacePackageList';

UPDATE `bp_menu`
SET `component` = '/package/point'
WHERE `name` = 'PointPackageList';

UPDATE `bp_menu`
SET `component` = '/purchase/global'
WHERE `name` = 'UserGlobalPackage';

UPDATE `bp_menu`
SET `component` = '/purchase/interface'
WHERE `name` = 'UserInterfacePackage';

UPDATE `bp_menu`
SET `component` = '/purchase/point'
WHERE `name` = 'UserPointPackage';
