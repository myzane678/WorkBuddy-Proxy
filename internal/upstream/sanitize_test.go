package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

const (
	ccIdentity = "You are Claude Code, Anthropic's official CLI for Claude."
	ccBranch   = "Main branch (you will usually use this for PRs)"
	ccHeader   = "x-anthropic-billing-header: cc_version=1.0; cc_entrypoint=cli;"
	// Codex instructions 首段（上游逐字精确指纹，三句缺一不可）。
	codexInstructions = "You are a coding agent running in the Codex CLI, a terminal-based coding assistant. Codex CLI is an open source project led by OpenAI. You are expected to be precise, safe, and helpful."
)

func TestIdentityRewritten(t *testing.T) {
	out := sanitizeText(ccIdentity)
	if !strings.Contains(out, "official CLI tool for Claude.") {
		t.Errorf("identity not rewritten: %q", out)
	}
	if strings.Contains(out, ccIdentity) {
		t.Errorf("original identity still present: %q", out)
	}
}

// 桌面版（claude-desktop-3p / Agent SDK）的身份句以逗号接后继内容，结尾不是句号。
// 回归用例：匹配串曾带结尾句号，导致该形态漏网、指纹原样发上游 → 400 code=11128。
func TestIdentityDesktopVariantRewritten(t *testing.T) {
	in := "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
	out := sanitizeText(in)
	if strings.Contains(out, "official CLI for Claude") {
		t.Errorf("desktop identity not rewritten: %q", out)
	}
	if !strings.Contains(out, "official CLI tool for Claude, running within the Claude Agent SDK.") {
		t.Errorf("desktop identity suffix not preserved: %q", out)
	}
}

func TestBranchRewritten(t *testing.T) {
	out := sanitizeText(ccBranch)
	if !strings.Contains(out, "Default branch (you will usually use this for PRs)") {
		t.Errorf("branch not rewritten: %q", out)
	}
	if strings.Contains(out, "Main branch") {
		t.Errorf("original branch still present: %q", out)
	}
}

func TestBillingHeaderStrippedValueIrrelevant(t *testing.T) {
	out := sanitizeText(ccHeader)
	if strings.Contains(out, "x-anthropic-billing-header") {
		t.Errorf("header not stripped: %q", out)
	}
}

// 反馈句带 Anthropic 仓库链接，上游按整句拦截（实测只留链接或只留半边均不拦）。
// 回归用例：give→provide 一词之差即可绕过。
func TestFeedbackSentenceRewritten(t *testing.T) {
	in := "To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues"
	out := sanitizeText(in)
	if strings.Contains(out, "To give feedback") {
		t.Errorf("feedback sentence not rewritten: %q", out)
	}
	if !strings.Contains(out, "To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues") {
		t.Errorf("feedback sentence not rewritten as expected: %q", out)
	}
}

// 上游反探测：请求体里出现裸数字 11128 即整单拦截（与上下文无关）。
// 回归用例：该串会被改写为 11-128 以打断精确匹配。
func TestUpstreamErrorCodeRewritten(t *testing.T) {
	in := "upstream returned code=11128 for this request"
	out := sanitizeText(in)
	if strings.Contains(out, "11128") {
		t.Errorf("error code not rewritten: %q", out)
	}
	if !strings.Contains(out, "11-128") {
		t.Errorf("error code not rewritten as expected: %q", out)
	}
}

// 回归：工具调用消息的 content 常为 null，而旧版 sanitizeMessages 在 content 缺失时
// 直接 continue，整条消息连 tool_calls 一起被跳过 → arguments 里的被拦字符串原样漏出。
func TestToolCallArgumentsSanitized(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "run"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{
				"name":      "Bash",
				"arguments": `{"command":"echo 11128"}`,
			}},
		}},
	}
	if !sanitizeMessages(msgs) {
		t.Fatal("sanitizeMessages 未报告任何改动，tool_calls 被跳过")
	}
	fn := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	got := fn["arguments"].(string)
	if strings.Contains(got, "11128") {
		t.Errorf("tool_call arguments 未被净化: %q", got)
	}
}

