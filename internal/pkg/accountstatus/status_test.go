package accountstatus

import (
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestEffectiveStatusSQL(t *testing.T) {
	aliased := EffectiveStatusSQL("u")
	for _, required := range []string{"u.status", "u.ban_permanent", "u.ban_until", "now()"} {
		if !strings.Contains(aliased, required) {
			t.Fatalf("effective status expression missing %q: %s", required, aliased)
		}
	}
	if strings.ContainsAny(aliased, ";?") {
		t.Fatalf("effective status expression must remain a static SQL fragment: %s", aliased)
	}
	plain := EffectiveStatusSQL("")
	if strings.Contains(plain, "u.") || !strings.Contains(plain, "status") {
		t.Fatalf("unexpected unaliased expression: %s", plain)
	}
}

func TestBanDetailsContract(t *testing.T) {
	temporary := NewBanDetails(0, " 2026-08-12 10:00:00 ", " repeated abuse ")
	if temporary.Tag != BanTag || temporary.AccountStatus != AccountStatusBanned || temporary.ResourceAccess != ResourceAccessDeny {
		t.Fatalf("unexpected temporary ban labels: %+v", temporary)
	}
	if temporary.Permanent || temporary.BanType != BanTypeTemporary || temporary.BanUntil != "2026-08-12 10:00:00" || temporary.BanReason != "repeated abuse" {
		t.Fatalf("unexpected temporary ban details: %+v", temporary)
	}
	if got := temporary.Message(); got != "账号已被临时封禁至 2026-08-12 10:00:00，原因：repeated abuse" {
		t.Fatalf("temporary message = %q", got)
	}
	headers := temporary.ResponseHeaders()
	if headers["X-Bugpk-Account-Status"] != "BANNED" || headers["X-Bugpk-Ban-Type"] != "TEMPORARY" || headers["X-Bugpk-Ban-Until"] != temporary.BanUntil {
		t.Fatalf("unexpected temporary ban headers: %+v", headers)
	}

	permanent := NewBanDetails(1, "2026-08-12 10:00:00", "")
	if !permanent.Permanent || permanent.BanType != BanTypePermanent || permanent.BanUntil != "" || permanent.BanReason != "未提供封禁原因" {
		t.Fatalf("unexpected permanent ban details: %+v", permanent)
	}
	if got := permanent.Message(); got != "账号已被永久封禁，原因：未提供封禁原因" {
		t.Fatalf("permanent message = %q", got)
	}
	if _, ok := permanent.ResponseHeaders()["X-Bugpk-Ban-Until"]; ok {
		t.Fatal("permanent ban must not expose an inapplicable expiry header")
	}
}

func TestForbiddenCarriesStructuredBanData(t *testing.T) {
	err := Forbidden(1, "", "security policy")
	if err.StatusCode != consts.StatusForbidden {
		t.Fatalf("status = %d", err.StatusCode)
	}
	details, ok := err.Data.(BanDetails)
	if !ok || details.Tag != BanTag || details.BanReason != "security policy" {
		t.Fatalf("unexpected structured error data: %#v", err.Data)
	}
}

func TestActiveTrafficSQL(t *testing.T) {
	condition := ActiveTrafficSQL("l")
	for _, required := range []string{
		"l.charge_type <> 'ADMIN'",
		"bugpk_traffic_user.id = l.user_id",
		"bugpk_traffic_user.ban_permanent",
		"bugpk_traffic_user.ban_until",
	} {
		if !strings.Contains(condition, required) {
			t.Fatalf("active traffic condition missing %q: %s", required, condition)
		}
	}
	if strings.ContainsAny(condition, ";?") {
		t.Fatalf("active traffic condition must remain a static SQL fragment: %s", condition)
	}
}
