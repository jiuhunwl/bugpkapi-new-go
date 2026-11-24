package content

import (
	"context"
	"database/sql"
	"encoding/json"
)

func (r *Repository) DonationConfig(ctx context.Context) (DonationConfig, error) {
	var cfg DonationConfig
	var methodsJSON []byte
	err := r.db.QueryRowContext(ctx, `
		select id, enabled, eyebrow, title, description,
		       scan_title, scan_description, thanks, methods,
		       date_format(updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_donation_config
		where id=1
	`).Scan(
		&cfg.ID,
		&cfg.Enabled,
		&cfg.Eyebrow,
		&cfg.Title,
		&cfg.Description,
		&cfg.ScanTitle,
		&cfg.ScanDescription,
		&cfg.Thanks,
		&methodsJSON,
		&cfg.UpdateTime,
	)
	if err == sql.ErrNoRows {
		return defaultDonationConfig(), nil
	}
	if err != nil {
		return DonationConfig{}, err
	}
	if len(methodsJSON) > 0 {
		if err := json.Unmarshal(methodsJSON, &cfg.Methods); err != nil {
			return DonationConfig{}, err
		}
	}
	if cfg.Methods == nil {
		cfg.Methods = []DonationMethod{}
	}
	return cfg, nil
}

func (r *Repository) SaveDonationConfig(ctx context.Context, cfg DonationConfig, updatedBy int64) error {
	methods, err := json.Marshal(cfg.Methods)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_donation_config(
			id, enabled, eyebrow, title, description,
			scan_title, scan_description, thanks, methods, updated_by
		) values(1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on duplicate key update
			enabled=values(enabled),
			eyebrow=values(eyebrow),
			title=values(title),
			description=values(description),
			scan_title=values(scan_title),
			scan_description=values(scan_description),
			thanks=values(thanks),
			methods=values(methods),
			updated_by=values(updated_by)
	`, cfg.Enabled, cfg.Eyebrow, cfg.Title, cfg.Description, cfg.ScanTitle, cfg.ScanDescription, cfg.Thanks, string(methods), nullableDonationUserID(updatedBy))
	return err
}

func nullableDonationUserID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func defaultDonationConfig() DonationConfig {
	return DonationConfig{
		Description:     "如果 BugPk-Api 对你有帮助，欢迎用你喜欢的方式支持项目。",
		Enabled:         0,
		Eyebrow:         "赞赏",
		ID:              1,
		Methods:         []DonationMethod{},
		ScanDescription: "选择你习惯的支付方式，感谢每一份支持。",
		ScanTitle:       "扫码赞赏",
		Thanks:          "感谢你的支持，我们会继续把服务做好。",
		Title:           "赞赏支持",
	}
}
