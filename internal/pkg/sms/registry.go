package sms

import (
	"fmt"
	"sort"
	"strings"
)

type factory func(Config) (Provider, error)

// factories 是通道注册表。新增服务商只需要在这里加一行 + 一个 adapter 文件，
// 与验证码 Provider（pow/geetest4/cloudflare）保持同一套扩展方式。
var factories = map[string]factory{
	ProviderAliyun:  newAliyun,
	ProviderTencent: newTencent,
}

// New 按 cfg.ProviderKey 构造通道。配置不全时返回错误，调用方必须据此保持
// fail-closed：不允许"先发一条试试"，也不允许降级到未配置的通道。
func New(cfg Config) (Provider, error) {
	normalized := cfg
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	key := strings.ToLower(normalized.ProviderKey)
	build, ok := factories[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotImplemented, key)
	}
	return build(normalized)
}

// Implemented 返回已接入的 provider_key（升序），供管理端下拉与
// PublicRegisterConfig 一类的能力广播使用。
func Implemented() []string {
	keys := make([]string, 0, len(factories))
	for key := range factories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Supports 报告某个 provider_key 是否已实现。
func Supports(providerKey string) bool {
	_, ok := factories[strings.ToLower(strings.TrimSpace(providerKey))]
	return ok
}
