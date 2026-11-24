package payment

import (
	"context"
	"net/http"
)

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

// 支付通道与产品常量（微信支付 v3 增量接入）。
const (
	ChannelBalance = "BALANCE"
	ChannelAlipay  = "ALIPAY"
	ChannelWechat  = "WECHAT"

	// 微信交易类型。
	TradeTypeNative = "NATIVE"
	TradeTypeH5     = "H5"
	TradeTypeJSAPI  = "JSAPI"
	TradeTypeAPP    = "APP"
	TradeTypeMini   = "MINI"
)

// PayGateway 统一支付网关契约。
// 微信 v3 由 WechatClient 完整实现；支付宝沿用既有 AlipayClient，不强制实现本接口以保证零回归。
type PayGateway interface {
	// Create 按 order.TradeType 路由到对应微信下单接口，返回统一结果。
	Create(ctx context.Context, order PaymentOrder) (PayCreateResult, error)
	// Query 按商户单号查单。
	Query(ctx context.Context, outTradeNo string) (PayTradeStatus, error)
	// Close 关单。
	Close(ctx context.Context, outTradeNo string) error
	// VerifyNotify 校验并解密微信回调，返回标准化 PayNotify。
	VerifyNotify(ctx context.Context, body []byte, headers map[string]string) (*PayNotify, error)
}

// PayCreateResult 通道无关的下单结果（覆盖支付宝 PayForm/PayURL/QRCode 与微信 CodeURL/H5URL/PrepayID）。
type PayCreateResult struct {
	CodeURL   string `json:"codeUrl,omitempty"`   // NATIVE: 二维码内容
	H5URL     string `json:"h5Url,omitempty"`     // H5: 跳转 URL
	PrepayID  string `json:"prepayId,omitempty"`  // JSAPI/APP/MINI: 预支付ID
	PaySign   string `json:"paySign,omitempty"`   // JSAPI/APP: 二次签名串
	NonceStr  string `json:"nonceStr,omitempty"`  // JSAPI/APP: 随机串
	Timestamp string `json:"timestamp,omitempty"` // JSAPI/APP: 签名时间戳(秒)
	PayParams string `json:"payParams,omitempty"` // APP: 完整调起参数字典 JSON
}

// PayTradeStatus 通道无关的交易状态（微信 transaction_id / 支付宝 trade_no）。
type PayTradeStatus struct {
	OutTradeNo string
	TradeNo    string // 微信 transaction_id
	TradeState string // SUCCESS / CLOSED / ...
	Total      string // 金额（4 位小数字符串，与订单 pay_amount 同口径）
	BuyerID    string // 微信 openid
	Success    bool
}

// PayNotify 通道无关的通知（微信/支付宝回调标准化）。
type PayNotify struct {
	NotifyID   string // Identity from the verified notification envelope, not a request header.
	OutTradeNo string
	TradeNo    string
	TradeState string
	Total      string
	BuyerID    string
	Success    bool
}

type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

type ListParams struct {
	Keyword   string
	Page      int
	PageSize  int
	Status    string
	OrderType string
	SortBy    string
	SortOrder string
	PaidFrom  string
	PaidTo    string
	MinAmount string
	MaxAmount string
}

type AlipayConfig struct {
	AlipayPublicKey string `json:"alipayPublicKey,omitempty"`
	AppID           string `json:"appId"`
	AppPrivateKey   string `json:"appPrivateKey,omitempty"`
	Enabled         bool   `json:"enabled"`
	GatewayURL      string `json:"gatewayUrl"`
	Mode            string `json:"mode"`
	NotifyURL       string `json:"notifyUrl,omitempty"`
	ReturnURL       string `json:"returnUrl,omitempty"`
	SellerID        string `json:"sellerId,omitempty"`
}

type SaveAlipayConfigRequest struct {
	AlipayPublicKey string `json:"alipayPublicKey"`
	AppID           string `json:"appId"`
	AppPrivateKey   string `json:"appPrivateKey"`
	Enabled         bool   `json:"enabled"`
	GatewayURL      string `json:"gatewayUrl"`
	Mode            string `json:"mode"`
	NotifyURL       string `json:"notifyUrl"`
	ReturnURL       string `json:"returnUrl"`
	SellerID        string `json:"sellerId"`
}

