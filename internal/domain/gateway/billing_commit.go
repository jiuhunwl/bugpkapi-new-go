package gateway

import (
	"context"
	"database/sql"
	"errors"

	"bugpk-api/internal/pkg/decimalvalue"
)

func (r *Repository) verifyBillingCommit(ctx context.Context, res BillingReservation, status string, logID int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var storedStatus string
	var storedLog int64
	err = tx.QueryRowContext(ctx, "select status,coalesce(call_log_id,0) from bp_billing_record where id=? for update", res.BillingRecordID).Scan(&storedStatus, &storedLog)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return storedStatus == status && (logID < 0 || storedLog == logID), nil
}
func (r *Repository) verifyReservationCommit(ctx context.Context, res BillingReservation, api InterfaceConfig, keyID, userID int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var storedUser, storedInterface, storedKey, storedPoints, storedPackage int64
	var status, chargeType, amount string
	err = tx.QueryRowContext(ctx, "select user_id,interface_id,coalesce(api_key_id,0),status,charge_type,cast(charge_amount as char),point_amount,coalesce(package_record_id,0) from bp_billing_record where id=? for update", res.BillingRecordID).Scan(&storedUser, &storedInterface, &storedKey, &status, &chargeType, &amount, &storedPoints, &storedPackage)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := decimalvalue.ParseNonNegative(res.ChargeAmount)
	if err != nil {
		return false, err
	}
	stored, err := decimalvalue.ParseNonNegative(amount)
	if err != nil {
		return false, err
	}
	return storedUser == userID && storedInterface == api.ID && storedKey == keyID && status == "RESERVED" && chargeType == res.ChargeType && stored.Cmp(expected) == 0 && storedPoints == res.PointAmount && storedPackage == res.PackageRecordID, nil
}
func (r *Repository) verifyOpenLogCommit(ctx context.Context, logID, billingRecordID int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var storedBilling int64
	err = tx.QueryRowContext(ctx, "select coalesce(billing_record_id,0) from bp_interface_call_log where id=? for update", logID).Scan(&storedBilling)
	if errors.Is(err, sql.ErrNoRows) {
		// Logs can be deleted by retention/admin cleanup immediately after
		// COMMIT. Their absence is not evidence that the insert rolled back.
		// A paid release also stores this exact log ID on the durable bill.
		if billingRecordID > 0 {
			var storedLog int64
			err = tx.QueryRowContext(ctx, "select coalesce(call_log_id,0) from bp_billing_record where id=? for update", billingRecordID).Scan(&storedLog)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
			if err == nil && storedLog == logID {
				return true, nil
			}
		}
		return false, errors.New("call-log commit witness missing; rollback cannot be established")
	}
	if err != nil {
		return false, err
	}
	if storedBilling != billingRecordID {
		return false, errors.New("call-log commit witness does not match the billing record")
	}
	return true, nil
}
