// payload.go 改写发往上游的 chat 请求体：
//  1. 强制 stream:true（上游拒绝非流式）
//  2. max_completion_tokens → max_tokens 翻译（新客户端只发别名，上游只认后者）
//  3. tool_choice 归一化（上游该字段是 string，对象形式会 400 code=11101）
//  4. developer 角色归一 + image_url 字符串兼容
//  5. tool_call ↔ tool 结果配对清理（见 tool_pairing.go）
package upstream

import (
	"encoding/json"
	"log"
	"strings"
)

// PrepareBodyOpt 单 pass 改写；sanitize=false 时行为完全还原（仅强制 stream + 归一化 tool_choice）。
func PrepareBodyOpt(src []byte, sanitize bool) []byte {
	return PrepareBodyOptWithEfforts(src, sanitize, nil)
}

// PrepareBodyOptWithEfforts 在 PrepareBodyOpt 基础上按模型 supportedEfforts 降级 reasoning_effort：
// 仅当请求显式携带且模型不支持该档位时，改为 ≤请求档位的最高支持档；支持档全部高于请求档时取最低档；
// 未知模型/未知档位/未携带该字段一律透传。efforts 为 nil 表示未知（不降级）。
func PrepareBodyOptWithEfforts(src []byte, sanitize bool, efforts map[string][]string) []byte {
	if len(src) == 0 {
		return src
	}
	var obj map[string]any
	if err := json.Unmarshal(src, &obj); err != nil {
		return src
	}
	obj["stream"] = true
	translateMaxCompletionTokens(obj)
	normalizeToolChoice(obj)
	normalizeRoles(obj)
	normalizeImageURL(obj)
	// tool 配对两步：先重排再清理。所有模型一律执行（独立于 deepseek-only 的 sanitize 开关）。
	// 不完整配对的 tool_calls/tool 结果会让上游对之后每条消息都返 400，必须先行剔除；
	// 插在结果中间的非 tool 消息（Codex image_resize_notice）同样判配对断裂，先 repack
	// 挪后，再 cleanup 删孤儿，两侧同口径。
	if msgs, ok := obj["messages"].([]any); ok {
		msgs, _ = repackToolResultBlocks(msgs)
		msgs, _ = cleanupOrphanToolCalls(msgs)
		// 无改动时两步都返回原 slice，这里回写等于零操作；任一步重排/删除（哪怕后续
		// 步骤零改动）也必须落到 obj，否则 repack 单独生效的结果会被原 slice 覆盖丢失。
		obj["messages"] = msgs
	}
	normalizeReasoningEffort(obj, efforts)
	if sanitize {
		if msgs, ok := obj["messages"].([]any); ok {
			sanitizeMessages(msgs)
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return src
	}
	return out
}

// effortRank 档位从低到高。
var effortRank = map[string]int{"off": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}

// normalizeReasoningEffort 按模型 supportedEfforts 降级 reasoning_effort（snake/camel 双字段兼容）。
//   - 请求档位模型支持 → 原样透传
//   - 请求档位不支持 → 改为 ≤请求档位的最高支持档（降级）
//   - 支持档全部高于请求档 → 取最低支持档（偏离最小）
//   - 未知模型/未知档位/未携带字段/模型未缓存 → 一律透传
func normalizeReasoningEffort(obj map[string]any, efforts map[string][]string) {
	if len(efforts) == 0 {
		return
	}
	model, _ := obj["model"].(string)
	if model == "" {
		return
	}
	supported, ok := efforts[model]
	if !ok || len(supported) == 0 {
		return
	}
	key := ""
	if _, present := obj["reasoning_effort"]; present {
		key = "reasoning_effort"
	} else if _, present := obj["reasoningEffort"]; present {
		key = "reasoningEffort"
	} else {
		return
	}
	reqStr, ok := obj[key].(string)
	if !ok {
		return
	}
	reqStr = strings.TrimSpace(strings.ToLower(reqStr))
	reqIdx, known := effortRank[reqStr]
	if !known {
		return
	}
	// 在 ≤请求档位的支持档里选最高档；命中且与请求不同才改写。
	best, bestIdx := "", -1
	for _, s := range supported {
		idx, k := effortRank[strings.TrimSpace(strings.ToLower(s))]
		if k && idx <= reqIdx && idx > bestIdx {
			best, bestIdx = s, idx
		}
	}
	if best != "" {
		if !strings.EqualFold(best, reqStr) {
			obj[key] = best
			log.Printf("reasoning_effort downgraded model=%s %s -> %s", model, reqStr, best)
		}
		return
	}
	// 支持档全部高于请求档：取最低支持档。
	lowest, lowestIdx := "", 1<<30
	for _, s := range supported {
		idx, k := effortRank[strings.TrimSpace(strings.ToLower(s))]
		if k && idx < lowestIdx {
			lowest, lowestIdx = s, idx
		}
	}
	if lowest != "" {
		obj[key] = lowest
		log.Printf("reasoning_effort floored model=%s %s -> %s", model, reqStr, lowest)
	}
}

// normalizeRoles 把 messages 里的 developer 角色归一为 system。
//
// 背景：上游对 messages 的 role 字段做白名单校验，developer 不在白名单内，
// 命中即 HTTP 400 code=11128。developer 是 OpenAI 新规范里 system 的别名
// （Codex / Cursor 等新客户端用它承载 system 级指令），改写为 system 不丢语义。
//
// 此归一化是「协议兼容」（补上游 role 白名单），不是「内容脱敏」，
// 因此有意与 SanitizeFingerprints / sanitize 参数解耦：即使 sanitize=false 也照常归一。
//
// 只认 developer 这一个值：其余 role（system/user/assistant/tool/任意未知值）一律原样保留，
// 不合并、不重排、不删除任何消息（上游对多 system 的行为尚未实测，合并会引入新变量）。
func normalizeRoles(obj map[string]any) {
	msgs, ok := obj["messages"].([]any)
	if !ok {
		return
	}
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, ok := msg["role"].(string)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(role), "developer") {
			msg["role"] = "system"
			log.Printf("role normalized developer->system idx=%d", i)
		}
	}
}

