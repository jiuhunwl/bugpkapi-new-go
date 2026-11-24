package observability

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	callLogCursorSeparator = "|"
	callLogCursorVersion   = "1"
	callLogTimeLayout      = "2006-01-02 15:04:05"
)

// errInvalidCallLogCursor 表示游标令牌损坏；handler 层将其映射为 400。
var errInvalidCallLogCursor = apperrors.New(consts.StatusBadRequest, "cursor is invalid")

// callLogCursor 定位 (created_at, id) 倒序游标的上一页末尾位置。
type callLogCursor struct {
	CreatedAt string
	ID        int64
}

func encodeCallLogCursor(cursor callLogCursor) string {
	raw := strings.Join([]string{callLogCursorVersion, cursor.CreatedAt, strconv.FormatInt(cursor.ID, 10)}, callLogCursorSeparator)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCallLogCursor 解析游标令牌；空令牌返回零值（首页）。
func decodeCallLogCursor(token string) (callLogCursor, error) {
	if strings.TrimSpace(token) == "" {
		return callLogCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return callLogCursor{}, errInvalidCallLogCursor
	}
	parts := strings.Split(string(raw), callLogCursorSeparator)
	if len(parts) != 3 || parts[0] != callLogCursorVersion {
		return callLogCursor{}, errInvalidCallLogCursor
	}
	if _, err := time.ParseInLocation(callLogTimeLayout, parts[1], businessclock.Location()); err != nil {
		return callLogCursor{}, errInvalidCallLogCursor
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return callLogCursor{}, errInvalidCallLogCursor
	}
	return callLogCursor{CreatedAt: parts[1], ID: id}, nil
}
