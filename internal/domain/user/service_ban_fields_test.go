package user

import "testing"

// 防回归：更新请求省略 status 时必须沿用当前状态。
// 缺省取 1 会让「只改昵称」把封禁中的账号静默解封，并连带恢复其 Key。
func TestApplyUpdateDefaultsKeepsCurrentStatusWhenOmitted(t *testing.T) {
	previous := UserRecord{Status: 0, BanPermanent: 1, BanReason: "批量注册"}
	req := SaveUserRequest{}
	applyUpdateDefaults(&req, previous)
	if req.Status == nil || *req.Status != 0 {
		t.Fatalf("status = %v, want 0", req.Status)
	}
}

func TestApplyUpdateDefaultsRespectsExplicitStatus(t *testing.T) {
	previous := UserRecord{Status: 0, BanPermanent: 1, BanReason: "批量注册"}
	enabled := 1
	req := SaveUserRequest{Status: &enabled}
	applyUpdateDefaults(&req, previous)
	if req.Status == nil || *req.Status != 1 {
		t.Fatalf("status = %v, want 1", req.Status)
	}
}

// 封禁中的账号只改昵称时，封禁字段必须原样保留，
// 否则 normalizeBanFields 会因为原因/到期时间为空拒绝请求，
// 或者（原因被清空时）把一次资料修改变成一次解封。
func TestApplyUpdateDefaultsInheritsBanFields(t *testing.T) {
	previous := UserRecord{
		Status:       0,
		BanPermanent: 0,
		BanUntil:     "2026-11-05 12:00:00",
		BanReason:    "请求内容重叠",
	}
	req := SaveUserRequest{RealName: "张三"}
	applyUpdateDefaults(&req, previous)

	if req.BanPermanent == nil || *req.BanPermanent != 0 {
		t.Fatalf("banPermanent = %v, want 0", req.BanPermanent)
	}
	if req.BanUntil != previous.BanUntil {
		t.Fatalf("banUntil = %q, want %q", req.BanUntil, previous.BanUntil)
	}
	if req.BanReason != previous.BanReason {
		t.Fatalf("banReason = %q, want %q", req.BanReason, previous.BanReason)
	}
}

// 永久封禁没有到期时间，不能被继承出「永久 + 到期时间」的矛盾组合。
func TestApplyUpdateDefaultsDropsUntilForPermanentBan(t *testing.T) {
	previous := UserRecord{Status: 0, BanPermanent: 1, BanUntil: "", BanReason: "刷量"}
	req := SaveUserRequest{}
	applyUpdateDefaults(&req, previous)
	if req.BanUntil != "" {
		t.Fatalf("banUntil = %q, want empty", req.BanUntil)
	}
}

// 显式给出的封禁字段优先，运营改封禁期限时不能被旧值覆盖。
func TestApplyUpdateDefaultsKeepsExplicitBanFields(t *testing.T) {
	previous := UserRecord{Status: 0, BanPermanent: 0, BanUntil: "2026-11-05 12:00:00", BanReason: "旧原因"}
	permanent := 1
	req := SaveUserRequest{BanPermanent: &permanent, BanReason: "新原因"}
	applyUpdateDefaults(&req, previous)

	if req.BanPermanent == nil || *req.BanPermanent != 1 {
		t.Fatalf("banPermanent = %v, want 1", req.BanPermanent)
	}
	if req.BanReason != "新原因" {
		t.Fatalf("banReason = %q, want 新原因", req.BanReason)
	}
}

// 启用中的账号不受影响：不继承任何封禁残留。
func TestApplyUpdateDefaultsLeavesActiveAccountAlone(t *testing.T) {
	previous := UserRecord{Status: 1}
	req := SaveUserRequest{}
	applyUpdateDefaults(&req, previous)
	if req.Status == nil || *req.Status != 1 {
		t.Fatalf("status = %v, want 1", req.Status)
	}
	if req.BanPermanent != nil || req.BanUntil != "" || req.BanReason != "" {
		t.Fatalf("active account picked up ban fields: %+v", req)
	}
}
