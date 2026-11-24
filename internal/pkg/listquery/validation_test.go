package listquery

import (
	"errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestValidateListBounds(t *testing.T) {
	for _, test := range []struct {
		keyword string
		page    int
		size    int
	}{
		{keyword: strings.Repeat("查", MaxKeywordRunes+1), page: 1, size: 20},
		{page: 0, size: 20},
		{page: 1001, size: 20},
		{page: 1, size: 101},
	} {
		err := Validate(test.keyword, test.page, test.size, 100)
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
			t.Fatalf("Validate(%q,%d,%d) error = %v", test.keyword, test.page, test.size, err)
		}
	}
	if err := Validate(strings.Repeat("查", MaxKeywordRunes), 1000, 100, 100); err != nil {
		t.Fatal(err)
	}
}

func TestValidateListFilters(t *testing.T) {
	invalid := 2
	if err := Binary("status", &invalid); err == nil {
		t.Fatal("invalid binary filter was accepted")
	}
	if err := Enum("type", "unknown", "GLOBAL", "INTERFACE"); err == nil {
		t.Fatal("invalid enum filter was accepted")
	}
	if err := NonNegativeID("interfaceId", -1); err == nil {
		t.Fatal("negative id filter was accepted")
	}
}
