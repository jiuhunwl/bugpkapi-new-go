package packagepkg

import (
	"encoding/json"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestNormalizeInterfaceAcceptsCanonicalSpecContract(t *testing.T) {
	item, err := normalizeInterface(SaveInterfacePackageRequest{
		InterfaceID: 1,
		Name:        "接口套餐",
		Specs: []SaveInterfacePackageSpecRequest{{
			DailyLimit: 100,
			Price:      "9.9000",
			QPSLimit:   5,
			Remark:     "优先规格",
			SortNo:     2,
			SpecName:   "专业版",
			ValidDays:  30,
		}},
	})
	if err != nil {
		t.Fatalf("normalizeInterface returned error: %v", err)
	}
	if len(item.Specs) != 1 {
		t.Fatalf("spec count = %d, want 1", len(item.Specs))
	}
	spec := item.Specs[0]
	if spec.SpecName != "专业版" || spec.Remark != "优先规格" || spec.SortNo != 2 {
		t.Fatalf("canonical spec contract was not preserved: %#v", spec)
	}
}

func TestNormalizeInterfaceAcceptsLegacyNameButRejectsConflict(t *testing.T) {
	legacy, err := normalizeInterface(SaveInterfacePackageRequest{
		InterfaceID: 1,
		Name:        "legacy",
		Specs: []SaveInterfacePackageSpecRequest{{
			Name:      "legacy spec",
			Price:     "1",
			ValidDays: 30,
		}},
	})
	if err != nil || legacy.Specs[0].SpecName != "legacy spec" {
		t.Fatalf("legacy name compatibility failed: item=%#v err=%v", legacy, err)
	}

	_, err = normalizeInterface(SaveInterfacePackageRequest{
		InterfaceID: 1,
		Name:        "conflict",
		Specs: []SaveInterfacePackageSpecRequest{{
			Name:      "legacy",
			SpecName:  "canonical",
			Price:     "1",
			ValidDays: 30,
		}},
	})
	if err == nil {
		t.Fatal("normalizeInterface accepted conflicting canonical and legacy names")
	}
}

func TestInterfacePackageSpecResponseUsesCanonicalFields(t *testing.T) {
	data, err := json.Marshal(InterfacePackageSpec{
		PackageID: "9",
		Remark:    "persisted",
		SortNo:    3,
		SpecName:  "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, fragment := range []string{`"packageId":"9"`, `"remark":"persisted"`, `"sortNo":3`, `"specName":"standard"`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("response JSON lost %s: %s", fragment, text)
		}
	}
	if strings.Contains(text, `"name":`) {
		t.Fatalf("response JSON exposed obsolete name field: %s", text)
	}
}

func TestMinimumSpecPriceIsIndependentOfDisplayOrder(t *testing.T) {
	got := minimumSpecPrice([]InterfacePackageSpec{
		{Price: "20.0000", SortNo: 1},
		{Price: "9.9000", SortNo: 2},
		{Price: "10.0000", SortNo: 3},
	})
	if got != "9.9000" {
		t.Fatalf("minimumSpecPrice() = %q, want 9.9000", got)
	}
}

func TestPackageDeleteHistoryMapsToConflict(t *testing.T) {
	err := mapDeleteErr(ErrPackageHasBusinessHistory, "套餐不存在")
	business, ok := apperrors.AsBusinessError(err)
	if !ok {
		t.Fatalf("mapDeleteErr returned %T, want BusinessError", err)
	}
	if business.StatusCode != consts.StatusConflict || !strings.Contains(business.Message, "改为禁用") {
		t.Fatalf("unexpected delete conflict: %#v", business)
	}
}

func TestFreePackageRepeatClaimMapsToConflict(t *testing.T) {
	err := mapPurchaseErr(ErrFreePackageAlreadyClaimed)
	business, ok := apperrors.AsBusinessError(err)
	if !ok {
		t.Fatalf("mapPurchaseErr returned %T, want BusinessError", err)
	}
	if business.StatusCode != consts.StatusConflict || !strings.Contains(business.Message, "只能领取一次") {
		t.Fatalf("unexpected free-package conflict: %#v", business)
	}
}
