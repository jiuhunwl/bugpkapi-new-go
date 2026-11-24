package payment

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// wechatAPIv3HeaderPrefix 是微信 v3 鉴权头前缀。
const wechatAPIv3HeaderPrefix = "WECHATPAY2-SHA256-RSA2048"

// buildAuthorization 生成微信 v3 请求鉴权头（Authorization）。
// 签名串为 METHOD\nURL\nTIMESTAMP\nNONCE\nBODY\n，对该串做 SHA256-RSA2048 签名后
// 与 mchid/nonce_str/timestamp/serial_no 一起拼成 WECHATPAY2-SHA256-RSA2048 头。
// 商户身份由调用方的可信签名配置显式传入，不能留空占位。
func buildAuthorization(method, path, body, timestamp, nonce, merchantID string, privateKey *rsa.PrivateKey, serialNo string) (string, error) {
	messages := []string{
		strings.ToUpper(strings.TrimSpace(method)),
		path,
		timestamp,
		nonce,
		body,
		"",
	}
	message := strings.Join(messages, "\n")
	signature, err := signWechatPay(message, privateKey)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s mchid=\"%s\",nonce_str=\"%s\",signature=\"%s\",timestamp=\"%s\",serial_no=\"%s\"",
		wechatAPIv3HeaderPrefix, merchantID, nonce, signature, timestamp, serialNo), nil
}

// signWechatPay 对 message 做 SHA256-RSA2048 签名并返回 base64 结果。
// 微信 v3 的 Authorization、JSAPI/APP 二次签名均使用此算法。
func signWechatPay(message string, privateKey *rsa.PrivateKey) (string, error) {
	if privateKey == nil {
		return "", errors.New("wechat private key is nil")
	}
	sum := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign wechat request: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// verifySignature 使用平台证书公钥对 message + sig（base64）做 SHA256-RSA 验签。
func verifySignature(message string, signature []byte, platformPub *rsa.PublicKey) error {
	if platformPub == nil {
		return errors.New("wechat platform public key is nil")
	}
	sum := sha256.Sum256([]byte(message))
	return rsa.VerifyPKCS1v15(platformPub, crypto.SHA256, sum[:], signature)
}

// decryptResource 用 APIv3Key（32 字节）对微信回调密文做 AES-256-GCM 解密。
// nonce 作为 IV（微信固定 12 字节），associatedData 作为 additionalData；
// GCM tag 位于 ciphertext 末尾 16 字节，cipher.NewGCM 的 Open 会自动处理。
// ciphertext 为原始密文字节串（调用方需先对微信 JSON 里的 base64 密文解码）。
func decryptResource(ciphertext, nonce, associatedData, apiV3Key string) ([]byte, error) {
	key := []byte(apiV3Key)
	if len(key) != 32 {
		return nil, apperrors.New(consts.StatusInternalServerError, "微信 APIv3Key 长度必须为 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	nonceBytes := []byte(nonce)
	if len(nonceBytes) != gcm.NonceSize() {
		return nil, apperrors.New(consts.StatusBadRequest, "微信回调随机数长度不合法")
	}
	plaintext, err := gcm.Open(nil, nonceBytes, []byte(ciphertext), []byte(associatedData))
	if err != nil {
		return nil, apperrors.Wrap(consts.StatusBadRequest, "微信回调资源解密失败", err)
	}
	return plaintext, nil
}

// amountToFen 将 4 位小数字符串金额换算为「分」整数：
//   - 拒绝 < ¥0.01（即换算后 < 1 分）的订单，避免资损/对账差异；
//   - 金额必须能精确表示为整数分，禁止静默舍入导致到账金额不匹配；
//   - 超过系统上限（maxPaymentMoney）报错。
func amountToFen(decimalStr string) (int64, error) {
	rat, err := decimalvalue.ParseNonNegative(strings.TrimSpace(decimalStr))
	if err != nil {
		if errors.Is(err, decimalvalue.ErrNegative) {
			return 0, apperrors.New(consts.StatusBadRequest, "金额不能为负数")
		}
		if errors.Is(err, decimalvalue.ErrExponentRange) || errors.Is(err, decimalvalue.ErrTextTooLong) {
			return 0, apperrors.New(consts.StatusBadRequest, "金额超过系统允许范围")
		}
		return 0, apperrors.New(consts.StatusBadRequest, "金额格式不合法")
	}
	// 拒绝小于 1 分（0.01 元）的金额。
	if rat.Cmp(big.NewRat(1, 100)) < 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "支付金额不能小于 0.01 元")
	}
	fen := new(big.Rat).Mul(rat, big.NewRat(100, 1))
	if !fen.IsInt() {
		return 0, apperrors.New(consts.StatusBadRequest, "微信支付金额必须精确到分")
	}
	maxFen := new(big.Rat).Mul(maxPaymentMoney, big.NewRat(100, 1))
	if fen.Cmp(maxFen) > 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "金额超过系统允许范围")
	}
	return fen.Num().Int64(), nil
}
