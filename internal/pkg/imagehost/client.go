package imagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

const (
	ProviderCloudflareImgBed = "cloudflare-imgbed"
	DefaultUploadPath        = "/upload"
	DefaultMaxBytes          = 10 << 20
	MinMaxBytes              = 1 << 20
	MaxMaxBytes              = 10 << 20

	maxResponseBytes = 64 << 10
)

var (
	ErrDisabled = errors.New("image host is disabled")
	ErrConfig   = errors.New("image host configuration is invalid")
)

var allowedChannels = map[string]struct{}{
	"":            {},
	"telegram":    {},
	"cfr2":        {},
	"s3":          {},
	"discord":     {},
	"huggingface": {},
	"webdav":      {},
	"external":    {},
}

type Config struct {
	AuthCode      string `json:"authCode"`
	BaseURL       string `json:"baseUrl"`
	Enabled       int    `json:"enabled"`
	Folder        string `json:"folder"`
	MaxBytes      int64  `json:"maxBytes"`
	Provider      string `json:"provider"`
	UploadChannel string `json:"uploadChannel"`
	UploadPath    string `json:"uploadPath"`
}

func (c Config) Normalized() Config {
	cfg := c
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = ProviderCloudflareImgBed
	}
	cfg.UploadPath = strings.TrimSpace(cfg.UploadPath)
	if cfg.UploadPath == "" {
		cfg.UploadPath = DefaultUploadPath
	}
	if !strings.HasPrefix(cfg.UploadPath, "/") {
		cfg.UploadPath = "/" + cfg.UploadPath
	}
	cfg.UploadChannel = strings.ToLower(strings.TrimSpace(cfg.UploadChannel))
	cfg.Folder = strings.Trim(strings.TrimSpace(cfg.Folder), "/")
	cfg.AuthCode = strings.TrimSpace(cfg.AuthCode)
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	return cfg
}

func (c Config) Validate() error {
	cfg := c.Normalized()
	if cfg.Provider != ProviderCloudflareImgBed {
		return fmt.Errorf("%w: unsupported provider %q", ErrConfig, cfg.Provider)
	}
	if _, ok := allowedChannels[cfg.UploadChannel]; !ok {
		return fmt.Errorf("%w: unsupported upload channel %q", ErrConfig, cfg.UploadChannel)
	}
	if strings.ContainsAny(cfg.UploadPath, "?#") || strings.Contains(cfg.UploadPath, "..") {
		return fmt.Errorf("%w: upload path is invalid", ErrConfig)
	}
	if cfg.MaxBytes < MinMaxBytes || cfg.MaxBytes > MaxMaxBytes {
		return fmt.Errorf("%w: max bytes is out of range", ErrConfig)
	}
	if cfg.BaseURL != "" {
		if err := httpclient.ValidateURL(cfg.BaseURL); err != nil {
			return fmt.Errorf("%w: %v", ErrConfig, err)
		}
		parsed, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrConfig, err)
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("%w: base url must not contain query or fragment", ErrConfig)
		}
	}
	if cfg.Enabled != 1 {
		return nil
	}
	if cfg.BaseURL == "" {
		return fmt.Errorf("%w: base url is required", ErrConfig)
	}
	return nil
}

type File struct {
	ContentType string
	Data        []byte
	Filename    string
}

type Upload struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
}

type Uploader struct {
	client *http.Client
}

func NewUploader(client *http.Client) *Uploader {
	if client == nil {
		client = httpclient.Outbound(20 * time.Second)
	}
	return &Uploader{client: client}
}

