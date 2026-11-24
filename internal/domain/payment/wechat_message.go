package payment

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const wechatMessageValidity = 5 * time.Minute

func (c *WechatClient) requirePlatformVerifier() error {
	if c.platformPub == nil || c.platformKeyID == "" {
		return apperrors.New(consts.StatusServiceUnavailable, "微信平台验签公钥及对应ID未配置")
	}
	return nil
}

// Both notifications and successful HTTP responses use the same signed
// timestamp/nonce/body contract. Event creation time is not delivery freshness.
func (c *WechatClient) verifyPlatformMessage(ctx context.Context, header http.Header, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.requirePlatformVerifier(); err != nil {
		return err
	}
	read := func(name string, maxBytes int) (string, error) {
		values := header.Values(name)
		if len(values) != 1 {
			return "", fmt.Errorf("wechat signed header %s must occur once", name)
		}
		value := strings.TrimSpace(values[0])
		if value == "" || len(value) > maxBytes {
			return "", fmt.Errorf("wechat signed header %s is invalid", name)
		}
		return value, nil
	}
	serial, err := read("Wechatpay-Serial", 256)
	if err != nil {
		return err
	}
	if serial != c.platformKeyID {
		return errors.New("wechat verification key ID mismatch")
	}
	timestamp, err := read("Wechatpay-Timestamp", 20)
	if err != nil {
		return err
	}
	if err := validateWechatMessageTimestamp(timestamp, time.Now()); err != nil {
		return err
	}
	nonce, err := read("Wechatpay-Nonce", 128)
	if err != nil {
		return err
	}
	signature, err := read("Wechatpay-Signature", base64.StdEncoding.EncodedLen(c.platformPub.Size()))
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("wechat message signature encoding is invalid")
	}
	if err := verifySignature(timestamp+"\n"+nonce+"\n"+string(body)+"\n", raw, c.platformPub); err != nil {
		return fmt.Errorf("wechat message signature verification failed: %w", err)
	}
	return nil
}

func validateWechatMessageTimestamp(raw string, now time.Time) error {
	timestamp, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(timestamp, 10) != raw {
		return errors.New("wechat message timestamp is invalid")
	}
	// Compare against a small bounded window instead of subtracting attacker-
	// supplied int64 values, which could overflow before an absolute-value check.
	seconds := int64(wechatMessageValidity / time.Second)
	if timestamp <= now.Unix()-seconds || timestamp >= now.Unix()+seconds {
		return errors.New("wechat message timestamp is outside the validity window")
	}
	return nil
}

type wechatTransaction struct {
	AppID         string `json:"appid"`
	Mchid         string `json:"mchid"`
	SpAppID       string `json:"sp_appid"`
	SpMchid       string `json:"sp_mchid"`
	SubAppID      string `json:"sub_appid"`
	SubMchid      string `json:"sub_mchid"`
	OutTradeNo    string `json:"out_trade_no"`
	TransactionID string `json:"transaction_id"`
	TradeType     string `json:"trade_type"`
	TradeState    string `json:"trade_state"`
	Amount        struct {
		Total    *int64 `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Payer struct {
		OpenID    string `json:"openid"`
		SpOpenID  string `json:"sp_openid"`
		SubOpenID string `json:"sub_openid"`
	} `json:"payer"`
}

func configuredWechatApp(actual string, allowed ...string) bool {
	if actual == "" {
		return false
	}
	for _, candidate := range allowed {
		if candidate != "" && actual == candidate {
			return true
		}
	}
	return false
}

func (c *WechatClient) validateWechatTransaction(tx wechatTransaction) error {
	if c.isServiceProvider() {
		if tx.SpMchid != c.spMchid || tx.SubMchid != c.subMchid {
			return errors.New("wechat provider/sub-merchant mismatch")
		}
		if !configuredWechatApp(tx.SpAppID, c.spAppID) {
			return errors.New("wechat provider application mismatch")
		}
		if tx.SubAppID != "" && !configuredWechatApp(tx.SubAppID, c.subAppID, c.oaAppID, c.miniAppID, c.appAppID) {
			return errors.New("wechat sub-application mismatch")
		}
	} else {
		if tx.Mchid != c.mchid {
			return errors.New("wechat merchant mismatch")
		}
		if !configuredWechatApp(tx.AppID, c.appid, c.oaAppID, c.miniAppID, c.appAppID) {
			return errors.New("wechat application mismatch")
		}
	}
	if tx.OutTradeNo == "" {
		return errors.New("wechat transaction order number is missing")
	}
	if tx.Amount.Total == nil || *tx.Amount.Total <= 0 || tx.Amount.Currency != wechatDefaultCur {
		return errors.New("wechat transaction amount or currency is invalid")
	}
	if strings.EqualFold(tx.TradeState, "SUCCESS") && tx.TransactionID == "" {
		return errors.New("wechat paid transaction ID is missing")
	}
	return nil
}

func (c *WechatClient) wechatTransactionBuyer(tx wechatTransaction) string {
	if !c.isServiceProvider() {
		return tx.Payer.OpenID
	}
	if tx.SubAppID != "" {
		return tx.Payer.SubOpenID
	}
	return tx.Payer.SpOpenID
}
