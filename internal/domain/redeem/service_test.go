package redeem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// newBareService 构造不依赖 DB 的 Service：仅用于纯校验路径
// （normalizeType 的奖励矩阵前置校验、mapRedeemError 等）。
func newBareService() *Service { return &Service{} }

func baseTypeRequest() SaveTypeRequest {
	return SaveTypeRequest{
		Code:         "welcome-2026",
		Name:         "  新手礼包  ",
		RewardType:   RewardTypePoints,
		RewardPoints: 100,
		StartAt:      "2026-01-01 00:00:00",
		EndAt:        "2026-02-01 00:00:00",
	}
}

func TestNormalizeTypeDefaults(t *testing.T) {
	s := newBareService()
	item, err := s.normalizeType(context.Background(), baseTypeRequest())
	if err != nil {
		t.Fatalf("normalizeType: %v", err)
	}
	if item.Code != "WELCOME-2026" {
		t.Fatalf("code not upper/trimmed: %q", item.Code)
	}
	if item.Name != "新手礼包" {
		t.Fatalf("name not trimmed: %q", item.Name)
	}
	if item.Status != 1 {
		t.Fatalf("default status = %d, want 1", item.Status)
	}
	if item.RewardBalance != "0.0000" {
		t.Fatalf("nil balance = %q, want 0.0000", item.RewardBalance)
	}
	disabled := 0
	req := baseTypeRequest()
	req.Status = &disabled
	item, err = s.normalizeType(context.Background(), req)
	if err != nil {
		t.Fatalf("normalizeType with status: %v", err)
	}
	if item.Status != 0 {
		t.Fatalf("status = %d, want 0", item.Status)
	}
}

func TestNormalizeTypeRewardMatrix(t *testing.T) {
	s := newBareService()
	cases := []struct {
		name string
		req  SaveTypeRequest
	}{
		{"points", func() SaveTypeRequest { r := baseTypeRequest(); return r }()},
		{"balance", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeBalance
			r.RewardPoints = 0
			r.RewardBalance = "9.9"
			return r
		}()},
		{"balance numeric", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeBalance
			r.RewardPoints = 0
			r.RewardBalance = 12.5
			return r
		}()},
		{"combined", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeCombined
			r.RewardBalance = "1.0000"
			return r
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.normalizeType(context.Background(), tc.req); err != nil {
				t.Fatalf("normalizeType: %v", err)
			}
		})
	}
}

func TestNormalizeTypeRejectsInvalidCombos(t *testing.T) {
	s := newBareService()
	cases := []struct {
		name string
		req  SaveTypeRequest
		want string
	}{
		{"points with balance", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardBalance = "1.0000"
			return r
		}(), "点数奖励要求点数面值大于 0"},
		{"points zero", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardPoints = 0
			return r
		}(), "点数奖励要求点数面值大于 0"},
		{"balance zero", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeBalance
			r.RewardPoints = 0
			r.RewardBalance = "0.0000"
			return r
		}(), "余额奖励要求余额面值大于 0"},
		{"balance with ref", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeBalance
			r.RewardPoints = 0
			r.RewardBalance = "5"
			r.RewardRefID = 3
			return r
		}(), "不引用套餐"},
		{"global with balance", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeGlobal
			r.RewardPoints = 0
			r.RewardBalance = "1"
			r.RewardRefID = 5
			return r
		}(), "套餐奖励要求面值为 0"},
		{"combined missing balance", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeCombined
			return r
		}(), "组合奖励要求点数与余额面值均大于 0"},
		{"global without ref", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = RewardTypeGlobal
			r.RewardPoints = 0
			r.RewardRefID = 0
			return r
		}(), "必须引用已创建的套餐"},
		{"unknown reward type", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardType = 9
			return r
		}(), "奖励类型不支持"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.normalizeType(context.Background(), tc.req)
			if err == nil {
				t.Fatal("expected error")
			}
			biz, ok := apperrors.AsBusinessError(err)
			if !ok {
				t.Fatalf("error is not BusinessError: %v", err)
			}
			if biz.StatusCode != consts.StatusBadRequest {
				t.Fatalf("status = %d, want %d", biz.StatusCode, consts.StatusBadRequest)
			}
			if !strings.Contains(biz.Message, tc.want) {
				t.Fatalf("message = %q, want contains %q", biz.Message, tc.want)
			}
		})
	}
}

