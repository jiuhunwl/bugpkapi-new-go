package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFileCandidates 返回 .env 的候选路径：
//  1. 可执行文件同目录的 .env（生产平铺部署：二进制、config.yaml、.env 同目录）
//  2. 当前工作目录的 .env（开发 go run 场景兜底）
//
// 与 defaultConfigPath 的查找约定保持一致。
func envFileCandidates() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), ".env"))
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".env"))
	}
	return paths
}

// loadDotEnv 自动加载 .env（KEY=VALUE 格式，与二进制、config.yaml 同目录）到进程环境变量。
//
// 背景：start.sh 会先 source .env 再 exec 二进制；但宝塔「Go 项目」插件直接 exec 二进制时
// 进程环境变量为空，JWT_SECRET 等落回开发占位符，在生产环境下触发
// "JWT_SECRET must be explicitly configured for production" 校验失败。
// 本函数让二进制自身具备加载 .env 的能力，行为与 start.sh 一致，宝塔可直接填二进制路径启动。
//
// 规则：
//   - 已存在的环境变量不会被覆盖（外部显式配置优先，与 godotenv 默认行为一致）
//   - 支持 # 注释、空行、export 前缀、单引号/双引号/无引号值、CRLF 换行
//   - .env 存在但内容非法时返回错误，避免静默使用错误配置
func loadDotEnv() error {
	for _, path := range envFileCandidates() {
		loaded, err := loadDotEnvFile(path)
		if err != nil {
			return err
		}
		if loaded {
			return nil // 可执行文件同目录命中即停；CWD 兜底仅在前者缺失时生效
		}
	}
	return nil
}

// loadDotEnvFile 解析单个 .env 文件；文件不存在时返回 (false, nil)。
func loadDotEnvFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read env file %s: %w", path, err)
	}

	for lineNo, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r") // 兼容 CRLF 换行
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 可选 export 前缀（兼容 .env 里写 export KEY=value 的写法）
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false, fmt.Errorf("parse env file %s: line %d: missing '='", path, lineNo+1)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return false, fmt.Errorf("parse env file %s: line %d: empty key", path, lineNo+1)
		}
		value = parseEnvValue(strings.TrimSpace(value))
		if _, exists := os.LookupEnv(key); exists {
			continue // 已存在的环境变量优先，不覆盖
		}
		if err := os.Setenv(key, value); err != nil {
			return false, fmt.Errorf("set env %s from %s: %w", key, path, err)
		}
	}
	return true, nil
}

// parseEnvValue 解析 .env 的值：
//   - 双引号包裹：解析 \n \r \t \" \\ 等转义
//   - 单引号包裹：原样返回（不解析转义）
//   - 无引号：去除首尾空白，并截断 " #" 行内注释（# 前需有空白，避免误伤密码中的 #）
func parseEnvValue(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return unquoteEnvDouble(value[1 : len(value)-1])
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1]
	}
	if idx := strings.Index(value, " #"); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

// unquoteEnvDouble 还原双引号字符串中的常见转义序列。
func unquoteEnvDouble(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '\\' || i+1 >= len(s) {
			b.WriteByte(ch)
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\', '"':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
