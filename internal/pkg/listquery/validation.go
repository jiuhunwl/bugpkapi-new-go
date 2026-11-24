package listquery

import (
	"fmt"
	"strings"
	"unicode/utf8"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/pagination"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const MaxKeywordRunes = 128

func Validate(keyword string, page, pageSize, maxPageSize int) error {
	if utf8.RuneCountInString(strings.TrimSpace(keyword)) > MaxKeywordRunes {
		return apperrors.New(consts.StatusBadRequest, fmt.Sprintf("keyword must not exceed %d characters", MaxKeywordRunes))
	}
	if page < 1 || page > pagination.MaxPage {
		return apperrors.New(consts.StatusBadRequest, fmt.Sprintf("page must be between 1 and %d", pagination.MaxPage))
	}
	if maxPageSize < 1 {
		maxPageSize = pagination.DefaultPageSize
	}
	if pageSize < 1 || pageSize > maxPageSize {
		return apperrors.New(consts.StatusBadRequest, fmt.Sprintf("pageSize must be between 1 and %d", maxPageSize))
	}
	return nil
}

func Binary(name string, value *int) error {
	if value != nil && *value != 0 && *value != 1 {
		return apperrors.New(consts.StatusBadRequest, name+" must be 0 or 1")
	}
	return nil
}

func Enum(name, value string, allowed ...string) error {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	for _, candidate := range allowed {
		if value == strings.ToUpper(strings.TrimSpace(candidate)) {
			return nil
		}
	}
	return apperrors.New(consts.StatusBadRequest, name+" is invalid")
}

func NonNegativeID(name string, value int64) error {
	if value < 0 {
		return apperrors.New(consts.StatusBadRequest, name+" must be a non-negative integer")
	}
	return nil
}
