// Package headers 构造三类上游请求头（common / chat / billing / refresh）。
// 规则来自 docs/api-reference.md §0/§4/§6。
package upstream

import (
	"net/http"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/session"
)

const (
	// clientUA 出站 UA：对齐官方 WorkBuddy Desktop 三段式（applicationName/version
	// + platform/version + CLI/<cliVersion>）。旧值 `CLI/2.63.2 CodeBuddy/2.63.2`
	// 已被上游风控拉黑（上游 issue #54），换官方形状即解除拦截。
	clientUA            = "WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1"
	originRefererCN     = "https://www.codebuddy.cn"
	originRefererGlobal = "https://www.workbuddy.ai"
)

func originRefererFor(a *auth.Auth) string {
	if a != nil && a.Region() == "global" {
		return originRefererGlobal
	}
	return originRefererCN
}

// CommonHeaders 设置所有 API 共享的请求头。
func CommonHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	origin := originRefererFor(a)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", clientUA)
}

// ChatHeaders 在 common 之上加 chat 专属的账号头。
// 缺省字段用 X-No-* 约定（与 CodeBuddy 官方 CLI 一致）。
func ChatHeaders(req *http.Request, a *auth.Auth) {
	CommonHeaders(req, a)
	// 官方客户端流式 chat 的 Accept 形态：CommonHeaders 里带 */* 的是非流式 RPC 默认，
	// 与官方桌面端 chat 请求不一致，属于渠道指纹的一部分。
	req.Header.Set("Accept", "application/json, text/event-stream")
	if a.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	} else {
		req.Header.Set("X-No-Enterprise-Id", "1")
	}
	// 安全红线：绝不在 chat 请求里携带 X-Refresh-Token。
	if a.Domain != "" {
		req.Header.Set("X-Domain", a.Domain)
	} else {
		req.Header.Set("X-No-Department-Info", "1")
	}
	req.Header.Set("X-Product", "SaaS")
}

// ChatMeta 一次 chat 出站的会话头族元数据。handler 必须在**轮转循环外**填好，
// 循环内每次出站复用同一组值——上游按 X-Conversation-Request-ID 聚合请求，换号/重试/
// 降级若各算一套，同一次对话在后台会被拆成多条。
type ChatMeta struct {
	ConversationID        string // X-Conversation-ID：body 提取的客户端原值，非空时优先
	ConversationHeaderID  string // X-Conversation-ID 兜底：客户端没给时由 handler 合成（上游渠道校验要求该头恒在）
	ConversationRequestID string // X-Conversation-Request-ID / X-Root-Request-ID：轮级聚合主键；空则本级补一个
	TraceID               string // X-Trace-ID：入站透传值，空则回落 ConversationRequestID
}

// ChatHeadersWithMeta 在 ChatHeaders 之上注入官方客户端会话头族。
func ChatHeadersWithMeta(req *http.Request, a *auth.Auth, meta ChatMeta) {
	ChatHeaders(req, a)
	injectConversationHeaders(req, meta)
}

// injectConversationHeaders 注入官方客户端会话头族，对齐官方客户端每次请求必带的一组：
//   - X-Conversation-ID：会话级。客户端原值优先；缺省用 handler 合成的兜底值（上游渠道
//     校验要求该头恒在，缺头会被判非官方客户端并拒收）；两者都空时本级再补一个，保证恒发。
//   - X-Conversation-Request-ID：对话轮级聚合主键，必发。同轮内重试/换号/降级复用同值。
//   - X-Conversation-Message-ID = X-Request-ID：消息级，每条独立（32 位 hex）。
//   - X-Root-Request-ID：= 轮级聚合主键（根请求追踪）。
//   - X-Trace-ID：入站透传或 = 轮级聚合主键。
//   - X-B3-TraceId / X-B3-SpanId / X-B3-Sampled：链路族。B3 只认 16/32 位 hex TraceId
//     与 16 位 hex SpanId；轮级键非法时 TraceId 回落 messageID（恒 32 hex），SpanId 取
//     messageID[:16]（每消息新）。
func injectConversationHeaders(req *http.Request, meta ChatMeta) {
	convReqID := meta.ConversationRequestID
	if convReqID == "" {
		// 零值 meta（测试/裸用路径）也要保证聚合主键必发；handler 已生成稳定的不会走到这里。
		convReqID = session.NewMessageID()
	}
	messageID := session.NewMessageID()

	convID := meta.ConversationID
	if convID == "" {
		convID = meta.ConversationHeaderID
	}
	if convID == "" {
		// 零值 meta（测试/裸用路径）也必须发：上游渠道校验缺头即拒收，不存在「可以不发」的场景。
		convID = session.NewMessageID()
	}
	req.Header.Set("X-Conversation-ID", convID)
	req.Header.Set("X-Conversation-Request-ID", convReqID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)
	req.Header.Set("X-Root-Request-ID", convReqID)

	traceID := meta.TraceID
	if traceID == "" {
		traceID = convReqID
	}
	req.Header.Set("X-Trace-ID", traceID)

	b3Trace := convReqID
	if !validTraceID(b3Trace) {
		b3Trace = messageID // 非法 B3 TraceId → 回落恒 32 hex 的消息级 ID
	}
	req.Header.Set("X-B3-TraceId", b3Trace)
	req.Header.Set("X-B3-SpanId", messageID[:16])
	req.Header.Set("X-B3-Sampled", "1")
}

// validTraceID 判断 B3 TraceId 是否合法：16 或 32 位 hex（大小写均可）。
// 客户端透传的轮级键可能是任意形状（含横线/超长/非 hex），直接塞进 B3 头会破坏链路关联。
func validTraceID(s string) bool {
	if len(s) != 16 && len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// BillingHeaders billing 接口请求头。
func BillingHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if a.Domain != "" {
		req.Header.Set("X-Domain", a.Domain)
	}
}

// RefreshHeaders refresh 端点专属头（X-Refresh-Token 只允许出现在这里）。
func RefreshHeaders(req *http.Request, a *auth.Auth) {
	CommonHeaders(req, a)
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	}
	req.Header.Set("X-Auth-Refresh-Source", "workbuddy")
}
