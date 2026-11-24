package interfaceapi

import (
	"encoding/json"
	"testing"
)

func TestBillingSwitchValidation(t *testing.T) {
	for _, body := range []string{`{"rules":[]}`, `{"billingEnabled":0,"billingLogEnabled":1}`, `{"billingEnabled":1,"billingLogEnabled":0}`} {
		var req SaveBillingRulesRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if err := validateBillingSwitches(req); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{`{"billingEnabled":-1}`, `{"billingLogEnabled":2}`} {
		var req SaveBillingRulesRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if err := validateBillingSwitches(req); err == nil {
			t.Fatalf("accepted invalid switch: %s", body)
		}
	}
	var omitted SaveBillingRulesRequest
	if err := json.Unmarshal([]byte(`{"rules":[]}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.BillingEnabled != nil || omitted.BillingLogEnabled != nil {
		t.Fatal("omitted switches must preserve stored settings")
	}
}
