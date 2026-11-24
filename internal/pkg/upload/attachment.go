package upload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const MaxAttachmentSize = 10 * 1024 * 1024

var (
	allowedAttachmentExt = map[string]struct{}{
		".csv": {}, ".gif": {}, ".jpeg": {}, ".jpg": {}, ".json": {},
		".log": {}, ".pdf": {}, ".png": {}, ".txt": {}, ".webp": {}, ".zip": {},
	}
	attachmentFileNamePattern = regexp.MustCompile(`^[a-f0-9]{32}\.(?:csv|gif|jpe?g|json|log|pdf|png|txt|webp|zip)$`)
)

type SavedAttachment struct {
	ContentType  string
	OriginalName string
	SizeBytes    int64
	StoredName   string
}

// SaveAttachment retains the library limit for non-HTTP callers. HTTP handlers
// use SaveAttachmentWithLimit with their advertised server-specific capability.
func SaveAttachment(file *multipart.FileHeader, dir string) (SavedAttachment, error) {
	return SaveAttachmentWithLimit(file, dir, MaxAttachmentSize)
}

func SaveAttachmentWithLimit(file *multipart.FileHeader, dir string, maxBytes int64) (SavedAttachment, error) {
	if maxBytes <= 0 {
		return SavedAttachment{}, apperrors.New(consts.StatusServiceUnavailable, "attachment uploads are unavailable")
	}
	if maxBytes > MaxAttachmentSize {
		maxBytes = MaxAttachmentSize
	}
	if file == nil || file.Size <= 0 {
		return SavedAttachment{}, apperrors.New(consts.StatusBadRequest, "attachment must not be empty")
	}
	if file.Size > maxBytes {
		return SavedAttachment{}, attachmentTooLarge(maxBytes)
	}
	originalName, ext, err := normalizeAttachmentName(file.Filename)
	if err != nil {
		return SavedAttachment{}, err
	}
	data, err := readAttachment(file, maxBytes)
	if err != nil {
		return SavedAttachment{}, err
	}
	contentType, err := validateAttachmentContent(data, ext)
	if err != nil {
		return SavedAttachment{}, err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return SavedAttachment{}, err
	}
	if err := ensureSafeUploadDirectory(dir); err != nil {
		return SavedAttachment{}, err
	}
	root, err := filepath.Abs(uploadRoot(dir))
	if err != nil {
		return SavedAttachment{}, err
	}
	unlock := lockUploadStorage(root)
	defer unlock()
	if err := checkStorageCapacity(root, int64(len(data)), MaxStorageBytes, MaxStorageFiles); err != nil {
		return SavedAttachment{}, err
	}
	for attempt := 0; attempt < nameAttempts; attempt++ {
		name, err := randomName()
		if err != nil {
			return SavedAttachment{}, fmt.Errorf("generate attachment filename: %w", err)
		}
		storedName := name + ext
		if err := writeExclusive(filepath.Join(dir, storedName), data); err != nil {
			if os.IsExist(err) {
				continue
			}
			return SavedAttachment{}, err
		}
		return SavedAttachment{
			ContentType:  contentType,
			OriginalName: originalName,
			SizeBytes:    int64(len(data)),
			StoredName:   storedName,
		}, nil
	}
	return SavedAttachment{}, errors.New("failed to allocate a unique attachment filename")
}

func ResolveAttachmentPath(dir, rawName string) (string, error) {
	filename := strings.ToLower(strings.TrimSpace(rawName))
	if !attachmentFileNamePattern.MatchString(filename) {
		return "", apperrors.New(consts.StatusBadRequest, "attachment filename is invalid")
	}
	if err := ensureSafeUploadDirectory(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", apperrors.New(consts.StatusNotFound, "attachment does not exist")
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", apperrors.New(consts.StatusNotFound, "attachment does not exist")
	}
	return path, nil
}

func RemoveAttachment(dir, rawName string) error {
	path, err := ResolveAttachmentPath(dir, rawName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func normalizeAttachmentName(value string) (string, string, error) {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(value, "\\", "/")))
	if name == "" || name == "." || strings.ContainsAny(name, "\r\n\x00") || utf8.RuneCountInString(name) > 180 {
		return "", "", apperrors.New(consts.StatusBadRequest, "attachment filename is invalid")
	}
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := allowedAttachmentExt[ext]; !ok {
		return "", "", apperrors.New(consts.StatusBadRequest, "attachment type is not supported")
	}
	return name, ext, nil
}

func readAttachment(file *multipart.FileHeader, maxBytes int64) ([]byte, error) {
	src, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, apperrors.New(consts.StatusBadRequest, "attachment must not be empty")
	}
	if int64(len(data)) > maxBytes {
		return nil, attachmentTooLarge(maxBytes)
	}
	return data, nil
}

func validateAttachmentContent(data []byte, ext string) (string, error) {
	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	switch ext {
	case ".gif":
		if err := validateImage(data, "gif"); err != nil {
			return "", err
		}
		return "image/gif", nil
	case ".jpeg", ".jpg":
		if err := validateImage(data, "jpeg"); err != nil {
			return "", err
		}
		return "image/jpeg", nil
	case ".png":
		if err := validateImage(data, "png"); err != nil {
			return "", err
		}
		return "image/png", nil
	case ".webp":
		if err := validateImage(data, "webp"); err != nil {
			return "", err
		}
		return "image/webp", nil
	case ".pdf":
		if detected != "application/pdf" || !bytes.HasPrefix(data, []byte("%PDF-")) {
			return "", apperrors.New(consts.StatusBadRequest, "attachment content does not match file extension")
		}
		return "application/pdf", nil
	case ".zip":
		if !(bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06"))) {
			return "", apperrors.New(consts.StatusBadRequest, "attachment content does not match file extension")
		}
		return "application/zip", nil
	case ".json":
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') || !jsonLike(trimmed) {
			return "", apperrors.New(consts.StatusBadRequest, "attachment content does not match file extension")
		}
		return "application/json", nil
	case ".csv", ".log", ".txt":
		if detected == "text/html; charset=utf-8" || strings.Contains(detected, "javascript") || strings.ContainsRune(string(data), '\x00') {
			return "", apperrors.New(consts.StatusBadRequest, "attachment content does not match file extension")
		}
		return "text/plain; charset=utf-8", nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "attachment type is not supported")
	}
}

func jsonLike(data []byte) bool {
	var value any
	return json.Unmarshal(data, &value) == nil
}

func attachmentTooLarge(maxBytes int64) error {
	return apperrors.New(consts.StatusRequestEntityTooLarge, fmt.Sprintf("attachment size must not exceed %d bytes", maxBytes))
}
