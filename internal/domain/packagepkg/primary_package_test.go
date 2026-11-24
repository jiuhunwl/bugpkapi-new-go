package packagepkg

import (
	"context"
	"testing"
)

// 主套餐只允许落在 GLOBAL / INTERFACE 两层，且编号不能为负；
// 这些都是不触库就能拒绝的输入，非法值必须在进仓储前被挡下。
func TestSetPrimaryPackageRejectsInvalidInput(t *testing.T) {
	service := NewService(nil, nil)
	principal := Principal{ID: 1}
	ctx := context.Background()

	cases := []struct {
		name string
		req  SetPrimaryPackageRequest
	}{
		{name: "unknown scope", req: SetPrimaryPackageRequest{Scope: "POINT", ID: 1}},
		{name: "empty scope", req: SetPrimaryPackageRequest{Scope: " ", ID: 1}},
		{name: "negative id", req: SetPrimaryPackageRequest{Scope: "GLOBAL", ID: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := service.SetPrimaryPackage(ctx, principal, tc.req); err == nil {
				t.Fatal("invalid primary package request was accepted")
			}
		})
	}
}
