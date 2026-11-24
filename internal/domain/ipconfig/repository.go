package ipconfig

import (
	"context"
	"database/sql"
	"errors"
)

// Repository 提供 bp_ip_config 单点表（id=1）的读写。
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Get 读取当前数据源配置；表不存在或无记录时返回内置默认值。
func (r *Repository) Get(ctx context.Context) (Config, error) {
	var cfg Config
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select data_source_name, api_url, parse_rule, enabled, remark,
		       date_format(updated_at, '%Y-%m-%d %H:%i:%s')
		from bp_ip_config
		where id = 1
	`).Scan(&cfg.DataSourceName, &cfg.APIURL, &cfg.ParseRule, &enabled, &cfg.Remark, &cfg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg.Enabled = enabled
	return cfg, nil
}

// Save 保存数据源配置（upsert 单点行）。
func (r *Repository) Save(ctx context.Context, cfg Config) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_ip_config(id, data_source_name, api_url, parse_rule, enabled, remark)
		values (1, ?, ?, ?, ?, ?)
		on duplicate key update
			data_source_name = values(data_source_name),
			api_url = values(api_url),
			parse_rule = values(parse_rule),
			enabled = values(enabled),
			remark = values(remark)
	`, cfg.DataSourceName, cfg.APIURL, cfg.ParseRule, cfg.Enabled, cfg.Remark)
	return err
}
