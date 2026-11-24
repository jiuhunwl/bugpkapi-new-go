package interfaceapi

import (
	"context"
	"encoding/json"
	"testing"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

func TestAvatarFileInvalidFilenameReturnsReadableError(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	handler := &Handler{}
	engine.GET("/interface-avatar/:filename", func(ctx context.Context, c *hertzapp.RequestContext) {
		handler.avatarFile(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/interface-avatar/.", nil)
	if resp.Code != consts.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, consts.StatusBadRequest, resp.Body.String())
	}

	var body struct {
		Code    int    `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, resp.Body.String())
	}
	if body.Code != -1 {
		t.Fatalf("code = %d, want -1", body.Code)
	}
	if body.Message != "文件名不合法" || body.Error != "文件名不合法" {
		t.Fatalf("unexpected message/error: %#v", body)
	}
}
