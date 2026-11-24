package upload

import (
	"bytes"
	"crypto/rand"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaveImageAcceptsValidPNG(t *testing.T) {
	file := multipartFile(t, "avatar.png", encodedPNG(t, 1, 1))
	dir := t.TempDir()

	result, err := SaveImage(file, dir, "/system-api/upload/interface-avatar/")
	if err != nil {
		t.Fatalf("SaveImage returned error: %v", err)
	}
	url := result["url"]
	if !strings.HasPrefix(url, "/system-api/upload/interface-avatar/") || !strings.HasSuffix(url, ".png") {
		t.Fatalf("unexpected upload url: %q", url)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(url))); err != nil {
		t.Fatalf("expected saved file to exist: %v", err)
	}
}

func TestSaveImageRejectsFormatExtensionMismatch(t *testing.T) {
	file := multipartFile(t, "avatar.jpg", encodedPNG(t, 1, 1))
	if _, err := SaveImage(file, t.TempDir(), "/upload/"); err == nil {
		t.Fatal("expected PNG content with JPG extension to be rejected")
	}
}

func TestSaveImageRejectsExcessiveDimensions(t *testing.T) {
	file := multipartFile(t, "wide.png", encodedPNG(t, MaxImageDimension+1, 1))
	if _, err := SaveImage(file, t.TempDir(), "/upload/"); err == nil {
		t.Fatal("expected excessive image dimensions to be rejected")
	}
}

func TestSaveImageRejectsMismatchedContent(t *testing.T) {
	file := multipartFile(t, "avatar.png", []byte("not an image"))
	if _, err := SaveImage(file, t.TempDir(), "/upload/"); err == nil {
		t.Fatal("expected mismatched image content to be rejected")
	}
}

func TestValidateImageConvertsDecoderPanicToBadRequest(t *testing.T) {
	image.RegisterFormat(
		"panic-test",
		"PANIC",
		func(io.Reader) (image.Image, error) { return nil, errors.New("unused") },
		func(io.Reader) (image.Config, error) {
			var invalid []byte
			_ = invalid[0]
			return image.Config{}, nil
		},
	)
	if err := validateImage([]byte("PANIC"), "panic-test"); err == nil {
		t.Fatal("validateImage allowed a decoder panic to escape as success")
	}
}

func TestSaveImageRejectsUnsupportedExtension(t *testing.T) {
	file := multipartFile(t, "avatar.svg", []byte("<svg></svg>"))
	if _, err := SaveImage(file, t.TempDir(), "/upload/"); err == nil {
		t.Fatal("expected unsupported extension to be rejected")
	}
}

func TestResolveImagePathAcceptsGeneratedNameAndRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("a", 32) + ".png"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, encodedPNG(t, 1, 1), 0644); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveImagePath(dir, name)
	if err != nil || resolved != path {
		t.Fatalf("ResolveImagePath() = %q, %v", resolved, err)
	}
	for _, value := range []string{"..", "../" + name, "a.png", strings.Repeat("g", 32) + ".png"} {
		if _, err := ResolveImagePath(dir, value); err == nil {
			t.Fatalf("ResolveImagePath accepted %q", value)
		}
	}
}

func TestUploadRejectsSymbolicLinkDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "uploads")
	target := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "interface-avatar")
	if err := os.Symlink(target, linkedDir); err != nil {
		t.Skipf("symbolic links are unavailable in this environment: %v", err)
	}
	// 某些沙箱/虚拟文件系统会把 os.Symlink 物化为“独立目录”：Lstat 不标记
	// os.ModeSymlink、EvalSymlinks 不解析到目标、写入也不落向目标（既非符号链接
	// 也非联接）。此时 Go 文件 API 无法把该路径与普通目录区分开，符号链接安全
	// 属性在本环境不可验证，按与“符号链接不可用”相同的方式跳过。
	if resolved, evalErr := filepath.EvalSymlinks(linkedDir); evalErr == nil && resolved == linkedDir {
		if si, statErr := os.Stat(linkedDir); statErr == nil {
			if ti, targetErr := os.Stat(target); targetErr == nil && !os.SameFile(si, ti) {
				t.Skipf("filesystem materializes symlinks as standalone directories; symbolic-link safety is not verifiable here")
			}
		}
	}

	file := multipartFile(t, "avatar.png", encodedPNG(t, 1, 1))
	if _, err := SaveImage(file, linkedDir, "/system-api/upload/interface-avatar/"); err == nil {
		t.Fatal("SaveImage followed a symbolic upload directory")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symbolic upload directory wrote outside managed root: %v", entries)
	}

	name := strings.Repeat("a", 32) + ".png"
	if err := os.WriteFile(filepath.Join(target, name), encodedPNG(t, 1, 1), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveImagePath(linkedDir, name); err == nil {
		t.Fatal("ResolveImagePath served through a symbolic upload directory")
	}
}

