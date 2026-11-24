package content

import (
	"context"
	"database/sql"
)

const agreementTemplateColumns = `
	doc_key, mode, lang, title, summary, version,
	coalesce(date_format(effective_date,'%Y-%m-%d'),''),
	content_html, status, date_format(updated_at,'%Y-%m-%d %H:%i:%s')`

func (r *Repository) AgreementActiveMode(ctx context.Context) (string, error) {
	var mode string
	err := r.db.QueryRowContext(ctx, `select active_mode from bp_agreement_config where id=1`).Scan(&mode)
	if err == sql.ErrNoRows {
		return AgreementModeWelfare, nil
	}
	return mode, err
}

func (r *Repository) SaveAgreementActiveMode(ctx context.Context, mode string) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_agreement_config(id,active_mode) values(1,?)
		on duplicate key update active_mode=values(active_mode)
	`, mode)
	return err
}

func (r *Repository) AgreementTemplates(ctx context.Context) ([]AgreementTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `
		select `+agreementTemplateColumns+`
		from bp_agreement_template
		order by field(doc_key,'payment','privacy','terms'),
		         field(mode,'welfare','commercial'),
		         field(lang,'zh-CN','en-US')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgreementTemplates(rows)
}

func (r *Repository) AgreementTemplate(ctx context.Context, docKey, mode, lang string) (AgreementTemplate, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		select `+agreementTemplateColumns+`
		from bp_agreement_template
		where doc_key=? and mode=? and lang=?
	`, docKey, mode, lang)
	item, err := scanAgreementTemplate(row)
	if err == sql.ErrNoRows {
		return AgreementTemplate{}, false, nil
	}
	if err != nil {
		return AgreementTemplate{}, false, err
	}
	return item, true, nil
}

func (r *Repository) UpsertAgreementTemplate(ctx context.Context, item AgreementTemplate, updatedBy int64) error {
	var effectiveDate any
	if item.EffectiveDate != "" {
		effectiveDate = item.EffectiveDate
	}
	_, err := r.db.ExecContext(ctx, `
		insert into bp_agreement_template
			(doc_key,mode,lang,title,summary,version,effective_date,content_html,status,updated_by)
		values(?,?,?,?,?,?,?,?,?,?)
		on duplicate key update
			title=values(title),summary=values(summary),version=values(version),
			effective_date=values(effective_date),content_html=values(content_html),
			status=values(status),updated_by=values(updated_by)
	`, item.DocKey, item.Mode, item.Lang, item.Title, item.Summary, item.Version, effectiveDate, item.ContentHTML, item.Status, updatedBy)
	return err
}

type agreementScanner interface {
	Scan(dest ...any) error
}

func scanAgreementTemplate(row agreementScanner) (AgreementTemplate, error) {
	var item AgreementTemplate
	err := row.Scan(
		&item.DocKey, &item.Mode, &item.Lang, &item.Title, &item.Summary, &item.Version,
		&item.EffectiveDate, &item.ContentHTML, &item.Status, &item.UpdateTime,
	)
	return item, err
}

func scanAgreementTemplates(rows *sql.Rows) ([]AgreementTemplate, error) {
	items := make([]AgreementTemplate, 0, 6)
	for rows.Next() {
		item, err := scanAgreementTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
