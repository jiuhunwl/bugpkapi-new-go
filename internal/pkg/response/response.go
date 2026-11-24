package response

import (
	"crypto/rand"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/masking"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

var responseLogger atomic.Pointer[zap.Logger]

func init() {
	responseLogger.Store(zap.NewNop())
}

type Body struct {
	Code    int     `json:"code"`
	Data    any     `json:"data"`
	Error   *string `json:"error"`
	Message string  `json:"message"`
}

// serverErrorBody 是 5xx 统一错误响应体：只暴露 code/message/error_id，
// 绝不包含 upstream URL / 内部接口路径 / 内部 token 等敏感信息。
type serverErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	ErrorID string `json:"error_id"`
}

const serverUnavailableMessage = "服务暂时不可用"

func OK(c *hertzapp.RequestContext, data any) {
	c.JSON(consts.StatusOK, Body{Code: 0, Data: data, Error: nil, Message: "ok"})
}

// Fail 用于业务 4xx 错误：保留用户友好的业务 message。
func Fail(c *hertzapp.RequestContext, statusCode int, message string) {
	FailWithData(c, statusCode, message, nil)
}

func FailWithData(c *hertzapp.RequestContext, statusCode int, message string, data any) {
	message = masking.ErrorText(message)
	errorMessage := message
	c.JSON(statusCode, Body{Code: -1, Data: data, Error: &errorMessage, Message: message})
}

func SetLogger(log *zap.Logger) {
	if log == nil {
		log = zap.NewNop()
	}
	responseLogger.Store(log)
}

// FromError 统一错误出口：
//   - 5xx（500/502/503/504）：只返回 code/message/error_id，内部细节仅写日志；
//   - 4xx 业务错误：保留业务 message。
func FromError(c *hertzapp.RequestContext, err error) {
	var business *apperrors.BusinessError
	if errors.As(err, &business) {
		if business.StatusCode >= consts.StatusInternalServerError {
			FailServerError(c, business.StatusCode, business.Err)
			return
		}
		applyMetadataHeaders(c, business.Data)
		FailWithData(c, business.StatusCode, business.Message, business.Data)
		return
	}
	FailServerError(c, consts.StatusInternalServerError, err)
}

type responseHeaderProvider interface {
	ResponseHeaders() map[string]string
}

func applyMetadataHeaders(c *hertzapp.RequestContext, data any) {
	provider, ok := data.(responseHeaderProvider)
	if !ok {
		return
	}
	for name, value := range provider.ResponseHeaders() {
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" || strings.ContainsAny(name+value, "\r\n") {
			continue
		}
		c.Header(name, value)
	}
}

// FailServerError 返回统一 5xx 响应并记录完整内部细节（含 error_id）。
func FailServerError(c *hertzapp.RequestContext, statusCode int, err error) {
	FailServerErrorID(c, statusCode, NewErrorID(), err)
}

// FailServerErrorID 使用调用方已记录的 error_id 返回统一 5xx 响应；
// err 为 nil 时不再重复记录日志（调用方已自行记录详细日志）。
func FailServerErrorID(c *hertzapp.RequestContext, statusCode int, errorID string, err error) {
	if errorID == "" {
		errorID = NewErrorID()
	}
	if err != nil {
		logRequestError(c, statusCode, errorID, err)
	}
	c.JSON(statusCode, serverErrorBody{Code: statusCode, Message: serverUnavailableMessage, ErrorID: errorID})
}

// NewErrorID 生成用于追踪 5xx 错误的随机标识（UUID v4 风格）。
func NewErrorID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("err-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func logRequestError(c *hertzapp.RequestContext, statusCode int, errorID string, err error) {
	if err == nil {
		return
	}
	stack := masking.StackText(string(debug.Stack()))
	responseLogger.Load().Error(serverUnavailableMessage,
		zap.String("error_id", errorID),
		zap.String("request_id", string(c.Response.Header.Peek("X-Request-ID"))),
		zap.Int("status", statusCode),
		zap.String("method", string(c.Method())),
		zap.String("path", string(c.Path())),
		zap.String("client_ip", masking.IP(c.ClientIP())),
		zap.String("error", masking.Error(err)),
		zap.String("stack", stack),
	)
}
