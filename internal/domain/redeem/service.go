package redeem

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/observability"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 钱包容量与流水口径：与 payment 域逐字对齐（守卫式 UPDATE 的上界 +
// bp_wallet_transaction 的 biz_type）。
const (
	maxUserBalanceText  = "99999999999999.9999"
	maxUserPointBalance = int64(1_000_000_000_000_000)
	walletBizType       = "REDEEM_CODE"
	maxRewardBalance    = "99999999.9999" // decimal(12,4) 上界
	auditModule         = "redeem"
	auditPreviewCodes   = 5
)

// 哨兵错误：仓储层返回，service 统一映射为 HTTP 文案。
var (
	errCodeInvalid       = errors.New("redeem code invalid")
	errCodeUsed          = errors.New("redeem code already used")
	errTypeDisabled      = errors.New("redeem type disabled")
	errTypeExpired       = errors.New("redeem type outside window")
	errLimitReached      = errors.New("redeem per-user limit reached")
	errIPLimitReached    = errors.New("redeem per-ip limit reached")
	errIPUnavailable     = errors.New("redeem client ip unavailable")
	errPointBalance      = errors.New("redeem point balance limit")
	errBalanceCapacity   = errors.New("redeem balance capacity")
	errPackageDisabled   = errors.New("redeem package disabled")
	errPackageRefInvalid = errors.New("redeem package reference invalid")
	errGuardUnavailable  = errors.New("redeem guard unavailable")
)

// operationRecorder 是审计写入窄接口（observability.Service 实现），
// 避免 redeem 反向依赖具体装配。
type operationRecorder interface {
	RecordOperation(ctx context.Context, rec observability.OperationLogRecord)
}

// AuditMeta 携带 HTTP 层信息供审计记录使用（领域不依赖 hertz 类型）。
type AuditMeta struct {
	Method    string
	Path      string
	ClientIP  string
	UserAgent string
}

// Service 是兑换码域应用服务。
type Service struct {
	repo  *Repository
	authz *rbac.Service
	guard *Guard
	db    *sql.DB
	audit operationRecorder
	// localizer 提供字典译文（兑换类型名 / 奖励套餐名）；nil 时输出原文。
	localizer i18n.Localizer
}

func NewService(repo *Repository, authz *rbac.Service, guard *Guard, db *sql.DB) *Service {
	return &Service{repo: repo, authz: authz, guard: guard, db: db}
}

// SetLocalizer 接入翻译字典；nil 时保持默认语言原文。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

