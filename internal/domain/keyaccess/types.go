package keyaccess

// InvokeKeyOption 是接口调试页与公开接口文档可见的密钥候选信息。
// 明文密钥通过单独的、受鉴权保护的读取接口按需获取。
type InvokeKeyOption struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MaskedKey       string `json:"maskedKey"`
	KeyScope        string `json:"keyScope"`
	ExpiresAt       string `json:"expiresAt,omitempty"`
	SecretAvailable bool   `json:"secretAvailable"`
}
