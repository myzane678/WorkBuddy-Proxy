package server

import (
	"encoding/json"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// TestChatRequestLevelErrorPassesThrough 确定性请求级错误（11133 参数被模型拒绝等）
// 必须原样透传 4xx，而不是轮转后给一个可重试的 503：
//   - 客户端拿到 503 会按可重试语义无限重试，每次放大成 MaxRotate 次上游调用；
//   - 同一 body 换号结果不变，轮转只会白烧健康号配额。
func TestChatRequestLevelErrorPassesThrough(t *testing.T) {
	const upBody = `{"code":11133,"msg":"the request parameters were rejected by the model provider",` +
		`"extError":{"code":"400001","type":"input_invalid"}}`
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 400, upBody, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := postChat(t, h, `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	if rec.Code != 400 {
		t.Fatalf("code=%d want 400（请求级错误应透传，不能退化成 503）body=%s", rec.Code, rec.Body)
	}
	if calls != 1 {
		t.Errorf("上游调用次数=%d want 1（确定性错误必须终止轮转）", calls)
	}
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resp not json: %v body=%s", err, rec.Body)
	}
	if !strings.Contains(resp.Error.Message, `"code":11133`) {
		t.Errorf("message 应透传上游原文，得 %q", resp.Error.Message)
	}
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Cooling {
			t.Errorf("%s 不应被冷却（请求级错误与账号无关）: %+v", uid, st)
		}
	}
}

// TestChatRetriesSameAccountOnChannelRefused 11128 是概率性渠道拦截：实测同一请求
// 1 秒后重试即通过。单账号部署下轮转等于直接失败（这正是用户看到的 503 来源），
// 必须在同一账号上短退避重试。
func TestChatRetriesSameAccountOnChannelRefused(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		if calls <= 2 {
			return 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, false
		}
		return 200, sseOK, true
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	rec := postChat(t, h, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rec.Code != 200 {
		t.Fatalf("code=%d want 200（同号重试应把 11128 自愈掉）body=%s", rec.Code, rec.Body)
	}
	if calls != 3 {
		t.Errorf("上游调用次数=%d want 3（同号重试 2 次后成功）", calls)
	}
}

// TestChatChannelRefusedReportsDistinctError 重试用尽仍被拦时，错误必须与
// 「账号池不可用」可区分，并且不得为此轮转掉其他健康号。
func TestChatChannelRefusedReportsDistinctError(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := postChat(t, h, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	if rec.Code != 503 {
		t.Fatalf("code=%d want 503 body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resp not json: %v body=%s", err, rec.Body)
	}
	if resp.Error.Code != "upstream_channel_refused" {
		t.Errorf("code=%q want upstream_channel_refused（不能伪装成 no_healthy_account）", resp.Error.Code)
	}
	if calls != 3 {
		t.Errorf("上游调用次数=%d want 3（只同号重试，不轮转其他账号）", calls)
	}
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Cooling {
			t.Errorf("%s 不应被冷却（渠道拦截与账号无关）: %+v", uid, st)
		}
	}
}
