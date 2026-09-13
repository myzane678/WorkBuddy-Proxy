package upstream

import (
	"encoding/json"
	"testing"
)

func TestPrepareBodyOptWithEfforts(t *testing.T) {
	efforts := map[string][]string{
		"glm-5.2":      {"off", "low", "high"},
		"glm-5.2-mini": {"low", "medium"},
		"glm-5.2-max":  {"high", "xhigh"},
	}
	cases := []struct {
		name    string
		body    string
		efforts map[string][]string
		wantKey string // 输出应带有的 effort 字段名；空表示该字段应不存在
		wantVal string // 期望值
	}{
		{"downgrade to highest supported at or below request",
			`{"model":"glm-5.2-mini","reasoning_effort":"high"}`, efforts, "reasoning_effort", "medium"},
		{"floor to lowest when all supported above request",
			`{"model":"glm-5.2-max","reasoning_effort":"low"}`, efforts, "reasoning_effort", "high"},
		{"supported effort passes through unchanged",
			`{"model":"glm-5.2","reasoning_effort":"low"}`, efforts, "reasoning_effort", "low"},
		{"camelCase field name downgrades and keeps key",
			`{"model":"glm-5.2-mini","reasoningEffort":"high"}`, efforts, "reasoningEffort", "medium"},
		{"unknown model passes through",
			`{"model":"unknown","reasoning_effort":"max"}`, efforts, "reasoning_effort", "max"},
		{"unknown effort value passes through",
			`{"model":"glm-5.2","reasoning_effort":"ultra"}`, efforts, "reasoning_effort", "ultra"},
		{"empty cache passes through",
			`{"model":"glm-5.2","reasoning_effort":"max"}`, map[string][]string{}, "reasoning_effort", "max"},
		{"no effort field untouched",
			`{"model":"glm-5.2-mini","messages":[]}`, efforts, "", ""},
		{"nil efforts map passes through",
			`{"model":"glm-5.2","reasoning_effort":"max"}`, nil, "reasoning_effort", "max"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := PrepareBodyOptWithEfforts([]byte(c.body), false, c.efforts)
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v (body=%s)", err, out)
			}
			if c.wantKey == "" {
				if _, ok := m["reasoning_effort"]; ok {
					t.Errorf("reasoning_effort should be absent, got %v", m["reasoning_effort"])
				}
				if _, ok := m["reasoningEffort"]; ok {
					t.Errorf("reasoningEffort should be absent, got %v", m["reasoningEffort"])
				}
				return
			}
			got, ok := m[c.wantKey].(string)
			if !ok || got != c.wantVal {
				t.Errorf("%s: got %v (%T) want %q", c.wantKey, m[c.wantKey], m[c.wantKey], c.wantVal)
			}
		})
	}
}

// normalizeRoles：developer role 不在上游白名单，命中即 400 code=11128；
// 归一为 system 后语义不变，且不受 sanitize 开关影响。
func TestNormalizeRoles(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"developer normalized", `{"messages":[{"role":"developer","content":"be brief"}]}`, "system"},
		{"developer case-insensitive", `{"messages":[{"role":"Developer","content":"x"}]}`, "system"},
		{"developer trimmed", `{"messages":[{"role":" developer ","content":"x"}]}`, "system"},
		{"system untouched", `{"messages":[{"role":"system","content":"x"}]}`, "system"},
		{"user untouched", `{"messages":[{"role":"user","content":"x"}]}`, "user"},
	}
	for _, c := range cases {
		out := PrepareBodyOpt([]byte(c.body), false)
		var obj map[string]any
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		msgs := obj["messages"].([]any)
		role, _ := msgs[0].(map[string]any)["role"].(string)
		if role != c.want {
			t.Errorf("%s: role = %q, want %q", c.name, role, c.want)
		}
	}
	// messages 缺失时不 panic、不新增 messages。
	out := PrepareBodyOpt([]byte(`{"model":"glm-5.2"}`), false)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if _, present := obj["messages"]; present {
		t.Errorf("no-messages body gained messages: %s", out)
	}
}
