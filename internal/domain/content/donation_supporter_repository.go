package content

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/pagination"
)

func (r *Repository) ListDonationSupporters(ctx context.Context, params ListParams) (PageResult[DonationSupporterItem], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	where, args := donationSupporterWhere(params)
	var (
		total int64
		items []DonationSupporterItem
	)
	countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, donationSupporterCountQuery(where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, donationSupporterListQuery(where), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items, err = scanDonationSupporters(rows)
			return err
		},
	)
	if err != nil {
		return PageResult[DonationSupporterItem]{}, err
	}
	return PageResult[DonationSupporterItem]{Items: items, Total: total}, nil
}

func donationSupporterWhere(params ListParams) (string, []any) {
	where := "where 1=1"
	args := make([]any, 0, 4)
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		where += " and (s.nickname like ? or s.message like ?)"
		like := "%" + keyword + "%"
		args = append(args, like, like)
	}
	return where, args
}

func donationSupporterCountQuery(where string) string {
	return `select count(*)
		from (
			select s.id
			from bp_donation_supporter s
			` + where + `
			limit ?
		) counted`
}

func donationSupporterListQuery(where string) string {
	return `
		select s.id,s.nickname,coalesce(s.avatar_url,''),cast(s.amount as char),coalesce(s.message,''),
		       date_format(s.donated_at,'%Y-%m-%d %H:%i:%s'),
		       coalesce(nullif(u.display_name,''),u.username,''),
		       date_format(s.created_at,'%Y-%m-%d %H:%i:%s')
		from (
			select s.id
			from bp_donation_supporter s
			` + where + `
			order by s.donated_at desc,s.id desc
			limit ? offset ?
		) filtered
		inner join bp_donation_supporter s on s.id=filtered.id
		left join bp_user u on u.id=s.created_by
		order by s.donated_at desc,s.id desc
	`
}

func (r *Repository) DonationSupporterByID(ctx context.Context, id int64) (DonationSupporterItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		select s.id,s.nickname,coalesce(s.avatar_url,''),cast(s.amount as char),coalesce(s.message,''),
		       date_format(s.donated_at,'%Y-%m-%d %H:%i:%s'),
		       coalesce(nullif(u.display_name,''),u.username,''),
		       date_format(s.created_at,'%Y-%m-%d %H:%i:%s')
		from bp_donation_supporter s
		left join bp_user u on u.id=s.created_by
		where s.id=?
	`, id)
	if err != nil {
		return DonationSupporterItem{}, err
	}
	defer rows.Close()
	items, err := scanDonationSupporters(rows)
	if err != nil {
		return DonationSupporterItem{}, err
	}
	if len(items) == 0 {
		return DonationSupporterItem{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Repository) CreateDonationSupporter(ctx context.Context, item DonationSupporterItem, createdBy int64) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_donation_supporter(nickname,avatar_url,amount,message,donated_at,created_by)
		values(?,?,?,?,?,?)
	`, item.Nickname, item.AvatarURL, item.Amount, item.Message, item.DonatedAt, nullableDonationUserID(createdBy))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func scanDonationSupporters(rows *sql.Rows) ([]DonationSupporterItem, error) {
	items := make([]DonationSupporterItem, 0)
	for rows.Next() {
		var id int64
		var item DonationSupporterItem
		if err := rows.Scan(
			&id,
			&item.Nickname,
			&item.AvatarURL,
			&item.Amount,
			&item.Message,
			&item.DonatedAt,
			&item.CreatorName,
			&item.CreateTime,
		); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		items = append(items, item)
	}
	return items, rows.Err()
}
