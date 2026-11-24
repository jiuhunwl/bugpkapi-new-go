package translation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/locale"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	permissionList   = "system:i18n:list"
	permissionUpdate = "system:i18n:update"

	invalidateChannel        = "bugpk:i18n:invalidate:v1"
	invalidatePublishTimeout = 250 * time.Millisecond
	refreshInterval          = 2 * time.Minute
)

type Service struct {
	authz        *rbac.Service
	db           *sql.DB
	log          *zap.Logger
	providers    map[string]i18n.ResourceProvider
	rdb          *redis.Client
	refreshMu    sync.Mutex
	repo         *Repository
	sanitizeHTML func(string) string
	snapshot     atomic.Pointer[snapshot]
	specs        map[string]resourceSpec
	cancel       context.CancelFunc
	closeOnce    sync.Once
	wg           sync.WaitGroup
}

func NewService(db *sql.DB, rdb *redis.Client, authz *rbac.Service) *Service {
	specs := make(map[string]resourceSpec, len(defaultResourceSpecs))
	providers := make(map[string]i18n.ResourceProvider, len(defaultResourceSpecs))
	for _, spec := range defaultResourceSpecs {
		specs[spec.Type] = spec
		providers[spec.Type] = newSQLProvider(db, spec)
	}
	return &Service{
		authz:     authz,
		db:        db,
		log:       zap.NewNop(),
		providers: providers,
		rdb:       rdb,
		repo:      NewRepository(db),
		specs:     specs,
	}
}

func (s *Service) SetLogger(log *zap.Logger) {
	if log != nil {
		s.log = log
	}
}

// SetHTMLSanitizer 注入内容领域共享的富文本净化策略。
func (s *Service) SetHTMLSanitizer(sanitize func(string) string) {
	s.sanitizeHTML = sanitize
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

func (s *Service) Start(ctx context.Context) {
	if s.db == nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.Refresh(runCtx)

	if s.rdb != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.subscribeInvalidation(runCtx)
		}()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.Refresh(runCtx)
			}
		}
	}()
}

func (s *Service) Close() {
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

// Refresh 从 MySQL 重建进程内快照。
func (s *Service) Refresh(ctx context.Context) {
	if s.db == nil {
		return
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	locales, err := s.repo.Locales(ctx)
	if err != nil {
		s.log.Warn("load i18n locales failed", zap.String("error", err.Error()))
		return
	}
	entries, err := s.repo.Entries(ctx)
	if err != nil {
		s.log.Warn("load i18n entries failed", zap.String("error", err.Error()))
		return
	}
	next := newSnapshot(locales, entries)
	s.snapshot.Store(next)
	s.log.Debug("i18n snapshot loaded", zap.Int("locales", len(next.locales)), zap.Int("entries", len(entries)))
}

func (s *Service) subscribeInvalidation(ctx context.Context) {
	pubsub := s.rdb.Subscribe(ctx, invalidateChannel)
	defer func() {
		_ = pubsub.Close()
	}()
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-messages:
			if !ok {
				return
			}
			s.Refresh(ctx)
		}
	}
}

func (s *Service) broadcastInvalidation(ctx context.Context) {
	if s.rdb == nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidatePublishTimeout)
	defer cancel()
	if err := s.rdb.Publish(publishCtx, invalidateChannel, time.Now().UTC().Format(time.RFC3339Nano)).Err(); err != nil {
		s.log.Warn("publish i18n invalidation failed", zap.String("error", err.Error()))
	}
}

// Resolve 实现 i18n.LocaleResolver。
func (s *Service) Resolve(explicit, appLocale, acceptLanguage string) string {
	if snapshot := s.snapshot.Load(); snapshot != nil {
		return snapshot.resolve(explicit, appLocale, acceptLanguage)
	}
	return "zh-CN"
}

// Locale 实现 i18n.Localizer。
func (s *Service) Locale(ctx context.Context) string {
	if value := strings.TrimSpace(i18n.LocaleFromContext(ctx)); value != "" {
		return value
	}
	if snapshot := s.snapshot.Load(); snapshot != nil {
		return snapshot.defaultCode
	}
	return "zh-CN"
}

// Localize 实现 i18n.Localizer。
func (s *Service) Localize(ctx context.Context, resourceType, key string) map[string]string {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	return snapshot.Localize(s.Locale(ctx), resourceType, key)
}