func TestNormalizeTypeRejectsMalformedFields(t *testing.T) {
	s := newBareService()
	cases := []struct {
		name string
		req  SaveTypeRequest
		want string
	}{
		{"empty code", func() SaveTypeRequest { r := baseTypeRequest(); r.Code = "  "; return r }(), "类型标识"},
		{"code too long", func() SaveTypeRequest { r := baseTypeRequest(); r.Code = strings.Repeat("A", 33); return r }(), "类型标识"},
		{"code bad chars", func() SaveTypeRequest { r := baseTypeRequest(); r.Code = "新手礼包"; return r }(), "类型标识"},
		{"empty name", func() SaveTypeRequest { r := baseTypeRequest(); r.Name = " "; return r }(), "类型名称"},
		{"negative points", func() SaveTypeRequest { r := baseTypeRequest(); r.RewardPoints = -1; return r }(), "点数面值"},
		{"points overflow", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.RewardPoints = maxUserPointBalance + 1
			return r
		}(), "点数面值"},
		{"bad balance text", func() SaveTypeRequest { r := baseTypeRequest(); r.RewardBalance = "abc"; return r }(), "余额面值"},
		{"negative per-user", func() SaveTypeRequest { r := baseTypeRequest(); r.PerUserLimit = -1; return r }(), "每账号限领次数"},
		{"per-user overflow", func() SaveTypeRequest { r := baseTypeRequest(); r.PerUserLimit = maxPerUserLimit + 1; return r }(), "每账号限领次数"},
		{"per-ip overflow", func() SaveTypeRequest { r := baseTypeRequest(); r.PerIPLimit = maxPerIPLimit + 1; return r }(), "每 IP 限领次数"},
		{"bad start time", func() SaveTypeRequest { r := baseTypeRequest(); r.StartAt = "yesterday"; return r }(), "开始时间"},
		{"bad end time", func() SaveTypeRequest { r := baseTypeRequest(); r.EndAt = ""; return r }(), "结束时间"},
		{"start equals end", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.StartAt = "2026-02-01 00:00:00"
			r.EndAt = "2026-02-01 00:00:00"
			return r
		}(), "开始时间必须早于结束时间"},
		{"start after end", func() SaveTypeRequest {
			r := baseTypeRequest()
			r.StartAt = "2026-03-01 00:00:00"
			r.EndAt = "2026-02-01 00:00:00"
			return r
		}(), "开始时间必须早于结束时间"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.normalizeType(context.Background(), tc.req)
			if err == nil {
				t.Fatal("expected error")
			}
			biz, ok := apperrors.AsBusinessError(err)
			if !ok {
				t.Fatalf("error is not BusinessError: %v", err)
			}
			if !strings.Contains(biz.Message, tc.want) {
				t.Fatalf("message = %q, want contains %q", biz.Message, tc.want)
			}
		})
	}
}