// 附加验证：正常对话里出现 github.com/anthropics/ 链接（但不是反馈句整句）
// 时，预检特征命中（进入净化），但改写层只动精确匹配的整句——普通链接文本
// 不该被改写。同理，既不含 11128 也不含反馈整句的文本原样返回。
func TestNormalAnthropicLinkNotRewritten(t *testing.T) {
	in := "see https://github.com/anthropics/anthropic-cookbook for examples"
	if out := sanitizeText(in); out != in {
		t.Errorf("normal anthropic link should be untouched: %q -> %q", in, out)
	}
}

// Codex instructions 首段：命中预告且整句改写，逐字指纹被破坏、语义保留。
func TestCodexInstructionsRewritten(t *testing.T) {
	out := sanitizeText(codexInstructions)
	if strings.Contains(out, codexInstructions) {
		t.Errorf("codex fingerprint still present: %q", out)
	}
	if !strings.Contains(out, "You are a coding agent running in the Codex CLI tool, a terminal-based coding assistant.") {
		t.Errorf("codex first sentence not rewritten: %q", out)
	}
	// 其余两句原样保留，语义不变。
	if !strings.Contains(out, "Codex CLI is an open source project led by OpenAI.") ||
		!strings.Contains(out, "You are expected to be precise, safe, and helpful.") {
		t.Errorf("codex remaining sentences altered: %q", out)
	}
}

// 预检必须能认出 Codex 特征（此前只含 Claude Code，导致提前放行）。
func TestCodexFingerprintDetected(t *testing.T) {
	if !hasFingerprint(codexInstructions) {
		t.Error("codex fingerprint not detected by precheck")
	}
}

// 非精确变体不应被改写：仅去掉其中一个词即视为已破坏，无需改动。
func TestCodexVariantNotTouched(t *testing.T) {
	in := "You are a coding agent running in a CLI, a terminal-based coding assistant."
	if out := sanitizeText(in); out != in {
		t.Errorf("already-broken variant should be untouched: %q -> %q", in, out)
	}
}

func TestBillingHeaderCaseInsensitive(t *testing.T) {
	alt := "X-Anthropic-Billing-Header: cc_version=1.0;"
	out := strings.ToLower(sanitizeText(alt))
	if strings.Contains(out, "billing") {
		t.Errorf("case-insensitive header not stripped: %q", out)
	}
}

func TestTrailingKVStripped(t *testing.T) {
	out := sanitizeText("...; cc_version=2.0; cc_entrypoint=cli;")
	if strings.Contains(out, "cc_version") || strings.Contains(out, "cc_entrypoint") {
		t.Errorf("trailing kv not stripped: %q", out)
	}
}

func TestExactMatchOnlyVariantNotTouched(t *testing.T) {
	in := "...official CLI for Claude!"
	if out := sanitizeText(in); out != in {
		t.Errorf("variant should be untouched: %q -> %q", in, out)
	}
}

func TestUserFreeTextNotTouched(t *testing.T) {
	in := "please use main branch for this repo"
	if out := sanitizeText(in); out != in {
		t.Errorf("free text should be untouched: %q -> %q", in, out)
	}
}

func TestNoFeatureReturnsSameString(t *testing.T) {
	in := "ordinary user message"
	if out := sanitizeText(in); out != in {
		t.Errorf("no-feature text should pass through unchanged: %q -> %q", in, out)
	}
}

func TestMultimodalTextPartOnly(t *testing.T) {
	imgPart := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "data": "..."}}
	content := []any{
		map[string]any{"type": "text", "text": ccIdentity},
		imgPart,
	}
	out, changed := sanitizeContent(content)
	if !changed {
		t.Fatal("expected change")
	}
	parts := out.([]any)
	txt, _ := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "CLI tool") {
		t.Errorf("text part not sanitized: %q", txt)
	}
	img, _ := parts[1].(map[string]any)
	if img["type"] != "image" || img["source"].(map[string]any)["data"] != "..." {
		t.Error("image part modified")
	}
}

