package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// parseBody 把改写结果解析成 map，便于断言。
func parseBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, raw)
	}
	return obj
}

func messagesOf(t *testing.T, obj map[string]any) []any {
	t.Helper()
	msgs, ok := obj["messages"].([]any)
	if !ok {
		t.Fatalf("messages 缺失或类型错误: %#v", obj["messages"])
	}
	return msgs
}

func TestTranslateMaxCompletionTokens(t *testing.T) {
	cases := []struct {
		name, body string
		wantMax    any    // nil = 不应存在 max_tokens
		wantAlias  bool   // true = 别名仍应存在（本实现一律删除，恒 false）
	}{
		{"别名翻译", `{"max_completion_tokens":8192}`, float64(8192), false},
		{"显式 max_tokens 优先", `{"max_tokens":1024,"max_completion_tokens":8192}`, float64(1024), false},
		{"0 不翻译", `{"max_completion_tokens":0}`, nil, false},
		{"负数不翻译", `{"max_completion_tokens":-5}`, nil, false},
		{"null 不翻译", `{"max_completion_tokens":null}`, nil, false},
		{"非数值不翻译", `{"max_completion_tokens":"8192"}`, nil, false},
	}
	for _, c := range cases {
		raw := PrepareBodyOpt([]byte(c.body), false)
		obj := parseBody(t, raw)
		got, present := obj["max_tokens"]
		if c.wantMax == nil {
			if present {
				t.Errorf("%s: 不应写 max_tokens，得 %v", c.name, got)
			}
		} else if got != c.wantMax {
			t.Errorf("%s: max_tokens=%v(%T) want %v(%T)", c.name, got, got, c.wantMax, c.wantMax)
		}
		if _, has := obj["max_completion_tokens"]; has != c.wantAlias {
			t.Errorf("%s: 别名应被删除", c.name)
		}
		// 回写必须是整数形态：float64 直出会变成 8.192e3，上游按数值解析虽可容忍，
		// 但 body 里出现科学计数法会让排障与 diff 变得难以阅读。
		if c.name == "别名翻译" && !strings.Contains(string(raw), `"max_tokens":8192`) {
			t.Errorf("别名翻译应写成整数值，得 %s", raw)
		}
	}
}

func TestNormalizeImageURLStringForm(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"https://x/y.png"}]}]}`
	msgs := messagesOf(t, parseBody(t, PrepareBodyOpt([]byte(body), false)))
	part := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	img, ok := part["image_url"].(map[string]any)
	if !ok || img["url"] != "https://x/y.png" {
		t.Fatalf("image_url 未兼容为对象形态: %#v", part["image_url"])
	}
}

func TestCleanupOrphanToolCallWithoutResult(t *testing.T) {
	body := `{"messages":[
		{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"user","content":"next"}]}`
	msgs := messagesOf(t, parseBody(t, PrepareBodyOpt([]byte(body), false)))
	asst := msgs[0].(map[string]any)
	if _, has := asst["tool_calls"]; has {
		t.Errorf("无结果的 tool_call 应被剔除，得 %#v", asst["tool_calls"])
	}
	if len(msgs) != 2 {
		t.Errorf("assistant 消息本身应保留，len=%d", len(msgs))
	}
}

func TestCleanupOrphanToolResult(t *testing.T) {
	body := `{"messages":[{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	msgs := messagesOf(t, parseBody(t, PrepareBodyOpt([]byte(body), false)))
	if len(msgs) != 0 {
		t.Errorf("孤儿 tool 结果应整条删除，得 %#v", msgs)
	}
}

func TestCleanupKeepsCompletePair(t *testing.T) {
	body := `{"messages":[
		{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	msgs := messagesOf(t, parseBody(t, PrepareBodyOpt([]byte(body), false)))
	if len(msgs) != 2 {
		t.Fatalf("完整配对不得改动，len=%d", len(msgs))
	}
	if _, has := msgs[0].(map[string]any)["tool_calls"]; !has {
		t.Error("完整配对的 tool_calls 被误删")
	}
}

// TestRepackNoticeBetweenParallelResults Codex 的 image_resize_notice（developer 消息）
// 夹在并行 tool 结果中间会被上游判配对断裂（11148）：必须挪到整组之后，且结果顺序不变。
func TestRepackNoticeBetweenParallelResults(t *testing.T) {
	body := `{"messages":[
		{"role":"assistant","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},
			{"id":"c2","type":"function","function":{"name":"g","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"developer","content":"<image_resize_notice/>"},
		{"role":"tool","tool_call_id":"c2","content":"r2"}]}`
	msgs := messagesOf(t, parseBody(t, PrepareBodyOpt([]byte(body), false)))
	if len(msgs) != 4 {
		t.Fatalf("消息条数不应变化，len=%d", len(msgs))
	}
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	// developer 已由 normalizeRoles 归一为 system，且被挪到两条 tool 结果之后。
	want := []string{"assistant", "tool", "tool", "system"}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("顺序=%v want %v", roles, want)
		}
	}
	if id := msgs[1].(map[string]any)["tool_call_id"]; id != "c1" {
		t.Errorf("结果顺序应保持，c1 位置=%v", id)
	}
	if id := msgs[2].(map[string]any)["tool_call_id"]; id != "c2" {
		t.Errorf("结果顺序应保持，c2 位置=%v", id)
	}
}

// TestToolPairingRunsWithoutSanitize 配对清理是「让请求通过」的安全网，必须独立于
// sanitize（deepseek-only 脱敏）开关执行。
func TestToolPairingRunsWithoutSanitize(t *testing.T) {
	body := `{"messages":[{"role":"tool","tool_call_id":"orphan","content":"ok"}]}`
	if msgs := messagesOf(t, parseBody(t, PrepareBodyOpt([]byte(body), false))); len(msgs) != 0 {
		t.Errorf("sanitize=false 时也必须清理孤儿 tool 结果，得 %d 条", len(msgs))
	}
}
