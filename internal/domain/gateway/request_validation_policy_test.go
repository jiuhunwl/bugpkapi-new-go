package gateway

import (
	"testing"

	"bugpk-api/internal/pkg/requestvalidation"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestRequestValidationRejectionMapsPresetResults(t *testing.T) {
	if rejection := requestValidationRejection(requestvalidation.Decision{Result: requestvalidation.ResultPass}); rejection != nil {
		t.Fatalf("PASS returned rejection: %v", rejection)
	}
	invalid := requestValidationRejection(requestvalidation.Decision{Result: requestvalidation.ResultInvalidParams})
	if invalid == nil || invalid.StatusCode != consts.StatusBadRequest || invalid.Message != "请求参数不合规" {
		t.Fatalf("unexpected invalid-params rejection: %#v", invalid)
	}
	mismatch := requestValidationRejection(requestvalidation.Decision{Result: requestvalidation.ResultPlatformMismatch})
	if mismatch == nil || mismatch.StatusCode != consts.StatusUnprocessableEntity || mismatch.Message != "解析参数与该平台不匹配" {
		t.Fatalf("unexpected platform-mismatch rejection: %#v", mismatch)
	}
}
