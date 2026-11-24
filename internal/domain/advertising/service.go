package advertising

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/jsonrequest"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

const (
	activeAdvertisementTTL = 30 * time.Second
	maxAdPayloadBytes      = 16 << 10
)

type Service struct {
	authz       *rbac.Service
	localizer   i18n.Localizer
	randomIndex func(int) int
	rdb         *redis.Client
	repo        *Repository
}

func NewService(repo *Repository, authz *rbac.Service, rdb *redis.Client) *Service {
	return &Service{repo: repo, authz: authz, rdb: rdb, randomIndex: rand.IntN}
}

// SetLocalizer 为公共广告接口接入后端数据翻译。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

func (s *Service) PublicAds(ctx context.Context, placement string) ([]PublicAdvertisement, error) {
	placement = normalizePlacement(placement)
	if placement != PlacementHome && placement != PlacementConsole && placement != PlacementNotice && placement != PlacementService {
		return nil, apperrors.New(consts.StatusBadRequest, "广告位不合法")
	}
	items, err := s.active(ctx, placement)
	if err != nil {
		return nil, err
	}
	result := make([]PublicAdvertisement, 0, len(items))
	for _, item := range items {
		result = append(result, publicAdvertisement(item))
	}
	s.localizePublicAds(ctx, result)
	return result, nil
}

func (s *Service) localizePublicAds(ctx context.Context, items []PublicAdvertisement) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "advertisement", keys)
	for index := range items {
		translations := overrides[items[index].ID]
		if value, ok := translations["title"]; ok {
			items[index].Title = value
		}
		if value, ok := translations["description"]; ok {
			items[index].Description = value
		}
	}
}

func (s *Service) ResolveResponseAd(ctx context.Context, id int64) (Advertisement, bool, error) {
	if id > 0 {
		item, err := s.repo.ActiveByID(ctx, id, PlacementAPIResponse)
		if err == sql.ErrNoRows {
			return Advertisement{}, false, nil
		}
		return item, err == nil, err
	}
	// A zero/negative policy value means “choose one currently active API ad”.
	items, err := s.active(ctx, PlacementAPIResponse)
	if err != nil || len(items) == 0 {
		return Advertisement{}, false, err
	}
	return s.pickResponseAdvertisement(items), true, nil
}

func (s *Service) pickResponseAdvertisement(items []Advertisement) Advertisement {
	if len(items) == 0 {
		return Advertisement{}
	}
	if len(items) == 1 {
		return items[0]
	}
	pickIndex := s.randomIndex
	if pickIndex == nil {
		pickIndex = rand.IntN
	}
	index := pickIndex(len(items))
	if index < 0 || index >= len(items) {
		index = 0
	}
	return items[index]
}

func (s *Service) AdminList(ctx context.Context, principal Principal, params ListParams) (PageResult[Advertisement], error) {
	if err := s.authz.Require(ctx, principal, "advertisement:list"); err != nil {
		return PageResult[Advertisement]{}, err
	}
	params.Placement = normalizePlacement(params.Placement)
	if params.Placement != "" && !validPlacement(params.Placement) {
		return PageResult[Advertisement]{}, apperrors.New(consts.StatusBadRequest, "广告位不合法")
	}
	if params.Status != nil && *params.Status != 0 && *params.Status != 1 {
		return PageResult[Advertisement]{}, apperrors.New(consts.StatusBadRequest, "状态不合法")
	}
	return s.repo.List(ctx, params)
}

func (s *Service) AdminDetail(ctx context.Context, principal Principal, id int64) (Advertisement, error) {
	if err := s.authz.Require(ctx, principal, "advertisement:list"); err != nil {
		return Advertisement{}, err
	}
	item, err := s.repo.ByID(ctx, id)
	return item, mapNotFound(err)
}

func (s *Service) Create(ctx context.Context, principal Principal, req SaveAdvertisementRequest) (Advertisement, error) {
	if err := s.authz.Require(ctx, principal, "advertisement:create"); err != nil {
		return Advertisement{}, err
	}
	item, err := normalizeAdvertisement(req)
	if err != nil {
		return Advertisement{}, err
	}
	id, err := s.repo.Create(ctx, item, principal.ID)
	if err != nil {
		return Advertisement{}, err
	}
	s.invalidate(ctx)
	return s.repo.ByID(ctx, id)
}

func (s *Service) Update(ctx context.Context, principal Principal, id int64, req SaveAdvertisementRequest) error {
	if err := s.authz.Require(ctx, principal, "advertisement:update"); err != nil {
		return err
	}
	item, err := normalizeAdvertisement(req)
	if err != nil {
		return err
	}
	if err := s.repo.Update(ctx, id, item, principal.ID); err != nil {
		return mapNotFound(err)
	}
	s.invalidate(ctx)
	return nil
}

func (s *Service) Delete(ctx context.Context, principal Principal, id int64) error {
	if err := s.authz.Require(ctx, principal, "advertisement:delete"); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapNotFound(err)
	}
	s.invalidate(ctx)
	return nil
}

