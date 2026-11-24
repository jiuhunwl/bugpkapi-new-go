package redeem

import (
	"crypto/rand"
	"strings"
)

// generateCode 生成一枚 19 字符明文码：XXXX-XXXX-XXXX-XXXX。
// 用 crypto/rand + 拒绝采样保证 31 字符集上均匀分布（取模偏差在
// 256 % 31 = 8 的拒绝窗口内消除）。
func generateCode() (string, error) {
	raw := make([]byte, codeGroups*codeGroupLength)
	buf := make([]byte, codeGroups*codeGroupLength)
	for filled := 0; filled < len(raw); {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= 252 { // 252 = 31*8，落在拒绝窗口外，丢弃保均匀
				continue
			}
			raw[filled] = codeAlphabet[int(b)%len(codeAlphabet)]
			filled++
			if filled == len(raw) {
				break
			}
		}
	}
	groups := make([]string, 0, codeGroups)
	for i := 0; i < codeGroups; i++ {
		groups = append(groups, string(raw[i*codeGroupLength:(i+1)*codeGroupLength]))
	}
	return strings.Join(groups, "-"), nil
}

// normalizeCode 归一化用户输入为规范存储形（与 generateCode 同形的
// 19 字符 XXXX-XXXX-XXXX-XXXX）：去全部空白与连字符、大写、按 4-4-4-4
// 重新分组。长度或字符集不符直接判「不存在」（与已作废统一文案，
// 消除撞码存在性 oracle）。库内 code_plain 即此规范形，故归一化结果
// 可直接用于查询。
func normalizeCode(input string) (string, error) {
	compact := strings.ToUpper(strings.Join(strings.Fields(input), ""))
	compact = strings.ReplaceAll(compact, "-", "")
	if len(compact) != codeGroups*codeGroupLength {
		return "", errCodeInvalid
	}
	for i := 0; i < len(compact); i++ {
		if !strings.ContainsRune(codeAlphabet, rune(compact[i])) {
			return "", errCodeInvalid
		}
	}
	groups := make([]string, 0, codeGroups)
	for i := 0; i < codeGroups; i++ {
		groups = append(groups, compact[i*codeGroupLength:(i+1)*codeGroupLength])
	}
	return strings.Join(groups, "-"), nil
}

// maskCode 脱敏展示：保留前 4 后 4，如 "ABCD-****-****-WXYZ"。
// 输入为空时返回空串（不伪造掩码）。
func maskCode(plain string) string {
	trimmed := strings.TrimSpace(plain)
	if len(trimmed) < 8 {
		return ""
	}
	return trimmed[:4] + "-****-****-" + trimmed[len(trimmed)-4:]
}
