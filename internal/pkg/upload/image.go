package upload

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "golang.org/x/image/webp"
)

const (
	MaxImageDimension = 8192
	MaxImagePixels    = 16_777_216
	MaxImageSize      = 2 * 1024 * 1024
	MaxStorageBytes   = int64(4 * 1024 * 1024 * 1024)
	MaxStorageFiles   = int64(100_000)
	nameAttempts      = 3
)

var (
	allowedImageExt = map[string]string{
		".gif":  "gif",
		".jpeg": "jpeg",
		".jpg":  "jpeg",
		".png":  "png",
		".webp": "webp",
	}
	imageFileNamePattern = regexp.MustCompile(`^[a-f0-9]{32}\.(?:gif|jpe?g|png|webp)$`)
	storageLocksMu       sync.Mutex
	storageLocks         = make(map[string]*uploadStorageLock)
)

type uploadStorageLock struct {
	mu    sync.Mutex
	users int
}

// uploadStorageLockKey normalizes a storage root into its registry key.
//
// Every reader of storageLocks must go through this, including tests that
// observe lock counts: the Windows case-folding below means a raw
// storageLocks[root] lookup misses the entry and yields a nil *uploadStorageLock.
func uploadStorageLockKey(root string) string {
	key := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

// uploadStorageLockUsers reports how many holders currently share a storage
// root's quota lock. It is the supported way to observe the registry without
// duplicating the key normalization or the mutex discipline.
func uploadStorageLockUsers(root string) int {
	storageLocksMu.Lock()
	defer storageLocksMu.Unlock()
	lock := storageLocks[uploadStorageLockKey(root)]
	if lock == nil {
		return 0
	}
	return lock.users
}

// Lock the shared quota root, including all of its upload categories. Separate
// roots can proceed independently; remove idle locks to avoid retaining paths.
func lockUploadStorage(root string) func() {
	key := uploadStorageLockKey(root)
	storageLocksMu.Lock()
	lock := storageLocks[key]
	if lock == nil {
		lock = &uploadStorageLock{}
		storageLocks[key] = lock
	}
	lock.users++
	storageLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		storageLocksMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(storageLocks, key)
		}
		storageLocksMu.Unlock()
	}
}

func SaveImage(file *multipart.FileHeader, dir, urlPrefix string) (map[string]string, error) {
	if file == nil || file.Size <= 0 || file.Size > MaxImageSize {
		return nil, apperrors.New(consts.StatusBadRequest, "image size must not exceed 2MB")
	}
	ext := strings.ToLower(filepath.Ext(file.Filename))
	wantFormat, ok := allowedImageExt[ext]
	if !ok {
		return nil, apperrors.New(consts.StatusBadRequest, "image only supports png/jpg/jpeg/webp/gif")
	}

	data, err := readImage(file)
	if err != nil {
		return nil, err
	}
	if err := validateImage(data, wantFormat); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := ensureSafeUploadDirectory(dir); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(uploadRoot(dir))
	if err != nil {
		return nil, err
	}
	unlock := lockUploadStorage(root)
	defer unlock()
	if err := checkStorageCapacity(root, int64(len(data)), MaxStorageBytes, MaxStorageFiles); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < nameAttempts; attempt++ {
		name, err := randomName()
		if err != nil {
			return nil, fmt.Errorf("generate image filename: %w", err)
		}
		filename := name + ext
		dstPath := filepath.Join(dir, filename)
		if err := writeExclusive(dstPath, data); err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, err
		}
		return map[string]string{"url": urlPrefix + filename}, nil
	}
	return nil, errors.New("failed to allocate a unique image filename")
}

func ResolveImagePath(dir, rawName string) (string, error) {
	filename := strings.ToLower(strings.TrimSpace(rawName))
	if !ValidGeneratedImageName(filename) {
		return "", apperrors.New(consts.StatusBadRequest, "文件名不合法")
	}
	if err := ensureSafeUploadDirectory(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", apperrors.New(consts.StatusNotFound, "文件不存在")
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", apperrors.New(consts.StatusNotFound, "文件不存在")
	}
	return path, nil
}

func ValidGeneratedImageName(filename string) bool {
	return imageFileNamePattern.MatchString(filename)
}

func uploadRoot(dir string) string {
	cleaned := filepath.Clean(dir)
	current := cleaned
	for {
		if strings.EqualFold(filepath.Base(current), "uploads") {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current || parent == "." {
			return cleaned
		}
		current = parent
	}
}

func ensureSafeUploadDirectory(dir string) error {
	cleaned := filepath.Clean(dir)
	root := uploadRoot(cleaned)
	relative, err := filepath.Rel(root, cleaned)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("upload directory escapes its managed root")
	}

	paths := []string{root}
	if relative != "." {
		current := root
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			paths = append(paths, current)
		}
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("upload directory contains a symbolic link or non-directory component")
		}
		// 兜底：Windows 目录联接（junction/reparse point）不会在 Mode 中标记
		// os.ModeSymlink，但 EvalSymlinks 会解析到真实目标；只要真实目标与
		// 当前路径不一致即拒绝，防止上传目录被符号链接带出托管根目录。
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if !sameManagedPath(resolved, path) {
			return errors.New("upload directory contains a symbolic link or non-directory component")
		}
	}
	return nil
}

func sameManagedPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func checkStorageCapacity(root string, incoming, maxBytes, maxFiles int64) error {
	capacityErr := apperrors.New(507, "upload storage capacity exceeded")
	if incoming < 0 || maxBytes <= 0 || maxFiles <= 0 || incoming > maxBytes {
		return capacityErr
	}
	var usedBytes int64
	var usedFiles int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		usedFiles++
		// Once this upload cannot fit, stop walking. No usage cache is kept:
		// deletions and externally added files remain visible on the next check.
		if usedFiles >= maxFiles || info.Size() > maxBytes-incoming-usedBytes {
			return capacityErr
		}
		usedBytes += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, capacityErr) {
			return capacityErr
		}
		return fmt.Errorf("inspect upload storage: %w", err)
	}
	return nil
}

func readImage(file *multipart.FileHeader) ([]byte, error) {
	src, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	data, err := io.ReadAll(io.LimitReader(src, MaxImageSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxImageSize {
		return nil, apperrors.New(consts.StatusBadRequest, "image size must not exceed 2MB")
	}
	return data, nil
}

func validateImage(data []byte, wantFormat string) (err error) {
	defer func() {
		if recover() != nil {
			err = apperrors.New(consts.StatusBadRequest, "image content is invalid")
		}
	}()
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != wantFormat {
		return apperrors.New(consts.StatusBadRequest, "image content does not match file extension")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > MaxImageDimension || config.Height > MaxImageDimension || int64(config.Width)*int64(config.Height) > MaxImagePixels {
		return apperrors.New(consts.StatusBadRequest, "image dimensions are invalid or too large")
	}
	return nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if written != len(data) {
		_ = os.Remove(path)
		return io.ErrShortWrite
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return closeErr
	}
	return nil
}

func randomName() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
