package upload

import (
	"mime/multipart"
	"path/filepath"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type ValidatedImage struct {
	ContentType  string
	Data         []byte
	Ext          string
	OriginalName string
	SizeBytes    int64
}

func IsImageFilename(name string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	_, ok := allowedImageExt[ext]
	return ok
}

// ReadValidatedImage reads and fully validates an image before it is handed to
// an external image host. maxBytes is capped by the same library ceiling used
// for ticket attachments.
func ReadValidatedImage(file *multipart.FileHeader, maxBytes int64) (ValidatedImage, error) {
	if maxBytes <= 0 {
		return ValidatedImage{}, apperrors.New(consts.StatusServiceUnavailable, "image uploads are unavailable")
	}
	if maxBytes > MaxAttachmentSize {
		maxBytes = MaxAttachmentSize
	}
	if file == nil || file.Size <= 0 {
		return ValidatedImage{}, apperrors.New(consts.StatusBadRequest, "image must not be empty")
	}
	originalName, ext, err := normalizeAttachmentName(file.Filename)
	if err != nil {
		return ValidatedImage{}, err
	}
	if _, ok := allowedImageExt[ext]; !ok {
		return ValidatedImage{}, apperrors.New(consts.StatusBadRequest, "image only supports png/jpg/jpeg/webp/gif")
	}
	data, err := readAttachment(file, maxBytes)
	if err != nil {
		return ValidatedImage{}, err
	}
	contentType, err := validateAttachmentContent(data, ext)
	if err != nil {
		return ValidatedImage{}, err
	}
	return ValidatedImage{
		ContentType:  contentType,
		Data:         data,
		Ext:          ext,
		OriginalName: originalName,
		SizeBytes:    int64(len(data)),
	}, nil
}

func NewStoredName(ext string) (string, error) {
	name, err := randomName()
	if err != nil {
		return "", err
	}
	return name + strings.ToLower(ext), nil
}
