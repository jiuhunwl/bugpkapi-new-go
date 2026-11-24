package payment

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestValidateNotifyContentTypeRequiresExactFormMediaType(t *testing.T) {
	for _, value := range []string{
		"application/x-www-form-urlencoded",
		"application/x-www-form-urlencoded; charset=UTF-8",
		"Application/X-Www-Form-Urlencoded; Charset=utf-8",
	} {
		if err := validateNotifyContentType(value); err != nil {
			t.Fatalf("valid notify Content-Type %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{
		"",
		"application/x-www-form-urlencoded-malicious",
		"application/json",
		"application/x-www-form-urlencoded; charset",
	} {
		err := validateNotifyContentType(value)
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != consts.StatusUnsupportedMediaType {
			t.Fatalf("invalid notify Content-Type %q error=%v", value, err)
		}
	}
}

func TestNotifyValuesRejectsOversizedContentLengthBeforeBodyRead(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx.Request.Header.SetContentLength(maxAlipayNotifyBodyBytes + 1)

	_, err := notifyValues(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusRequestEntityTooLarge {
		t.Fatalf("oversized notify Content-Length error=%v", err)
	}
}

func TestNotifyValuesRejectsInvalidContentTypeBeforeBodyParsing(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded-malicious")
	ctx.Request.SetBodyString("out_trade_no=BP123")

	_, err := notifyValues(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusUnsupportedMediaType {
		t.Fatalf("invalid notify Content-Type error=%v", err)
	}
}

func TestHupijiaoNotifyValuesAcceptsStrictFormEnvelope(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx.Request.SetBodyString("appid=app-1&trade_order_id=BP20260721123456ABCDEF123456&total_fee=10.00&status=OD&transaction_id=hp-trade-1&hash=0123456789abcdef0123456789abcdef")

	values, err := hupijiaoNotifyValues(ctx)
	if err != nil {
		t.Fatalf("hupijiaoNotifyValues() error = %v", err)
	}
	if values["status"] != "OD" || values["trade_order_id"] == "" {
		t.Fatalf("unexpected Hupijiao values: %#v", values)
	}
}

func TestHupijiaoNotifyValuesRejectsDuplicateAndUnknownStatus(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate appid": "appid=one&appid=two&trade_order_id=BP20260721123456ABCDEF123456&total_fee=10.00&status=OD&hash=0123456789abcdef0123456789abcdef",
		"unknown status":  "appid=app-1&trade_order_id=BP20260721123456ABCDEF123456&total_fee=10.00&status=XX&hash=0123456789abcdef0123456789abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := hertzapp.NewContext(0)
			ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx.Request.SetBodyString(body)
			if _, err := hupijiaoNotifyValues(ctx); err == nil {
				t.Fatal("unsafe Hupijiao notify accepted")
			}
		})
	}
}

func TestValidateNotifyValuesRejectsDuplicateAndOversizedFields(t *testing.T) {
	for name, values := range map[string]url.Values{
		"duplicate":  {"out_trade_no": {"one", "two"}},
		"empty key":  {"": {"value"}},
		"long key":   {strings.Repeat("k", 65): {"value"}},
		"long value": {"value": {strings.Repeat("x", 8193)}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateNotifyValues(values); err == nil {
				t.Fatalf("unsafe notify values accepted: %#v", values)
			}
		})
	}
}

func TestValidateNotifyEnvelopeAcceptsWellFormedCallback(t *testing.T) {
	if err := validateNotifyEnvelope(validNotifyEnvelope()); err != nil {
		t.Fatalf("well-formed notify envelope rejected: %v", err)
	}
}

func TestValidateNotifyEnvelopeRejectsMalformedFieldsBeforeVerification(t *testing.T) {
	tests := map[string]func(url.Values){
		"missing sign":      func(values url.Values) { values.Del("sign") },
		"missing notify id": func(values url.Values) { values.Del("notify_id") },
		"long notify id":    func(values url.Values) { values.Set("notify_id", strings.Repeat("n", maxAlipayNotifyIDBytes+1)) },
		"wrong sign type":   func(values url.Values) { values.Set("sign_type", "RSA") },
		"invalid signature": func(values url.Values) { values.Set("sign", "not-base64") },
		"short signature":   func(values url.Values) { values.Set("sign", base64.StdEncoding.EncodeToString(make([]byte, 128))) },
		"invalid app id":    func(values url.Values) { values.Set("app_id", "app-123") },
		"invalid seller id": func(values url.Values) { values.Set("seller_id", "seller") },
		"foreign order":     func(values url.Values) { values.Set("out_trade_no", "OTHER123") },
		"fraction amount":   func(values url.Values) { values.Set("total_amount", "1/2") },
		"exponent amount":   func(values url.Values) { values.Set("total_amount", "1e2") },
		"invalid status":    func(values url.Values) { values.Set("trade_status", "trade success") },
		"missing trade no":  func(values url.Values) { values.Del("trade_no") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := validNotifyEnvelope()
			mutate(values)
			err := validateNotifyEnvelope(values)
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
				t.Fatalf("malformed envelope error=%v", err)
			}
		})
	}
}

func TestValuesToMapRedactsSensitiveAlipayFields(t *testing.T) {
	values := url.Values{
		"buyer_id": {"2088000000000000"}, "buyer_logon_id": {"user@example.com"},
		"sign": {"signature"}, "out_trade_no": {"BP123"},
	}
	got := valuesToMap(values)
	for _, key := range []string{"buyer_id", "buyer_logon_id", "sign"} {
		if got[key] != "***" {
			t.Fatalf("%s was not redacted: %q", key, got[key])
		}
	}
	if got["out_trade_no"] != "BP123" {
		t.Fatal("order number should remain available for audit")
	}
}

func validNotifyEnvelope() url.Values {
	return url.Values{
		"app_id":       {"2026000000000000"},
		"notify_id":    {"202607272000000000000000000000000000"},
		"out_trade_no": {"BP20260721123456ABCDEF123456"},
		"seller_id":    {"2088000000000000"},
		"sign":         {base64.StdEncoding.EncodeToString(make([]byte, 256))},
		"sign_type":    {"RSA2"},
		"total_amount": {"10.00"},
		"trade_no":     {"2026072122000000000000000000"},
		"trade_status": {"TRADE_SUCCESS"},
	}
}
