package advertising

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/pagination"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) List(ctx context.Context, params ListParams) (PageResult[Advertisement], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	where, args := advertisementWhere(params)
	var (
		total int64
		items []Advertisement
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_advertisement "+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, advertisementSelect+where+`
				order by sort_no desc,id desc
				limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			items, err = scanAdvertisements(rows)
			closeErr := rows.Close()
			if err != nil {
				return err
			}
			return closeErr
		},
	)
	if err != nil {
		return PageResult[Advertisement]{}, err
	}
	return PageResult[Advertisement]{Items: items, Total: total}, nil
}

func (r *Repository) ByID(ctx context.Context, id int64) (Advertisement, error) {
	row := r.db.QueryRowContext(ctx, advertisementSelect+" where id=?", id)
	return scanAdvertisement(row)
}

func (r *Repository) ActiveByID(ctx context.Context, id int64, placement string) (Advertisement, error) {
	row := r.db.QueryRowContext(ctx, advertisementSelect+`
		where id=? and placement=? and status=1
		  and (start_at is null or start_at<=now())
		  and (end_at is null or end_at>now())
	`, id, placement)
	return scanAdvertisement(row)
}

func (r *Repository) Active(ctx context.Context, placement string, limit int) ([]Advertisement, error) {
	if limit < 1 || limit > 20 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, advertisementSelect+`
		where placement=? and status=1
		  and (start_at is null or start_at<=now())
		  and (end_at is null or end_at>now())
		order by sort_no desc,id desc
		limit ?
	`, placement, limit)
	if err != nil {
		return nil, err
	}
	items, err := scanAdvertisements(rows)
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return items, nil
}

func (r *Repository) Create(ctx context.Context, item Advertisement, creatorID int64) (int64, error) {
	payload, err := advertisementPayload(item.Payload)
	if err != nil {
		return 0, err
	}
	result, err := r.db.ExecContext(ctx, `
		insert into bp_advertisement
		(name,placement,title,description,image_url,link_url,link_target,payload_json,status,sort_no,start_at,end_at,created_by,updated_by)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, item.Name, item.Placement, item.Title, item.Description, item.ImageURL, item.LinkURL, item.LinkTarget,
		payload, item.Status, item.SortNo, nullIfBlank(item.StartTime), nullIfBlank(item.EndTime), creatorID, creatorID)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *Repository) Update(ctx context.Context, id int64, item Advertisement, updaterID int64) error {
	payload, err := advertisementPayload(item.Payload)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_advertisement
		set name=?,placement=?,title=?,description=?,image_url=?,link_url=?,link_target=?,payload_json=?,
		    status=?,sort_no=?,start_at=?,end_at=?,updated_by=?
		where id=?
	`, item.Name, item.Placement, item.Title, item.Description, item.ImageURL, item.LinkURL, item.LinkTarget,
		payload, item.Status, item.SortNo, nullIfBlank(item.StartTime), nullIfBlank(item.EndTime), updaterID, id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, "delete from bp_advertisement where id=?", id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

const advertisementSelect = `
	select id,name,placement,title,description,image_url,link_url,link_target,
	       coalesce(cast(payload_json as char),'{}'),status,sort_no,
	       coalesce(date_format(start_at,'%Y-%m-%d %H:%i:%s'),''),
	       coalesce(date_format(end_at,'%Y-%m-%d %H:%i:%s'),''),
	       date_format(created_at,'%Y-%m-%d %H:%i:%s'),date_format(updated_at,'%Y-%m-%d %H:%i:%s')
	from bp_advertisement
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAdvertisement(row rowScanner) (Advertisement, error) {
	var item Advertisement
	var id int64
	var payloadText string
	err := row.Scan(&id, &item.Name, &item.Placement, &item.Title, &item.Description, &item.ImageURL,
		&item.LinkURL, &item.LinkTarget, &payloadText, &item.Status, &item.SortNo, &item.StartTime,
		&item.EndTime, &item.CreateTime, &item.UpdateTime)
	if err != nil {
		return Advertisement{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	_ = json.Unmarshal([]byte(payloadText), &item.Payload)
	return item, nil
}

func scanAdvertisements(rows *sql.Rows) ([]Advertisement, error) {
	items := make([]Advertisement, 0)
	for rows.Next() {
		item, err := scanAdvertisement(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func advertisementWhere(params ListParams) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		where = append(where, "(name like ? or title like ? or description like ?)")
		args = append(args, like, like, like)
	}
	if placement := strings.TrimSpace(params.Placement); placement != "" {
		where = append(where, "placement=?")
		args = append(args, placement)
	}
	if params.Status != nil {
		where = append(where, "status=?")
		args = append(args, *params.Status)
	}
	return strings.Join(where, " and "), args
}

func advertisementPayload(payload map[string]any) (any, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func normalizePage(page, pageSize int) (int, int) {
	return pagination.Normalize(page, pageSize, 100)
}
