-- 修复历史缺陷：调用日志“链接或值”(request_value) 列曾误写入客户端 IP，
-- 导致该列与“调用IP”(client_ip) 列显示相同值（错位/重复）。
-- 只清理 request_value 精确等于 client_ip、且 request_value 本身是合法 IP 字面量的行，
-- 避免误伤正常把 IP 作为请求值传入的业务数据。
UPDATE `bp_interface_call_log`
SET `request_value` = ''
WHERE `request_value` = `client_ip`
  AND (`request_value` REGEXP '^[0-9]{1,3}(\\.[0-9]{1,3}){3}$' OR INET6_ATON(`request_value`) IS NOT NULL)
  AND `request_value` <> '';
