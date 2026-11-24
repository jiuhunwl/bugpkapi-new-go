package main

import (
	"strings"
	"testing"
)

func TestDatabaseBackupStderrMustBeMasked(t *testing.T) {
	t.Parallel()
	input := "mysqldump: Got error: connection to https://db.example.internal:3306 failed password=super-secret"
	got := maskedCommandStderr(input)
	for _, forbidden := range []string{"db.example.internal", "3306", "super-secret", "https://"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("backup stderr leaked %q: %s", forbidden, got)
		}
	}
}
