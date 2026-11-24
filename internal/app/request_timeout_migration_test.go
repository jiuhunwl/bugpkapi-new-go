package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestTimeoutMigrationContract(t *testing.T) {
	repoRoot := sqlArtifactRoot()
	read := func(path string) string {
		contents, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return strings.ToLower(string(contents))
	}

	up := read(filepath.Join("migrations", "000091_interface_request_timeout.up.sql"))
	for _, fragment := range []string{
		"add column `request_timeout_ms` int not null default 0",
		"add column `request_timeout_override` tinyint not null default 0",
		"add column `request_timeout_ms` int not null default 15000",
		"on duplicate key update `id` = values(`id`)",
	} {
		if !strings.Contains(up, fragment) {
			t.Fatalf("request-timeout up migration missing %q", fragment)
		}
	}

	down := read(filepath.Join("migrations", "000091_interface_request_timeout.down.sql"))
	for _, fragment := range []string{
		"drop column `request_timeout_ms`",
		"drop column `request_timeout_override`",
	} {
		if !strings.Contains(down, fragment) {
			t.Fatalf("request-timeout down migration missing %q", fragment)
		}
	}

	// 全量包必须与当前 VERSION / 迁移总数绑定。
	full := readDistributionSQL(t, "full")
	assertContainsAll(t, "full SQL", full, []string{"request_timeout_ms"})
}
