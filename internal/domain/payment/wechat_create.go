package payment

import (
	"net"
	"net/url"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type wechatCreatePlan struct {
	path      string
	tradeType string
	appID     string
	body      map[string]any
}

func (c *WechatClient) prepareCreate(order PaymentOrder) (wechatCreatePlan, error) {
	var plan wechatCreatePlan
	bad := func(message string) (wechatCreatePlan, error) {
		return plan, apperrors.New(consts.StatusBadRequest, message)
	}
	if err := c.requirePlatformVerifier(); err != nil {
		return plan, err
	}
	total, err := amountToFen(order.PayAmount)
	if err != nil {
		return plan, err
	}
	if strings.TrimSpace(order.OrderNo) == "" {
		return bad("微信商户订单号不能为空")
	}
	if c.notifyURL == "" {
		return bad("微信异步通知地址不能为空")
	}
	if err := validateAlipayCallbackURL(c.notifyURL, "微信异步通知地址", false); err != nil {
		return plan, err
	}
	notifyURL, _ := url.Parse(c.notifyURL) // The shared HTTPS validation above already parsed this URL.
	if notifyURL.RawQuery != "" || notifyURL.ForceQuery {
		return bad("微信异步通知地址不能包含查询参数")
	}
	if strings.Contains(c.notifyURL, "#") {
		return bad("微信异步通知地址不能包含 URL 片段")
	}
	if (c.spMchid == "") != (c.subMchid == "") {
		return bad("微信服务商和子商户必须同时配置")
	}
	partner := c.isServiceProvider()
	if partner && c.spAppID == "" {
		return bad("微信服务商应用ID未配置")
	}
	trade := strings.ToUpper(strings.TrimSpace(order.TradeType))
	suffix := ""
	switch trade {
	case TradeTypeNative:
		suffix = "native"
	case TradeTypeH5:
		suffix = "h5"
	case TradeTypeJSAPI, TradeTypeMini:
		suffix = "jsapi"
	case TradeTypeAPP:
		suffix = "app"
	default:
		return bad("不支持的微信交易类型")
	}
	plan.tradeType = trade
	plan.path = "/v3/pay/transactions/" + suffix
	description := strings.TrimSpace(order.Subject)
	if description == "" {
		description = "BugPk 充值"
	}
	plan.body = map[string]any{"description": description, "out_trade_no": order.OrderNo, "notify_url": c.notifyURL, "amount": wechatAmountBody{Total: total, Currency: wechatDefaultCur}}
	if partner {
		plan.path = "/v3/pay/partner/transactions/" + suffix
		plan.body["sp_mchid"], plan.body["sp_appid"], plan.body["sub_mchid"] = c.spMchid, c.spAppID, c.subMchid
	}
	if trade == TradeTypeNative || trade == TradeTypeH5 {
		if partner {
			if c.subAppID != "" {
				plan.body["sub_appid"] = c.subAppID
			}
		} else {
			if c.appid == "" {
				return bad("微信应用ID未配置")
			}
			plan.body["appid"], plan.body["mchid"] = c.appid, c.mchid
		}
	} else {
		app := ""
		switch trade {
		case TradeTypeJSAPI:
			app = c.oaAppID
		case TradeTypeMini:
			app = c.miniAppID
		case TradeTypeAPP:
			app = c.appAppID
		}
		payerKey := "openid"
		if partner {
			if app == "" {
				app = c.subAppID
			}
			if app == "" || app == c.spAppID {
				app = c.spAppID
				payerKey = "sp_openid"
			} else {
				plan.body["sub_appid"] = app
				payerKey = "sub_openid"
			}
		} else {
			if app == "" {
				app = c.appid
			}
			if app == "" {
				return bad("微信支付应用ID未配置")
			}
			plan.body["appid"], plan.body["mchid"] = app, c.mchid
		}
		plan.appID = app
		if trade == TradeTypeJSAPI || trade == TradeTypeMini {
			openid := strings.TrimSpace(order.BuyerID)
			if openid == "" || len(openid) > 128 {
				return bad("微信JSAPI/小程序下单需要有效openid")
			}
			plan.body["payer"] = map[string]string{payerKey: openid}
		}
	}
	if trade == TradeTypeH5 {
		ip := net.ParseIP(strings.TrimSpace(order.ClientIP))
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return bad("微信H5下单缺少有效的调用方IP")
		}
		plan.body["scene_info"] = map[string]any{"payer_client_ip": ip.String(), "h5_info": map[string]string{"type": "Wap"}}
	}
	return plan, nil
}
