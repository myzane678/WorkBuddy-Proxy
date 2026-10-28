package upstream

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// sseResponse 最小合法 SSE 响应（只看请求头的用例不需要真实帧）。
func sseResponse() *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
}

// captureChatHeaders 发一次 chat 并返回上游收到的头。
func captureChatHeaders(t *testing.T, meta ChatMeta) http.Header {
	t.Helper()
	var got http.Header
	c := testClient(func(r *http.Request) (*http.Response, error) {
		got = r.Header.Clone()
		return sseResponse(), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	rc, status, _, err := c.ChatStreamWithMeta(a, []byte(`{"model":"glm-5.2","messages":[]}`), meta)
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	rc.Close()
	return got
}

// TestChatStreamClientConversationIDWins 客户端给了会话 ID 就透传原值，兜底值不得覆盖。
func TestChatStreamClientConversationIDWins(t *testing.T) {
	h := captureChatHeaders(t, ChatMeta{ConversationID: "client-conv", ConversationHeaderID: "fallback-conv"})
	if got := h.Get("X-Conversation-ID"); got != "client-conv" {
		t.Errorf("X-Conversation-ID=%q want client-conv（客户端原值优先）", got)
	}
}

// TestChatStreamUsesConversationFallback 客户端没带会话 ID 时用 handler 合成的兜底值。
func TestChatStreamUsesConversationFallback(t *testing.T) {
	h := captureChatHeaders(t, ChatMeta{ConversationHeaderID: "fallback-conv"})
	if got := h.Get("X-Conversation-ID"); got != "fallback-conv" {
		t.Errorf("X-Conversation-ID=%q want fallback-conv", got)
	}
}

// TestChatStreamConversationIDAlwaysSent 零值 meta（测试/裸用路径）也不能漏发该头：
// 上游渠道校验缺 X-Conversation-ID 会返回 400 11-128，OpenAI 兼容客户端普遍不带
// conversationId，漏发即整类流量被拒。
func TestChatStreamConversationIDAlwaysSent(t *testing.T) {
	for name, call := range map[string]func(*Client, *auth.Auth) error{
		"ChatStream": func(c *Client, a *auth.Auth) error {
			rc, _, _, err := c.ChatStream(a, []byte(`{}`))
			if rc != nil {
				rc.Close()
			}
			return err
		},
		"ChatStreamWithMeta(零值)": func(c *Client, a *auth.Auth) error {
			rc, _, _, err := c.ChatStreamWithMeta(a, []byte(`{}`), ChatMeta{})
			if rc != nil {
				rc.Close()
			}
			return err
		},
	} {
		var got string
		c := testClient(func(r *http.Request) (*http.Response, error) {
			got = r.Header.Get("X-Conversation-ID")
			return sseResponse(), nil
		})
		if err := call(c, &auth.Auth{AccessToken: "at", UID: "u1"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got == "" {
			t.Errorf("%s: 漏发 X-Conversation-ID", name)
		}
	}
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestChatStreamConversationHeaderFamily 官方客户端的整组会话头必须齐全且自洽：
// 轮级键必发并复用到 X-Root-Request-ID，消息级 ID 与 X-Request-ID 同值，
// B3 链路按规范取 16/32 hex（非法轮级键回落消息 ID）。
func TestChatStreamConversationHeaderFamily(t *testing.T) {
	h := captureChatHeaders(t, ChatMeta{
		ConversationHeaderID:  "fallback-conv",
		ConversationRequestID: "req-1", // 非 hex → B3 TraceId 必须回落
		TraceID:               "trace-in",
	})
	msgID := h.Get("X-Conversation-Message-ID")
	if !hex32.MatchString(msgID) {
		t.Errorf("X-Conversation-Message-ID=%q 应为 32 位 hex", msgID)
	}
	if h.Get("X-Request-ID") != msgID {
		t.Errorf("X-Request-ID=%q 应与消息 ID 同值", h.Get("X-Request-ID"))
	}
	if got := h.Get("X-Conversation-Request-ID"); got != "req-1" {
		t.Errorf("X-Conversation-Request-ID=%q want req-1", got)
	}
	if got := h.Get("X-Root-Request-ID"); got != "req-1" {
		t.Errorf("X-Root-Request-ID=%q want req-1", got)
	}
	if got := h.Get("X-Trace-ID"); got != "trace-in" {
		t.Errorf("X-Trace-ID=%q want trace-in（入站透传优先）", got)
	}
	if got := h.Get("X-B3-TraceId"); got != msgID {
		t.Errorf("X-B3-TraceId=%q 非法轮级键应回落消息 ID %q", got, msgID)
	}
	if got := h.Get("X-B3-SpanId"); got != msgID[:16] {
		t.Errorf("X-B3-SpanId=%q want %q", got, msgID[:16])
	}
	if got := h.Get("X-B3-Sampled"); got != "1" {
		t.Errorf("X-B3-Sampled=%q want 1", got)
	}
}

// TestChatStreamB3TraceIDFromHexTurnKey 合法的 32 hex 轮级键直接作为 B3 TraceId
//（同一次 user send 内重试/换号链路可关联）。
func TestChatStreamB3TraceIDFromHexTurnKey(t *testing.T) {
	const turn = "0123456789abcdef0123456789abcdef"
	h := captureChatHeaders(t, ChatMeta{ConversationRequestID: turn})
	if got := h.Get("X-B3-TraceId"); got != turn {
		t.Errorf("X-B3-TraceId=%q want %q", got, turn)
	}
	if got := h.Get("X-Trace-ID"); got != turn {
		t.Errorf("X-Trace-ID=%q 空入站值应回落轮级键 %q", got, turn)
	}
}

// TestChatHeadersAcceptMatchesOfficialClient 官方桌面端 chat 请求用 event-stream 的
// Accept 形态；CommonHeaders 的 */* 是非流式 RPC 默认，属于渠道指纹偏差。
func TestChatHeadersAcceptMatchesOfficialClient(t *testing.T) {
	h := captureChatHeaders(t, ChatMeta{})
	if got := h.Get("Accept"); got != "application/json, text/event-stream" {
		t.Errorf("Accept=%q want application/json, text/event-stream", got)
	}
}
