package interfaceapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/requesttimeout"
)

func TestNormalizeResponseCacheRules(t *testing.T) {
	rules, err := normalizeResponseCacheRules([]ResponseCacheRule{{
		Name: "成功响应", StatusCode: 200, FieldName: "code", Operator: "eq", ExpectedValue: "0", TTLSeconds: 120,
	}})
	if err != nil {
		t.Fatalf("normalizeResponseCacheRules() error=%v", err)
	}
	if len(rules) != 1 || rules[0].Operator != "EQ" || rules[0].SortNo != 1 {
		t.Fatalf("unexpected rules: %#v", rules)
	}
}

func TestNormalizeResponseCacheRulesRejectsUnsafeRules(t *testing.T) {
	tests := [][]ResponseCacheRule{
		{{TTLSeconds: 30}},
		{{StatusCode: 999, TTLSeconds: 30}},
		{{FieldName: "code", Operator: "SCRIPT", TTLSeconds: 30}},
		{{FieldName: "code", Operator: "EQ", TTLSeconds: 0}},
	}
	for _, rules := range tests {
		if _, err := normalizeResponseCacheRules(rules); err == nil {
			t.Fatalf("expected invalid rules to fail: %#v", rules)
		}
	}
}

func TestDeliveryPolicyResponseAdvertisementIDUsesPrecisionSafeJSON(t *testing.T) {
	data, err := json.Marshal(DeliveryPolicy{ResponseAdID: 9_007_199_254_740_993})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"responseAdId":"9007199254740993"`) {
		t.Fatalf("response advertisement id was not encoded as a precision-safe string: %s", data)
	}
}

func TestNormalizeDeliveryPolicyDefaultsOmittedOverridesToInterfacePolicy(t *testing.T) {
	policy, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), SaveDeliveryPolicyRequest{})
	if err != nil {
		t.Fatalf("normalizeDeliveryPolicy() error=%v", err)
	}
	if policy.CachePolicyOverride != 1 || policy.ResponseAdPolicyOverride != 1 {
		t.Fatalf("omitted overrides must preserve interface-local behavior: %#v", policy)
	}
	if policy.ResponseAdField != "_advertisement" {
		t.Fatalf("unexpected default advertisement field: %q", policy.ResponseAdField)
	}
}

func TestNormalizeDeliveryPolicyRejectsInvalidOverrideValues(t *testing.T) {
	tests := []struct {
		name string
		req  SaveDeliveryPolicyRequest
	}{
		{name: "cache override", req: SaveDeliveryPolicyRequest{CachePolicyOverride: intPtr(2)}},
		{name: "advertisement override", req: SaveDeliveryPolicyRequest{ResponseAdPolicyOverride: intPtr(-1)}},
		{name: "request timeout override", req: SaveDeliveryPolicyRequest{RequestTimeoutOverride: intPtr(2)}},
		{name: "request timeout range", req: SaveDeliveryPolicyRequest{RequestTimeoutMs: 121_000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), tt.req); err == nil {
				t.Fatal("expected invalid override value to be rejected")
			}
		})
	}
}

func TestNormalizeDeliveryPolicySupportsInheritedRequestTimeout(t *testing.T) {
	policy, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), SaveDeliveryPolicyRequest{
		RequestTimeoutMs:       2500,
		RequestTimeoutOverride: intPtr(1),
	})
	if err != nil {
		t.Fatalf("normalizeDeliveryPolicy() error=%v", err)
	}
	if policy.RequestTimeoutMs != 2500 || policy.RequestTimeoutOverride != 1 {
		t.Fatalf("unexpected request timeout policy: %+v", policy)
	}

	inherited, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), SaveDeliveryPolicyRequest{})
	if err != nil {
		t.Fatalf("normalize inherited policy error=%v", err)
	}
	if inherited.RequestTimeoutMs != 0 || inherited.RequestTimeoutOverride != 0 {
		t.Fatalf("omitted timeout must inherit global: %+v", inherited)
	}
}

func TestNormalizeDeliveryPolicyInfersInterfaceOverrideForNonZeroTimeout(t *testing.T) {
	policy, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), SaveDeliveryPolicyRequest{
		RequestTimeoutMs: 2500,
	})
	if err != nil {
		t.Fatalf("normalizeDeliveryPolicy() error=%v", err)
	}
	if policy.RequestTimeoutMs != 2500 || policy.RequestTimeoutOverride != 1 {
		t.Fatalf("a non-zero interface timeout without an explicit flag must override global: %+v", policy)
	}
}

func TestNormalizeDeliveryPolicyPreservesTimeoutPriorityContract(t *testing.T) {
	tests := []struct {
		name                                   string
		request                                SaveDeliveryPolicyRequest
		wantLocal, wantOverride, wantEffective int
	}{
		{"unset inherits", SaveDeliveryPolicyRequest{}, 0, 0, 100},
		{"zero with override inherits", SaveDeliveryPolicyRequest{RequestTimeoutOverride: intPtr(1)}, 0, 1, 100},
		{"explicit upper bound overrides lower global", SaveDeliveryPolicyRequest{RequestTimeoutMs: 120000, RequestTimeoutOverride: intPtr(1)}, 120000, 1, 120000},
		{"legacy nonzero infers override", SaveDeliveryPolicyRequest{RequestTimeoutMs: 120000}, 120000, 1, 120000},
		{"disabled override keeps stored local value", SaveDeliveryPolicyRequest{RequestTimeoutMs: 120000, RequestTimeoutOverride: intPtr(0)}, 120000, 0, 100},
		{"explicit lower bound", SaveDeliveryPolicyRequest{RequestTimeoutMs: 100, RequestTimeoutOverride: intPtr(1)}, 100, 1, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), tt.request)
			if err != nil {
				t.Fatal(err)
			}
			if policy.RequestTimeoutMs != tt.wantLocal || policy.RequestTimeoutOverride != tt.wantOverride {
				t.Fatalf("normalized timeout = (%d, %d), want (%d, %d)", policy.RequestTimeoutMs, policy.RequestTimeoutOverride, tt.wantLocal, tt.wantOverride)
			}
			if got := requesttimeout.ResolveMilliseconds(policy.RequestTimeoutMs, 100, policy.RequestTimeoutOverride); got != tt.wantEffective {
				t.Fatalf("effective timeout = %d, want %d", got, tt.wantEffective)
			}
		})
	}
}

func TestNormalizeDeliveryPolicyRejectsTimeoutBoundsEvenWhenOverrideIsDisabled(t *testing.T) {
	for _, flag := range []int{0, 1} {
		for _, value := range []int{-1, 1, 99, 120001} {
			_, err := (&Service{}).normalizeDeliveryPolicy(context.Background(), SaveDeliveryPolicyRequest{
				RequestTimeoutMs:       value,
				RequestTimeoutOverride: intPtr(flag),
			})
			if err == nil {
				t.Fatalf("out-of-range timeout %d accepted with override %d", value, flag)
			}
		}
	}
}
