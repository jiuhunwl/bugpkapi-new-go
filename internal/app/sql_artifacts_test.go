package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqlArtifactRoot 返回后端模块根目录（internal/app 的上两级），
// 无论它位于"仓库根/backend"还是本身就是仓库根，路径都成立。
func sqlArtifactRoot() string {
	return filepath.Join("..", "..")
}

// readRawArtifact 读取模块根下的文件，保留原始大小写。
func readRawArtifact(t *testing.T, relative string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(sqlArtifactRoot(), relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return strings.ReplaceAll(string(contents), "\r\n", "\n")
}

// readSQLArtifact 读取模块根下的文件并转为小写，便于做片段断言。
func readSQLArtifact(t *testing.T, relative string) string {
	t.Helper()
	return strings.ToLower(readRawArtifact(t, relative))
}

// releaseVersion 返回 VERSION 中的语义版本号。
// 独立后端仓库里 VERSION 位于模块根；共享工作区里它位于模块根的上一级，两者都支持。
func releaseVersion(t *testing.T) string {
	t.Helper()
	root := sqlArtifactRoot()
	for _, relative := range []string{"VERSION", filepath.Join("..", "VERSION")} {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err == nil {
			return strings.TrimSpace(string(contents))
		}
	}
	t.Fatalf("VERSION not found under %s or its parent", root)
	return ""
}

// distributionSQLPath 定位 sql/full 下与当前 VERSION 绑定的全量安装包。
func distributionSQLPath(t *testing.T, kind string) string {
	t.Helper()
	prefix, ok := map[string]string{
		"full": "bugpk_api_full_v",
	}[kind]
	if !ok {
		t.Fatalf("unknown SQL artifact kind %q", kind)
	}
	return filepath.Join("sql", kind, prefix+releaseVersion(t)+".sql")
}

// readDistributionSQL 读取与当前 VERSION 绑定的分发 SQL（小写）。
func readDistributionSQL(t *testing.T, kind string) string {
	t.Helper()
	return readSQLArtifact(t, distributionSQLPath(t, kind))
}

// assertContainsAll 断言 sql 中包含全部片段。
func assertContainsAll(t *testing.T, label string, sql string, fragments []string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(sql, strings.ToLower(fragment)) {
			t.Fatalf("%s missing %q", label, fragment)
		}
	}
}
