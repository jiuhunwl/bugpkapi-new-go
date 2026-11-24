package payment

import (
	"context"
	"encoding/json"
	"strings"

	"bugpk-api/internal/pkg/masking"
)

type storedHupijiaoConfig struct {
	HupijiaoConfig
	AppSecretCipher string
}

func (r *Repository) HupijiaoConfig(ctx context.Context, method string) (storedHupijiaoConfig, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	var item storedHupijiaoConfig
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select method,enabled,app_id,coalesce(app_secret_cipher,''),create_url,query_url,
		       coalesce(notify_url,''),coalesce(return_url,''),coalesce(callback_url,''),
		       coalesce(wap_url,''),coalesce(wap_name,'')
		from bp_payment_hupijiao_config where method=?
	`, method).Scan(
		&item.Method, &enabled, &item.AppID, &item.AppSecretCipher,
		&item.CreateURL, &item.QueryURL, &item.NotifyURL, &item.ReturnURL,
		&item.CallbackURL, &item.WAPURL, &item.WAPName,
	)
	item.Enabled = enabled == 1
	return item, err
}

func (r *Repository) HupijiaoConfigs(ctx context.Context) ([]storedHupijiaoConfig, error) {
	rows, err := r.db.QueryContext(ctx, `
		select method,enabled,app_id,coalesce(app_secret_cipher,''),create_url,query_url,
		       coalesce(notify_url,''),coalesce(return_url,''),coalesce(callback_url,''),
		       coalesce(wap_url,''),coalesce(wap_name,'')
		from bp_payment_hupijiao_config
		where method in ('ALIPAY','WECHAT')
		order by field(method,'ALIPAY','WECHAT')
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]storedHupijiaoConfig, 0, 2)
	for rows.Next() {
		var item storedHupijiaoConfig
		var enabled int
		if err := rows.Scan(
			&item.Method, &enabled, &item.AppID, &item.AppSecretCipher,
			&item.CreateURL, &item.QueryURL, &item.NotifyURL, &item.ReturnURL,
			&item.CallbackURL, &item.WAPURL, &item.WAPName,
		); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byMethod := make(map[string]storedHupijiaoConfig, len(items))
	for _, item := range items {
		byMethod[item.Method] = item
	}
	ordered := make([]storedHupijiaoConfig, 0, 2)
	for _, method := range []string{"ALIPAY", "WECHAT"} {
		item, ok := byMethod[method]
		if !ok {
			item = storedHupijiaoConfig{HupijiaoConfig: HupijiaoConfig{
				Method: method, CreateURL: defaultHupijiaoCreateURL, QueryURL: defaultHupijiaoQueryURL,
			}}
		}
		ordered = append(ordered, item)
	}
	return ordered, nil
}

