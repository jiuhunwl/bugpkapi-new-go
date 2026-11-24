package payment_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"bugpk-api/internal/domain/payment"
)

func TestSavePaymentMethodsIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
	}

	initial := []payment.PaymentMethod{
		{Code: payment.ChannelBalance, Enabled: true, SortOrder: 10},
		{Code: payment.ChannelAlipay, Enabled: true, SortOrder: 20},
		{Code: payment.ChannelWechat, Enabled: false, SortOrder: 30},
		{Code: payment.ChannelHupijiaoAlipay, Enabled: false, SortOrder: 40},
		{Code: payment.ChannelHupijiaoWechat, Enabled: false, SortOrder: 50},
	}
	for _, tc := range []struct {
		name    string
		methods []payment.PaymentMethod
		want    []payment.PaymentMethod
	}{
		{name: "unchanged settings", methods: initial, want: initial},
		{
			name: "toggle alongside unchanged settings",
			methods: []payment.PaymentMethod{
				{Code: payment.ChannelBalance, Enabled: true, SortOrder: 10},
				{Code: payment.ChannelAlipay, Enabled: false, SortOrder: 20},
				{Code: payment.ChannelWechat, Enabled: false, SortOrder: 30},
			},
			want: []payment.PaymentMethod{
				{Code: payment.ChannelBalance, Enabled: true, SortOrder: 10},
				{Code: payment.ChannelAlipay, Enabled: false, SortOrder: 20},
				{Code: payment.ChannelWechat, Enabled: false, SortOrder: 30},
				{Code: payment.ChannelHupijiaoAlipay, Enabled: false, SortOrder: 40},
				{Code: payment.ChannelHupijiaoWechat, Enabled: false, SortOrder: 50},
			},
		},
		{
			name: "reorder alongside unchanged settings",
			methods: []payment.PaymentMethod{
				{Code: payment.ChannelBalance, Enabled: true, SortOrder: 10},
				{Code: payment.ChannelHupijiaoWechat, Enabled: false, SortOrder: 15},
			},
			want: []payment.PaymentMethod{
				{Code: payment.ChannelBalance, Enabled: true, SortOrder: 10},
				{Code: payment.ChannelHupijiaoWechat, Enabled: false, SortOrder: 15},
				{Code: payment.ChannelAlipay, Enabled: true, SortOrder: 20},
				{Code: payment.ChannelWechat, Enabled: false, SortOrder: 30},
				{Code: payment.ChannelHupijiaoAlipay, Enabled: false, SortOrder: 40},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := payment.NewRepository(openPaymentMethodsTestDB(t, initial))
			for attempt := 1; attempt <= 2; attempt++ {
				if err := repo.SavePaymentMethods(context.Background(), tc.methods); err != nil {
					t.Fatalf("save attempt %d: %v", attempt, err)
				}
				got, err := repo.PaymentMethods(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("persisted methods = %#v, want %#v", got, tc.want)
				}
			}
		})
	}

	t.Run("missing method rolls back earlier updates", func(t *testing.T) {
		seed := initial[:2]
		repo := payment.NewRepository(openPaymentMethodsTestDB(t, seed))
		err := repo.SavePaymentMethods(context.Background(), []payment.PaymentMethod{
			{Code: payment.ChannelAlipay, Enabled: false, SortOrder: 25},
			{Code: payment.ChannelWechat, Enabled: true, SortOrder: 30},
		})
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("save missing method error = %v, want sql.ErrNoRows", err)
		}
		got, err := repo.PaymentMethods(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, seed) {
			t.Fatalf("failed batch persisted changes: %#v, want %#v", got, seed)
		}
	})
}

func openPaymentMethodsTestDB(t *testing.T, methods []payment.PaymentMethod) *sql.DB {
	t.Helper()
	db := openPaymentIntegrationDB(t)
	// Keep the temporary table on one connection so no shared configuration is changed.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var tableName, schema string
	if err := db.QueryRow(`show create table bp_payment_method`).Scan(&tableName, &schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(strings.Replace(schema, "CREATE TABLE", "CREATE TEMPORARY TABLE", 1)); err != nil {
		t.Fatal(err)
	}
	for _, method := range methods {
		if _, err := db.Exec(`insert into bp_payment_method(method_code,enabled,sort_order) values(?,?,?)`,
			method.Code, method.Enabled, method.SortOrder); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
