// keys.go 多 API key 管理：附加密钥存 config.json 的 api_keys 数组，与主 key（cfg.APIKey）并存。
// 变更走 writeConfigField 持久化 + 内存整体替换（引用不可变，并发读安全，同 allowlist 模式）。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// APIKeyEntry 一条附加 API key。
type APIKeyEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"` // "2006-01-02 15:04"
}

// newAPIKeyEntry 生成一条新密钥：id 随机 6 hex，key 为 wb-sk- + 32 hex。
func newAPIKeyEntry(name string) (APIKeyEntry, error) {
	id, err := randHex(3)
	if err != nil {
		return APIKeyEntry{}, err
	}
	body, err := randHex(16)
	if err != nil {
		return APIKeyEntry{}, err
	}
	return APIKeyEntry{
		ID:        id,
		Name:      name,
		Key:       "wb-sk-" + body,
		Enabled:   true,
		CreatedAt: time.Now().Format("2006-01-02 15:04"),
	}, nil
}

// randHex 返回 n 字节的 hex 字符串（2n 字符）。
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
