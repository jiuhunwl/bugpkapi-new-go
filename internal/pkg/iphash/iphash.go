// Package iphash 派生客户端 IP 在落库（汇总/风控/审计表）前的去标识化盐。
//
// 盐由按环境密钥（ANALYTICS_IP_HASH_SECRET）派生。未配置密钥的安装保持与
// 历史公开字面前缀**逐字节一致**，因此无需重建 rollup；配置密钥即轮换分组键，
// 需按交付说明重建受影响的三张汇总表。
//
// 两个硬约束（不要破坏）：
//  1. SQL 侧与 Go 侧必须对同一 IP 产出相同摘要 —— 风控的 Go 侧摘要（riskcontrol
//     overview 查询键）要与 analytics 的 SQL 写入（bp_call_risk_fingerprint_5m.
//     client_ip_hash，由 sha2(concat('<prefix>',ip),256) 生成）配对。因此本包
//     统一采用「keyed SHA-256」：sha256(prefix || value)，两侧拼同一字节串。
//     MySQL 无内置 HMAC，故不能用 HMAC；keyed SHA-256 同样消灭「公开盐可穷举」。
//  2. 密钥部分以 hex 编码后内联进 SQL 单引号字面量：[0-9a-f] 字符集不可能
//     破坏 SQL 语法，也就不需要任何转义逻辑。
package iphash

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"sync"
)

// 各用途前缀。与配置密钥组合成实际盐；前缀彼此不同，保留「同一 IP 不可跨表
// 关联」的原有性质。
const (
	Region       = "bugpk-region-ip-v1:"
	Failure      = "bugpk-failure-ip-v1:"
	Risk         = "bugpk-risk-ip-v1:"
	OperationLog = "bugpk-operation-log-v1:"
)

var (
	mu     sync.RWMutex
	secret string // 空 => 沿用历史公开前缀（未配置状态）
)

// Configure 安装按环境密钥。空串/空白保持历史公开前缀。在启动装配阶段调用
// 一次；在任何汇总 SQL 执行前完成（router 装配先于 worker 启动）。
func Configure(s string) {
	mu.Lock()
	defer mu.Unlock()
	secret = strings.TrimSpace(s)
}

// hexSecret 返回 hex 编码后的密钥（未配置时为空串）。
func hexSecret() string {
	mu.RLock()
	defer mu.RUnlock()
	if secret == "" {
		return ""
	}
	return hex.EncodeToString([]byte(secret))
}

// 各用途的实际前缀（内联进 SQL 单引号字面量的文本，不含引号）。
// 未配置时逐字节等于历史公开前缀。
func RegionSQLPrefix() string       { return Region + hexSecret() }
func FailureSQLPrefix() string      { return Failure + hexSecret() }
func RiskSQLPrefix() string         { return Risk + hexSecret() }
func OperationLogSQLPrefix() string { return OperationLog + hexSecret() }

// RiskDigest 返回 sha256(实际 risk 前缀 || ip)，与 SQL 侧
// unhex(sha2(concat('<RiskSQLPrefix()>',ip),256)) 写入的值逐字节一致。
func RiskDigest(ip string) []byte {
	sum := sha256.Sum256([]byte(RiskSQLPrefix() + ip))
	return sum[:]
}

// PrefixDigest 返回客户端 IP 所在网段的去标识化摘要，只用于聚合计数：
// IPv4 归一到 /24，IPv6 归一到 /64。完整 IP 的 RiskDigest 仍用于逐条审计；
// 两者用途不同，不能互相替代。
//
// 背景：单个用户/一台 VPS 通常拥有整个 IPv6 /64，按完整地址计数毫无意义；
// 反过来把 IPv4 完整地址当分组键，会让同一 CGNAT 出口下的真实用户彼此挤占。
func PrefixDigest(ip string) []byte {
	sum := sha256.Sum256([]byte(RiskSQLPrefix() + "prefix:" + NetPrefix(ip)))
	return sum[:]
}

// NetPrefix 返回用于聚合的网段文本：IPv4 /24、IPv6 /64。
// 非法或空地址原样返回（仍可作为稳定的聚合键）。
func NetPrefix(ip string) string {
	value := strings.TrimSpace(ip)
	parsed := net.ParseIP(value)
	if parsed == nil {
		return value
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
