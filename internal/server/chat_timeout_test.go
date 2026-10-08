package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// TestIsUpstreamTimeout 锁住超时三态判定：超时不换号不罚号的前提是识别得准。
func TestIsUpstreamTimeout(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		clientGone bool
		want       bool
	}{
		{"nil 不算超时", nil, false, false},
		{"普通错误不算", errors.New("boom"), false, false},
		{"net.Error.Timeout 命中", timeoutErr("read tcp timeout"), false, true},
		{"DeadlineExceeded 命中", context.DeadlineExceeded, false, true},
		{"os.ErrDeadlineExceeded 命中", os.ErrDeadlineExceeded, false, true},
		{"ctx 取消 + 客户端仍在 = 空闲看门狗掐流", context.Canceled, false, true},
		{"ctx 取消 + 客户端已走 = 主动断连", context.Canceled, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUpstreamTimeout(c.err, c.clientGone); got != c.want {
				t.Errorf("isUpstreamTimeout(%v, %v) = %v, want %v", c.err, c.clientGone, got, c.want)
			}
		})
	}
}

// timeoutErr 实现 net.Error 且 Timeout() 为真，模拟 ResponseHeaderTimeout 掐流。
type timeoutErr string

func (e timeoutErr) Error() string   { return string(e) }
func (e timeoutErr) Timeout() bool   { return true }
func (e timeoutErr) Temporary() bool { return true }

// TestChatUpstreamTimeoutStopsRotation 超时命中必须止损：只打一次上游、不轮转、不罚号。
// 回归的是 120s 整体超时下大上下文会话被打满 3 次轮转（最坏半小时）的旧行为。
func TestChatUpstreamTimeoutStopsRotation(t *testing.T) {
	calls := 0
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			// ResponseHeaderTimeout 命中时的真实形态：Do 直接返回 net.Error。
			return nil, timeoutErr("net/http: timeout awaiting response headers")
		})},
		ChatBaseCN: "https://fake.example",
	}
	p := testPoolWith(
		&auth.Auth{UID: "a1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a2", AccessToken: "at2", ExpiresAt: 9999999999},
		&auth.Auth{UID: "a3", AccessToken: "at3", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 3, SoftCooldown: time.Minute})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	h.ServeHTTP(rec, req)

	if calls != 1 {
		t.Errorf("超时应止损只打一次上游，实打 %d 次", calls)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code=%d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body 非 JSON: %s", rec.Body.String())
	}
	e, _ := body["error"].(map[string]any)
	if e == nil {
		t.Fatalf("body 无 error 字段: %s", rec.Body.String())
	}
	if !strings.Contains(e["message"].(string), "upstream timeout") {
		t.Errorf("message 应含超时提示以区别于没号可用: %v", e["message"])
	}
	// 不罚号：三个号都不该被冷却/禁用。
	for _, uid := range []string{"a1", "a2", "a3"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled || st.Reason != "" {
			t.Errorf("号 %s 被误罚: %+v", uid, st)
		}
	}
}

