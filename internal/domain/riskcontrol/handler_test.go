package riskcontrol

import (
	"errors"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestBoundedRiskIntUsesDefaultOnlyForEmptyInput(t *testing.T) {
	value, err := boundedRiskInt("", "limit", defaultLimit, 1, maxLimit)
	if err != nil || value != defaultLimit {
		t.Fatalf("empty limit = %d, %v", value, err)
	}

	value, err = boundedRiskInt(" 12 ", "limit", defaultLimit, 1, maxLimit)
	if err != nil || value != 12 {
		t.Fatalf("explicit limit = %d, %v", value, err)
	}
}

func TestBoundedRiskIntRejectsMalformedAndOutOfRangeInput(t *testing.T) {
	for _, raw := range []string{"abc", "0", "-1", "101"} {
		_, err := boundedRiskInt(raw, "limit", defaultLimit, 1, maxLimit)
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
			t.Fatalf("limit %q error = %v", raw, err)
		}
	}

	for _, raw := range []string{"0", "1441", "1.5"} {
		_, err := boundedRiskInt(raw, "windowMinutes", defaultWindowMinutes, 1, maxWindowMinutes)
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
			t.Fatalf("windowMinutes %q error = %v", raw, err)
		}
	}
}