// LocalizeBatch 实现 i18n.Localizer。
func (s *Service) LocalizeBatch(ctx context.Context, resourceType string, keys []string) map[string]map[string]string {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	return snapshot.LocalizeBatch(s.Locale(ctx), resourceType, keys)
}

func (s *Service) Locales(ctx context.Context, includeDisabled bool) ([]Locale, error) {
	items, err := s.repo.Locales(ctx)
	if err != nil {
		return nil, err
	}
	if includeDisabled {
		return items, nil
	}
	result := make([]Locale, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Service) SaveLocales(ctx context.Context, principal Principal, req SaveLocalesRequest) ([]Locale, error) {
	if err := s.authz.Require(ctx, principal, permissionUpdate); err != nil {
		return nil, err
	}
	if len(req.Locales) == 0 {
		return nil, apperrors.New(consts.StatusBadRequest, "语言列表不能为空")
	}

	current, err := s.repo.Locales(ctx)
	if err != nil {
		return nil, err
	}
	currentByCode := make(map[string]Locale, len(current))
	for _, item := range current {
		currentByCode[item.Code] = item
	}
	currentDefault := ""
	for _, item := range current {
		if item.IsDefault {
			currentDefault = item.Code
			break
		}
	}

	seen := make(map[string]struct{}, len(req.Locales))
	fallback := make(map[string]string, len(current)+len(req.Locales))
	for code, item := range currentByCode {
		fallback[code] = item.FallbackCode
	}
	defaultCode := ""
	normalized := make([]Locale, 0, len(req.Locales))
	for _, item := range req.Locales {
		code := locale.NormalizeTag(item.Code)
		if code == "" {
			return nil, apperrors.New(consts.StatusBadRequest, "语言码不合法")
		}
		if _, ok := seen[code]; ok {
			return nil, apperrors.New(consts.StatusBadRequest, "语言码重复")
		}
		seen[code] = struct{}{}
		if strings.TrimSpace(item.Label) == "" {
			return nil, apperrors.New(consts.StatusBadRequest, "语言名称不能为空")
		}
		if item.IsDefault {
			if defaultCode != "" {
				return nil, apperrors.New(consts.StatusBadRequest, "只能有一个默认语言")
			}
			defaultCode = code
		}
		normalizedFallback := locale.NormalizeTag(item.FallbackCode)
		if strings.TrimSpace(item.FallbackCode) != "" && normalizedFallback == "" {
			return nil, apperrors.New(consts.StatusBadRequest, "回退语言码不合法")
		}
		item.Code = code
		item.FallbackCode = normalizedFallback
		normalized = append(normalized, item)
		fallback[code] = item.FallbackCode
	}
	if defaultCode == "" {
		return nil, apperrors.New(consts.StatusBadRequest, "必须指定默认语言")
	}
	if currentDefault != "" && !strings.EqualFold(defaultCode, currentDefault) {
		return nil, apperrors.New(consts.StatusBadRequest, "默认语言不支持在线切换")
	}
	for _, item := range normalized {
		if item.IsDefault && !item.Enabled {
			return nil, apperrors.New(consts.StatusBadRequest, "默认语言必须启用")
		}
		if item.FallbackCode != "" {
			if _, ok := fallback[item.FallbackCode]; !ok {
				return nil, apperrors.New(consts.StatusBadRequest, "回退语言不存在")
			}
			if hasFallbackLoop(item.Code, fallback) {
				return nil, apperrors.New(consts.StatusBadRequest, "回退语言不能形成回环")
			}
		}
	}

	if err := s.repo.SaveLocales(ctx, normalized); err != nil {
		return nil, err
	}
	s.Refresh(ctx)
	s.broadcastInvalidation(ctx)
	return s.Locales(ctx, true)
}

func hasFallbackLoop(start string, fallback map[string]string) bool {
	visited := make(map[string]struct{}, len(fallback))
	for code := start; code != ""; {
		if _, ok := visited[code]; ok {
			return true
		}
		visited[code] = struct{}{}
		code = fallback[code]
	}
	return false
}

func (s *Service) ResourceTypes(ctx context.Context, principal Principal) ([]ResourceTypeInfo, error) {
	if err := s.authz.Require(ctx, principal, permissionList); err != nil {
		return nil, err
	}
	result := make([]ResourceTypeInfo, 0, len(defaultResourceSpecs))
	for _, spec := range defaultResourceSpecs {
		info := resourceTypeInfo(spec)
		// 规模计数：sqlProvider 支持则取源表行数，失败不阻塞类型列表。
		if counter, ok := s.providers[spec.Type].(interface {
			CountResource(context.Context) (int64, error)
		}); ok {
			if count, err := counter.CountResource(ctx); err == nil {
				info.Count = count
			}
		}
		result = append(result, info)
	}
	return result, nil
}

func resourceTypeInfo(spec resourceSpec) ResourceTypeInfo {
	// 资源标签使用驼峰后缀（packageGlobal），而 API 资源类型保持下划线
	// （package_global）。字段标签前缀从资源标签推导，保证两份语言包对称。
	labelSuffix := strings.TrimPrefix(spec.LabelKey, "translationAdmin.resources.")
	fields := make([]FieldInfo, 0, len(spec.Fields))
	for _, field := range spec.Fields {
		fields = append(fields, FieldInfo{
			Name:     field.Name,
			LabelKey: "translationAdmin.fields." + labelSuffix + "." + field.Name,
			MaxBytes: field.MaxBytes,
			HTML:     field.HTML,
		})
	}
	return ResourceTypeInfo{Type: spec.Type, LabelKey: spec.LabelKey, Fields: fields}
}

func (s *Service) ListResources(ctx context.Context, principal Principal, resourceType, keyword string, page, pageSize int) (ResourceList, error) {
	if err := s.authz.Require(ctx, principal, permissionList); err != nil {
		return ResourceList{}, err
	}
	spec, provider, err := s.lookupProvider(resourceType)
	if err != nil {
		return ResourceList{}, err
	}
	result, err := provider.List(ctx, i18n.ListQuery{Keyword: keyword, Page: page, PageSize: pageSize})
	if err != nil {
		return ResourceList{}, err
	}

	keys := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		keys = append(keys, item.Key)
	}
	entries, err := s.repo.EntriesForResource(ctx, spec.Type, keys)
	if err != nil {
		return ResourceList{}, err
	}
	locales, err := s.enabledLocales(ctx)
	if err != nil {
		return ResourceList{}, err
	}
	items := make([]ResourceItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, buildResourceItem(spec, item, entries[item.Key], locales))
	}
	return ResourceList{Items: items, Total: result.Total}, nil
}