func TestMapRedeemError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"invalid", errCodeInvalid, consts.StatusBadRequest, "兑换码不存在"},
		{"used", errCodeUsed, consts.StatusBadRequest, "兑换码已被使用"},
		{"type disabled", errTypeDisabled, consts.StatusBadRequest, "该兑换码类型已停用"},
		{"expired", errTypeExpired, consts.StatusBadRequest, "兑换码不在有效期内"},
		{"limit", errLimitReached, consts.StatusBadRequest, "已达到该类型兑换码的限领次数"},
		{"ip limit", errIPLimitReached, consts.StatusBadRequest, "该 IP 已达到限领次数"},
		{"ip unavailable", errIPUnavailable, consts.StatusServiceUnavailable, "服务繁忙"},
		{"point balance", errPointBalance, consts.StatusConflict, "账户点数已达到系统上限"},
		{"balance capacity", errBalanceCapacity, consts.StatusConflict, "账户余额已达到系统上限"},
		{"package disabled", errPackageDisabled, consts.StatusConflict, "套餐已停用或不允许参与兑换"},
		{"package ref invalid", errPackageRefInvalid, consts.StatusBadRequest, "套餐引用无效"},
		{"guard unavailable", errGuardUnavailable, consts.StatusServiceUnavailable, "服务繁忙"},
		{"unknown falls back to 500", errors.New("boom"), consts.StatusInternalServerError, "服务繁忙"},
		{"business error passes through", apperrors.New(consts.StatusTooManyRequests, "频控"), consts.StatusTooManyRequests, "频控"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapRedeemError(tc.err)
			biz, ok := apperrors.AsBusinessError(got)
			if !ok {
				t.Fatalf("not BusinessError: %v", got)
			}
			if biz.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", biz.StatusCode, tc.status)
			}
			if !strings.Contains(biz.Message, tc.want) {
				t.Fatalf("message = %q, want contains %q", biz.Message, tc.want)
			}
		})
	}
}

func TestRewardConfigChanged(t *testing.T) {
	base := typeRow{
		RewardType: RewardTypePoints, RewardPoints: 100, RewardBalance: "0.0000",
		RewardRefID: 0, PerUserLimit: 1, PerIPLimit: 0,
	}
	if rewardConfigChanged(base, base) {
		t.Fatal("identical config must not count as changed")
	}
	// 名称/窗口/状态不在冻结判据内。
	renamed := base
	renamed.Name = "新名字"
	renamed.Status = 0
	renamed.StartAt = time.Now()
	if rewardConfigChanged(base, renamed) {
		t.Fatal("name/status/window changes must not freeze")
	}
	mutations := map[string]func(r *typeRow){
		"reward type": func(r *typeRow) { r.RewardType = RewardTypeBalance },
		"points":      func(r *typeRow) { r.RewardPoints = 200 },
		"balance":     func(r *typeRow) { r.RewardBalance = "1.0000" },
		"ref id":      func(r *typeRow) { r.RewardRefID = 42 },
		"per user":    func(r *typeRow) { r.PerUserLimit = 2 },
		"per ip":      func(r *typeRow) { r.PerIPLimit = 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if !rewardConfigChanged(base, changed) {
				t.Fatalf("%s change must be detected", name)
			}
		})
	}
}

func TestNullInt64String(t *testing.T) {
	if got := nullInt64String(sql.NullInt64{Int64: 7, Valid: true}); got != "7" {
		t.Fatalf("valid = %q", got)
	}
	if got := nullInt64String(sql.NullInt64{}); got != "" {
		t.Fatalf("invalid = %q, want empty", got)
	}
}

// TestGenerateResultJSONContract 锁定明文码的 JSON 出口边界。
//
// Codes 是开放 API 生成链路专用的明文出口（AstrBot 活动发放依赖它把 CDK
// 发到聊天；缺失则 /open-api/redeem-codes 不可用），而控制台链路按
// omitempty 完全不出现该字段——不把控制台响应变成第二个明文出口。
func TestGenerateResultJSONContract(t *testing.T) {
	console, err := json.Marshal(GenerateResult{BatchID: "1", Count: 2})
	if err != nil {
		t.Fatalf("marshal console: %v", err)
	}
	if strings.Contains(string(console), `"codes"`) {
		t.Fatalf("console payload must omit the codes field: %s", console)
	}
	if !strings.Contains(string(console), `"batchId":"1"`) ||
		!strings.Contains(string(console), `"count":2`) {
		t.Fatalf("console payload lost base fields: %s", console)
	}

	open, err := json.Marshal(GenerateResult{
		BatchID: "2", Count: 1, Codes: []string{"ABCD-EFGH-IJKL-MNOP"},
	})
	if err != nil {
		t.Fatalf("marshal open: %v", err)
	}
	if !strings.Contains(string(open), `"codes":["ABCD-EFGH-IJKL-MNOP"]`) {
		t.Fatalf("open payload must expose the plaintext codes: %s", open)
	}
}
