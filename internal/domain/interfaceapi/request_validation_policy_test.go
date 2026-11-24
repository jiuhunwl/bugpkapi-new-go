package interfaceapi

import (
	"errors"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/requestvalidation"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestNormalizeRequestValidationPolicyDefaultsAndNormalizes(t *testing.T) {
	enabled := 1
	policy, err := normalizeRequestValidationPolicy(SaveRequestValidationPolicyRequest{
		DefaultResult: requestvalidation.ResultPlatformMismatch,
		Enabled:       &enabled,
		Rules: []requestvalidation.Rule{{
			Logic:  "all",
			Result: "pass",
			Conditions: []requestvalidation.Condition{{
				ExpectedValue: "DouYin.com",
				FieldName:     "url",
				Operator:      "domain_in",
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Enabled != 1 || policy.Rules[0].SortNo != 1 || policy.Rules[0].Conditions[0].ExpectedValue != "douyin.com" {
		t.Fatalf("unexpected policy: %#v", policy)
	}
}

func TestNormalizeRequestValidationPolicyReturnsBadRequest(t *testing.T) {
	enabled := 2
	_, err := normalizeRequestValidationPolicy(SaveRequestValidationPolicyRequest{Enabled: &enabled})
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("unexpected error: %v", err)
	}
}
