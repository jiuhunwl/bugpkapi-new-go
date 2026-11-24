package systemsettings

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/imagehost"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	maxImageHostBaseURLLength = 512
	maxImageHostUploadPath    = 128
	maxImageHostAuthCode      = 256
	maxImageHostFolder        = 128
	// 1×1 transparent PNG，用于后台“测试上传”；不写入图床配置。
	testImagePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF9sAAAAASUVORK5CYII="
)

func (r *Repository) ImageHostConfig(ctx context.Context) (imagehost.Config, error) {
	cfg := imagehost.Config{
		Enabled:    0,
		Provider:   imagehost.ProviderCloudflareImgBed,
		UploadPath: imagehost.DefaultUploadPath,
		MaxBytes:   imagehost.DefaultMaxBytes,
	}
	var authCode string
	err := r.db.QueryRowContext(ctx, `
		select enabled, provider, base_url, upload_path, auth_code, upload_channel, upload_folder, max_bytes
		from bp_image_host_config
		where id = 1
	`).Scan(
		&cfg.Enabled,
		&cfg.Provider,
		&cfg.BaseURL,
		&cfg.UploadPath,
		&authCode,
		&cfg.UploadChannel,
		&cfg.Folder,
		&cfg.MaxBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return imagehost.Config{}, err
	}
	cfg.AuthCode, err = r.cipher.DecryptCompatible(authCode)
	if err != nil {
		return imagehost.Config{}, err
	}
	cfg = cfg.Normalized()
	return cfg, nil
}

func (r *Repository) SaveImageHostConfig(ctx context.Context, cfg imagehost.Config) error {
	authCode, err := r.cipher.Encrypt(cfg.AuthCode)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_image_host_config(
			id, enabled, provider, base_url, upload_path, auth_code, upload_channel, upload_folder, max_bytes
		)
		values (1, ?, ?, ?, ?, ?, ?, ?, ?)
		on duplicate key update
			enabled = values(enabled),
			provider = values(provider),
			base_url = values(base_url),
			upload_path = values(upload_path),
			auth_code = values(auth_code),
			upload_channel = values(upload_channel),
			upload_folder = values(upload_folder),
			max_bytes = values(max_bytes)
	`, cfg.Enabled, cfg.Provider, cfg.BaseURL, cfg.UploadPath, authCode, cfg.UploadChannel, cfg.Folder, cfg.MaxBytes)
	return err
}

func (s *Service) ImageHostConfig(ctx context.Context, p rbac.Principal) (imagehost.Config, error) {
	if err := s.authz.Require(ctx, p, "system:image-host:view"); err != nil {
		return imagehost.Config{}, err
	}
	cfg, err := s.repo.ImageHostConfig(ctx)
	if err != nil {
		return imagehost.Config{}, mapConfigSecretErr(err)
	}
	cfg.AuthCode = maskedSecret(cfg.AuthCode)
	return cfg, nil
}

func (s *Service) SaveImageHostConfig(ctx context.Context, p rbac.Principal, req imagehost.Config) error {
	if err := s.authz.Require(ctx, p, "system:image-host:update"); err != nil {
		return err
	}
	current, err := s.repo.ImageHostConfig(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	req.AuthCode = preservedSecret(req.AuthCode, current.AuthCode)
	cfg, err := normalizeImageHostConfig(req)
	if err != nil {
		return err
	}
	return mapConfigSecretErr(s.repo.SaveImageHostConfig(ctx, cfg))
}

func (s *Service) TestImageHostConfig(ctx context.Context, p rbac.Principal, req imagehost.Config) (imagehost.Upload, error) {
	if err := s.authz.Require(ctx, p, "system:image-host:test"); err != nil {
		return imagehost.Upload{}, err
	}
	if s.imageHostUploader == nil {
		return imagehost.Upload{}, apperrors.New(consts.StatusServiceUnavailable, "图床上传客户端未初始化")
	}
	current, err := s.repo.ImageHostConfig(ctx)
	if err != nil {
		return imagehost.Upload{}, mapConfigSecretErr(err)
	}
	req.AuthCode = preservedSecret(req.AuthCode, current.AuthCode)
	cfg, err := normalizeImageHostConfig(req)
	if err != nil {
		return imagehost.Upload{}, err
	}
	cfg.Enabled = 1
	if err := cfg.Validate(); err != nil {
		return imagehost.Upload{}, imageHostConfigError(err)
	}
	data, err := base64.StdEncoding.DecodeString(testImagePNGBase64)
	if err != nil {
		return imagehost.Upload{}, apperrors.Wrap(consts.StatusInternalServerError, "测试图片生成失败", err)
	}
	result, err := s.imageHostUploader.Upload(ctx, cfg, imagehost.File{
		ContentType: "image/png",
		Data:        data,
		Filename:    fmt.Sprintf("bugpk-image-host-%d.png", time.Now().Unix()),
	})
	if err != nil {
		return imagehost.Upload{}, apperrors.Wrap(consts.StatusBadGateway, "图床上传测试失败，请检查图床地址、授权码、上传渠道与文件格式限制", err)
	}
	return result, nil
}

func normalizeImageHostConfig(req imagehost.Config) (imagehost.Config, error) {
	cfg := req.Normalized()
	if cfg.Enabled != 0 && cfg.Enabled != 1 {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "图床启用状态不合法")
	}
	if cfg.Provider != imagehost.ProviderCloudflareImgBed {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "图床类型仅支持 CloudFlare-ImgBed")
	}
	if len(cfg.BaseURL) > maxImageHostBaseURLLength {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "图床地址长度不能超过 512 个字符")
	}
	if len(cfg.UploadPath) > maxImageHostUploadPath {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "上传路径长度不能超过 128 个字符")
	}
	if len(cfg.AuthCode) > maxImageHostAuthCode {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "图床授权码/API Token 长度不能超过 256 个字符")
	}
	if strings.ContainsAny(cfg.AuthCode, "\r\n\x00") {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "图床授权码/API Token 包含非法字符")
	}
	if utf8.RuneCountInString(cfg.Folder) > maxImageHostFolder {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "上传目录长度不能超过 128 个字符")
	}
	if !validImageHostFolder(cfg.Folder) {
		return imagehost.Config{}, apperrors.New(consts.StatusBadRequest, "上传目录不能包含 ..、反斜杠或控制字符")
	}
	if cfg.MaxBytes < imagehost.MinMaxBytes || cfg.MaxBytes > imagehost.MaxMaxBytes {
		cfg.MaxBytes = imagehost.DefaultMaxBytes
	}
	if err := cfg.Validate(); err != nil {
		return imagehost.Config{}, imageHostConfigError(err)
	}
	return cfg, nil
}

func imageHostConfigError(err error) error {
	if errors.Is(err, imagehost.ErrConfig) {
		return apperrors.New(consts.StatusBadRequest, "图床配置不合法："+strings.TrimPrefix(err.Error(), imagehost.ErrConfig.Error()+": "))
	}
	return apperrors.Wrap(consts.StatusBadRequest, "图床配置不合法", err)
}

func validImageHostFolder(value string) bool {
	if value == "" {
		return true
	}
	if strings.Contains(value, "..") || strings.Contains(value, "\\") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
