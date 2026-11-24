package upload

import (
	"errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"
)

func TestReadValidatedImage(t *testing.T) {
	data := encodedPNG(t, 2, 2)
	file := multipartFile(t, "Shot.PNG", data)
	result, err := ReadValidatedImage(file, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "image/png" || result.Ext != ".png" || result.OriginalName != "Shot.PNG" {
		t.Fatalf("unexpected result %+v", result)
	}
	if int64(len(result.Data)) != result.SizeBytes || result.SizeBytes != int64(len(data)) {
		t.Fatalf("size mismatch %+v", result)
	}

	if _, err := ReadValidatedImage(multipartFile(t, "notes.txt", data), 1<<20); err == nil {
		t.Fatal("non-image extension accepted")
	}
	if _, err := ReadValidatedImage(multipartFile(t, "fake.png", []byte("not a png")), 1<<20); err == nil {
		t.Fatal("invalid image content accepted")
	}
	_, err = ReadValidatedImage(multipartFile(t, "large.png", data), int64(len(data)-1))
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != 413 {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestIsImageFilenameAndStoredName(t *testing.T) {
	for _, name := range []string{"a.png", "B.JPG", "c.jpeg", "d.webp", "e.gif"} {
		if !IsImageFilename(name) {
			t.Fatalf("%s should be an image", name)
		}
	}
	for _, name := range []string{"a.pdf", "b.zip", "c.txt", "noext"} {
		if IsImageFilename(name) {
			t.Fatalf("%s should not be an image", name)
		}
	}
	name, err := NewStoredName(".PNG")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, ".png") || !imageFileNamePattern.MatchString(name) {
		t.Fatalf("stored name %q", name)
	}
}
