UPDATE `bp_menu`
SET `component` = '/payment/amount/index',
    `path` = '/payment/amounts',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.amountTitle')
WHERE `name` = 'RechargePresetList';

UPDATE `bp_menu`
SET `component` = '/payment/order/index',
    `path` = '/payment/orders',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.orderTitle')
WHERE `name` = 'PaymentOrderList';

UPDATE `bp_menu`
SET `component` = '/system/site/index',
    `path` = '/content/site',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.site.title')
WHERE `name` = 'SiteConfig';

UPDATE `bp_menu`
SET `component` = '/friend-link/list',
    `path` = '/content/friend-links',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.friendLink.title')
WHERE `name` = 'FriendLinkList';

UPDATE `bp_menu`
SET `component` = '/friend-link/applications',
    `path` = '/content/friend-link-applications',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.friendLink.applicationTitle')
WHERE `name` = 'FriendLinkApplications';

UPDATE `bp_menu`
SET `component` = '/friend-link/config',
    `path` = '/content/friend-link-config',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.friendLink.configTitle')
WHERE `name` = 'FriendLinkConfig';

UPDATE `bp_menu`
SET `component` = '/dashboard/workspace/index',
    `path` = '/dashboard-stats/admin',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.dashboard.adminTitle')
WHERE `name` = 'AdminDashboardStats';

UPDATE `bp_menu`
SET `component` = '/dashboard/workspace/index',
    `path` = '/dashboard-stats/user',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.dashboard.userTitle')
WHERE `name` = 'UserDashboardStats';

UPDATE `bp_menu`
SET `component` = '/interface/log',
    `path` = '/package/billing-records',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.billingRecordTitle')
WHERE `name` = 'BillingRecordList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.mallTitle')
WHERE `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.userPaymentOrderTitle')
WHERE `name` = 'UserPaymentOrder';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.dashboard.title')
WHERE `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.title')
WHERE `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.configTitle')
WHERE `name` = 'PaymentConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.content.title')
WHERE `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.notice.listTitle')
WHERE `name` = 'NoticeList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.notice.homeConfigTitle')
WHERE `name` = 'HomeNoticeConfig';

UPDATE `bp_menu`
SET `path` = '/dashboard/home-overview',
    `component` = '/dashboard/workspace/index',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.home.overviewTitle')
WHERE `name` = 'HomeOverview';

UPDATE `bp_menu`
SET `component` = '/package/global/list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.globalTitle')
WHERE `name` = 'GlobalPackageList';

UPDATE `bp_menu`
SET `component` = '/package/interface/list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.interfaceTitle')
WHERE `name` = 'InterfacePackageList';

UPDATE `bp_menu`
SET `component` = '/package/point/list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.pointTitle')
WHERE `name` = 'PointPackageList';

UPDATE `bp_menu`
SET `component` = '/purchase/global/list'
WHERE `name` = 'UserGlobalPackage';

UPDATE `bp_menu`
SET `component` = '/purchase/interface/list'
WHERE `name` = 'UserInterfacePackage';

UPDATE `bp_menu`
SET `component` = '/purchase/point/list'
WHERE `name` = 'UserPointPackage';
