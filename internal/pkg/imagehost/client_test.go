package imagehost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploaderPostsMultipartAndResolvesURL(t *testing.T) {
	var receivedAuth string
	var receivedBearer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/upload" {
			t.Errorf("path = %s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("returnFormat") != "full" || query.Get("responseFormat") != "object" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if query.Get("uploadChannel") != "s3" || query.Get("uploadFolder") != "tickets/2026" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		receivedAuth = r.Header.Get("authCode")
		receivedBearer = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "png-bytes" || header.Filename != "shot.png" {
			t.Errorf("file = %q name=%q", data, header.Filename)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"src":"/file/abc123.png"}`))
	}))
	defer server.Close()

	uploader := NewUploader(server.Client())
	result, err := uploader.Upload(context.Background(), Config{
		AuthCode:      "secret-code",
		BaseURL:       server.URL,
		Enabled:       1,
		Folder:        "tickets/2026",
		MaxBytes:      1 << 20,
		UploadChannel: "s3",
		UploadPath:    "/upload",
	}, File{
		ContentType: "image/png",
		Data:        []byte("png-bytes"),
		Filename:    "shot.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receivedAuth != "secret-code" {
		t.Fatalf("authCode header = %q", receivedAuth)
	}
	if receivedBearer != "Bearer secret-code" {
		t.Fatalf("authorization header = %q", receivedBearer)
	}
	if result.URL != server.URL+"/file/abc123.png" {
		t.Fatalf("url = %q", result.URL)
	}
}

func TestUploaderAcceptsArrayAndPrefersPublicURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"src":"https://origin.example/file/a.png","publicUrl":"https://cdn.example/abc/a.png"}]`))
	}))
	defer server.Close()

	result, err := NewUploader(server.Client()).Upload(context.Background(), Config{
		BaseURL:    server.URL,
		Enabled:    1,
		MaxBytes:   1 << 20,
		UploadPath: "/upload",
	}, File{ContentType: "image/png", Data: []byte("x"), Filename: "a.png"})
	if err != nil {
		t.Fatal(err)
	}
	if result.URL != "https://cdn.example/abc/a.png" {
		t.Fatalf("url = %q", result.URL)
	}
}

func TestUploaderRejectsFailuresAndInvalidConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad auth", http.StatusUnauthorized)
	}))
	defer server.Close()

	uploader := NewUploader(server.Client())
	_, err := uploader.Upload(context.Background(), Config{
		BaseURL:    server.URL,
		Enabled:    1,
		MaxBytes:   1 << 20,
		UploadPath: "/upload",
	}, File{ContentType: "image/png", Data: []byte("x"), Filename: "a.png"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected upstream error, got %v", err)
	}

	if _, err := uploader.Upload(context.Background(), Config{BaseURL: server.URL, MaxBytes: 1 << 20}, File{Data: []byte("x"), Filename: "a.png"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled error = %v", err)
	}
	for _, cfg := range []Config{
		{BaseURL: "file:///tmp/x", Enabled: 1, MaxBytes: 1 << 20, UploadPath: "/upload"},
		{BaseURL: server.URL, Enabled: 1, MaxBytes: 1 << 20, UploadPath: "/upload", UploadChannel: "nope"},
		{BaseURL: server.URL, Enabled: 1, MaxBytes: 1 << 20, UploadPath: "/../etc"},
	} {
		if err := cfg.Validate(); !errors.Is(err, ErrConfig) {
			t.Fatalf("config %+v error = %v", cfg, err)
		}
	}
}