func (s *Service) GetResource(ctx context.Context, principal Principal, resourceType, resourceKey string) (ResourceItem, error) {
	if err := s.authz.Require(ctx, principal, permissionList); err != nil {
		return ResourceItem{}, err
	}
	spec, provider, err := s.lookupProvider(resourceType)
	if err != nil {
		return ResourceItem{}, err
	}
	base, err := provider.Get(ctx, resourceKey)
	if err != nil {
		return ResourceItem{}, mapNotFound(err)
	}
	entries, err := s.repo.EntriesForResource(ctx, spec.Type, []string{base.Key})
	if err != nil {
		return ResourceItem{}, err
	}
	locales, err := s.enabledLocales(ctx)
	if err != nil {
		return ResourceItem{}, err
	}
	return buildResourceItem(spec, base, entries[base.Key], locales), nil
}

func (s *Service) SaveTranslations(ctx context.Context, principal Principal, resourceType, resourceKey string, req SaveTranslationsRequest) (SaveTranslationsResult, error) {
	if err := s.authz.Require(ctx, principal, permissionUpdate); err != nil {
		return SaveTranslationsResult{}, err
	}
	spec, provider, err := s.lookupProvider(resourceType)
	if err != nil {
		return SaveTranslationsResult{}, err
	}
	base, err := provider.Get(ctx, resourceKey)
	if err != nil {
		return SaveTranslationsResult{}, mapNotFound(err)
	}
	locales, err := s.enabledLocales(ctx)
	if err != nil {
		return SaveTranslationsResult{}, err
	}
	enabled := make(map[string]Locale, len(locales))
	defaultCode := ""
	for _, item := range locales {
		enabled[item.Code] = item
		if item.IsDefault {
			defaultCode = item.Code
		}
	}
	fieldByName := make(map[string]columnField, len(spec.Fields))
	for _, field := range spec.Fields {
		fieldByName[field.Name] = field
	}

	upserts := make([]entry, 0)
	deletes := make([]entryKey, 0)
	for rawLocale, fields := range req.Translations {
		code := locale.NormalizeTag(rawLocale)
		item, ok := enabled[code]
		if !ok {
			return SaveTranslationsResult{}, apperrors.New(consts.StatusBadRequest, "语言未启用："+rawLocale)
		}
		if item.IsDefault || strings.EqualFold(code, defaultCode) {
			return SaveTranslationsResult{}, apperrors.New(consts.StatusBadRequest, "默认语言请直接编辑原文")
		}
		for fieldName, rawValue := range fields {
			field, ok := fieldByName[fieldName]
			if !ok {
				return SaveTranslationsResult{}, apperrors.New(consts.StatusBadRequest, "字段不支持翻译："+fieldName)
			}
			value := strings.TrimSpace(rawValue)
			if value == "" {
				deletes = append(deletes, entryKey{Field: fieldName, Locale: code})
				continue
			}
			if field.HTML && s.sanitizeHTML != nil {
				value = s.sanitizeHTML(value)
				if strings.TrimSpace(value) == "" {
					return SaveTranslationsResult{}, apperrors.New(consts.StatusBadRequest, "富文本内容不合法："+fieldName)
				}
			}
			if len([]byte(value)) > field.MaxBytes {
				return SaveTranslationsResult{}, apperrors.New(consts.StatusBadRequest, "译文超出长度限制："+fieldName)
			}
			upserts = append(upserts, entry{
				ResourceType: spec.Type,
				ResourceKey:  base.Key,
				Field:        fieldName,
				Locale:       code,
				Value:        value,
				SourceHash:   sourceHash(base.Values[fieldName]),
			})
		}
	}
	if len(upserts) == 0 && len(deletes) == 0 {
		return SaveTranslationsResult{}, nil
	}
	if err := s.repo.SaveResourceTranslations(ctx, spec.Type, base.Key, upserts, deletes, principal.ID); err != nil {
		return SaveTranslationsResult{}, err
	}
	s.Refresh(ctx)
	s.broadcastInvalidation(ctx)
	return SaveTranslationsResult{Saved: len(upserts), Deleted: len(deletes)}, nil
}