// SetOperationRecorder 装配审计写入（失败不阻断业务，内部容错）。
func (s *Service) SetOperationRecorder(recorder operationRecorder) {
	s.audit = recorder
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	principal, err := s.authz.Authenticate(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, nil
}

func (s *Service) require(ctx context.Context, principal Principal, code string) error {
	return s.authz.Require(ctx, rbac.Principal{
		ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion,
	}, code)
}

func (s *Service) record(ctx context.Context, principal Principal, meta AuditMeta, action, summary, targetType, targetID, detail string) {
	if s.audit == nil {
		return
	}
	rec := observability.OperationLogRecord{
		UserID: principal.ID, Username: principal.Username,
		Module: auditModule, Action: action, Summary: summary,
		Method: meta.Method, Path: meta.Path,
		TargetType: targetType, TargetID: targetID,
		ClientIP: meta.ClientIP, UserAgent: meta.UserAgent,
		Status: true,
	}
	if detail != "" {
		rec.Detail = detail
	}
	s.audit.RecordOperation(ctx, rec)
}

// ---------------------------------------------------------------- 类型模板

func (s *Service) ListTypes(ctx context.Context, principal Principal, params ListParams) (PageResult[CodeType], error) {
	if err := s.require(ctx, principal, "redeem:type:list"); err != nil {
		return PageResult[CodeType]{}, err
	}
	result, err := s.repo.ListTypes(ctx, params)
	if err != nil {
		return PageResult[CodeType]{}, err
	}
	items := make([]CodeType, 0, len(result.Items))
	for _, row := range result.Items {
		item := CodeType{
			ID: strconv.FormatInt(row.ID, 10), Code: row.Code, Name: row.Name,
			RewardType: row.RewardType, RewardPoints: row.RewardPoints, RewardBalance: row.RewardBalance,
			RewardRefID: row.RewardRefID, PerUserLimit: row.PerUserLimit, PerIPLimit: row.PerIPLimit,
			StartAt: formatTime(row.StartAt), EndAt: formatTime(row.EndAt), Status: row.Status,
			GeneratedCount: row.GeneratedCount, RedeemedCount: row.RedeemedCount,
			Frozen:     row.GeneratedCount > 0,
			CreateTime: formatTime(row.CreatedAt), UpdateTime: formatTime(row.UpdatedAt),
		}
		items = append(items, item)
	}
	return PageResult[CodeType]{Items: items, Total: result.Total}, nil
}

// normalizeType 归一化并校验类型请求（奖励矩阵 / 限领 / 窗口 / 套餐引用）。
func (s *Service) normalizeType(ctx context.Context, req SaveTypeRequest) (typeRow, error) {
	item := typeRow{
		Code:         strings.ToUpper(strings.TrimSpace(req.Code)),
		Name:         strings.TrimSpace(req.Name),
		RewardType:   req.RewardType,
		RewardPoints: req.RewardPoints,
		RewardRefID:  req.RewardRefID,
		PerUserLimit: req.PerUserLimit,
		PerIPLimit:   req.PerIPLimit,
		Status:       1,
	}
	if req.Status != nil {
		item.Status = *req.Status
	}
	if item.Code == "" || len(item.Code) > 32 || !typeCodePattern.MatchString(item.Code) {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "类型标识需为 2-32 位大写字母、数字、- 或 _")
	}
	if item.Name == "" {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "类型名称不能为空")
	}
	balance, err := parseMoneyField(req.RewardBalance)
	if err != nil {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "余额面值格式不正确")
	}
	item.RewardBalance = balance
	if item.RewardPoints < 0 || item.RewardPoints > maxUserPointBalance {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "点数面值超出允许范围")
	}
	if item.PerUserLimit < 0 || item.PerUserLimit > maxPerUserLimit {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "每账号限领次数需为 0（不限）、1 或 2-999")
	}
	if item.PerIPLimit < 0 || item.PerIPLimit > maxPerIPLimit {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "每 IP 限领次数需为 0（不限）或 1-999")
	}
	startAt, err := parseTimeField(req.StartAt)
	if err != nil {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "开始时间格式不正确")
	}
	endAt, err := parseTimeField(req.EndAt)
	if err != nil {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "结束时间格式不正确")
	}
	if !startAt.Before(endAt) {
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "开始时间必须早于结束时间")
	}
	item.StartAt, item.EndAt = startAt, endAt

	// 奖励矩阵（迁移 CHECK 的镜像，保存期给出友好文案）。
	switch item.RewardType {
	case RewardTypePoints:
		if item.RewardPoints <= 0 || item.RewardBalance != "0.0000" || item.RewardRefID != 0 {
			return typeRow{}, apperrors.New(consts.StatusBadRequest, "点数奖励要求点数面值大于 0，余额为 0，不引用套餐")
		}
	case RewardTypeBalance:
		if item.RewardBalance == "0.0000" || item.RewardRefID != 0 {
			return typeRow{}, apperrors.New(consts.StatusBadRequest, "余额奖励要求余额面值大于 0，不引用套餐")
		}
	case RewardTypeGlobal, RewardTypeSpec:
		if item.RewardPoints != 0 || item.RewardBalance != "0.0000" || item.RewardRefID <= 0 {
			return typeRow{}, apperrors.New(consts.StatusBadRequest, "套餐奖励要求面值为 0 且必须引用已创建的套餐")
		}
		ref, err := s.resolvePackageRef(ctx, item.RewardType, item.RewardRefID)
		if err != nil {
			return typeRow{}, err
		}
		if ref.ValidDays <= 0 {
			return typeRow{}, apperrors.New(consts.StatusBadRequest, "永久有效的套餐不允许参与兑换，请为其设置有效期")
		}
	case RewardTypeCombined:
		if item.RewardPoints <= 0 || item.RewardBalance == "0.0000" || item.RewardRefID != 0 {
			return typeRow{}, apperrors.New(consts.StatusBadRequest, "组合奖励要求点数与余额面值均大于 0，不引用套餐")
		}
	default:
		return typeRow{}, apperrors.New(consts.StatusBadRequest, "奖励类型不支持")
	}
	return item, nil
}

// resolvePackageRef 按奖励类型解析套餐引用（不存在/停用给出区分文案）。
func (s *Service) resolvePackageRef(ctx context.Context, rewardType int, refID int64) (packageRef, error) {
	if rewardType == RewardTypeGlobal {
		ref, err := s.repo.GetGlobalPackageRef(ctx, refID)
		if errors.Is(err, sql.ErrNoRows) {
			return packageRef{}, apperrors.New(consts.StatusBadRequest, "引用的全站套餐不存在")
		}
		if err != nil {
			if errors.Is(err, errPackageDisabled) {
				return packageRef{}, apperrors.New(consts.StatusBadRequest, "引用的全站套餐已停用")
			}
			return packageRef{}, err
		}
		return ref, nil
	}
	ref, err := s.repo.GetInterfaceSpecRef(ctx, refID)
	if errors.Is(err, sql.ErrNoRows) {
		return packageRef{}, apperrors.New(consts.StatusBadRequest, "引用的接口套餐规格不存在")
	}
	if err != nil {
		if errors.Is(err, errPackageDisabled) {
			return packageRef{}, apperrors.New(consts.StatusBadRequest, "引用的接口套餐或规格已停用")
		}
		return packageRef{}, err
	}
	return ref, nil
}