func (s *Service) active(ctx context.Context, placement string) ([]Advertisement, error) {
	key := "advertising:active:v1:" + strings.ToLower(placement)
	if s.rdb != nil {
		if text, err := s.rdb.Get(ctx, key).Bytes(); err == nil {
			var items []Advertisement
			if json.Unmarshal(text, &items) == nil {
				return items, nil
			}
		}
	}
	items, err := s.repo.Active(ctx, placement, 10)
	if err != nil {
		return nil, err
	}
	if s.rdb != nil {
		if data, marshalErr := json.Marshal(items); marshalErr == nil {
			_ = s.rdb.Set(ctx, key, data, activeAdvertisementTTL).Err()
		}
	}
	return items, nil
}

func (s *Service) invalidate(ctx context.Context) {
	if s.rdb == nil {
		return
	}
	keys := []string{
		"advertising:active:v1:home",
		"advertising:active:v1:console",
		"advertising:active:v1:notice",
		"advertising:active:v1:service_provider",
		"advertising:active:v1:api_response",
	}
	_ = s.rdb.Del(ctx, keys...).Err()
}

func normalizeAdvertisement(req SaveAdvertisementRequest) (Advertisement, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告名称不能为空且不能超过64个字符")
	}
	placement := normalizePlacement(req.Placement)
	if !validPlacement(placement) {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告位不合法")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" || utf8.RuneCountInString(title) > 128 {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告标题不能为空且不能超过128个字符")
	}
	description := strings.TrimSpace(req.Description)
	if utf8.RuneCountInString(description) > 512 {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告说明不能超过512个字符")
	}
	imageURL, err := normalizeAdURL(req.ImageURL, "广告图片")
	if err != nil {
		return Advertisement{}, err
	}
	linkURL, err := normalizeAdURL(req.LinkURL, "广告链接")
	if err != nil {
		return Advertisement{}, err
	}
	linkTarget := strings.TrimSpace(req.LinkTarget)
	if linkTarget == "" {
		linkTarget = "_blank"
	}
	if linkTarget != "_blank" && linkTarget != "_self" {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告打开方式不合法")
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	if status != 0 && status != 1 {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告状态不合法")
	}
	if req.SortNo < -100000 || req.SortNo > 100000 {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告排序值超出范围")
	}
	startTime, start, err := normalizeAdTime(req.StartTime)
	if err != nil {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告开始时间格式不合法")
	}
	endTime, end, err := normalizeAdTime(req.EndTime)
	if err != nil {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告结束时间格式不合法")
	}
	if start != nil && end != nil && !end.After(*start) {
		return Advertisement{}, apperrors.New(consts.StatusBadRequest, "广告结束时间必须晚于开始时间")
	}
	payload, err := normalizeAdPayload(req.Payload)
	if err != nil {
		return Advertisement{}, err
	}
	if placement != PlacementAPIResponse {
		payload = nil
	}
	if placement == PlacementService {
		description = ""
		imageURL = ""
	}
	return Advertisement{
		Description: description,
		EndTime:     endTime,
		ImageURL:    imageURL,
		LinkTarget:  linkTarget,
		LinkURL:     linkURL,
		Name:        name,
		Payload:     payload,
		Placement:   placement,
		SortNo:      req.SortNo,
		StartTime:   startTime,
		Status:      status,
		Title:       title,
	}, nil
}

func normalizeAdURL(raw, label string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if len(value) > 1024 || strings.ContainsAny(value, "\\\r\n\x00") {
		return "", apperrors.New(consts.StatusBadRequest, label+"不合法")
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", apperrors.New(consts.StatusBadRequest, label+"必须是 http(s) 地址或站内绝对路径")
	}
	return parsed.String(), nil
}

func normalizeAdTime(raw string) (string, *time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil, nil
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, location)
	if err != nil {
		return "", nil, err
	}
	return parsed.Format("2006-01-02 15:04:05"), &parsed, nil
}

func normalizeAdPayload(payload map[string]any) (map[string]any, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > maxAdPayloadBytes {
		return nil, apperrors.New(consts.StatusBadRequest, "广告扩展 JSON 不能超过16 KiB")
	}
	var normalized map[string]any
	if err := jsonrequest.UnmarshalUnambiguous(data, &normalized, jsonrequest.DefaultMaxDepth); err != nil {
		return nil, apperrors.New(consts.StatusBadRequest, "广告扩展 JSON 不合法")
	}
	return normalized, nil
}

func normalizePlacement(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func validPlacement(value string) bool {
	switch value {
	case PlacementHome, PlacementConsole, PlacementNotice, PlacementService, PlacementAPIResponse:
		return true
	default:
		return false
	}
}

func publicAdvertisement(item Advertisement) PublicAdvertisement {
	return PublicAdvertisement{
		Description: item.Description,
		ID:          item.ID,
		ImageURL:    item.ImageURL,
		LinkTarget:  item.LinkTarget,
		LinkURL:     item.LinkURL,
		Placement:   item.Placement,
		Title:       item.Title,
	}
}

func mapNotFound(err error) error {
	if err == sql.ErrNoRows {
		return apperrors.New(consts.StatusNotFound, "广告不存在")
	}
	return err
}
