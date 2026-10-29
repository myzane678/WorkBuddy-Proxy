package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// modelLimitedBody 上游模型级限流的真实响应体（2026-10-29 现场原文）。
// 关键特征：429 + code=6004 + 重置时间 + 「您也可以切换其他模型继续使用」。
const modelLimitedBody = `{"code":6004,"msg":"您的使用量已超出频率限制，将在 2026-10-29 23:19:36 UTC+8 重置，您也可以切换其他模型继续使用。","requestId":"req-lim1"}`

// modelAwareFake 记录按模型分桶的上游调用次数：本次修复的核心语义是
// 「限流按模型算」，所以必须能按 model 断言调用次数与响应。
type modelAwareFake struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *modelAwareFake) count(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[model]
}

// newModelAwareUpstream 依据请求体里的 model 决定响应。
func newModelAwareUpstream(t *testing.T, fn func(model string) (status int, body string, isStream bool)) (*upstream.Client, *modelAwareFake) {
	t.Helper()
	f := &modelAwareFake{calls: map[string]int{}}
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(raw, &req)
			f.mu.Lock()
			f.calls[req.Model]++
			f.mu.Unlock()
			status, body, isStream := fn(req.Model)
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
		ChatBaseCN:      "https://fake.example",
		BillingBaseCN:   "https://fake.example",
		ChatBaseGlobal:  "https://fake.example",
		BillingBaseGlob: "https://fake.example",
	}
	return up, f
}

// TestChatModelRateLimitDoesNotCoolAccount 上游 429（模型级配额）不得冷却账号、
// 不得喂熔断器，出口必须是 429 model_rate_limited 而不是 503 no_healthy_account。
//
// 现场依据（2026-10-29 19:31）：同账号 deepseek-v4.1-flash 返 429 code=6004、
// glm-5.3-flash 同刻 200；旧行为把 429 当账号故障，Cooldown 内部还累计熔断
// fails（3 次即熔断、指数退避到 4 小时），单账号部署下整站看起来挂掉。
func TestChatModelRateLimitDoesNotCoolAccount(t *testing.T) {
	up, f := newModelAwareUpstream(t, func(string) (int, string, bool) {
		return http.StatusTooManyRequests, modelLimitedBody, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := postChat(t, h, `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429（模型级限流必须保留 429 语义）body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resp not json: %v body=%s", err, rec.Body)
	}
	if resp.Error.Code != "model_rate_limited" {
		t.Errorf("code=%q want model_rate_limited（不能伪装成 no_healthy_account）", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, `"code":6004`) {
		t.Errorf("message 应透传上游原文（含重置时间），得 %q", resp.Error.Message)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("429 应带 Retry-After")
	}
	if got := f.count("deepseek-v4.1-flash"); got != 2 {
		t.Errorf("上游调用次数=%d want 2（两个号各探一次，不重复重试）", got)
	}
	for _, uid := range []string{"a1", "a2"} {
		st, _ := p.Status(uid)
		if st.Cooling {
			t.Errorf("%s 不应被冷却（429 是模型级配额，与账号无关）: %+v", uid, st)
		}
		if st.BreakerFails != 0 || !st.BreakerUntil.IsZero() {
			t.Errorf("%s 不应喂熔断器（429 必须排除在熔断信号外）: fails=%d until=%v",
				uid, st.BreakerFails, st.BreakerUntil)
		}
	}
}

// TestChatModelRateLimitOnlyBlocksThatModel 同一个号上，被限流的模型返 429，
// 其他模型必须照常 200——这是「模型维度限流」的全部意义，也是用户能继续干活的前提。
// 且在限流窗口内该模型不再打上游（避免每次请求白烧 MaxRotate 次调用并刷日志）。
func TestChatModelRateLimitOnlyBlocksThatModel(t *testing.T) {
	up, f := newModelAwareUpstream(t, func(model string) (int, string, bool) {
		if model == "deepseek-v4.1-flash" {
			return http.StatusTooManyRequests, modelLimitedBody, false
		}
		return http.StatusOK, sseOK, true
	})
	// 单账号：正是本次现场的部署形态（多号也躲不开同号其他模型被连坐）。
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec1 := postChat(t, h, `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rec1.Code != http.StatusTooManyRequests {
		t.Fatalf("第一次 deepseek code=%d want 429 body=%s", rec1.Code, rec1.Body)
	}

	// 同号换模型必须仍可用（旧行为：账号已进冷却，这里会 503）。
	rec2 := postChat(t, h, `{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("同号 glm code=%d want 200（模型级限流不得连坐其他模型）body=%s", rec2.Code, rec2.Body)
	}

	// 窗口内再请求 deepseek：命中限流记忆，不再打上游，仍返 429。
	rec3 := postChat(t, h, `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("窗口内 deepseek code=%d want 429 body=%s", rec3.Code, rec3.Body)
	}
	if got := f.count("deepseek-v4.1-flash"); got != 1 {
		t.Errorf("deepseek 上游调用次数=%d want 1（窗口内应直接拒绝，不白打上游）", got)
	}
	if got := f.count("glm-5.3-flash"); got != 1 {
		t.Errorf("glm 上游调用次数=%d want 1", got)
	}
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Errorf("账号不应被冷却: %+v", st)
	}
}

// TestChatModelRateLimitWindowExpires 限流窗口过期后必须重新探测上游：
// 窗口是保守估计（上游原文里的重置时间不做解析），到期后若模型其实已恢复，
// 代理要能自己回到正常路径。
func TestChatModelRateLimitWindowExpires(t *testing.T) {
	up, f := newModelAwareUpstream(t, func(string) (int, string, bool) {
		return http.StatusTooManyRequests, modelLimitedBody, false
	})
	h := NewHandler(Config{
		Pool:         testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:     up,
		SoftCooldown: time.Millisecond,
	})
	postChat(t, h, `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if got := f.count("deepseek-v4.1-flash"); got != 1 {
		t.Fatalf("首次上游调用次数=%d want 1", got)
	}
	time.Sleep(5 * time.Millisecond)
	postChat(t, h, `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if got := f.count("deepseek-v4.1-flash"); got != 2 {
		t.Errorf("窗口过期后上游调用次数=%d want 2（应重新探测）", got)
	}
}