// rewardConfigChanged 判断奖励矩阵与限领字段是否变化（M-2 冻结判据）。
// 名称/窗口/状态不在判据内——生成过码后仍允许调整。
func rewardConfigChanged(existing, item typeRow) bool {
	return existing.RewardType != item.RewardType ||
		existing.RewardPoints != item.RewardPoints ||
		existing.RewardBalance != item.RewardBalance ||
		existing.RewardRefID != item.RewardRefID ||
		existing.PerUserLimit != item.PerUserLimit ||
		existing.PerIPLimit != item.PerIPLimit
}

func (s *Service) SaveType(ctx context.Context, principal Principal, id int64, req SaveTypeRequest) error {
	code := "redeem:type:create"
	if id > 0 {
		code = "redeem:type:update"
	}
	if err := s.require(ctx, principal, code); err != nil {
		return err
	}
	item, err := s.normalizeType(ctx, req)
	if err != nil {
		return err
	}
	if id > 0 {
		existing, err := s.repo.GetType(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusBadRequest, "兑换码类型不存在")
		}
		if err != nil {
			return err
		}
		frozen, err := s.repo.TypeHasCodes(ctx, id)
		if err != nil {
			return err
		}
		// M-2：生成过码的类型，奖励与限领字段服务端冻结（防改面值波及
		// 存量码、防放宽限领扩大既得利益）；仅名称/窗口/状态可改。
		if frozen && rewardConfigChanged(existing, item) {
			return apperrors.New(consts.StatusConflict, "该类型已生成兑换码，奖励与限领配置不可修改，请新建类型")
		}
		taken, err := s.repo.TypeCodeTaken(ctx, item.Code, id)
		if err != nil {
			return err
		}
		if taken {
			return apperrors.New(consts.StatusConflict, "类型标识已被占用")
		}
		if err := s.repo.UpdateType(ctx, id, item); err != nil {
			if errors.Is(err, errNotFound) {
				return apperrors.New(consts.StatusBadRequest, "兑换码类型不存在")
			}
			return err
		}
		return nil
	}
	taken, err := s.repo.TypeCodeTaken(ctx, item.Code, 0)
	if err != nil {
		return err
	}
	if taken {
		return apperrors.New(consts.StatusConflict, "类型标识已被占用")
	}
	if _, err := s.repo.CreateType(ctx, item); err != nil {
		if IsDuplicateKey(err, "uk_redeem_code_type_code") {
			return apperrors.New(consts.StatusConflict, "类型标识已被占用")
		}
		return err
	}
	return nil
}

