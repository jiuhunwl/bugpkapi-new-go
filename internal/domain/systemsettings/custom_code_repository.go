package systemsettings

import (
	"context"
	"database/sql"
)

func (r *Repository) CustomCodeConfig(ctx context.Context) (CustomCodeConfig, error) {
	var config CustomCodeConfig
	err := r.db.QueryRowContext(ctx, `
		select enabled,css_code,js_code,date_format(updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_custom_code_config where id=1
	`).Scan(&config.Enabled, &config.CSSCode, &config.JSCode, &config.UpdateTime)
	if err == sql.ErrNoRows {
		return CustomCodeConfig{}, nil
	}
	return config, err
}

func (r *Repository) SaveCustomCodeConfig(ctx context.Context, config CustomCodeConfig, updaterID int64) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_custom_code_config(id,enabled,css_code,js_code,updated_by)
		values(1,?,?,?,?)
		on duplicate key update enabled=values(enabled),css_code=values(css_code),js_code=values(js_code),updated_by=values(updated_by)
	`, config.Enabled, config.CSSCode, config.JSCode, updaterID)
	return err
}
