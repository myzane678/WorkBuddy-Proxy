// capture.go 11128 渠道拦截的请求体留样。
//
// 为什么需要：11128「Illegal API invocation from an unapproved channel」是上游对
// 请求指纹的逐字反探测。实测同一账号、同一套头部下，「简单请求 200、客户端请求
// 连续 400」——触发源在请求体内容里，不在头部、不在账号。没有原文就只能猜。
// 留样文件写 state_file 同目录（默认 data/），含会话内容，仅本机私用；
// features.capture_refused_body=false 可完全关闭。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"workbuddy2api/internal/upstream"
)

const (
	// captureMax 每进程最多留样份数：足够定位，又不会让磁盘无限增长。
	captureMax = 3
	// captureBodyMax 单份留样的大小上限：超长（内联截图）只留前 8 MiB 并标注截断。
	captureBodyMax = 8 << 20
)

var captureState struct {
	sync.Mutex
	count int
}

// captureRefused 把触发 11128 的请求落盘。任何失败只打日志，绝不影响请求本身。
func (h *Handler) captureRefused(body []byte, meta upstream.ChatMeta, upstreamStatus int, upstreamBody []byte) {
	if !h.cfg.CaptureRefusedBody {
		return
	}
	captureState.Lock()
	if captureState.count >= captureMax {
		captureState.Unlock()
		return
	}
	captureState.count++
	captureState.Unlock()

	dir := h.cfg.CaptureDir
	if dir == "" {
		dir = "data"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("capture 11128: mkdir %s failed: %v", dir, err)
		return
	}
	truncated := false
	if len(body) > captureBodyMax {
		body = body[:captureBodyMax]
		truncated = true
	}
	rec := map[string]any{
		"time":                    time.Now().Format(time.RFC3339),
		"upstream_status":         upstreamStatus,
		"upstream_body":           string(upstreamBody),
		"body_bytes":              len(body),
		"body_truncated":          truncated,
		"conversation_id":         meta.ConversationID,
		"conversation_header_id":  meta.ConversationHeaderID,
		"conversation_request_id": meta.ConversationRequestID,
		// 落盘的是**入站原文**（客户端发来的样子）；出站前还会经过脱敏与
		// 角色/工具配对归一，两者差分即可看出净化是否漏掉了触发串。
		"inbound_body": string(body),
	}
	raw, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		log.Printf("capture 11128: marshal failed: %v", err)
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("channel-refused-%s.json", time.Now().Format("20060102-150405")))
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		log.Printf("capture 11128: write %s failed: %v", name, err)
		return
	}
	log.Printf("chat: 已留样 11128 请求体 -> %s（%d bytes）", name, len(body))
}
