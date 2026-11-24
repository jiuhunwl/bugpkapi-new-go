package payment

import (
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// Normalize channel-owned product fields before querying or persisting an
// order; WeChat products are not Alipay PAGE/WAP/FACE product values.
func normalizeOnlineOrderRequest(channel string, req CreateOrderRequest) (CreateOrderRequest, error) {
	if channel != ChannelWechat && channel != ChannelHupijiaoWechat {
		var err error
		req.Product, err = normalizeProduct(req.Product)
		return req, err
	}
	product := strings.ToUpper(strings.TrimSpace(req.Product))
	trade := strings.ToUpper(strings.TrimSpace(req.TradeType))
	legacy := product == "PAGE" || product == "WAP" || product == "FACE"
	if trade == "" {
		if product == "" || legacy {
			trade = TradeTypeNative
		} else {
			trade = product
		}
	} else if product != "" && !legacy && product != trade {
		return req, apperrors.New(consts.StatusBadRequest, "微信产品与交易类型不一致")
	}
	switch trade {
	case TradeTypeNative, TradeTypeH5, TradeTypeJSAPI:
	case TradeTypeMini, TradeTypeAPP:
		if channel != ChannelWechat {
			return req, apperrors.New(consts.StatusBadRequest, "所选渠道不支持该交易类型")
		}
	default:
		return req, apperrors.New(consts.StatusBadRequest, "微信交易类型不合法")
	}
	req.Product, req.TradeType = trade, trade
	req.OpenID = strings.TrimSpace(req.OpenID)
	return req, nil
}
