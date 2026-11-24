UPDATE `bp_menu`
SET `component` = '/payment/config/index'
WHERE `name` = 'PaymentConfig'
  AND `component` = '/payment/config';

UPDATE `bp_menu`
SET `component` = '/notice/admin/home-notice/index'
WHERE `name` = 'HomeNoticeConfig'
  AND `component` = '/notice/admin/home-notice';
