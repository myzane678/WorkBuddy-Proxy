package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// convIDCapture 按出站顺序记录每次上游请求的会话头。
type convIDCapture struct {
	ids    []string // X-Conversation-ID
	reqIDs []string // X-Conversation-Request-ID
}

// upstreamCapturingConvIDs 构造一个捕获会话头、行为由 behavior 决定的假上游。
func upstreamCapturingConvIDs(cap *convIDCapture, behavior func(authz string) (int, string, bool)) *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			cap.ids = append(cap.ids, r.Header.Get("X-Conversation-ID"))
			cap.reqIDs = append(cap.reqIDs, r.Header.Get("X-Conversation-Request-ID"))
			status, body, isStream := behavior(r.Header.Get("Authorization"))
			ct := "application/json"
			if isStream {
				ct = "text/event-stream"
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{ct}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
		ChatBaseCN: "https://fake.example",
	}
}

func postChat(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec
}

// TestChatSendsConversationHeaderWhenClientOmitsIt 客户端（dsh / Codex 等）不带
// conversationId 时也必须发出 X-Conversation-ID：上游渠道校验缺头即拒收（400 11-128）。
func TestChatSendsConversationHeaderWhenClientOmitsIt(t *testing.T) {
	cap := &convIDCapture{}
	up := upstreamCapturingConvIDs(cap, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	rec := postChat(t, h, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if len(cap.ids) != 1 || cap.ids[0] == "" {
		t.Fatalf("X-Conversation-ID 漏发: %v", cap.ids)
	}
}

// TestChatPassesClientConversationID 客户端给了会话 ID 就透传原值（不覆盖、不改写）。
func TestChatPassesClientConversationID(t *testing.T) {
	cap := &convIDCapture{}
	up := upstreamCapturingConvIDs(cap, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	rec := postChat(t, h, `{"model":"glm-5.2","metadata":{"conversation_id":"conv-abc"},"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if len(cap.ids) != 1 || cap.ids[0] != "conv-abc" {
		t.Fatalf("X-Conversation-ID=%v want [conv-abc]", cap.ids)
	}
}

// TestChatReusesConversationHeaderAcrossRotation 轮转换号必须复用同一组轮级会话头：
// 会话 ID 与轮级聚合键都在循环外生成一次，否则同一次对话在后台被拆成多条。
func TestChatReusesConversationHeaderAcrossRotation(t *testing.T) {
	cap := &convIDCapture{}
	up := upstreamCapturingConvIDs(cap, func(authz string) (int, string, bool) {
		if authz == "Bearer at-bad" {
			// 用通用 4xx（ErrClient：换号可能有不同模型权限）触发轮转。
			// 不能用 11128——它走同号重试不轮转（见 request_error_test.go）。
			return 400, `{"code":9999,"msg":"bad token"}`, false
		}
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	// 让 bad 积分更高被先选中（与 TestChatRotatesOnHardCredit 同口径）
	p.SetCredits("bad", 2000)
	p.SetCredits("good", 1000)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := postChat(t, h, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if len(cap.ids) != 2 {
		t.Fatalf("上游调用次数=%d want 2: %v", len(cap.ids), cap.ids)
	}
	if cap.ids[0] == "" || cap.ids[0] != cap.ids[1] {
		t.Fatalf("换号后会话 ID 不一致（应在轮转循环外只算一次）: %v", cap.ids)
	}
	if cap.reqIDs[0] == "" || cap.reqIDs[0] != cap.reqIDs[1] {
		t.Fatalf("换号后轮级聚合键不一致（应在轮转循环外只算一次）: %v", cap.reqIDs)
	}
}

// TestChatPassesInboundTurnRequestID 入站自带 X-Conversation-Request-ID 时透传原值，
// 让上游沿用客户端自己的对话轮 ID 聚合。
func TestChatPassesInboundTurnRequestID(t *testing.T) {
	cap := &convIDCapture{}
	up := upstreamCapturingConvIDs(cap, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Conversation-Request-ID", "client-turn-9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if len(cap.reqIDs) != 1 || cap.reqIDs[0] != "client-turn-9" {
		t.Fatalf("X-Conversation-Request-ID=%v want [client-turn-9]", cap.reqIDs)
	}
}
