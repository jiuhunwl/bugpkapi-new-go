package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupOrphanUploadsKeepsReferencesAndRecentFiles(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	referenced := strings.Repeat("a", 32) + ".png"
	orphan := strings.Repeat("b", 32) + ".jpg"
	recent := strings.Repeat("c", 32) + ".webp"
	ignored := "manual-file.txt"
	for _, name := range []string{referenced, orphan, recent, ignored} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if name != recent && os.Chtimes(path, old, old) != nil {
			t.Fatal("set old file time")
		}
	}
	removed, err := cleanupOrphanUploads(root, map[string]struct{}{referenced: {}}, time.Now().Add(-24*time.Hour), 100)
	if err != nil || removed != 1 {
		t.Fatalf("cleanup removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, orphan)); !os.IsNotExist(err) {
		t.Fatal("old orphan was not removed")
	}
	for _, name := range []string{referenced, recent, ignored} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("expected %s to remain: %v", name, err)
		}
	}
}

func TestCleanupOrphanUploadsHonorsBatchLimit(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for index := 0; index < 3; index++ {
		name := strings.Repeat(string(rune('d'+index)), 32) + ".png"
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := cleanupOrphanUploads(root, nil, time.Now().Add(-24*time.Hour), 2)
	if err != nil || removed != 2 {
		t.Fatalf("cleanup removed=%d err=%v", removed, err)
	}
}

func TestCollectUploadReferencesFindsDonationQRCodeInJSON(t *testing.T) {
	references := make(map[string]struct{})
	name := strings.Repeat("a", 32) + ".PNG"
	collectUploadReferences(references, `[{"key":"alipay","qrCodeUrl":"/system-api/upload/donation-qr/`+name+`"}]`)
	if _, ok := references[strings.ToLower(name)]; !ok {
		t.Fatalf("donation QR-code reference was not collected: %#v", references)
	}
}
