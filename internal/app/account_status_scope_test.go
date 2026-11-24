package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 账号封禁是账号级单一开关：bp_user.status 为 0 时，该账号名下的全部资产
// （调用 Key、余额、套餐、兑换、工单、开放 API）一起不可用。
//
// 为了让这条规则成立且不退化，账号状态的判定只允许出现在接入平面：
//   - app/account_guard.go       JWT 服务器级主网关（本包，不在扫描范围）
//   - domain/rbac/*              解析访问令牌时判定
//   - domain/auth/service.go     登录、刷新、找回密码等认证入口
//   - domain/user/service.go     取当前身份
//   - domain/gateway/service.go  /api 平面（X-API-Key）网关
//   - domain/openapi/middleware.go /open-api 平面网关
//
// 其余任何业务功能都不得自己判断账号状态。历史教训：Key 管理里曾有一处
// 「账号被封就不许启用 Key」的独立判断，账号解封后它仍按 Key 上的历史标记
// 拒绝，运营看到「账号已解封却无法启用密钥」，而且没有任何路径能恢复。
func TestAccountStatusDecisionsOnlyLiveInGateways(t *testing.T) {
	allowed := map[string]bool{
		"rbac/repository.go":    true,
		"rbac/service.go":       true,
		"auth/service.go":       true,
		"user/service.go":       true,
		"gateway/service.go":    true,
		"openapi/middleware.go": true,
	}
	root := filepath.Join("..", "domain")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		if !strings.Contains(text, "accountstatus.Forbidden(") &&
			!strings.Contains(text, "accountstatus.IsActive(") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !allowed[rel] {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("账号状态只允许在接入平面判定，业务功能不得自行判断：%v", offenders)
	}
}
