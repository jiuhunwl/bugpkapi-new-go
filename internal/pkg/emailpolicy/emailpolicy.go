// Package emailpolicy 提供邮箱域策略的统一实现。
//
// 该策略由后台「注册设置 → 邮箱域白/黑名单」维护（表 bp_email_domain_rule），
// 历史上只作用于注册链路，导致「绑定邮箱」等其它外发链路可以任意投递。
// 现在所有会真正发信的入口都走这里，保证同一条策略在
// 注册发码 / 注册提交 / 绑定邮箱发码 / 绑定提交 / 找回密码发码 上语义一致。
package emailpolicy

import "strings"

// Decision 是一次域策略判定的结果。
type Decision struct {
	// Domain 是邮箱的域名部分（小写）。无法解析域名时为空。
	Domain string
	// Allowed 表示该邮箱是否放行。
	Allowed bool
	// Rule 说明命中或拒绝的原因："blacklist" / "whitelist" / "unparsable"。
	Rule string
}

// Domain 取出邮箱域名（小写）。ok=false 表示地址里没有可用的域名部分。
func Domain(email string) (string, bool) {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return "", false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	if domain == "" || strings.ContainsAny(domain, " \t\r\n") {
		return "", false
	}
	return domain, true
}

// Matches 判断域名是否命中规则列表。
// `*.example.com` 只匹配子域（a.example.com），不匹配 example.com 本身。
func Matches(rules []string, domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	for _, rule := range rules {
		rule = strings.ToLower(strings.TrimSpace(rule))
		if rule == "" {
			continue
		}
		if strings.HasPrefix(rule, "*.") {
			suffix := rule[1:] // ".example.com"
			if strings.HasSuffix(domain, suffix) && len(domain) > len(suffix) {
				return true
			}
			continue
		}
		if domain == rule {
			return true
		}
	}
	return false
}

// Evaluate 按「黑名单优先、白名单非空则必须命中」的规则判定一个地址。
//
// strict=false 时只执行黑名单（用于找回密码这类收件人由系统决定的链路：
// 老账号可能绑定了一个后来才被加进白名单之外的域名，此处不应把它锁死，
// 但仍然要挡住明确拉黑的域名）。
func Evaluate(whitelist, blacklist []string, email string, strict bool) Decision {
	domain, ok := Domain(email)
	if !ok {
		return Decision{Allowed: false, Rule: "unparsable"}
	}
	if Matches(blacklist, domain) {
		return Decision{Domain: domain, Allowed: false, Rule: "blacklist"}
	}
	if strict && len(whitelist) > 0 && !Matches(whitelist, domain) {
		return Decision{Domain: domain, Allowed: false, Rule: "whitelist"}
	}
	return Decision{Domain: domain, Allowed: true}
}