func (u *Uploader) Upload(ctx context.Context, cfg Config, file File) (Upload, error) {
	cfg = cfg.Normalized()
	if cfg.Enabled != 1 {
		return Upload{}, ErrDisabled
	}
	if err := cfg.Validate(); err != nil {
		return Upload{}, err
	}
	if len(file.Data) == 0 || file.Filename == "" {
		return Upload{}, errors.New("image file is required")
	}
	if int64(len(file.Data)) > cfg.MaxBytes {
		return Upload{}, fmt.Errorf("image exceeds configured limit %d", cfg.MaxBytes)
	}

	target, err := uploadURL(cfg)
	if err != nil {
		return Upload{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(
		`form-data; name="file"; filename="%s"`,
		escapeMultipartName(file.Filename),
	))
	if file.ContentType != "" {
		partHeader.Set("Content-Type", file.ContentType)
	}
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return Upload{}, err
	}
	if _, err := part.Write(file.Data); err != nil {
		return Upload{}, err
	}
	if err := writer.Close(); err != nil {
		return Upload{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, &body)
	if err != nil {
		return Upload{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "BugPk-Api image-host client")
	if cfg.AuthCode != "" {
		// CloudFlare-ImgBed accepts either the user authCode header or an API
		// token in Authorization. Sending both keeps one credential field usable
		// for either deployment mode; an invalid bearer token falls back to the
		// authCode check in the upstream auth core.
		request.Header.Set("authCode", cfg.AuthCode)
		request.Header.Set("Authorization", "Bearer "+cfg.AuthCode)
	}

	response, err := u.client.Do(request)
	if err != nil {
		return Upload{}, err
	}
	defer response.Body.Close()
	payload, err := httpclient.ReadBody(response.Body, maxResponseBytes)
	if err != nil {
		return Upload{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(payload))
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return Upload{}, fmt.Errorf("image host returned HTTP %d: %s", response.StatusCode, truncate(message, 300))
	}
	rawURL, err := parseUploadURL(payload)
	if err != nil {
		return Upload{}, err
	}
	publicURL, err := resolvePublicURL(cfg.BaseURL, rawURL)
	if err != nil {
		return Upload{}, err
	}
	return Upload{Provider: cfg.Provider, URL: publicURL}, nil
}

func uploadURL(cfg Config) (string, error) {
	raw := strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.TrimLeft(cfg.UploadPath, "/")
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrConfig, err)
	}
	query := parsed.Query()
	query.Set("returnFormat", "full")
	query.Set("responseFormat", "object")
	if cfg.UploadChannel != "" {
		query.Set("uploadChannel", cfg.UploadChannel)
	}
	if cfg.Folder != "" {
		query.Set("uploadFolder", path.Clean("/" + cfg.Folder)[1:])
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type uploadResponse struct {
	PublicURL string `json:"publicUrl"`
	Src       string `json:"src"`
}

func parseUploadURL(payload []byte) (string, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return "", errors.New("image host returned an empty response")
	}
	var item uploadResponse
	switch trimmed[0] {
	case '[':
		var items []uploadResponse
		if err := json.Unmarshal(trimmed, &items); err != nil || len(items) == 0 {
			return "", errors.New("image host returned an invalid response")
		}
		item = items[0]
	case '{':
		if err := json.Unmarshal(trimmed, &item); err != nil {
			return "", errors.New("image host returned an invalid response")
		}
	default:
		return "", errors.New("image host returned an invalid response")
	}
	if strings.TrimSpace(item.PublicURL) != "" {
		return strings.TrimSpace(item.PublicURL), nil
	}
	if strings.TrimSpace(item.Src) != "" {
		return strings.TrimSpace(item.Src), nil
	}
	return "", errors.New("image host response does not contain a file URL")
}

func resolvePublicURL(baseURL, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("image host returned an invalid URL: %w", err)
	}
	if !parsed.IsAbs() {
		base, err := url.Parse(baseURL)
		if err != nil {
			return "", err
		}
		parsed = base.ResolveReference(parsed)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("image host returned an unsupported URL scheme")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("image host returned an invalid URL")
	}
	return parsed.String(), nil
}

func escapeMultipartName(value string) string {
	value = strings.ReplaceAll(value, "\\", "_")
	value = strings.ReplaceAll(value, "\"", "_")
	value = strings.ReplaceAll(value, "\r", "_")
	value = strings.ReplaceAll(value, "\n", "_")
	return value
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
