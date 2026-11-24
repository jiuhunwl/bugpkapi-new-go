package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

var migrationFilenamePattern = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)

func TestMigrationFilesHaveUniqueVersionPairs(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}

	type migrationPair struct {
		down string
		name string
		up   string
	}
	pairs := make(map[uint64]migrationPair)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationFilenamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		version, err := strconv.ParseUint(matches[1], 10, 64)
		if err != nil {
			t.Fatalf("parse migration version from %q: %v", entry.Name(), err)
		}
		pair := pairs[version]
		if pair.name != "" && pair.name != matches[2] {
			t.Fatalf("migration version %06d has conflicting names %q and %q", version, pair.name, matches[2])
		}
		pair.name = matches[2]
		switch matches[3] {
		case "up":
			if pair.up != "" {
				t.Fatalf("migration version %06d has duplicate up files %q and %q", version, pair.up, entry.Name())
			}
			pair.up = entry.Name()
		case "down":
			if pair.down != "" {
				t.Fatalf("migration version %06d has duplicate down files %q and %q", version, pair.down, entry.Name())
			}
			pair.down = entry.Name()
		}
		pairs[version] = pair
	}

	for version, pair := range pairs {
		if pair.up == "" || pair.down == "" {
			t.Fatalf("migration version %06d must have one up and one down file: up=%q down=%q", version, pair.up, pair.down)
		}
	}
}
