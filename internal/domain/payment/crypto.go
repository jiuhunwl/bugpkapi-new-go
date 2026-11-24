package payment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

var ErrPaymentSecretMissing = errors.New("payment config secret is missing")

type ConfigCipher struct {
	secret string
}

func NewConfigCipher(secret string) *ConfigCipher {
	return &ConfigCipher{secret: strings.TrimSpace(secret)}
}

func (c *ConfigCipher) Encrypt(plain string) (string, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return "", nil
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	data := append(nonce, gcm.Seal(nil, nonce, []byte(plain), nil)...)
	return base64.StdEncoding.EncodeToString(data), nil
}

func (c *ConfigCipher) Decrypt(cipherText string) (string, error) {
	cipherText = strings.TrimSpace(cipherText)
	if cipherText == "" {
		return "", nil
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted payment config")
	}
	nonce, encrypted := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (c *ConfigCipher) gcm() (cipher.AEAD, error) {
	if strings.TrimSpace(c.secret) == "" {
		return nil, ErrPaymentSecretMissing
	}
	sum := sha256.Sum256([]byte(c.secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
