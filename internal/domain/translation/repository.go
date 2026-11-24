package translation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

type entry struct {
	ResourceType string
	ResourceKey  string
	Field        string
	Locale       string
	Value        string
	SourceHash   string
}

type entryKey struct {
	Field  string
	Locale string
}

func (r *Repository) Locales(ctx context.Context) ([]Locale, error) {
	rows, err := r.db.QueryContext(ctx, `
		select code,label,coalesce(fallback_code,''),enabled,is_default,sort_no
		from bp_i18n_locale
		order by sort_no,code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Locale, 0)
	for rows.Next() {
		var item Locale
		var enabled int
		var isDefault sql.NullInt64
		if err := rows.Scan(&item.Code, &item.Label, &item.FallbackCode, &enabled, &isDefault, &item.SortNo); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.IsDefault = isDefault.Valid && isDefault.Int64 == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

// Entries 加载全部翻译行；字典规模预期足够小，可整体放入进程内快照。
func (r *Repository) Entries(ctx context.Context) ([]entry, error) {
	rows, err := r.db.QueryContext(ctx, `
		select resource_type,resource_key,field,locale,value,coalesce(source_hash,'')
		from bp_i18n_entry
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entry, 0)
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.ResourceType, &item.ResourceKey, &item.Field, &item.Locale, &item.Value, &item.SourceHash); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// EntriesForResource 返回 resourceKey -> locale -> field -> entry。
func (r *Repository) EntriesForResource(ctx context.Context, resourceType string, keys []string) (map[string]map[string]map[string]entry, error) {
	result := make(map[string]map[string]map[string]entry, len(keys))
	if len(keys) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(keys))
	args := make([]any, 0, len(keys)+1)
	args = append(args, resourceType)
	for i, key := range keys {
		placeholders[i] = "?"
		args = append(args, key)
	}
	query := fmt.Sprintf(`
		select resource_key,locale,field,value,coalesce(source_hash,'')
		from bp_i18n_entry
		where resource_type = ? and resource_key in (%s)
	`, strings.Join(placeholders, ","))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key, locale, field, value, sourceHash string
		if err := rows.Scan(&key, &locale, &field, &value, &sourceHash); err != nil {
			return nil, err
		}
		if result[key] == nil {
			result[key] = make(map[string]map[string]entry)
		}
		if result[key][locale] == nil {
			result[key][locale] = make(map[string]entry)
		}
		result[key][locale][field] = entry{
			ResourceType: resourceType,
			ResourceKey:  key,
			Field:        field,
			Locale:       locale,
			Value:        value,
			SourceHash:   sourceHash,
		}
	}
	return result, rows.Err()
}

func (r *Repository) SaveResourceTranslations(ctx context.Context, resourceType, resourceKey string, upserts []entry, deletes []entryKey, actorID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	updatedBy := any(nil)
	if actorID > 0 {
		updatedBy = actorID
	}
	for _, item := range upserts {
		if _, err := tx.ExecContext(ctx, `
			insert into bp_i18n_entry
				(resource_type,resource_key,field,locale,value,source_hash,updated_by)
			values (?,?,?,?,?,?,?)
			on duplicate key update
				value = values(value),
				source_hash = values(source_hash),
				updated_by = values(updated_by)
		`, resourceType, resourceKey, item.Field, item.Locale, item.Value, item.SourceHash, updatedBy); err != nil {
			return err
		}
	}
	for _, item := range deletes {
		if _, err := tx.ExecContext(ctx, `
			delete from bp_i18n_entry
			where resource_type = ? and resource_key = ? and field = ? and locale = ?
		`, resourceType, resourceKey, item.Field, item.Locale); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) SaveLocales(ctx context.Context, locales []Locale) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, item := range locales {
		var isDefault any
		if item.IsDefault {
			isDefault = 1
		}
		enabled := 0
		if item.Enabled {
			enabled = 1
		}
		fallback := any(nil)
		if strings.TrimSpace(item.FallbackCode) != "" {
			fallback = item.FallbackCode
		}
		if _, err := tx.ExecContext(ctx, `
			insert into bp_i18n_locale (code,label,fallback_code,enabled,is_default,sort_no)
			values (?,?,?,?,?,?)
			on duplicate key update
				label = values(label),
				fallback_code = values(fallback_code),
				enabled = values(enabled),
				is_default = values(is_default),
				sort_no = values(sort_no)
		`, item.Code, item.Label, fallback, enabled, isDefault, item.SortNo); err != nil {
			return err
		}
	}
	return tx.Commit()
}