type RechargePreset struct {
	Amount     string `json:"amount"`
	GiftAmount string `json:"giftAmount"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	SortOrder  int    `json:"sortOrder"`
	Status     int    `json:"status"`
	UpdateTime string `json:"updateTime,omitempty"`
}

type SaveRechargePresetRequest struct {
	Amount     any    `json:"amount"`
	GiftAmount any    `json:"giftAmount"`
	Name       string `json:"name"`
	SortOrder  int    `json:"sortOrder"`
	Status     *int   `json:"status"`
}

type CreateOrderRequest struct {
	ClientIP string `json:"-"` // Set only by the trusted server request context.
	Product  string `json:"product"`
	PresetID int64  `json:"presetId"`
	// 支付通道：ALIPAY（默认，支付宝旧逻辑）或 WECHAT（微信 v3 增量）。
	PayChannel string `json:"payChannel,omitempty"`
	// 微信交易类型：NATIVE/H5/JSAPI/APP/MINI。仅当 PayChannel=WECHAT 时生效。
	TradeType string `json:"tradeType,omitempty"`
	// 微信 JSAPI/小程序下单所需的用户唯一标识（公众号/小程序 openid）。
	// 调用方需提供与所选应用对应的 openid；这里不隐式执行 code 换取。
	OpenID string `json:"openId,omitempty"`
	// ForceNew 表示用户已确认存在相同商品的待支付订单，仍要创建新订单。
	// 未带此标记时，服务端会拒绝重复创建并提示继续支付旧订单。
	ForceNew bool `json:"forceNew,omitempty"`
}

type PaymentOrder struct {
	ClientIP       string            `json:"-"`
	PayParams      string            `json:"payParams,omitempty"` // Fresh JSAPI/MINI/APP invocation data in the create response.
	AlipayTradeNo  string            `json:"alipayTradeNo,omitempty"`
	BuyerID        string            `json:"buyerId,omitempty"`
	ChannelTradeNo string            `json:"channelTradeNo,omitempty"` // 通道交易号（微信 transaction_id）
	CreateTime     string            `json:"createTime,omitempty"`
	ExpireTime     string            `json:"expireTime"`
	FormActionURL  string            `json:"formActionUrl,omitempty"`
	FormParams     map[string]string `json:"formParams,omitempty"`
	GiftAmount     string            `json:"giftAmount,omitempty"`
	GrantPoints    int64             `json:"grantPoints,omitempty"`
	ID             string            `json:"id,omitempty"`
	OrderNo        string            `json:"orderNo"`
	OrderType      string            `json:"orderType"`
	PaidTime       string            `json:"paidTime,omitempty"`
	PayAmount      string            `json:"payAmount"`
	PayChannel     string            `json:"payChannel"`
	// Currency 本单结算货币（CNY/USD），下单时按站点配置落库。
	Currency string `json:"currency,omitempty"`
	// OriginAmount USD 结算时的商品原价（人民币口径），仅换算过的订单有值。
	OriginAmount string `json:"originAmount,omitempty"`
	// TRC20 链上信息：收款地址快照、精确金额（USDT）、交易号与确认数。
	ChainAddress      string `json:"chainAddress,omitempty"`
	ChainAmount       string `json:"chainAmount,omitempty"`
	ChainTxID         string `json:"chainTxid,omitempty"`
	ChainConfirmCount int    `json:"chainConfirmCount,omitempty"`
	PayForm           string `json:"payForm,omitempty"`
	PayURL            string `json:"payUrl,omitempty"`
	PrepayID          string `json:"prepayId,omitempty"` // 微信预支付ID
	Product           string `json:"product"`
	QRCode            string `json:"qrCode,omitempty"`
	RemainingSecs     int64  `json:"remainingSeconds"`
	Status            string `json:"status"`
	Subject           string `json:"subject"`
	TargetID          string `json:"targetId,omitempty"`
	TradeType         string `json:"tradeType,omitempty"` // 微信交易类型
	UserID            string `json:"userId,omitempty"`
	Username          string `json:"username,omitempty"`
}

// AssetRecord is one row of bp_wallet_transaction exposed to the admin payment
// record page. Only income/purchase/adjustment ledger types are listed so
// per-call OPEN_CALL_* reservations cannot flood the audit view.
type AssetRecord struct {
	ID           string `json:"id"`
	UserID       string `json:"userId"`
	Username     string `json:"username"`
	DisplayName  string `json:"displayName,omitempty"`
	BizType      string `json:"bizType"`
	BizID        string `json:"bizId,omitempty"`
	Amount       string `json:"amount"`
	Points       int64  `json:"points"`
	BalanceAfter string `json:"balanceAfter"`
	PointsAfter  int64  `json:"pointsAfter"`
	OperatorName string `json:"operatorName,omitempty"`
	Remark       string `json:"remark"`
	CreateTime   string `json:"createTime"`
}

type AssetRecordListParams struct {
	BizType  string
	DateFrom string
	DateTo   string
	Keyword  string
	Page     int
	PageSize int
}

type PaymentCapabilities struct {
	AlipayEnabled bool `json:"alipayEnabled"`
	// Currency 站点结算货币（CNY/USD），前端据此渲染货币符号。
	Currency string `json:"currency"`
	// USDCNYRate 美元结算时的人民币汇率（1 USD = X CNY），供前端做展示换算。
	USDCNYRate string `json:"usdCnyRate,omitempty"`
	// DisplayName 用户端货币展示名称（如积分、赞助额度）；空=按结算货币显示 ¥/$。
	// 只替换称呼，不参与金额或汇率换算。
	DisplayName string `json:"displayName,omitempty"`
	// TRC20/USDT 链上充值通道（海外站），未启用时前端不展示。
	Trc20Enabled       bool   `json:"trc20Enabled"`
	Trc20Address       string `json:"trc20Address,omitempty"`
	Trc20Network       string `json:"trc20Network,omitempty"`
	Trc20MinAmount     string `json:"trc20MinAmount,omitempty"`
	Trc20MaxAmount     string `json:"trc20MaxAmount,omitempty"`
	Trc20Confirmations int    `json:"trc20Confirmations,omitempty"`
	// BEP20/USDT 链上充值通道（BSC），与 TRC20 并列。
	Bep20Enabled       bool             `json:"bep20Enabled"`
	Bep20Address       string           `json:"bep20Address,omitempty"`
	Bep20Network       string           `json:"bep20Network,omitempty"`
	Bep20MinAmount     string           `json:"bep20MinAmount,omitempty"`
	Bep20MaxAmount     string           `json:"bep20MaxAmount,omitempty"`
	Bep20Confirmations int              `json:"bep20Confirmations,omitempty"`
	Methods            []PaymentMethod  `json:"methods"`
	Mode               string           `json:"mode,omitempty"`
	Products           []string         `json:"products"`
	Presets            []RechargePreset `json:"presets"`
	// 微信支付是否已启用（来自微信配置）。
	WechatEnabled bool `json:"wechatEnabled"`
	// 微信已启用的交易方式，如 ["NATIVE","H5"]。
	WechatProducts []string `json:"wechatProducts"`
}

// PaymentMethod is the server-authoritative list of checkout methods. A
// disabled method is never accepted for order creation even if a client sends
// its code directly.
type PaymentMethod struct {
	Code      string `json:"code"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sortOrder"`
}