func TestValidGeneratedImageNameIsStrict(t *testing.T) {
	valid := strings.Repeat("a", 32) + ".webp"
	if !ValidGeneratedImageName(valid) {
		t.Fatalf("generated image name rejected: %q", valid)
	}
	for _, value := range []string{
		" " + valid,
		strings.ToUpper(valid),
		"../" + valid,
		valid + ".html",
		strings.Repeat("a", 31) + ".png",
	} {
		if ValidGeneratedImageName(value) {
			t.Fatalf("invalid generated image name accepted: %q", value)
		}
	}
}

func TestCheckStorageCapacityEnforcesByteAndFileLimits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.bin"), []byte("1234"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkStorageCapacity(dir, 2, 6, 2); err != nil {
		t.Fatalf("capacity rejected an exact fit: %v", err)
	}
	if err := checkStorageCapacity(dir, 3, 6, 2); err == nil {
		t.Fatal("capacity accepted bytes beyond the limit")
	}
	if err := os.WriteFile(filepath.Join(dir, "two.bin"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkStorageCapacity(dir, 1, 100, 2); err == nil {
		t.Fatal("capacity accepted a file beyond the count limit")
	}
}

func TestCheckStorageCapacitySeesExternalChangesAndDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "external.bin")
	if err := checkStorageCapacity(dir, 1, 5, 2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkStorageCapacity(dir, 1, 5, 2); err == nil {
		t.Fatal("external file was not included in capacity")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := checkStorageCapacity(dir, 1, 5, 2); err != nil {
		t.Fatalf("deleted file still occupies capacity: %v", err)
	}
}

func TestUploadStorageLockSerializesOneRootAndIsolatesOthers(t *testing.T) {
	root := t.TempDir()
	otherRoot := t.TempDir()
	unlock := lockUploadStorage(root)
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()

	sameRoot := make(chan struct{})
	go func() {
		unlock := lockUploadStorage(filepath.Join(root, "unused", ".."))
		defer unlock()
		close(sameRoot)
	}()
	other := make(chan struct{})
	go func() {
		unlock := lockUploadStorage(otherRoot)
		defer unlock()
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("independent storage root was blocked")
	}
	select {
	case <-sameRoot:
		t.Fatal("same storage root bypassed the active capacity/write lock")
	default:
	}
	release()
	select {
	case <-sameRoot:
	case <-time.After(time.Second):
		t.Fatal("waiting upload was not released")
	}
}

func TestImagesAndAttachmentsShareTheStorageRootLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uploads")
	image := multipartFile(t, "image.png", encodedPNG(t, 1, 1))
	attachment := multipartFile(t, "notes.txt", []byte("test attachment"))
	unlock := lockUploadStorage(root)
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	finished := make(chan error, 2)
	go func() {
		_, err := SaveImage(image, filepath.Join(root, "images"), "/upload/")
		finished <- err
	}()
	go func() {
		_, err := SaveAttachment(attachment, filepath.Join(root, "attachments"))
		finished <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if uploadStorageLockUsers(root) == 3 {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("upload completed before the shared lock was released: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("both upload categories did not wait on their shared quota root")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	for i := 0; i < 2; i++ {
		if err := <-finished; err != nil {
			t.Fatalf("upload failed after releasing the shared lock: %v", err)
		}
	}
}

func TestUploadRootUsesSharedUploadsDirectory(t *testing.T) {
	want := filepath.Join("var", "app", "uploads")
	got := uploadRoot(filepath.Join(want, "notice-image"))
	if got != want {
		t.Fatalf("uploadRoot=%q, want %q", got, want)
	}
	standalone := t.TempDir()
	if got := uploadRoot(standalone); got != filepath.Clean(standalone) {
		t.Fatalf("standalone uploadRoot=%q", got)
	}
}

func TestRandomNamePropagatesEntropyFailure(t *testing.T) {
	original := rand.Reader
	rand.Reader = uploadErrorReader{}
	t.Cleanup(func() { rand.Reader = original })
	if _, err := randomName(); err == nil {
		t.Fatal("randomName ignored entropy failure")
	}
}

type uploadErrorReader struct{}

func (uploadErrorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

func encodedPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("png.Encode failed: %v", err)
	}
	return buffer.Bytes()
}

var _ io.Reader = uploadErrorReader{}

func multipartFile(t *testing.T, filename string, data []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("part.Write failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close failed: %v", err)
	}
	reader := multipart.NewReader(&body, writer.Boundary())
	form, err := reader.ReadForm(MaxImageSize)
	if err != nil {
		t.Fatalf("ReadForm failed: %v", err)
	}
	files := form.File["file"]
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	return files[0]
}