func (s *Service) UpdateTypeStatus(ctx context.Context, principal Principal, id int64, status int) error {
	if err := s.require(ctx, principal, "redeem:type:update"); err != nil {
		return err
	}
	if status != 0 && status != 1 {
		return apperrors.New(consts.StatusBadRequest, "状态值不正确")
	}
	if err := s.repo.UpdateTypeStatus(ctx, id, status); err != nil {
		if errors.Is(err, errNotFound) {
			return apperrors.New(consts.StatusBadRequest, "兑换码类型不存在")
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------- 码管理

func (s *Service) ListCodes(ctx context.Context, principal Principal, params ListParams) (PageResult[RedeemCode], error) {
	if err := s.require(ctx, principal, "redeem:code:list"); err != nil {
		return PageResult[RedeemCode]{}, err
	}
	result, err := s.repo.ListCodes(ctx, params)
	if err != nil {
		return PageResult[RedeemCode]{}, err
	}
	items := make([]RedeemCode, 0, len(result.Items))
	for _, row := range result.Items {
		item := RedeemCode{
			ID: strconv.FormatInt(row.ID, 10), TypeID: strconv.FormatInt(row.TypeID, 10),
			TypeName: row.TypeName, CodeMask: maskCode(row.CodePlain), Status: row.Status,
			Source: row.Source,
			UsedBy: nullInt64String(row.UsedBy), UsedAt: formatNullTime(row.UsedAt),
			CreateTime: formatTime(row.CreatedAt),
		}
		if row.BatchID.Valid {
			item.BatchID = strconv.FormatInt(row.BatchID.Int64, 10)
		}
		items = append(items, item)
	}
	return PageResult[RedeemCode]{Items: items, Total: result.Total}, nil
}

// GenerateResult 是批量生成结果。
//
// Codes 仅在开放 API 生成链路填充明文码（json:"codes,omitempty"）：
// 开放 Key 持有者申请生成 CDK 的目的就是把码分发出去（如 AstrBot 活动
// 发放到聊天），不回明文则端点不可用。管理端控制台链路不填充，
// 明文仍以码列表/导出为准，避免把控制台响应变成第二个明文出口。
type GenerateResult struct {
	BatchID string   `json:"batchId"`
	Count   int      `json:"count"`
	Codes   []string `json:"codes,omitempty"`
}

// ErrTypeNotFound 标识「业务标识不存在」，供编排层映射为稳定错误码。
var ErrTypeNotFound = errors.New("兑换码类型不存在")

// TypeIDByCode 按业务标识（如 NEWBIE）解析类型 ID。
//
// 开放 API 的外部调用方（AstrBot 活动插件）在配置里写标识而非数据库
// 自增 ID：标识不随重建数据库漂移，运维也看得懂。找不到时返回
// ErrTypeNotFound，调用方据此返回 4xx 稳定码而不是把 sql.ErrNoRows
// 透传给外部。
func (s *Service) TypeIDByCode(ctx context.Context, code string) (int64, error) {
	item, err := s.repo.GetTypeByCode(ctx, strings.TrimSpace(code))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrTypeNotFound
	}
	if err != nil {
		return 0, err
	}
	return item.ID, nil
}

// GenerateCodes 批量生成码。限额快照进批次；撞唯一键自动补生成；
// 强制审计（M-3：凭空创造资产的最高敏感操作）。控制台链路不回传明文。
func (s *Service) GenerateCodes(ctx context.Context, principal Principal, typeID int64, req GenerateCodesRequest, meta AuditMeta) (GenerateResult, error) {
	return s.generateCodes(ctx, principal, typeID, req, meta, SourceConsole, 0, false)
}

// GenerateCodesForOpenAPI 供开放 API 编排层调用：与 GenerateCodes 同一
// 实现（内部 Require redeem:code:generate = S-8 双层校验的第二层），
// 批次标记来源为开放 API 并记录 Key 追溯；唯一回传明文码的链路。
func (s *Service) GenerateCodesForOpenAPI(ctx context.Context, principal Principal, typeID int64, req GenerateCodesRequest, meta AuditMeta, creatorKeyID int64) (GenerateResult, error) {
	return s.generateCodes(ctx, principal, typeID, req, meta, SourceOpenAPI, creatorKeyID, true)
}

func (s *Service) generateCodes(ctx context.Context, principal Principal, typeID int64, req GenerateCodesRequest, meta AuditMeta, source int, creatorKeyID int64, includeCodes bool) (GenerateResult, error) {
	if err := s.require(ctx, principal, "redeem:code:generate"); err != nil {
		return GenerateResult{}, err
	}
	if req.Count <= 0 || req.Count > MaxGeneratePerBatch {
		return GenerateResult{}, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("单次生成数量需在 1~%d 之间", MaxGeneratePerBatch))
	}
	typeItem, err := s.repo.GetType(ctx, typeID)
	if errors.Is(err, sql.ErrNoRows) {
		return GenerateResult{}, apperrors.New(consts.StatusBadRequest, "兑换码类型不存在")
	}
	if err != nil {
		return GenerateResult{}, err
	}
	perUser := typeItem.PerUserLimit
	if req.PerUserLimit != nil {
		if *req.PerUserLimit < 0 || *req.PerUserLimit > maxPerUserLimit {
			return GenerateResult{}, apperrors.New(consts.StatusBadRequest, "每账号限领次数需为 0（不限）、1 或 2-999")
		}
		perUser = *req.PerUserLimit
	}
	perIP := typeItem.PerIPLimit
	if req.PerIPLimit != nil {
		if *req.PerIPLimit < 0 || *req.PerIPLimit > maxPerIPLimit {
			return GenerateResult{}, apperrors.New(consts.StatusBadRequest, "每 IP 限领次数需为 0（不限）或 1-999")
		}
		perIP = *req.PerIPLimit
	}
	existing, err := s.repo.CountCodesByType(ctx, typeID)
	if err != nil {
		return GenerateResult{}, err
	}
	if existing+int64(req.Count) > MaxCodesPerType {
		return GenerateResult{}, apperrors.New(consts.StatusConflict, fmt.Sprintf("单类型累计码数不得超过 %d", MaxCodesPerType))
	}
	batchID, err := s.repo.CreateBatch(ctx, batchRow{
		TypeID: typeID, Count: req.Count, PerUserLimitSnap: perUser, PerIPLimitSnap: perIP,
		CampaignRef: strings.TrimSpace(req.CampaignRef), Source: source,
		CreatorKeyID: creatorKeyID, CreatedBy: principal.ID,
	})
	if err != nil {
		return GenerateResult{}, err
	}
	inserted := 0
	for attempt := 0; attempt < maxGenerateAttempts && inserted < req.Count; attempt++ {
		plains := make([]string, 0, req.Count-inserted)
		for i := 0; i < req.Count-inserted; i++ {
			plain, err := generateCode()
			if err != nil {
				return GenerateResult{}, apperrors.Wrap(consts.StatusInternalServerError, "生成失败，请重试", err)
			}
			plains = append(plains, plain)
		}
		count, err := s.repo.InsertCodes(ctx, typeID, batchID, plains)
		if err != nil {
			return GenerateResult{}, err
		}
		inserted += int(count)
	}
	if inserted < req.Count {
		return GenerateResult{}, apperrors.New(consts.StatusInternalServerError, "生成失败，请重试")
	}
	preview, err := s.repo.FirstCodesOfBatch(ctx, batchID, auditPreviewCodes)
	if err != nil {
		preview = nil
	}
	detail, _ := json.Marshal(map[string]any{
		"typeId": typeID, "count": inserted, "batchId": batchID, "source": source, "codes": preview,
	})
	s.record(ctx, principal, meta, string(observability.OperationLogActionCreate),
		fmt.Sprintf("批量生成兑换码：类型 %d，数量 %d", typeID, inserted),
		"redeem_code_type", strconv.FormatInt(typeID, 10), string(detail))
	result := GenerateResult{BatchID: strconv.FormatInt(batchID, 10), Count: inserted}
	if includeCodes {
		// 按批次回读全部明文（order by id asc），与落库顺序一致；
		// 重试补生成可能打乱内存里的 plains，因此不从局部变量取值。
		allCodes, err := s.repo.FirstCodesOfBatch(ctx, batchID, inserted)
		if err != nil {
			return GenerateResult{}, err
		}
		result.Codes = allCodes
	}
	return result, nil
}

func (s *Service) VoidCode(ctx context.Context, principal Principal, id int64, meta AuditMeta) error {
	if err := s.require(ctx, principal, "redeem:code:void"); err != nil {
		return err
	}
	if err := s.repo.VoidCode(ctx, id); err != nil {
		if errors.Is(err, errNotFound) {
			return apperrors.New(consts.StatusBadRequest, "兑换码不存在或已被使用")
		}
		return err
	}
	s.record(ctx, principal, meta, string(observability.OperationLogActionDelete),
		fmt.Sprintf("作废兑换码 #%d", id), "redeem_code", strconv.FormatInt(id, 10), "")
	return nil
}

// ExportCodes 导出明文码（xlsx）。独立 redeem:code:export 权限；
// 公式注入转义；强制审计（M-3）。返回建议文件名与工作簿字节流
// （excelize 全程内存成形，缓冲不引入新的内存量级）。
func (s *Service) ExportCodes(ctx context.Context, principal Principal, typeID int64, meta AuditMeta) (string, []byte, error) {
	if err := s.require(ctx, principal, "redeem:code:export"); err != nil {
		return "", nil, err
	}
	typeItem, err := s.repo.GetType(ctx, typeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, apperrors.New(consts.StatusBadRequest, "兑换码类型不存在")
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.repo.ListCodePlainsForExport(ctx, typeID)
	if err != nil {
		return "", nil, err
	}
	if len(rows) > maxExportRows {
		rows = rows[:maxExportRows]
	}
	var buffer bytes.Buffer
	filename, err := writeCodeWorkbook(typeItem, rows, &buffer)
	if err != nil {
		return "", nil, apperrors.Wrap(consts.StatusInternalServerError, "导出失败", err)
	}
	detail, _ := json.Marshal(map[string]any{"typeId": typeID, "count": len(rows)})
	s.record(ctx, principal, meta, string(observability.OperationLogActionExport),
		fmt.Sprintf("导出兑换码明文：类型 %s，数量 %d", typeItem.Code, len(rows)),
		"redeem_code_type", strconv.FormatInt(typeID, 10), string(detail))
	return filename, buffer.Bytes(), nil
}

// ---------------------------------------------------------------- 兑换记录（管理端）

func (s *Service) ListRecords(ctx context.Context, principal Principal, params ListParams) (PageResult[RedeemRecord], error) {
	if err := s.require(ctx, principal, "redeem:record:list"); err != nil {
		return PageResult[RedeemRecord]{}, err
	}
	return s.listRecords(ctx, params, 0)
}

// UserRecords 用户端「我的兑换记录」（仅本人）。
func (s *Service) UserRecords(ctx context.Context, principal Principal, params ListParams) (PageResult[RedeemRecord], error) {
	return s.listRecords(ctx, params, principal.ID)
}

func (s *Service) listRecords(ctx context.Context, params ListParams, scopeUser int64) (PageResult[RedeemRecord], error) {
	result, err := s.repo.ListRecords(ctx, params, scopeUser)
	if err != nil {
		return PageResult[RedeemRecord]{}, err
	}
	// scopeUser>0 即用户端「我的兑换记录」视图：行已限定本人，
	// username 属冗余信息，一律剥离（与设计文档口径一致）。
	selfView := scopeUser > 0
	items := make([]RedeemRecord, 0, len(result.Items))
	for _, row := range result.Items {
		item := RedeemRecord{
			ID: strconv.FormatInt(row.ID, 10), UserID: strconv.FormatInt(row.UserID, 10),
			Username: row.Username, CodeMask: maskCode(row.CodePlain),
			TypeID: strconv.FormatInt(row.TypeID, 10), TypeName: row.TypeName,
			RewardType: row.RewardType, RewardPoints: row.RewardPoints, RewardBalance: row.RewardBalance,
			Source: row.Source, CreateTime: formatTime(row.CreatedAt),
		}
		if selfView {
			item.Username = ""
		}
		if row.RewardName.Valid {
			item.RewardName = row.RewardName.String
		}
		if row.ValidDays.Valid {
			days := int(row.ValidDays.Int64)
			item.ValidDays = &days
		}
		items = append(items, item)
	}
	return PageResult[RedeemRecord]{Items: s.localizeRecordTypeNames(ctx, items), Total: result.Total}, nil
}

// localizeRecordTypeNames 按请求语言解析兑换记录的类型名；无译文时保留原文。
func (s *Service) localizeRecordTypeNames(ctx context.Context, items []RedeemRecord) []RedeemRecord {
	if s.localizer == nil || len(items) == 0 {
		return items
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if item.TypeID != "" {
			keys = append(keys, item.TypeID)
		}
	}
	overrides := s.localizer.LocalizeBatch(ctx, "redeem_type", keys)
	for index := range items {
		if name, ok := overrides[items[index].TypeID]["name"]; ok && name != "" {
			items[index].TypeName = name
		}
	}
	return items
}

// ---------------------------------------------------------------- 兑换

// Redeem 执行兑换：五层并发防线（行锁 / uk_user_code / type_lock /
// type_ip / 条件核销）单事务完成，发奖镜像 payment 守卫式口径。
// source=2（开放 API）豁免 IP 计数：插件/服务端集中出口 NAT 会把多用户
// 混成一个 IP，防刷依赖 per_user_limit + Key 频控 + 账号分钟档。
func (s *Service) Redeem(ctx context.Context, principal Principal, code string, clientIP string, source int) (RedeemResult, error) {
	plain, err := normalizeCode(code)
	if err != nil {
		return RedeemResult{}, mapRedeemError(errCodeInvalid)
	}
	if err := s.guard.check(ctx, s.db, principal.ID, clientIP); err != nil {
		return RedeemResult{}, mapRedeemError(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
	}
	defer func() { _ = tx.Rollback() }()

	locked, err := s.repo.LockCodeByPlain(ctx, tx, plain)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RedeemResult{}, mapRedeemError(errCodeInvalid)
		}
		return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
	}
	if locked.CodeStatus != CodeStatusUnused {
		if locked.CodeStatus == CodeStatusConsumed {
			return RedeemResult{}, mapRedeemError(errCodeUsed)
		}
		// 已作废与不存在统一文案（消除撞码存在性 oracle）。
		return RedeemResult{}, mapRedeemError(errCodeInvalid)
	}
	if locked.TypeStatus != 1 {
		return RedeemResult{}, mapRedeemError(errTypeDisabled)
	}
	now := time.Now()
	if now.Before(locked.StartAt) || !now.Before(locked.EndAt) {
		return RedeemResult{}, mapRedeemError(errTypeExpired)
	}

	// 每账号限领：1 次走 type_lock 唯一键（COUNT 仅作友好预检）；
	// N≥2 锁用户行后 COUNT；不限次不拦。
	var typeLock int64
	switch {
	case locked.PerUserLimit == 1:
		typeLock = locked.TypeID
		count, err := s.repo.CountUserTypeRecords(ctx, tx, principal.ID, locked.TypeID)
		if err != nil {
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
		}
		if count >= 1 {
			return RedeemResult{}, mapRedeemError(errLimitReached)
		}
	case locked.PerUserLimit >= 2:
		if err := s.repo.LockUserRow(ctx, tx, principal.ID); err != nil {
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
		}
		count, err := s.repo.CountUserTypeRecords(ctx, tx, principal.ID, locked.TypeID)
		if err != nil {
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
		}
		if count >= locked.PerUserLimit {
			return RedeemResult{}, mapRedeemError(errLimitReached)
		}
	}

	// 每 IP 限领（开放 API 来源豁免：集中出口 NAT）。
	var ipLock string
	if source != SourceOpenAPI && locked.PerIPLimit > 0 {
		if clientIP == "" {
			// 生效 IP 限额但取不到可信 IP ⇒ fail-closed（部署异常）。
			return RedeemResult{}, mapRedeemError(errIPUnavailable)
		}
		if locked.PerIPLimit == 1 {
			exists, err := s.repo.IPRecordExists(ctx, tx, locked.TypeID, clientIP)
			if err != nil {
				return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
			}
			if exists {
				return RedeemResult{}, mapRedeemError(errIPLimitReached)
			}
			ipLock = clientIP
		} else {
			used, err := s.repo.BumpIPCounter(ctx, tx, locked.TypeID, clientIP)
			if err != nil {
				return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
			}
			if used > int64(locked.PerIPLimit) {
				return RedeemResult{}, mapRedeemError(errIPLimitReached)
			}
		}
	}

	// 守卫式发奖（超限回滚）。
	rewardName := s.localizeRewardName(ctx, locked)
	validDays := locked.ValidDays.Int64
	switch locked.RewardType {
	case RewardTypePoints:
		if err := s.repo.AddPointsGuarded(ctx, tx, principal.ID, locked.RewardPoints); err != nil {
			if errors.Is(err, errNotFound) {
				return RedeemResult{}, mapRedeemError(errPointBalance)
			}
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "发放失败，请稍后重试", err)
		}
	case RewardTypeBalance:
		if err := s.repo.AddBalanceGuarded(ctx, tx, principal.ID, locked.RewardBalance); err != nil {
			if errors.Is(err, errNotFound) {
				return RedeemResult{}, mapRedeemError(errBalanceCapacity)
			}
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "发放失败，请稍后重试", err)
		}
	case RewardTypeGlobal:
		if err := s.repo.InsertGlobalPackage(ctx, tx, principal.ID, locked.RewardRefID); err != nil {
			if errors.Is(err, errNotFound) {
				return RedeemResult{}, mapRedeemError(errPackageDisabled)
			}
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "发放失败，请稍后重试", err)
		}
	case RewardTypeSpec:
		if err := s.repo.InsertInterfacePackage(ctx, tx, principal.ID, locked.RewardRefID); err != nil {
			if errors.Is(err, errNotFound) {
				return RedeemResult{}, mapRedeemError(errPackageDisabled)
			}
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "发放失败，请稍后重试", err)
		}
	case RewardTypeCombined:
		if err := s.repo.AddPointsGuarded(ctx, tx, principal.ID, locked.RewardPoints); err != nil {
			if errors.Is(err, errNotFound) {
				return RedeemResult{}, mapRedeemError(errPointBalance)
			}
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "发放失败，请稍后重试", err)
		}
		if err := s.repo.AddBalanceGuarded(ctx, tx, principal.ID, locked.RewardBalance); err != nil {
			if errors.Is(err, errNotFound) {
				return RedeemResult{}, mapRedeemError(errBalanceCapacity)
			}
			return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "发放失败，请稍后重试", err)
		}
	default:
		return RedeemResult{}, mapRedeemError(errPackageRefInvalid)
	}

	// 兑换记录（撞唯一键 = 并发重复，DB 层兜底）。
	if err := s.repo.InsertRecord(ctx, tx, recordInsert{
		UserID: principal.ID, CodeID: locked.CodeID, TypeID: locked.TypeID,
		TypeLock: typeLock, IPLock: ipLock,
		RewardType: locked.RewardType, RewardPoints: locked.RewardPoints, RewardBalance: locked.RewardBalance,
		RewardName: rewardName, ValidDays: validDays,
		ClientIP: clientIP, BatchID: locked.BatchID.Int64, Source: source,
	}); err != nil {
		switch {
		case IsDuplicateKey(err, "uk_redeem_record_user_type_lock"):
			return RedeemResult{}, mapRedeemError(errLimitReached)
		case IsDuplicateKey(err, "uk_redeem_record_type_ip"):
			return RedeemResult{}, mapRedeemError(errIPLimitReached)
		case IsDuplicateKey(err, "uk_redeem_record_user_code"):
			return RedeemResult{}, mapRedeemError(errCodeUsed)
		}
		return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
	}

	// 钱包流水：资产类各记一条；套餐类零额写入（一码一流水，审计对齐）。
	var balanceAfter string
	var pointsAfter int64
	switch locked.RewardType {
	case RewardTypePoints:
		balanceAfter, pointsAfter, err = s.repo.InsertWalletTx(ctx, tx, principal.ID, locked.CodeID, "0.0000", locked.RewardPoints, "兑换码奖励")
	case RewardTypeBalance:
		balanceAfter, pointsAfter, err = s.repo.InsertWalletTx(ctx, tx, principal.ID, locked.CodeID, locked.RewardBalance, 0, "兑换码奖励")
	case RewardTypeCombined:
		if _, _, err = s.repo.InsertWalletTx(ctx, tx, principal.ID, locked.CodeID, "0.0000", locked.RewardPoints, "兑换码奖励-点数"); err == nil {
			balanceAfter, pointsAfter, err = s.repo.InsertWalletTx(ctx, tx, principal.ID, locked.CodeID, locked.RewardBalance, 0, "兑换码奖励-余额")
		}
	default:
		balanceAfter, pointsAfter, err = s.repo.InsertWalletTx(ctx, tx, principal.ID, locked.CodeID, "0.0000", 0, "兑换码奖励-套餐")
	}
	if err != nil {
		return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
	}

	// 条件核销：affected!=1 即并发已用，整体回滚。
	if err := s.repo.ConsumeCode(ctx, tx, locked.CodeID, principal.ID); err != nil {
		return RedeemResult{}, apperrors.Wrap(consts.StatusConflict, "兑换码已被使用", err)
	}
	if err := tx.Commit(); err != nil {
		return RedeemResult{}, apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
	}
	result := RedeemResult{
		RewardType: locked.RewardType, RewardPoints: locked.RewardPoints, RewardBalance: locked.RewardBalance,
		BalanceAfter: balanceAfter, PointsAfter: pointsAfter,
	}
	if rewardName != "" {
		result.RewardName = rewardName
	}
	if validDays > 0 {
		days := int(validDays)
		result.ValidDays = &days
	}
	return result, nil
}

