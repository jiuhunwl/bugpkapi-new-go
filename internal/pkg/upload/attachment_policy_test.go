package upload

import (
	"bytes"
	"errors"
	"mime/multipart"
	"path/filepath"
	"reflect"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"
)

func TestAttachmentPolicyReservesMultipartHeadroom(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		body int
		want int64
	}{{4 * mib, 3 * mib}, {3 * mib, 2 * mib}, {11 * mib, 10 * mib}, {64 * mib, 10 * mib}, {4*mib + 65536, 4 * mib}, {65536, 0}, {0, 0}, {-1, 0}} {
		policy := AttachmentPolicyForBodyLimit(tc.body)
		if policy.MaxBytes != tc.want {
			t.Errorf("body=%d max=%d want=%d", tc.body, policy.MaxBytes, tc.want)
		}
		if policy.MaxBytes > 0 && policy.MaxBytes+65536 > int64(tc.body) {
			t.Errorf("file and multipart metadata cannot fit body limit %d", tc.body)
		}
	}
	first := AttachmentPolicyForBodyLimit(4 * mib)
	want := []string{".csv", ".gif", ".jpeg", ".jpg", ".json", ".log", ".pdf", ".png", ".txt", ".webp", ".zip"}
	if !reflect.DeepEqual(first.AllowedExtensions, want) {
		t.Fatalf("allowed types=%v", first.AllowedExtensions)
	}
	first.AllowedExtensions[0] = ".exe"
	if !reflect.DeepEqual(AttachmentPolicyForBodyLimit(4*mib).AllowedExtensions, want) {
		t.Fatal("caller mutated shared extension policy")
	}
}

func TestSaveAttachmentHonorsNegotiatedLimitAndActualBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		declared int64
		limit    int64
		pass     bool
	}{
		{name: "exact.txt", data: bytes.Repeat([]byte("a"), 32), declared: 32, limit: 32, pass: true},
		{name: "large.txt", data: bytes.Repeat([]byte("a"), 33), declared: 33, limit: 32},
		{name: "forged.txt", data: bytes.Repeat([]byte("a"), 33), declared: 1, limit: 32},
		{name: "disabled.txt", data: []byte("test"), declared: 4, limit: 0},
		{name: "negative.txt", data: []byte("test"), declared: 4, limit: -1},
		{name: "empty.txt", data: []byte{}, declared: 0, limit: 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := multipartFile(t, tc.name, tc.data)
			file.Size = tc.declared
			result, err := SaveAttachmentWithLimit(file, filepath.Join(t.TempDir(), "uploads", "attachments"), tc.limit)
			if tc.pass {
				if err != nil || result.SizeBytes != int64(len(tc.data)) {
					t.Fatalf("valid attachment rejected: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid/oversize attachment accepted")
			}
		})
	}
	if _, err := SaveAttachmentWithLimit((*multipart.FileHeader)(nil), t.TempDir(), 32); err == nil {
		t.Fatal("nil file accepted")
	}
	tooLarge := multipartFile(t, "large.txt", bytes.Repeat([]byte("a"), 33))
	_, err := SaveAttachmentWithLimit(tooLarge, t.TempDir(), 32)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != 413 {
		t.Fatalf("size violation must return 413, got %v", err)
	}
}