type SavePaymentMethodsRequest struct {
	Methods []PaymentMethod `json:"methods"`
}

// HupijiaoConfigView groups the separately-issued Alipay and WeChat
// credentials while keeping appsecret values masked at the API boundary.
type HupijiaoConfigView struct {
	Alipay HupijiaoChannelConfig `json:"alipay"`
	Wechat HupijiaoChannelConfig `json:"wechat"`
}

type HupijiaoChannelConfig struct {
	AppID       string `json:"appId"`
	AppSecret   string `json:"appSecret,omitempty"`
	CallbackURL string `json:"callbackUrl,omitempty"`
	CreateURL   string `json:"createUrl"`
	Enabled     bool   `json:"enabled"`
	Method      string `json:"method"`
	NotifyURL   string `json:"notifyUrl,omitempty"`
	QueryURL    string `json:"queryUrl"`
	ReturnURL   string `json:"returnUrl,omitempty"`
	WAPName     string `json:"wapName,omitempty"`
	WAPURL      string `json:"wapUrl,omitempty"`
}

type SaveHupijiaoConfigRequest struct {
	AppID       string `json:"appId"`
	AppSecret   string `json:"appSecret"`
	CallbackURL string `json:"callbackUrl"`
	CreateURL   string `json:"createUrl"`
	Enabled     bool   `json:"enabled"`
	Method      string `json:"method"`
	NotifyURL   string `json:"notifyUrl"`
	QueryURL    string `json:"queryUrl"`
	ReturnURL   string `json:"returnUrl"`
	WAPName     string `json:"wapName"`
	WAPURL      string `json:"wapUrl"`
}

