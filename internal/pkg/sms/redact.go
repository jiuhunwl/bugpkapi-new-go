package sms

import (
	"regexp"
	"sort"
	"strings"
)

// secretKeys 是配置与请求里可能出现的密钥字段名，用于兜底擦除上游回显。
var secretKeys = []string{
	"accessKeySecret", "accessKeyId", "secretId", "secretKey",
	"Signature", "SignatureNonce", "Authorization", "password",
}

var (
	// jsonSecretPattern 命中 "Signature":"xxxx" 形态；querySecretPattern 命中 Signature=xxxx 形态。
	// 上游报错经常把请求参数原样回显，签名与 AK/SK 就在其中。这里按字段名覆盖值，
	// 让"错误串可以直接进接口日志和告警"成为通道层的保证，而不是依赖每个调用方记得脱敏。
	jsonSecretPattern  = buildSecretPattern(`("%s"\s*:\s*")[^"]*(")`, "$1***$2")
	querySecretPattern = buildSecretPattern(`(%s\s*=\s*)[^&\s"']*`, "$1***")
)

func buildSecretPattern(format, replacement string) *regexp.Regexp {
	quoted := make([]string, 0, len(secretKeys))
	for _, key := range secretKeys {
		quoted = append(quoted, regexp.QuoteMeta(key))
	}
	return regexp.MustCompile("(?i)" + sprintf(format, strings.Join(quoted, "|")))
}

// sprintf 只为把 alternation 注入正则，避免手写两遍字段名。
func sprintf(format, alternatives string) string {
	return strings.Replace(format, "%s", alternatives, 1)
}

func redactSecret(value string) string {
	if value == "" {
		return value
	}
	value = jsonSecretPattern.ReplaceAllString(value, "$1***$2")
	return querySecretPattern.ReplaceAllString(value, "$1***")
}

// sortedParamKeys 让规范化查询串稳定可测。
func sortedParamKeys(params map[string]string) []string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
