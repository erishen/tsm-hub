package store

import (
	"encoding/base64"
	"strings"
	"testing"
)

// 32 字节主密钥的十六进制（64 字符）。
const testMasterKeyHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func TestSealOpenRoundTrip(t *testing.T) {
	t.Setenv(masterKeyEnv, testMasterKeyHex)
	plain := "sk-super-secret-key-12345"
	sealed, err := Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, secretPrefix) {
		t.Fatalf("sealed should have prefix %q, got %q", secretPrefix, sealed)
	}
	if sealed == plain {
		t.Fatal("sealed must not equal plaintext")
	}
	opened, err := Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != plain {
		t.Fatalf("round trip mismatch: got %q want %q", opened, plain)
	}
}

func TestSealWithoutKeyKeepsPlaintext(t *testing.T) {
	t.Setenv(masterKeyEnv, "") // 确保无密钥
	plain := "sk-plain-when-no-key"
	sealed, err := Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed != plain {
		t.Fatalf("without master key Seal must return plaintext, got %q", sealed)
	}
}

func TestSealEnvPrefixPassthrough(t *testing.T) {
	t.Setenv(masterKeyEnv, testMasterKeyHex)
	ref := "env:MY_PROVIDER_KEY"
	sealed, err := Seal(ref)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed != ref {
		t.Fatalf("env: reference must not be encrypted, got %q", sealed)
	}
}

func TestSealAlreadyEncryptedPassthrough(t *testing.T) {
	t.Setenv(masterKeyEnv, testMasterKeyHex)
	enc := secretPrefix + "somedata"
	out, err := Seal(enc)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if out != enc {
		t.Fatalf("already-encrypted must pass through unchanged, got %q", out)
	}
}

func TestOpenEncryptedWithoutKeyFails(t *testing.T) {
	t.Setenv(masterKeyEnv, "")
	enc := secretPrefix + "somedata"
	if _, err := Open(enc); err == nil {
		t.Fatal("Open of ciphertext without master key must fail")
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	t.Setenv(masterKeyEnv, testMasterKeyHex)
	enc, err := Seal("top-secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// 换个密钥解密应失败。
	t.Setenv(masterKeyEnv, "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100")
	if _, err := Open(enc); err == nil {
		t.Fatal("Open with wrong master key must fail")
	}
}

func TestMasterKeyFormats(t *testing.T) {
	// hex 64 字符
	t.Setenv(masterKeyEnv, testMasterKeyHex)
	if _, ok, err := masterKey(); err != nil || !ok {
		t.Fatalf("hex key: ok=%v err=%v", ok, err)
	}
	// base64 32 字节（动态生成，避免手算错误）
	b32 := make([]byte, 32)
	for i := range b32 {
		b32[i] = byte(i)
	}
	t.Setenv(masterKeyEnv, base64.StdEncoding.EncodeToString(b32))
	if _, ok, err := masterKey(); err != nil || !ok {
		t.Fatalf("base64 key: ok=%v err=%v", ok, err)
	}
	// 错误格式
	t.Setenv(masterKeyEnv, "tooshort")
	if _, _, err := masterKey(); err == nil {
		t.Fatal("short key must error")
	}
}

func TestSealOpenConfig(t *testing.T) {
	t.Setenv(masterKeyEnv, testMasterKeyHex)
	c := &Config{
		Providers: []Provider{
			{APIKey: "sk-real-1"},
			{APIKey: "env:REF"},
			{APIKey: ""},
		},
	}
	if err := SealConfig(c); err != nil {
		t.Fatalf("SealConfig: %v", err)
	}
	if !strings.HasPrefix(c.Providers[0].APIKey, secretPrefix) {
		t.Fatalf("provider 0 should be encrypted, got %q", c.Providers[0].APIKey)
	}
	if c.Providers[1].APIKey != "env:REF" {
		t.Fatalf("env ref must pass through, got %q", c.Providers[1].APIKey)
	}
	if c.Providers[2].APIKey != "" {
		t.Fatalf("empty key must stay empty, got %q", c.Providers[2].APIKey)
	}
	// 解密回到原始明文
	if err := OpenConfig(c); err != nil {
		t.Fatalf("OpenConfig: %v", err)
	}
	if c.Providers[0].APIKey != "sk-real-1" {
		t.Fatalf("decrypt mismatch, got %q", c.Providers[0].APIKey)
	}
	if c.Providers[1].APIKey != "env:REF" {
		t.Fatalf("env ref after open mismatch, got %q", c.Providers[1].APIKey)
	}
}
