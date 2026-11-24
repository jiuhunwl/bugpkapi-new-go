package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 分散在各迁移里的功能测试覆盖单条迁移；本契约测试防止发布时
// "新二进制 + 过期或缺失的初始化 SQL"被打包在一起。
func TestSQLArtifactsMatchCurrentMigrations(t *testing.T) {
	root := sqlArtifactRoot()
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(migrations)
	if len(migrations) == 0 {
		t.Fatal("no migrations")
	}

	fullPath := distributionSQLPath(t, "full")
	fullRaw := readRawArtifact(t, fullPath)

	for index, file := range migrations {
		if !strings.HasPrefix(filepath.Base(file), fmt.Sprintf("%06d_", index+1)) {
			t.Fatalf("noncontiguous migration %s", file)
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		expected := strings.TrimSpace(strings.ReplaceAll(string(body), "\r\n", "\n"))
		if expected == "" || !strings.Contains(fullRaw, expected) {
			t.Fatalf("full SQL does not include exact current migration %s", file)
		}
	}

	if !strings.Contains(fullRaw, fmt.Sprintf("-- Generated from migrations 000001 through %06d.", len(migrations))) {
		t.Fatalf("%s has stale migration header", fullPath)
	}

	full := strings.ToLower(fullRaw)
	marker := regexp.MustCompile(`(?i)INSERT INTO \x60schema_migrations\x60 \(\x60version\x60, \x60dirty\x60\) VALUES \(([0-9]+), 0\);`)
	matches := marker.FindAllStringSubmatch(fullRaw, -1)
	if len(matches) != 1 || matches[0][1] != fmt.Sprint(len(migrations)) {
		t.Fatalf("full SQL must mark exactly the latest schema, got %v", matches)
	}

	guard := strings.Index(full, "execute bugpk_init_guard_stmt;")
	if guard < 0 || guard > strings.Index(full, "create table") || !strings.Contains(full, "__bugpk_init_requires_empty_selected_database__") {
		t.Fatalf("full SQL lacks the pre-mutation empty-database guard")
	}

	// 全量包的两条不变量：不得携带用户种子，也不得自选数据库。
	// 只拦截 VALUES 形式的种子；迁移里对已有用户回填角色的 INSERT ... SELECT 属于正常数据修复。
	seed := regexp.MustCompile(`(?ims)^INSERT(?: IGNORE)? INTO \x60bp_user(?:_role)?\x60\s*\([^)]*\)\s*VALUES`)
	if seed.MatchString(fullRaw) {
		t.Fatal("full SQL must not seed bp_user/bp_user_role; INITIAL_ADMIN_PASSWORD bootstraps the administrator")
	}
	databaseSelection := regexp.MustCompile(`(?im)^\s*(CREATE DATABASE|USE\s)`)
	if databaseSelection.MatchString(fullRaw) {
		t.Fatal("full SQL must not create or switch databases; the operator selects an empty database")
	}
}