// 集成：完整请求体经 PrepareBodyOpt 净化后无残留指纹，且 stream/tool_choice 行为不受影响。
func TestPrepareBodyOptSanitizesSystem(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"` + ccIdentity + ` ` + ccHeader + `"},` +
		`{"role":"user","content":"hi"}]}`)
	out := PrepareBodyOpt(body, true)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["stream"] != true {
		t.Error("stream not forced")
	}
	msgs := obj["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, "x-anthropic-billing-header") || strings.Contains(sys, ccIdentity) || strings.Contains(sys, ccBranch) {
		t.Errorf("fingerprints remain: %q", sys)
	}
	if !strings.Contains(sys, "CLI tool") {
		t.Errorf("rewrite missing: %q", sys)
	}
}

func TestPrepareBodyOptDisabledPreservesFingerprints(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	out := PrepareBodyOpt(body, false)
	if !strings.Contains(string(out), ccIdentity) {
		t.Error("sanitize=false should preserve fingerprints")
	}
	// 但 stream 仍强制
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if obj["stream"] != true {
		t.Error("stream should still be forced")
	}
}

// PrepareBody 默认行为 = 开启脱敏（保持向后兼容）。
func TestPrepareBodyDefaultSanitizes(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	out := PrepareBodyOpt(body, true)
	if strings.Contains(string(out), ccIdentity) {
		t.Error("PrepareBody default should sanitize")
	}
}

// 出站边界集成：ChatStream 发往上游的 wire body 必须无残留指纹。
func TestChatStreamWireBodySanitized(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints = true
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"` + ccIdentity + ` ` + ccHeader + `"},` +
		`{"role":"user","content":"hi"}]}`)
	rc, status, respBody, err := c.ChatStream(acct, body)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d: %s", status, respBody)
	}
	// 上游收到的 body：stream 强制 + 指纹已净化
	var obj map[string]any
	if err := json.Unmarshal(gotBody, &obj); err != nil {
		t.Fatalf("wire body not json: %v", err)
	}
	if obj["stream"] != true {
		t.Error("wire body stream not forced")
	}
	sys, _ := obj["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, fp := range []string{"x-anthropic-billing-header", ccIdentity, ccBranch} {
		if strings.Contains(sys, fp) {
			t.Errorf("wire body contains fingerprint %q: %q", fp, sys)
		}
	}
}

// 出站边界（Codex 场景）：instructions 折成 role=system 的首条消息，
// 净化后 wire body 不得残留 Codex 逐字指纹；其余消息不受影响。
func TestChatStreamWireBodyCodexInstructionsSanitized(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints = true
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"kimi-k3","messages":[` +
		`{"role":"system","content":"` + codexInstructions + `"},` +
		`{"role":"user","content":"say ok"}]}`)
	rc, status, respBody, err := c.ChatStream(acct, body)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d: %s", status, respBody)
	}
	var obj map[string]any
	if err := json.Unmarshal(gotBody, &obj); err != nil {
		t.Fatalf("wire body not json: %v", err)
	}
	sys, _ := obj["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, codexInstructions) ||
		strings.Contains(sys, "in the Codex CLI, a terminal-based coding assistant.") {
		t.Errorf("wire body still contains codex fingerprint: %q", sys)
	}
	if !strings.Contains(sys, "running in the Codex CLI tool, a terminal-based") {
		t.Errorf("codex rewrite missing on wire: %q", sys)
	}
	user, _ := obj["messages"].([]any)[1].(map[string]any)["content"].(string)
	if user != "say ok" {
		t.Errorf("user message altered: %q", user)
	}
}

// 出站边界：关闭脱敏后 wire body 原样保留指纹（验证开关真实有效）。
func TestChatStreamWireBodySanitizeDisabled(t *testing.T) {
	var gotBody []byte
	ts := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer ts.Close()

	c := New()
	c.SanitizeFingerprints = false
	c.ChatBaseCN = ts.URL
	acct := &auth.Auth{AccessToken: "test-token", Domain: "copilot.tencent.com", UID: "u1"}

	body := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"` + ccIdentity + `"}]}`)
	rc, status, _, err := c.ChatStream(acct, body)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if status >= 400 {
		t.Fatalf("upstream status %d", status)
	}
	if !strings.Contains(string(gotBody), ccIdentity) {
		t.Error("sanitize disabled should preserve fingerprint on wire")
	}
}

