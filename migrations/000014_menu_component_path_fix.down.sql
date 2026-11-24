UPDATE `bp_menu`
SET `component` = '/payment/config'
WHERE `name` = 'PaymentConfig'
  AND `component` = '/payment/config/index';

UPDATE `bp_menu`
SET `component` = '/notice/admin/home-notice'
WHERE `name` = 'HomeNoticeConfig'
  AND `component` = '/notice/admin/home-notice/index';
