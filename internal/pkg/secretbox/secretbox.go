package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const encryptedPrefix = "enc:v1:"

var (
	ErrDecrypt       = errors.New("system config decryption failed")
	ErrSecretMissing = errors.New("system config secret is missing")
)

type Cipher struct {
	secret string
}

func New(secret string) *Cipher {
	return &Cipher{secret: strings.TrimSpace(secret)}
}

func IsEncrypted(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), encryptedPrefix)
}

func (c *Cipher) Encrypt(plain string) (string, error) {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return "", nil
	}
	if IsEncrypted(plain) {
		return "", errors.New("refusing to encrypt an already encrypted secret")
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	data := append(nonce, sealed...)
	return encryptedPrefix + base64.RawStdEncoding.EncodeToString(data), nil
}

func (c *Cipher) DecryptCompatible(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !IsEncrypted(value) {
		return value, nil
	}
	return c.decrypt(value)
}

func (c *Cipher) decrypt(value string) (string, error) {
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	encoded := strings.TrimPrefix(strings.TrimSpace(value), encryptedPrefix)
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("%w: decode ciphertext: %v", ErrDecrypt, err)
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("%w: ciphertext is too short", ErrDecrypt)
	}
	nonce, encrypted := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return string(plain), nil
}

func (c *Cipher) gcm() (cipher.AEAD, error) {
	if len([]byte(c.secret)) < 32 {
		return nil, ErrSecretMissing
	}
	sum := sha256.Sum256([]byte("BugPk-Api:system-config:v1\x00" + c.secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