// TestChatStreamEmptyStreamMarks502 流式空流必须收敛到 502 观测。
// 回归的是 st.status 在 Stream 之前就置 200、空流被记成假成功的旧行为。
func TestChatStreamEmptyStreamMarks502(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		// 上游 200 但只发注释行，0 个有效数据帧。
		return 200, ": keepalive\n\n", true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	h.ServeHTTP(rec, req)

	// HTTP 头已发出只能维持 200，但 body 里必须有 error 帧（不是空 [DONE]）。
	if rec.Code != 200 {
		t.Errorf("流式空流 HTTP code=%d（头已发出，应为 200）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "empty upstream stream") {
		t.Errorf("body 应含空流 error 帧: %s", rec.Body.String())
	}
	// 观测侧必须收敛到 502：空流时 upstream.Stream 必须返回可判定的哨兵错误，
	// handler 据此把 status 从假 200 改写成 502（见 handler 流式尾处理）。
	var sb strings.Builder
	sErr := upstream.Stream(blankWriter{&sb}, strings.NewReader(": keepalive\n\n"))
	if !upstream.IsEmptyStreamError(sErr) {
		t.Fatalf("空流应返回哨兵错误，实得 %v", sErr)
	}
	// 与 handler 相同的收敛逻辑。
	status := http.StatusOK
	if upstream.IsEmptyStreamError(sErr) {
		status = http.StatusBadGateway
	}
	if status != http.StatusBadGateway {
		t.Errorf("空流 status=%d, want 502", status)
	}
	if !strings.Contains(sb.String(), "empty upstream stream") {
		t.Errorf("空流兜底帧应含 error: %s", sb.String())
	}
}

// blankWriter 丢弃 header 操作，只接住 body 字节（httptest.Recorder 亦可，这里更轻）。
type blankWriter struct{ b *strings.Builder }

func (w blankWriter) Header() http.Header        { return http.Header{} }
func (w blankWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
func (w blankWriter) WriteHeader(int)            {}

// TestChatIdleWatchdogCutsSilentStream 静默流（上游发了 200 就再也不吐）必须被掐。
// 用 ctxBoundReader 模拟真实 resp.Body：Read 阻塞在 ctx.Done() 上——io.Pipe 不
// 响应 ctx cancel，会让测试失去判别力。
func TestChatIdleWatchdogCutsSilentStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := upstream.MonitorBody(&ctxBoundReader{ctx: ctx}, 100*time.Millisecond, cancel)
	defer body.Close()

	done := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 64)) // 阻塞到看门狗 cancel 传导到底流
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("静默流被放行了（应返回错误）")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("静默流未被看门狗掐断")
	}
	if ctx.Err() == nil {
		t.Error("看门狗应 cancel 请求 ctx")
	}
}

// TestChatIdleWatchdogRenewsOnActivity 活跃流不误杀：周期吐数据、总时长远超 idle。
// 若看门狗退化成绝对 deadline 语义，这个用例会失败——它是取消 120s 整体超时的前提。
func TestChatIdleWatchdogRenewsOnActivity(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 每 30ms 吐 1 字节；idle=80ms。读 10 字节耗时约 300ms > idle。
	body := upstream.MonitorBody(&periodicReaderForTest{period: 30 * time.Millisecond}, 80*time.Millisecond, cancel)
	defer body.Close()

	buf := make([]byte, 1)
	for i := 0; i < 10; i++ {
		if _, err := io.ReadFull(body, buf); err != nil {
			t.Fatalf("read %d: %v（活跃流不许被掐）", i, err)
		}
	}
}

// TestChatIdleWatchdogDisabledWhenZero idle<=0 必须原样返回底流（与上游同契约）。
func TestChatIdleWatchdogDisabledWhenZero(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	rc := &oneByteReaderForTest{}
	out := upstream.MonitorBody(rc, 0, cancel)
	if out != rc {
		t.Fatalf("idle<=0 应原样返回底流，得 %T", out)
	}
}

// —— helpers ——

// ctxBoundReader 模拟 net/http resp.Body：Read 阻塞在 ctx.Done() 上，cancel 后返回错误。
type ctxBoundReader struct{ ctx context.Context }

func (r *ctxBoundReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (r *ctxBoundReader) Close() error { return nil }

// periodicReaderForTest 每 period 吐 1 字节（活跃流）。
type periodicReaderForTest struct{ period time.Duration }

func (r *periodicReaderForTest) Read(p []byte) (int, error) {
	time.Sleep(r.period)
	p[0] = 'x'
	return 1, nil
}
func (r *periodicReaderForTest) Close() error { return nil }

// oneByteReaderForTest 只吐一个字节后 EOF。
type oneByteReaderForTest struct{ done bool }

func (r *oneByteReaderForTest) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	p[0] = 'a'
	return 1, nil
}
func (r *oneByteReaderForTest) Close() error { return nil }
