package jsonrequest

import (
	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const DefaultMaxBodyBytes = 2 * 1024 * 1024

func BindHertz(c *hertzapp.RequestContext, target any, maxBytes int) error {
	if c == nil {
		return apperrors.New(consts.StatusBadRequest, "请求参数不合法")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxBytes {
		return apperrors.New(consts.StatusRequestEntityTooLarge, "JSON请求体过大")
	}
	body, err := c.Body()
	if err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "请求参数不合法", err)
	}
	if len(body) > maxBytes {
		return apperrors.New(consts.StatusRequestEntityTooLarge, "JSON请求体过大")
	}
	if len(body) == 0 {
		return nil
	}
	if err := UnmarshalUnambiguous(body, target, DefaultMaxDepth); err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "请求参数不合法", err)
	}
	return nil
}
