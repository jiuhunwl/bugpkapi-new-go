package accountstatus

import (
	"strings"
	"testing"
)

// 封禁类型只决定 Key 被冻结时留下的来源标记：
//   - 临时封禁：标记 1（暂停）；
//   - 永久封禁：标记 2（作废）。
//
// 两者都是账号级封禁的投影，解封时统一恢复，见下方恢复语句的测试。
func TestSuspendKeysSQLDistinguishesBanType(t *testing.T) {
	temporary := SuspendKeysSQL(false)
	permanent := SuspendKeysSQL(true)

	if !strings.Contains(temporary, "suspended_by_ban = 1") {
		t.Fatalf("temporary ban must mark keys as 1:\n%s", temporary)
	}
	if strings.Contains(temporary, "suspended_by_ban = 2") {
		t.Fatalf("temporary ban must not mark keys as permanently revoked:\n%s", temporary)
	}
	if !strings.Contains(permanent, "suspended_by_ban = 2") {
		t.Fatalf("permanent ban must mark keys as 2:\n%s", permanent)
	}
	for _, statement := range []string{temporary, permanent} {
		if !strings.Contains(statement, "set status = 0") {
			t.Fatalf("ban must disable the key itself:\n%s", statement)
		}
		if !strings.Contains(statement, "user_id = ?") {
			t.Fatalf("ban must be scoped to one account:\n%s", statement)
		}
		// 用户自己禁用的 Key（status=0 且 suspended_by_ban=0）不能被改标，
		// 否则解封时会被误认为「因封禁暂停」而重新打开。
		if !strings.Contains(statement, "(status = 1 or suspended_by_ban = 1)") {
			t.Fatalf("ban must skip keys disabled by the user:\n%s", statement)
		}
		if strings.Contains(strings.ToLower(statement), "delete") {
			t.Fatalf("ban must not delete key rows:\n%s", statement)
		}
	}
}

// 解封是账号级动作，必须把该账号名下所有因封禁停用的 Key 一起恢复。
//
// 只恢复 suspended_by_ban=1 会留下 =2 的行，界面上这些 Key 永远显示
// 「因封禁作废」，点启用又被判为封禁期间操作 —— 账号已解封却再也用不了 Key。
func TestRestoreSuspendedKeysSQLRestoresEveryBanSuspension(t *testing.T) {
	statement := RestoreSuspendedKeysSQL
	for _, clause := range []string{
		"set status = 1",
		"suspended_by_ban = 0",
		"user_id = ?",
		"and suspended_by_ban > 0",
	} {
		if !strings.Contains(statement, clause) {
			t.Fatalf("statement missing %q:\n%s", clause, statement)
		}
	}
	// 只覆盖单一标记的形式都意味着另一类封禁留下的 Key 会被漏掉。
	for _, clause := range []string{"suspended_by_ban = 1", "suspended_by_ban = 2"} {
		if strings.Contains(statement, clause) {
			t.Fatalf("restore must not be limited to %q:\n%s", clause, statement)
		}
	}
	// suspended_by_ban=0 是用户主动禁用的行，恢复语句必须跳过。
	// 上面的 > 0 条件保证了这一点，这里再钉一次避免被改成无条件 update。
	if !strings.Contains(statement, "suspended_by_ban > 0") {
		t.Fatalf("restore must skip keys disabled by the user:\n%s", statement)
	}
}