// normalizeToolChoice 按上游 Go struct（string 类型）改写 OpenAI tool_choice。
//   - "none"            → 删 tool_choice + 删 tools/functions
//   - {"type":"none"}   → 同上
//   - {"type":"auto"/"required"} → 字符串 "auto"/"required"
//   - {"type":"function","function":{"name":"x"}} → 字符串 "x"
//   - 其他对象/非标量 → 删 tool_choice
func normalizeToolChoice(obj map[string]any) {
	suppress := func() {
		delete(obj, "tools")
		delete(obj, "functions")
	}
	tc, present := obj["tool_choice"]
	if !present {
		return
	}
	switch v := tc.(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(v), "none") {
			delete(obj, "tool_choice")
			suppress()
		}
	case map[string]any:
		typ, _ := v["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		switch typ {
		case "none":
			delete(obj, "tool_choice")
			suppress()
		case "auto", "required":
			obj["tool_choice"] = typ
		case "function":
			name := ""
			if fn, ok := v["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = v["name"].(string)
			}
			if name = strings.TrimSpace(name); name != "" {
				obj["tool_choice"] = name
			} else {
				obj["tool_choice"] = "auto"
			}
		default:
			delete(obj, "tool_choice")
		}
	default:
		delete(obj, "tool_choice")
	}
}

// translateMaxCompletionTokens 把 OpenAI 新别名 max_completion_tokens 翻译成上游认的
// max_tokens。OpenAI 规范里 max_tokens 已 deprecated、max_completion_tokens 是新字段
// （o-series 起引入），DeepSeek Harness 等新客户端只发别名；上游只认 max_tokens，
// 别名透传会被忽略后回落默认输出上限（实测 32000），长流任务被静默截断。
//   - 显式 max_tokens 存在 → 原样保留（显式优先，别名只删不译）；
//   - 别名值为 0/null/负数/非数值 → 不翻译（0/null 语义是「未设置」，负数是非法值）；
//   - 翻译后删别名字段，避免 body 冗余与排障噪音。
func translateMaxCompletionTokens(obj map[string]any) {
	alias, has := obj["max_completion_tokens"]
	delete(obj, "max_completion_tokens") // 无论翻译与否，别名一律删
	if !has {
		return
	}
	if _, explicit := obj["max_tokens"]; explicit {
		return // 显式 max_tokens 优先
	}
	// json.Unmarshal 数字 → float64：取整后回写 int64，避免科学计数法/小数尾进上游 body。
	switch v := alias.(type) {
	case float64:
		if v > 0 && v == float64(int64(v)) {
			obj["max_tokens"] = int64(v)
		}
	case int64:
		if v > 0 {
			obj["max_tokens"] = v
		}
	case int:
		if v > 0 {
			obj["max_tokens"] = int64(v)
		}
	}
}

// normalizeImageURL 把老式字符串形态的 image_url 兼容成 OpenAI 对象形态。
// 部分客户端（以及手写脚本）仍发 {"type":"image_url","image_url":"https://…"}，
// 而上游按对象解析，字符串形态会以图片无效（11135 家族）拒收整个请求。
func normalizeImageURL(obj map[string]any) {
	msgs, ok := obj["messages"].([]any)
	if !ok {
		return
	}
	for _, rawMsg := range msgs {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok || part["type"] != "image_url" {
				continue
			}
			imageURL, ok := part["image_url"].(string)
			if !ok || imageURL == "" {
				continue
			}
			part["image_url"] = map[string]any{"url": imageURL}
		}
	}
}