// WechatConfig 微信支付 v3 配置（镜像 AlipayConfig）。
// 敏感字段 APIv3Key/商户私钥/平台证书经 ConfigCipher 加密落库，接口返回 ****** 掩码。
type WechatConfig struct {
	// Optional programmatic transport; never supplied by JSON or persisted.
	HTTPClient          *http.Client `json:"-"`
	ID                  int64        `json:"id"`
	Enabled             bool         `json:"enabled"`
	Mode                string       `json:"mode"` // SANDBOX / PRODUCTION
	Mchid               string       `json:"mchid"`
	AppID               string       `json:"appId"`
	APIv3Key            string       `json:"apiV3Key,omitempty"`            // 落库加密(cipher)
	PrivateKeyPEM       string       `json:"privateKey,omitempty"`          // 落库加密(cipher)，商户 API 证书私钥
	SerialNo            string       `json:"serialNo,omitempty"`            // 商户 API 证书序列号
	PlatformPublicKeyID string       `json:"platformPublicKeyId,omitempty"` // 微信平台证书序列号
	PlatformPublicKey   string       `json:"platformPublicKey,omitempty"`   // 落库加密(cipher)，可选（留空则运行期下载缓存）
	NotifyURL           string       `json:"notifyUrl,omitempty"`
	// 服务商模式（可选，默认商户模式）。
	SpMchid  string `json:"spMchid,omitempty"`
	SpAppID  string `json:"spAppId,omitempty"`
	SubMchid string `json:"subMchid,omitempty"`
	SubAppID string `json:"subAppId,omitempty"`
	// 多端 AppID（可选）。
	OAAppID   string `json:"oaAppId,omitempty"`   // 公众号 appid（JSAPI）
	MiniAppID string `json:"miniAppId,omitempty"` // 小程序 appid
	AppAppID  string `json:"appAppId,omitempty"`  // APP appid
}

// SaveWechatConfigRequest 微信配置保存/校验请求。
type SaveWechatConfigRequest struct {
	Enabled             bool   `json:"enabled"`
	Mode                string `json:"mode"`
	Mchid               string `json:"mchid"`
	AppID               string `json:"appId"`
	APIv3Key            string `json:"apiV3Key"`
	PrivateKeyPEM       string `json:"privateKey"`
	SerialNo            string `json:"serialNo"`
	PlatformPublicKeyID string `json:"platformPublicKeyId"`
	PlatformPublicKey   string `json:"platformPublicKey"`
	NotifyURL           string `json:"notifyUrl"`
	SpMchid             string `json:"spMchid"`
	SpAppID             string `json:"spAppId"`
	SubMchid            string `json:"subMchid"`
	SubAppID            string `json:"subAppId"`
	OAAppID             string `json:"oaAppId"`
	MiniAppID           string `json:"miniAppId"`
	AppAppID            string `json:"appAppId"`
}

type OrderSeed struct {
	ClientIP    string
	GiftAmount  string
	GrantPoints int64
	OrderNo     string
	OrderType   string
	PayAmount   string
	Product     string
	Snapshot    string
	Subject     string
	TargetID    int64
	UserID      int64
	PayChannel  string
	// Currency 本单结算货币；USD 时 PayAmount 已按汇率换算，原价在 OriginAmount。
	Currency     string
	OriginAmount string
	// TRC20 链上字段：地址快照、精确到账金额（USDT）。
	ChainAddress string
	ChainAmount  string
	// 微信交易类型（NATIVE/H5/JSAPI/APP/MINI），仅微信通道使用。
	TradeType string
	// 微信 JSAPI/小程序下单所需的 openid（仅微信通道使用）。
	OpenID string
	// StockReserved 表示本次下单已占用套餐库存（仅限量套餐为 true）。
	StockReserved bool
}

type AlipayCreateResult struct {
	FormActionURL string
	FormParams    map[string]string
	PayForm       string
	PayURL        string
	QRCode        string
}

type AlipayTradeStatus struct {
	BuyerID     string
	OutTradeNo  string
	SellerID    string
	TotalAmount string
	TradeNo     string
	TradeStatus string
	Success     bool
}