func (s *Service) lookupProvider(resourceType string) (resourceSpec, i18n.ResourceProvider, error) {
	resourceType = strings.TrimSpace(resourceType)
	spec, ok := s.specs[resourceType]
	if !ok {
		return resourceSpec{}, nil, apperrors.New(consts.StatusBadRequest, "资源类型不存在")
	}
	provider, ok := s.providers[resourceType]
	if !ok {
		return resourceSpec{}, nil, apperrors.New(consts.StatusBadRequest, "资源类型不存在")
	}
	return spec, provider, nil
}

func (s *Service) enabledLocales(ctx context.Context) ([]Locale, error) {
	if snapshot := s.snapshot.Load(); snapshot != nil {
		return snapshot.locales, nil
	}
	items, err := s.repo.Locales(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Locale, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			result = append(result, item)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].SortNo < result[j].SortNo
	})
	return result, nil
}

func buildResourceItem(spec resourceSpec, base i18n.BaseResource, entries map[string]map[string]entry, locales []Locale) ResourceItem {
	fields := make([]string, 0, len(spec.Fields))
	values := make(map[string]string, len(spec.Fields))
	for _, field := range spec.Fields {
		fields = append(fields, field.Name)
		values[field.Name] = base.Values[field.Name]
	}
	defaultCode := "zh-CN"
	for _, item := range locales {
		if item.IsDefault {
			defaultCode = item.Code
			break
		}
	}

	translations := make(map[string]map[string]string)
	completeness := make(map[string]Completeness)
	staleFields := make(map[string][]string)
	for _, item := range locales {
		if item.IsDefault {
			continue
		}
		localeEntries := entries[item.Code]
		translatedValues := make(map[string]string)
		stale := make([]string, 0)
		for _, field := range spec.Fields {
			translation, ok := localeEntries[field.Name]
			if !ok || strings.TrimSpace(translation.Value) == "" {
				continue
			}
			translatedValues[field.Name] = translation.Value
			if translation.SourceHash != "" && translation.SourceHash != sourceHash(base.Values[field.Name]) {
				stale = append(stale, field.Name)
			}
		}
		if len(translatedValues) > 0 {
			translations[item.Code] = translatedValues
		}
		completeness[item.Code] = Completeness{Translated: len(translatedValues), Total: len(spec.Fields)}
		if len(stale) > 0 {
			staleFields[item.Code] = stale
		}
	}

	return ResourceItem{
		ResourceType: spec.Type,
		ResourceKey:  base.Key,
		DisplayName:  base.DisplayName,
		BaseLocale:   defaultCode,
		Fields:       fields,
		Values:       values,
		Translations: translations,
		Completeness: completeness,
		StaleFields:  staleFields,
	}
}

func sourceHash(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, "资源不存在")
	}
	return err
}
