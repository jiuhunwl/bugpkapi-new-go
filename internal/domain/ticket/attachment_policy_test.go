package ticket

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"mime/multipart"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/testdb"
	"bugpk-api/internal/pkg/upload"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestAttachmentPolicyRequiresAuthenticationAndBoundsUpload(t *testing.T) {
	queries := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		switch {
		case strings.Contains(q, "from bp_user"):
			return testdb.Rows([]driver.Value{int64(1), "fixture", 1, 0, "", "", 1}), nil
		case strings.Contains(q, "from bp_role"):
			return testdb.Rows([]driver.Value{"user"}), nil
		default:
			return nil, errors.New("unexpected query: " + q)
		}
	}})
	jwt, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("t", 64), Issuer: "ticket-policy-test", AccessTokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.CreateAccessToken(auth.User{ID: 1, Username: "fixture", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(NewService(nil, rbac.NewService(rbac.NewRepository(db), jwt), nil), 4<<20)
	handler.attachmentDir = filepath.Join(t.TempDir(), "uploads", "attachments")
	h := server.New()
	handler.Register(h.Group("/system-api/tickets"))
	anonymous := ut.PerformRequest(h.Engine, "GET", "/system-api/tickets/attachment-policy", nil)
	if anonymous.Code != 401 || queries != 0 {
		t.Fatalf("anonymous status=%d db queries=%d", anonymous.Code, queries)
	}
	response := ut.PerformRequest(h.Engine, "GET", "/system-api/tickets/attachment-policy?maxBytes=99999999", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	var body struct {
		Code int                     `json:"code"`
		Data upload.AttachmentPolicy `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Code != 0 || body.Data.MaxBytes != 3<<20 {
		t.Fatalf("policy status=%d body=%s", response.Code, response.Body.String())
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", "oversized.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(bytes.Repeat([]byte("a"), (3<<20)+1))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if payload.Len() >= 4<<20 {
		t.Fatal("fixture exceeded server envelope instead of only attachment limit")
	}
	uploadResponse := ut.PerformRequest(h.Engine, "POST", "/system-api/tickets/1/attachments", &ut.Body{Body: &payload, Len: payload.Len()}, ut.Header{Key: "Authorization", Value: "Bearer " + token}, ut.Header{Key: "Content-Type", Value: writer.FormDataContentType()})
	if uploadResponse.Code != 413 {
		t.Fatalf("oversize upload status=%d body=%s", uploadResponse.Code, uploadResponse.Body.String())
	}
}
