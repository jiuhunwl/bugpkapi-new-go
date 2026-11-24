package payment

import (
	"context"
	"reflect"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestSavePaymentMethodsAcceptsAllChannels(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "TRC20 disabled", true: "TRC20 enabled"}[enabled], func(t *testing.T) {
			repo := &hupijiaoConfigRepositoryStub{}
			service := &Service{hupijiaoConfigRepo: repo}
			err := service.savePaymentMethods(context.Background(), SavePaymentMethodsRequest{Methods: []PaymentMethod{
				{Code: " trc20 ", Enabled: enabled, SortOrder: 60},
				{Code: ChannelHupijiaoWechat, Enabled: false, SortOrder: 50},
				{Code: ChannelHupijiaoAlipay, Enabled: false, SortOrder: 40},
				{Code: ChannelWechat, Enabled: false, SortOrder: 30},
				{Code: ChannelAlipay, Enabled: true, SortOrder: 20},
				{Code: ChannelBalance, Enabled: true, SortOrder: 10},
			}})
			if err != nil {
				t.Fatalf("save six configured payment methods: %v", err)
			}
			want := []PaymentMethod{
				{Code: ChannelBalance, Enabled: true, SortOrder: 10},
				{Code: ChannelAlipay, Enabled: true, SortOrder: 20},
				{Code: ChannelWechat, Enabled: false, SortOrder: 30},
				{Code: ChannelHupijiaoAlipay, Enabled: false, SortOrder: 40},
				{Code: ChannelHupijiaoWechat, Enabled: false, SortOrder: 50},
				{Code: ChannelTrc20, Enabled: enabled, SortOrder: 60},
			}
			if !reflect.DeepEqual(repo.savedMethods, want) {
				t.Fatalf("saved methods = %#v, want %#v", repo.savedMethods, want)
			}
		})
	}
}

func TestNormalizePaymentMethodsAcceptsTrc20PartialUpdate(t *testing.T) {
	want := []PaymentMethod{{Code: ChannelTrc20, Enabled: true, SortOrder: 15}}
	got, err := normalizePaymentMethods(SavePaymentMethodsRequest{Methods: want})
	if err != nil {
		t.Fatalf("normalize TRC20 partial update: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized methods = %#v, want %#v", got, want)
	}
}

func TestSavePaymentMethodsRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		methods []PaymentMethod
	}{
		{name: "empty"},
		{name: "unknown channel", methods: []PaymentMethod{{Code: "UNKNOWN"}}},
		{name: "duplicate TRC20", methods: []PaymentMethod{{Code: ChannelTrc20}, {Code: " trc20 "}}},
		{name: "negative sort order", methods: []PaymentMethod{{Code: ChannelTrc20, SortOrder: -1}}},
		{name: "excessive sort order", methods: []PaymentMethod{{Code: ChannelTrc20, SortOrder: 1_000_001}}},
		{name: "too many methods", methods: []PaymentMethod{
			{Code: ChannelBalance}, {Code: ChannelAlipay}, {Code: ChannelWechat},
			{Code: ChannelHupijiaoAlipay}, {Code: ChannelHupijiaoWechat}, {Code: ChannelTrc20},
			{Code: "UNKNOWN"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &hupijiaoConfigRepositoryStub{}
			service := &Service{hupijiaoConfigRepo: repo}
			err := service.savePaymentMethods(context.Background(), SavePaymentMethodsRequest{Methods: tc.methods})
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != consts.StatusBadRequest {
				t.Fatalf("save invalid configuration error = %v, want business 400", err)
			}
			if repo.savedMethods != nil {
				t.Fatalf("invalid configuration was written: %#v", repo.savedMethods)
			}
		})
	}
}
