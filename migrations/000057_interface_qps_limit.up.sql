ALTER TABLE `bp_interface_api`
  ADD COLUMN `qps_limit` int NOT NULL DEFAULT 0
    COMMENT 'Interface-wide QPS hard limit shared by all users and keys; 0 means unlimited'
    AFTER `point_price`;
