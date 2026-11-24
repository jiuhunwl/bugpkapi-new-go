package payment

import (
	"context"
	"database/sql"
	"errors"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 充值赞助自动升级用户组：RECHARGE 订单结算入账（余额到账）的同一个事务里，
// 把付款用户的主用户组切换到后台配置的目标用户组。
// 开关关闭、目标用户组未配置/停用/管理员组时保持原行为；
// 当前主角色是管理员组的用户永不自动切换，避免误降权。

// SponsorRoleConfig 充值赞助自动升级用户组配置。
type SponsorRoleConfig struct {
	Enabled  bool   `json:"enabled"`
	RoleID   int64  `json:"roleId"`
	RoleName string `json:"roleName"`
}

// SaveSponsorRoleConfigRequest 管理端保存请求。
type SaveSponsorRoleConfigRequest struct {
	Enabled bool   `json:"enabled"`
	RoleID  int64  `json:"roleId"`
	RoleKey string `json:"-"`
}

// defaultSponsorRoleConfig 未配置/未迁移时的兜底值（关闭状态）。
func defaultSponsorRoleConfig() SponsorRoleConfig {
	return SponsorRoleConfig{Enabled: false, RoleID: 0, RoleName: ""}
}

// SponsorRoleConfig 读取配置；行不存在（迁移未跑）返回关闭态默认值。
func (r *Repository) SponsorRoleConfig(ctx context.Context) (SponsorRoleConfig, error) {
	var cfg SponsorRoleConfig
	var enabled int
	var roleName sql.NullString
	err := r.db.QueryRowContext(ctx, `
		select c.enabled, c.role_id, coalesce(r.role_name, '')
		from bp_payment_sponsor_config c
		left join bp_role r on r.id = c.role_id
		where c.id = 1
	`).Scan(&enabled, &cfg.RoleID, &roleName)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultSponsorRoleConfig(), nil
	}
	if err != nil {
		return SponsorRoleConfig{}, err
	}
	cfg.Enabled = enabled == 1
	cfg.RoleName = roleName.String
	return cfg, nil
}

// SaveSponsorRoleConfig 保存配置（单例 upsert）。
// 调用方（Service 层）负责校验目标用户组合法性。
func (r *Repository) SaveSponsorRoleConfig(ctx context.Context, cfg SponsorRoleConfig) error {
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		insert into bp_payment_sponsor_config(id,enabled,role_id)
		values(1,?,?)
		on duplicate key update enabled=values(enabled),role_id=values(role_id)
	`, enabled, cfg.RoleID)
	return err
}

// SponsorRoleConfig 管理端读取配置。
func (s *Service) SponsorRoleConfig(ctx context.Context, principal Principal) (SponsorRoleConfig, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return SponsorRoleConfig{}, err
	}
	return s.repo.SponsorRoleConfig(ctx)
}

// SaveSponsorRoleConfig 管理端保存配置。
// 开启时必须选择一个已启用且非管理员组的目标用户组；
// 关闭时允许保留原 roleId（重新开启时直接生效）。
func (s *Service) SaveSponsorRoleConfig(ctx context.Context, principal Principal, req SaveSponsorRoleConfigRequest) error {
	if err := s.require(ctx, principal, "payment:config:update"); err != nil {
		return err
	}
	if req.Enabled {
		if req.RoleID <= 0 {
			return apperrors.New(consts.StatusBadRequest, "请选择充值后自动切换的目标用户组")
		}
		var roleKey, roleName string
		err := s.repo.db.QueryRowContext(ctx, `
			select role_key, role_name from bp_role
			where id = ? and status = 1 and role_key <> 'admin'
		`, req.RoleID).Scan(&roleKey, &roleName)
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusBadRequest, "目标用户组不存在、已停用或为管理员组")
		}
		if err != nil {
			return err
		}
	} else if req.RoleID < 0 {
		req.RoleID = 0
	}
	return s.repo.SaveSponsorRoleConfig(ctx, SponsorRoleConfig{Enabled: req.Enabled, RoleID: req.RoleID})
}

// applySponsorRoleTx 在结算事务内执行「充值即升级用户组」。
// 任意一步失败都会回滚整个结算事务（余额入账与用户组切换原子提交），
// 保证不会出现「钱到账了但没升级」或反过来的中间态。
func (r *Repository) applySponsorRoleTx(ctx context.Context, tx *sql.Tx, userID int64) error {
	var enabled int
	var roleID int64
	err := tx.QueryRowContext(ctx,
		`select enabled, role_id from bp_payment_sponsor_config where id = 1`,
	).Scan(&enabled, &roleID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if enabled != 1 || roleID <= 0 {
		return nil
	}
	// 目标用户组必须仍然启用且非管理员组；配置失效时静默跳过，不阻塞入账。
	var roleActive int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from bp_role
		where id = ? and status = 1 and role_key <> 'admin'
	`, roleID).Scan(&roleActive); err != nil {
		return err
	}
	if roleActive == 0 {
		return nil
	}
	// 当前主角色为管理员组的用户永不自动切换。
	var primaryID int64
	var primaryKey string
	if err := tx.QueryRowContext(ctx, `
		select coalesce(u.primary_role_id, 0), coalesce(pr.role_key, '')
		from bp_user u
		left join bp_role pr on pr.id = u.primary_role_id
		where u.id = ?
	`, userID).Scan(&primaryID, &primaryKey); err != nil {
		return err
	}
	if primaryKey == "admin" || primaryID == roleID {
		return nil
	}
	// 先补用户组成员关系（幂等），再切主角色；账号 QPS 等账号级口径跟随主角色生效。
	if _, err := tx.ExecContext(ctx, `
		insert ignore into bp_user_role(user_id, role_id) values(?, ?)
	`, userID, roleID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		update bp_user set primary_role_id = ? where id = ?
	`, roleID, userID)
	return err
}
