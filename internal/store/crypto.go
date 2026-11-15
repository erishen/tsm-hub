package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// secretPrefix 是加密密文的标识前缀。
// 落盘格式：enc:v1:<base64(nonce||aes-gcm-ciphertext)>。
const secretPrefix = "enc:v1:"

// masterKeyEnv 是 AES-256 主密钥的环境变量名。
// 未设置时所有密钥保持明文落盘（完全向后兼容现有部署）。
const masterKeyEnv = "TSM_HUB_MASTER_KEY"

// masterKey 返回 32 字节加密主密钥。
// 支持两种格式：
//   - 64 个十六进制字符（32 字节）
//   - base64 标准编码的 32 字节（44 字符）
//
// 未设置时返回 ok=false（调用方应退化为明文，向后兼容）。
// 设置了但格式错误时返回 error（不让用错误密钥悄悄写入）。
func masterKey() (key []byte, ok bool, err error) {
	raw := strings.TrimSpace(os.Getenv(masterKeyEnv))
	if raw == "" {
		return nil, false, nil
	}
	// 先试 hex（64 字符 = 32 字节），再试标准 base64（44 字符 = 32 字节）。
	// 直接校验解码结果长度，而非靠字符串长度阈值预判格式。
	if b, derr := hex.DecodeString(raw); derr == nil && len(b) == 32 {
		return b, true, nil
	}
	if b, derr := base64.StdEncoding.DecodeString(raw); derr == nil && len(b) == 32 {
		return b, true, nil
	}
	return nil, false, fmt.Errorf("%s must be 32 bytes: 64 hex chars or 44-char base64; got len=%d", masterKeyEnv, len(raw))
}

// Seal 对明文做 AES-256-GCM 加密，返回带前缀的密文。
// 以下情况原样返回（不加密）：空串、已是密文（带前缀）、env: 引用（不含真实密钥）。
// 未配置主密钥时返回明文（向后兼容）。
func Seal(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, secretPrefix) || strings.HasPrefix(plain, EnvKeyPrefix) {
		return plain, nil
	}
	key, ok, err := masterKey()
	if err != nil {
		return "", err
	}
	if !ok {
		return plain, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return secretPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open 解密 Seal 的产物。无前缀视为明文原样返回。
// 密文但主密钥缺失或错误时返回 error（配置无法打开，fail-closed）。
func Open(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, secretPrefix) {
		return ciphertext, nil
	}
	key, ok, err := masterKey()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("config contains encrypted secrets but %s is not set", masterKeyEnv)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("encrypted secret too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret (wrong master key?): %w", err)
	}
	return string(plain), nil
}

// IsEncryptedSecret 报告 s 是否已是本包的加密密文。供安全自检等外部判断使用。
func IsEncryptedSecret(s string) bool {
	return strings.HasPrefix(s, secretPrefix)
}

// SealConfig 就地加密配置里所有 Provider.APIKey（有主密钥且非已加密/非 env 引用时）。
func SealConfig(c *Config) error {
	for i := range c.Providers {
		v, err := Seal(c.Providers[i].APIKey)
		if err != nil {
			return err
		}
		c.Providers[i].APIKey = v
	}
	return nil
}

// OpenConfig 就地把配置里的 Provider.APIKey 解密。
func OpenConfig(c *Config) error {
	for i := range c.Providers {
		v, err := Open(c.Providers[i].APIKey)
		if err != nil {
			return err
		}
		c.Providers[i].APIKey = v
	}
	return nil
}