// newTestUpstream 起一个假上游并捕获请求。
func newTestUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}

// reasoning_content / reasoning 是思维链镜像字段：客户端会把上一轮 reasoning 原样
// 带回，历史里出现过的指纹（如裸 11128）此前完全不被净化 → 原样出站触发反探测。
func TestReasoningFieldsSanitized(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "assistant", "content": "ok", "reasoning_content": "上次遇到 11128 被拦截"},
		map[string]any{"role": "assistant", "content": "ok", "reasoning": "again 11128 here"},
	}
	if !sanitizeMessages(msgs) {
		t.Fatal("sanitizeMessages 未报告改动，reasoning 字段被跳过")
	}
	if got := msgs[0].(map[string]any)["reasoning_content"].(string); strings.Contains(got, "11128") {
		t.Errorf("reasoning_content 未被净化: %q", got)
	}
	if got := msgs[1].(map[string]any)["reasoning"].(string); strings.Contains(got, "11128") {
		t.Errorf("reasoning 未被净化: %q", got)
	}
}

// 裸键名（无冒号无值）同样是指纹：剥离层只认「键名:值」形态，assistant 消息里
// 用反引号引用裸键名会整条漏网。兜底层应做最小缩写。
func TestBareBillingHeaderAbbreviated(t *testing.T) {
	msgs := []any{map[string]any{"role": "assistant", "content": "see `x-anthropic-billing-header` field"}}
	if !sanitizeMessages(msgs) {
		t.Fatal("sanitizeMessages 未报告改动，裸键名未命中预检")
	}
	got := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(strings.ToLower(got), "x-anthropic-billing-header") {
		t.Errorf("裸键名未被缩写: %q", got)
	}
	if !strings.Contains(got, "x-anthropic-billing-hdr") {
		t.Errorf("裸键名应缩写为 hdr 形态: %q", got)
	}
}

// DSH 开发者身份句整句逐字命上游渠道校验（400/11128）；改写一个词即绕过，
// 且原句结尾标点必须原样保留（匹配串不带标点）。
func TestDSHIdentityRewritten(t *testing.T) {
	msgs := []any{map[string]any{"role": "developer", "content": "You are an AI agent powered by DeepSeek Harness."}}
	if !sanitizeMessages(msgs) {
		t.Fatal("sanitizeMessages 未报告改动，DSH 身份句未命中")
	}
	got := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(got, "powered by DeepSeek Harness") {
		t.Errorf("身份句未被改写（仍会命中上游逐字拦截）: %q", got)
	}
	if !strings.Contains(got, "running on DeepSeek Harness") {
		t.Errorf("改写语义不对: %q", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("结尾标点应原样保留: %q", got)
	}
}

// 边界保护：裸短语与同模板换名都**不该**被改写（实测上游对它们放行），
// 避免过度净化污染用户内容。
func TestDSHLookalikesNotTouched(t *testing.T) {
	for _, s := range []string{
		"DeepSeek Harness",
		"You are a coding agent powered by the deepseek-v4.1-flash model.",
		"You are an AI agent powered by SomeOtherThing.",
	} {
		msgs := []any{map[string]any{"role": "developer", "content": s}}
		if sanitizeMessages(msgs) {
			t.Errorf("不应改写: %q", s)
		}
	}
}
