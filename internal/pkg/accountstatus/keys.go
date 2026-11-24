package accountstatus

// 账号封禁必须同时冻结点名下的调用凭证，否则「账号已封禁、Key 仍显示启用」
// 这种同一份数据两个真相的状态会反复出现（网关靠 user.status 拦住了调用，
// 但 Key 列表、导出、按 Key 维度的白名单下发仍把该 Key 当成可用）。
//
// 这里只处理 bp_api_key（/api 平面的接口 Key）。/open-api 平面的
// bp_open_api_key 不在此列，原因是它的 status 语义是不可逆吊销，并且带
// active_user_id 唯一约束（status=1 时等于 user_id）：暂停后再恢复会与
// 「每用户仅一个有效 Key」的约束和历史 revoked_at 审计冲突。该平面由
// OpenAuthMiddleware 直接判定 bp_user 有效状态（middleware.go 四层开关的
// 第二层），封禁即时生效，无需落库。
const (
	// SuspendKeysTemporarySQL 把启用中的 Key 冻结为「临时封禁暂停」，
	// 账号解封时由 RestoreSuspendedKeysSQL 精确恢复。
	//
	// 条件里的 (status = 1 or suspended_by_ban = 1) 覆盖两种进入路径：
	// 正常启用中的 Key，以及先前已因封禁暂停、随后被升级为永久封禁的 Key。
	// 用户自己主动禁用（status=0 且 suspended_by_ban=0）的行不会被改成 1，
	// 解封时也就不会把用户主动关闭的 Key 打开。
	SuspendKeysTemporarySQL = `
		update bp_api_key
		   set status = 0,
		       suspended_by_ban = 1
		 where user_id = ?
		   and (status = 1 or suspended_by_ban = 1)`

	// SuspendKeysPermanentSQL 永久封禁：Key 冻结并标记为 2。
	// 2 与 1 的唯一差别是界面上的来源展示（作废 / 暂停），
	// 两者都是账号封禁的投影，解封时一并恢复，不构成独立的封禁状态。
	SuspendKeysPermanentSQL = `
		update bp_api_key
		   set status = 0,
		       suspended_by_ban = 2
		 where user_id = ?
		   and (status = 1 or suspended_by_ban = 1)`

	// RestoreSuspendedKeysSQL 解封时恢复该账号名下所有因封禁而停用的 Key。
	//
	// 刻意不区分 1（临时封禁暂停）与 2（永久封禁作废）：账号封禁是一个账号级
	// 开关，凭证状态只是这个开关的投影，不能变成第二个独立的封禁真相源。
	// 只要账号仍被封禁，网关和各入口就按 bp_user 拦截；账号一旦解封，凭证
	// 必须一起回到可用状态，否则会出现「账号已解封、Key 仍因封禁作废」的死锁——
	// 管理员去点启用会被判定为封禁期间操作，任何路径都无法恢复。
	//
	// 用户自己主动禁用（status=0 且 suspended_by_ban=0）的行不会被改动。
	RestoreSuspendedKeysSQL = `
		update bp_api_key
		   set status = 1,
		       suspended_by_ban = 0
		 where user_id = ?
		   and suspended_by_ban > 0`
)

// SuspendKeysSQL 按封禁类型选择冻结语句。
func SuspendKeysSQL(permanent bool) string {
	if permanent {
		return SuspendKeysPermanentSQL
	}
	return SuspendKeysTemporarySQL
}
