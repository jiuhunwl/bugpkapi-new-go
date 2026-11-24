package interfaceapi

import (
	"testing"

	"bugpk-api/internal/pkg/jsonrequest"
)

// TestInvokePayloadKeyIDBind 回归测试：前端调试面板选择的密钥以 JSON number 提交，
// 后端 InvokePayload.KeyID(int64) 必须能正常绑定；未选择时缺省为 0。
func TestInvokePayloadKeyIDBind(t *testing.T) {
	t.Run("numeric keyId binds", func(t *testing.T) {
		var payload InvokePayload
		if err := jsonrequest.UnmarshalUnambiguous([]byte(`{"keyId":123,"method":"GET","body":{},"queryParams":{}}`), &payload, 32); err != nil {
			t.Fatalf("numeric keyId should bind without error: %v", err)
		}
		if payload.KeyID != 123 {
			t.Fatalf("KeyID = %d, want 123", payload.KeyID)
		}
	})

	t.Run("omitted keyId defaults to zero", func(t *testing.T) {
		var payload InvokePayload
		if err := jsonrequest.UnmarshalUnambiguous([]byte(`{"method":"POST","body":{}}`), &payload, 32); err != nil {
			t.Fatalf("payload without keyId should bind: %v", err)
		}
		if payload.KeyID != 0 {
			t.Fatalf("KeyID = %d, want 0", payload.KeyID)
		}
	})
}