// localizeRewardName 按请求语言解析奖励名称。
// reward_type=3 → 全站套餐名；=4 → 「套餐名-规格名」。
// 无译文或字典不可用时返回锁定行里的原文快照。
func (s *Service) localizeRewardName(ctx context.Context, locked lockedCode) string {
	if s.localizer == nil {
		return locked.RewardRefName.String
	}
	if locked.RewardType == RewardTypeGlobal {
		if name, ok := s.localizer.Localize(ctx, "package_global", strconv.FormatInt(locked.RewardRefID, 10))["name"]; ok && name != "" {
			return name
		}
		return locked.RewardRefName.String
	}
	if locked.RewardType == RewardTypeSpec && locked.RefPackageID > 0 {
		pkg, okPkg := s.localizer.Localize(ctx, "package_interface", strconv.FormatInt(locked.RefPackageID, 10))["name"]
		spec, okSpec := s.localizer.Localize(ctx, "package_interface_spec", strconv.FormatInt(locked.RewardRefID, 10))["specName"]
		if okPkg && pkg != "" && okSpec && spec != "" {
			return pkg + "-" + spec
		}
	}
	return locked.RewardRefName.String
}

// mapRedeemError 把域哨兵/业务错误映射为 HTTP 文案。
func mapRedeemError(err error) error {
	switch {
	case errors.Is(err, errCodeInvalid):
		return apperrors.New(consts.StatusBadRequest, "兑换码不存在")
	case errors.Is(err, errCodeUsed):
		return apperrors.New(consts.StatusBadRequest, "兑换码已被使用")
	case errors.Is(err, errTypeDisabled):
		return apperrors.New(consts.StatusBadRequest, "该兑换码类型已停用")
	case errors.Is(err, errTypeExpired):
		return apperrors.New(consts.StatusBadRequest, "兑换码不在有效期内")
	case errors.Is(err, errLimitReached):
		return apperrors.New(consts.StatusBadRequest, "已达到该类型兑换码的限领次数")
	case errors.Is(err, errIPLimitReached):
		return apperrors.New(consts.StatusBadRequest, "该 IP 已达到限领次数")
	case errors.Is(err, errIPUnavailable):
		return apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	case errors.Is(err, errPointBalance):
		return apperrors.New(consts.StatusConflict, "账户点数已达到系统上限")
	case errors.Is(err, errBalanceCapacity):
		return apperrors.New(consts.StatusConflict, "账户余额已达到系统上限")
	case errors.Is(err, errPackageDisabled):
		return apperrors.New(consts.StatusConflict, "套餐已停用或不允许参与兑换")
	case errors.Is(err, errPackageRefInvalid):
		return apperrors.New(consts.StatusBadRequest, "套餐引用无效")
	case errors.Is(err, errGuardUnavailable):
		return apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	var bizErr *apperrors.BusinessError
	if errors.As(err, &bizErr) {
		return err
	}
	return apperrors.Wrap(consts.StatusInternalServerError, "服务繁忙，请稍后重试", err)
}

// ---------------------------------------------------------------- 解析工具

var typeCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{2,32}$`)

func parseMoneyField(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "0.0000", nil
	case string:
		return parseMoneyText(v)
	case float64:
		return parseMoneyText(strconv.FormatFloat(v, 'f', 4, 64))
	case int:
		return parseMoneyText(strconv.FormatInt(int64(v), 10))
	case int64:
		return parseMoneyText(strconv.FormatInt(v, 10))
	case json.Number:
		return parseMoneyText(v.String())
	default:
		return "", errors.New("unsupported money field")
	}
}

func parseMoneyText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "0.0000", nil
	}
	rat, err := decimalvalue.ParseNonNegative(trimmed)
	if err != nil {
		return "", err
	}
	canonical := rat.FloatString(4)
	limit, err := decimalvalue.ParseNonNegative(maxRewardBalance)
	if err != nil {
		return "", err
	}
	if rat.Cmp(limit) > 0 {
		return "", errors.New("money field exceeds limit")
	}
	return canonical, nil
}

var timeLayouts = []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02"}

func parseTimeField(text string) (time.Time, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return time.Time{}, errors.New("empty time")
	}
	for _, layout := range timeLayouts {
		if value, err := time.ParseInLocation(layout, trimmed, time.Local); err == nil {
			return value, nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}

func nullInt64String(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return strconv.FormatInt(value.Int64, 10)
}