func (r *Repository) SaveHupijiaoConfig(ctx context.Context, cfg HupijiaoConfig, cipher string, updateAppSecret bool) error {
	query := `
		insert into bp_payment_hupijiao_config(method,enabled,app_id,create_url,query_url,notify_url,return_url,callback_url,wap_url,wap_name)
		values(?,?,?,?,?,?,?,?,?,?)
		on duplicate key update enabled=values(enabled),app_id=values(app_id),create_url=values(create_url),query_url=values(query_url),
			notify_url=values(notify_url),return_url=values(return_url),callback_url=values(callback_url),wap_url=values(wap_url),wap_name=values(wap_name)
	`
	args := []any{
		cfg.Method, boolInt(cfg.Enabled), cfg.AppID, cfg.CreateURL, cfg.QueryURL,
		nullString(cfg.NotifyURL), nullString(cfg.ReturnURL), nullString(cfg.CallbackURL),
		nullString(cfg.WAPURL), nullString(cfg.WAPName),
	}
	if updateAppSecret {
		query = `
			insert into bp_payment_hupijiao_config(method,enabled,app_id,app_secret_cipher,create_url,query_url,notify_url,return_url,callback_url,wap_url,wap_name)
			values(?,?,?,?,?,?,?,?,?,?,?)
			on duplicate key update enabled=values(enabled),app_id=values(app_id),app_secret_cipher=values(app_secret_cipher),
				create_url=values(create_url),query_url=values(query_url),notify_url=values(notify_url),return_url=values(return_url),
				callback_url=values(callback_url),wap_url=values(wap_url),wap_name=values(wap_name)
		`
		args = []any{
			cfg.Method, boolInt(cfg.Enabled), cfg.AppID, cipher, cfg.CreateURL, cfg.QueryURL,
			nullString(cfg.NotifyURL), nullString(cfg.ReturnURL), nullString(cfg.CallbackURL),
			nullString(cfg.WAPURL), nullString(cfg.WAPName),
		}
	}
	_, err := r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *Repository) PaymentMethods(ctx context.Context) ([]PaymentMethod, error) {
	rows, err := r.db.QueryContext(ctx, `
		select method_code,enabled,sort_order
		from bp_payment_method
		order by sort_order asc,method_code asc
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PaymentMethod, 0, 5)
	for rows.Next() {
		var item PaymentMethod
		var enabled int
		if err := rows.Scan(&item.Code, &enabled, &item.SortOrder); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) SavePaymentMethods(ctx context.Context, methods []PaymentMethod) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, method := range methods {
		result, err := tx.ExecContext(ctx, `
			update bp_payment_method set enabled=?,sort_order=? where method_code=?
		`, boolInt(method.Enabled), method.SortOrder, method.Code)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			// MySQL reports changed rows by default; unchanged settings still exist.
			// Use a locking read in the same transaction to distinguish a missing method.
			var exists int
			if err := tx.QueryRowContext(ctx, `select 1 from bp_payment_method where method_code=? for update`, method.Code).Scan(&exists); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repository) InsertHupijiaoNotifyLog(ctx context.Context, values map[string]string, verified, processed bool, message string) error {
	payload := sanitizeNotifyValues(values)
	if strings.TrimSpace(payload["notify_id"]) == "" {
		payload["notify_id"] = payload["open_order_id"]
	}
	if strings.TrimSpace(payload["trade_no"]) == "" {
		payload["trade_no"] = payload["transaction_id"]
	}
	if strings.TrimSpace(payload["out_trade_no"]) == "" {
		payload["out_trade_no"] = payload["trade_order_id"]
	}
	payload["event_type"] = "HUPIJIAO_NOTIFY"
	return r.insertPaymentNotifyLog(ctx, payload, verified, processed, message)
}

func (r *Repository) insertPaymentNotifyLog(ctx context.Context, values map[string]string, verified, processed bool, message string) error {
	// Keep this low-level writer safe for every internal caller.  Hupijiao
	// callback fields are provider-controlled and may contain arbitrary URLs or
	// credentials; sanitize again here rather than relying only on the public
	// adapter above.
	values = sanitizeNotifyValues(values)
	payload, err := json.Marshal(values)
	if err != nil {
		return err
	}
	message = masking.ErrorText(message)
	_, err = r.db.ExecContext(ctx, `
		insert into bp_payment_notify_log(order_no,trade_no,notify_id,event_type,verified,processed,message,payload)
		values(?,?,?,?,?,?,?,?)
		on duplicate key update
			order_no=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(order_no),order_no),
			trade_no=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(trade_no),trade_no),
			message=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(message),message),
			payload=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(payload),payload),
			verified=greatest(verified,values(verified)),
			processed=greatest(processed,values(processed)),
			attempt_count=if(attempt_count<4294967295,attempt_count+1,attempt_count),
			last_seen_at=now()
	`, nullString(values["out_trade_no"]), nullString(values["trade_no"]), nullString(values["notify_id"]), values["event_type"], boolInt(verified), boolInt(processed), nullString(message), string(payload))
	return err
}
