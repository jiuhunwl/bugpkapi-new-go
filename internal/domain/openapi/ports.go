package openapi

import (
	"context"

	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/domain/packagepkg"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/redeem"
	userdomain "bugpk-api/internal/domain/user"
)

// 编排端口：开放 API 只做身份适配与聚合，不开事务、不碰他域仓储。
// 各域 service 原样实现这些接口（路由装配时注入），因此页面侧既有校验
// 在开放 API 上天然生效，不存在旁路能力。

// AccountSource 提供只读账户快照（user 域实现）。
type AccountSource interface {
	AccountSnapshot(ctx context.Context, userID int64) (userdomain.AccountSnapshot, error)
}

// PackageSource 提供账号权益富快照（packagepkg 域实现）。
type PackageSource interface {
	UserPackageDetails(ctx context.Context, userID int64) ([]packagepkg.UserPackageDetail, error)
}

// GatewaySource 提供数据面接口 Key 的创建与查询（gateway 域实现）。
type GatewaySource interface {
	CreateKey(ctx context.Context, principal rbac.Principal, req gateway.SaveKeyRequest) (gateway.CreateKeyResult, error)
	KeyDetail(ctx context.Context, principal rbac.Principal, id int64) (gateway.APIKeyItem, error)
}

// PaymentSource 提供三类套餐下单入口（payment 域实现）。
type PaymentSource interface {
	CreateGlobalPackageOrder(ctx context.Context, principal payment.Principal, packageID int64, req payment.CreateOrderRequest) (payment.PaymentOrder, error)
	CreateInterfacePackageOrder(ctx context.Context, principal payment.Principal, specID int64, req payment.CreateOrderRequest) (payment.PaymentOrder, error)
	CreatePointPackageOrder(ctx context.Context, principal payment.Principal, packageID int64, req payment.CreateOrderRequest) (payment.PaymentOrder, error)
}

// RedeemSource 提供兑换与 CDK 生成（redeem 域实现；生成端点自带第二层
// RBAC 兜底 Require，与编排层显式 Require 组成 S-8 双层校验）。
type RedeemSource interface {
	Redeem(ctx context.Context, principal redeem.Principal, code string, clientIP string, source int) (redeem.RedeemResult, error)
	// TypeIDByCode 把业务标识（typeCode）解析成数据库自增 ID。外部插件
	// 在配置里写的是标识而不是 ID，见 generateCodesRequest.TypeCode。
	TypeIDByCode(ctx context.Context, code string) (int64, error)
	GenerateCodesForOpenAPI(ctx context.Context, principal redeem.Principal, typeID int64, req redeem.GenerateCodesRequest, meta redeem.AuditMeta, creatorKeyID int64) (redeem.GenerateResult, error)
}
