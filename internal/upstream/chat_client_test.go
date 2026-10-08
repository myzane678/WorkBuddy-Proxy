package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// TestChatStreamUsesChatHTTPNotHTTP 聊天必须走 ChatHTTP（独立 client），
// 短 RPC 继续走 HTTP。这是取消「聊天共用 120s 整体超时」的装配契约：
// 一旦回退成共用 HTTP，大上下文会话会再次被 502 一刀切死。
func TestChatStreamUsesChatHTTPNotHTTP(t *testing.T) {
	var chatHits, rpcHits int
	c := testClient(func(r *http.Request) (*http.Response, error) {
		rpcHits++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0}`))}, nil
	})
	c.ChatHTTP = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		chatHits++
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}, nil
	})}

	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	rc, status, _, err := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
	rc.Close()
	if chatHits != 1 || rpcHits != 0 {
		t.Errorf("chat=%d rpc=%d, want chat=1 rpc=0", chatHits, rpcHits)
	}
}

// TestChatStreamFallsBackToHTTP ChatHTTP 未设置（测试/裸用路径）时必须回落 HTTP，
// 行为与改造前一致。
func TestChatStreamFallsBackToHTTP(t *testing.T) {
	var hits int
	c := testClient(func(r *http.Request) (*http.Response, error) {
		hits++
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
		}, nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	rc, status, _, err := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
	rc.Close()
	if hits != 1 {
		t.Errorf("hits=%d, want 1", hits)
	}
}

// TestMonitorBodyIdleCutoff 静默超过 idle → ctx 被 cancel（掐流释放租约）。
func TestMonitorBodyIdleCutoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := MonitorBody(&ctxBoundReader{ctx: ctx}, 50*time.Millisecond, cancel)
	defer body.Close()
	if _, err := body.Read(make([]byte, 16)); err == nil {
		t.Fatal("idle 超阈值后 Read 应返回错误")
	}
}

// TestMonitorBodyRenewsOnActivity 活跃流（周期吐数据）总时长远超 idle 也不被掐，
// 否则恢复成绝对 deadline 语义，长生成会被误杀。
func TestMonitorBodyRenewsOnActivity(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 每 30ms 吐 1 字节；idle=80ms。读 10 字节约 300ms > idle。
	body := MonitorBody(&periodicReader{period: 30 * time.Millisecond}, 80*time.Millisecond, cancel)
	defer body.Close()
	buf := make([]byte, 1)
	for i := 0; i < 10; i++ {
		if _, err := io.ReadFull(body, buf); err != nil {
			t.Fatalf("read %d: %v（活跃流不许被掐）", i, err)
		}
	}
}

// TestMonitorBodyDisabledWhenZero idle<=0 原样返回底流（零包装、零 goroutine）。
func TestMonitorBodyDisabledWhenZero(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := &ctxBoundReader{ctx: context.Background()}
	if out := MonitorBody(rc, 0, cancel); out != rc {
		t.Fatalf("idle<=0 应原样返回底流，得 %T", out)
	}
}

// TestIdleTickBounds 监控周期钳在 [10ms, 1s]：小 idle 能快速发现，大值避免空转。
func TestIdleTickBounds(t *testing.T) {
	cases := []struct{ idle, want time.Duration }{
		{40 * time.Millisecond, 10 * time.Millisecond},
		{80 * time.Millisecond, 20 * time.Millisecond},
		{300 * time.Second, time.Second},
	}
	for _, c := range cases {
		if got := idleTick(c.idle); got != c.want {
			t.Errorf("idleTick(%v)=%v, want %v", c.idle, got, c.want)
		}
	}
}

// TestNewSeparatesChatClientFromRPC 生产装配：New() 必须让 ChatHTTP 与 HTTP 是
// 两个 client（超时策略不同），且默认 header/idle 均不启用（由 main 按 config 接）。
func TestNewSeparatesChatClientFromRPC(t *testing.T) {
	c := New()
	if c.ChatHTTP == nil {
		t.Fatal("New() 应预建 ChatHTTP")
	}
	if c.ChatHTTP == c.HTTP {
		t.Error("ChatHTTP 与 HTTP 不应是同一 client")
	}
	if c.HTTP.Timeout == 0 {
		t.Error("短 RPC client 应保留整体超时")
	}
	if c.ChatHTTP.Timeout != 0 {
		t.Errorf("聊天 client 不应有整体超时，得 %v", c.ChatHTTP.Timeout)
	}
	if c.HeaderTimeout != 0 || c.IdleTimeout != 0 {
		t.Errorf("HeaderTimeout=%v IdleTimeout=%v, want 0/0（未装配）", c.HeaderTimeout, c.IdleTimeout)
	}
}

// —— helpers ——

type ctxBoundReader struct{ ctx context.Context }

func (r *ctxBoundReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (r *ctxBoundReader) Close() error { return nil }

type periodicReader struct{ period time.Duration }

func (r periodicReader) Read(p []byte) (int, error) {
	time.Sleep(r.period)
	p[0] = 'x'
	return 1, nil
}
func (periodicReader) Close() error { return nil }
