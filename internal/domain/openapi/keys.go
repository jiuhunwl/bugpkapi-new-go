package openapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// newKeySecret 生成明文开放 Key：opk_/opka_ + 64 位十六进制（32 字节熵）。
// 泄露样本里前缀一眼可辨危险等级（管理员作用域 opka_）。
func newKeySecret(adminScope bool) (string, error) {
	raw := make([]byte, keySecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	marker := KeyMarkerNormal
	if adminScope {
		marker = KeyMarkerAdmin
	}
	return marker + hex.EncodeToString(raw), nil
}

// hashKey 计算存储用摘要（SHA-256 十六进制，与 key_hash char(64) 对齐）。
// 明文不落库、不进日志。
func hashKey(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return hex.EncodeToString(sum[:])
}

// keyPrefixOf / keySuffixOf 取明文标识段（掩码展示用，不含前缀标记）。
func keyPrefixOf(secret string) string {
	body := keyBody(secret)
	if len(body) < 4 {
		return body
	}
	return body[:4]
}

func keySuffixOf(secret string) string {
	body := keyBody(secret)
	if len(body) < 4 {
		return body
	}
	return body[len(body)-4:]
}

// keyBody 去掉前缀标记，返回标识本体。
func keyBody(secret string) string {
	secret = strings.TrimSpace(secret)
	for _, marker := range []string{KeyMarkerAdmin, KeyMarkerNormal} {
		if strings.HasPrefix(secret, marker) {
			return secret[len(marker):]
		}
	}
	return secret
}

// maskKey 生成掩码展示：保留前缀标记 + 前 4 后 4，中段以 **** 代替。
// 管理员作用域恒带 ⚠️ 徽标由前端/列表字段决定，掩码本身保留 opka_ 标记。
func maskKey(secret string) string {
	secret = strings.TrimSpace(secret)
	marker := KeyMarkerNormal
	if strings.HasPrefix(secret, KeyMarkerAdmin) {
		marker = KeyMarkerAdmin
	}
	body := keyBody(secret)
	if len(body) < 8 {
		return marker + "****"
	}
	return marker + body[:4] + "****" + body[len(body)-4:]
}

// normalizeScopes 去重、去空白、校验合法端点 code 并按目录顺序返回。
// 未知名直接拒绝（防前端手拼路径污染 scope 清单）。
func normalizeScopes(scopes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(scopes))
	for _, raw := range scopes {
		code := strings.TrimSpace(raw)
		if code == "" {
			continue
		}
		if !IsEndpointCode(code) {
			return nil, fmt.Errorf("unknown endpoint code %q", code)
		}
		seen[code] = struct{}{}
	}
	ordered := make([]string, 0, len(seen))
	for _, code := range EndpointCodes {
		if _, ok := seen[code]; ok {
			ordered = append(ordered, code)
		}
	}
	return ordered, nil
}

// hasAdminScope 判定 scope 清单是否含管理端点（is_admin_scope 由它派生）。
func hasAdminScope(scopes []string) bool {
	for _, code := range scopes {
		if code == EndpointRedeemCodeGenerate {
			return true
		}
	}
	return false
}

// normalizeIPRule 归一化单条白名单规则（单 IP 或 CIDR）。
// 拒绝全网段（0.0.0.0/0 与 ::/0）——等于不限却造成「已设防」错觉。
func normalizeIPRule(raw string) (string, error) {
	rule := strings.TrimSpace(raw)
	if rule == "" {
		return "", fmt.Errorf("empty ip rule")
	}
	if strings.Contains(rule, "/") {
		ip, network, err := net.ParseCIDR(rule)
		if err != nil {
			return "", fmt.Errorf("invalid cidr %q", rule)
		}
		ones, bits := network.Mask.Size()
		if ones == 0 {
			return "", fmt.Errorf("cidr %q must not cover the whole address family", rule)
		}
		if ip.To4() != nil {
			return (&net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(ones, bits)}).String(), nil
		}
		return network.String(), nil
	}
	ip := net.ParseIP(rule)
	if ip == nil {
		return "", fmt.Errorf("invalid ip %q", rule)
	}
	return normalizeIPText(ip), nil
}

// normalizeIPText 归一化 IP 文本：IPv4-mapped IPv6（::ffff:a.b.c.d）折叠为
// IPv4 表示，避免同一地址两种写法导致白名单比对失配/绕过。
func normalizeIPText(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// ipAllowed 判断客户端 IP 是否命中白名单（空清单=不限，仅普通 Key 成立）。
func ipAllowed(rules []string, clientIP string) bool {
	if len(rules) == 0 {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(clientIP))
	if ip == nil {
		return false
	}
	ipText := normalizeIPText(ip)
	for _, rule := range rules {
		if strings.Contains(rule, "/") {
			_, network, err := net.ParseCIDR(rule)
			if err != nil {
				continue
			}
			if network.Contains(ip) {
				return true
			}
			continue
		}
		if normalizeIPText(net.ParseIP(rule)) == ipText {
			return true
		}
	}
	return false
}
