package translation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"bugpk-api/internal/pkg/i18n"
)

type sqlProvider struct {
	db   *sql.DB
	spec resourceSpec
}

func newSQLProvider(db *sql.DB, spec resourceSpec) *sqlProvider {
	return &sqlProvider{db: db, spec: spec}
}

// CountResource 返回源表条目总数，用于资源类型下拉的规模展示。
func (p *sqlProvider) CountResource(ctx context.Context) (int64, error) {
	var total int64
	if err := p.db.QueryRowContext(ctx, "select count(*) from "+quote(p.spec.Table)).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (p *sqlProvider) Type() string {
	return p.spec.Type
}

func (p *sqlProvider) List(ctx context.Context, query i18n.ListQuery) (i18n.PageResult[i18n.BaseResource], error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	columns := p.selectColumns()
	display := quote(p.spec.DisplayColumn)
	key := quote(p.spec.KeyColumn)

	var total int64
	if p.spec.KeyKind != i18n.KeySingleton {
		keyword := strings.TrimSpace(query.Keyword)
		where := ""
		args := make([]any, 0, 2)
		if keyword != "" {
			where = fmt.Sprintf("where %s like ? or cast(%s as char) like ?", display, key)
			pattern := "%" + keyword + "%"
			args = append(args, pattern, pattern)
		}
		querySQL := fmt.Sprintf("select count(*) from %s %s", quote(p.spec.Table), where)
		if err := p.db.QueryRowContext(ctx, querySQL, args...).Scan(&total); err != nil {
			return i18n.PageResult[i18n.BaseResource]{}, err
		}

		listArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
		querySQL = fmt.Sprintf(
			"select %s from %s %s order by %s desc limit ? offset ?",
			columns, quote(p.spec.Table), where, key,
		)
		rows, err := p.db.QueryContext(ctx, querySQL, listArgs...)
		if err != nil {
			return i18n.PageResult[i18n.BaseResource]{}, err
		}
		items, err := p.scanRows(rows)
		if err != nil {
			return i18n.PageResult[i18n.BaseResource]{}, err
		}
		return i18n.PageResult[i18n.BaseResource]{Items: items, Total: total}, nil
	}

	querySQL := fmt.Sprintf("select %s from %s order by %s asc", columns, quote(p.spec.Table), key)
	rows, err := p.db.QueryContext(ctx, querySQL)
	if err != nil {
		return i18n.PageResult[i18n.BaseResource]{}, err
	}
	items, err := p.scanRows(rows)
	if err != nil {
		return i18n.PageResult[i18n.BaseResource]{}, err
	}
	return i18n.PageResult[i18n.BaseResource]{Items: items, Total: int64(len(items))}, nil
}

func (p *sqlProvider) Get(ctx context.Context, keyValue string) (i18n.BaseResource, error) {
	keyValue = p.normalizeKey(keyValue)
	if keyValue == "" {
		return i18n.BaseResource{}, sql.ErrNoRows
	}
	querySQL := fmt.Sprintf(
		"select %s from %s where %s = ? limit 1",
		p.selectColumns(), quote(p.spec.Table), quote(p.spec.KeyColumn),
	)
	var id string
	var display sql.NullString
	values := make([]sql.NullString, len(p.spec.Fields))
	dest := make([]any, 0, len(p.spec.Fields)+2)
	dest = append(dest, &id, &display)
	for i := range values {
		dest = append(dest, &values[i])
	}
	if err := p.db.QueryRowContext(ctx, querySQL, keyValue).Scan(dest...); err != nil {
		return i18n.BaseResource{}, err
	}

	result := i18n.BaseResource{Key: id, DisplayName: fallbackDisplay(display.String, id), Values: make(map[string]string, len(p.spec.Fields))}
	for i, field := range p.spec.Fields {
		result.Values[field.Name] = values[i].String
	}
	return result, nil
}

func (p *sqlProvider) selectColumns() string {
	parts := []string{quote(p.spec.KeyColumn), quote(p.spec.DisplayColumn)}
	for _, field := range p.spec.Fields {
		parts = append(parts, quote(field.Column))
	}
	return strings.Join(parts, ",")
}

func (p *sqlProvider) scanRows(rows *sql.Rows) ([]i18n.BaseResource, error) {
	defer rows.Close()
	items := make([]i18n.BaseResource, 0)
	for rows.Next() {
		var id string
		var display sql.NullString
		values := make([]sql.NullString, len(p.spec.Fields))
		dest := make([]any, 0, len(p.spec.Fields)+2)
		dest = append(dest, &id, &display)
		for i := range values {
			dest = append(dest, &values[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		item := i18n.BaseResource{Key: id, DisplayName: fallbackDisplay(display.String, id), Values: make(map[string]string, len(p.spec.Fields))}
		for i, field := range p.spec.Fields {
			item.Values[field.Name] = values[i].String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func fallbackDisplay(display, keyValue string) string {
	if strings.TrimSpace(display) != "" {
		return display
	}
	if keyValue != "" {
		return "#" + keyValue
	}
	return "-"
}

func (p *sqlProvider) normalizeKey(keyValue string) string {
	keyValue = strings.TrimSpace(keyValue)
	if p.spec.KeyKind == i18n.KeySingleton {
		if keyValue == "" || strings.EqualFold(keyValue, "singleton") {
			return "1"
		}
	}
	return keyValue
}

func quote(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
