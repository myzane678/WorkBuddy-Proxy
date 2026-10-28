package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// TestCaptureRefusedBodyWritesFile 11128 命中时把入站原文留样，供定位触发指纹。
// 没有原文就只能猜，这是把「猜」变成「看」的唯一手段。
func TestCaptureRefusedBodyWritesFile(t *testing.T) {
	dir := t.TempDir()
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, false
	})
	h := NewHandler(Config{
		Pool:               testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:           up,
		CaptureRefusedBody: true,
		CaptureDir:         dir,
	})
	postChat(t, h, `{"model":"glm-5.2","messages":[{"role":"user","content":"capture me"}]}`)

	files, err := filepath.Glob(filepath.Join(dir, "channel-refused-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("应留样 1 份，得 %v (err=%v)", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("读留样失败: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("留样不是合法 JSON: %v", err)
	}
	if body, _ := rec["inbound_body"].(string); !strings.Contains(body, "capture me") {
		t.Errorf("inbound_body 未保存原文: %q", body)
	}
	if ub, _ := rec["upstream_body"].(string); !strings.Contains(ub, "11128") {
		t.Errorf("upstream_body 未保存上游原文: %q", ub)
	}
}

// TestCaptureDisabledWritesNothing 关闭开关时必须完全不落盘（留样含会话内容）。
func TestCaptureDisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, false
	})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   up,
		CaptureDir: dir,
	})
	postChat(t, h, `{"model":"glm-5.2","messages":[{"role":"user","content":"nope"}]}`)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("开关关闭时不应落盘，得 %d 个文件", len(entries))
	}
}
