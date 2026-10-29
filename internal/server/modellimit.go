// modellimit.go 「账号 + 模型」维度的上游限流记忆。
//
// 背景：上游对单个模型有独立的用量配额，到顶返回 429 code=6004
// 「您的使用量已超出频率限制，将在 <时间> 重置，您也可以切换其他模型继续使用」。
// 这是**模型维度**的限流：同一账号上的其他模型完全正常——实测 2026-10-29 19:31
// 同账号 deepseek-v4.1-flash 返 429、glm-5.3-flash 同刻 200。
//
// 旧行为把它当账号故障：applyErrorPolicy → pool.Cooldown 冷却整个账号，而
// pool.Cooldown 内部同时喂熔断器（pool.go recordBreakerFailureLocked），3 次即熔断、
// 指数退避 30m→60m→120m→240m；出口再把结果包装成 503 no_healthy_account。
// 单账号部署下这等于「一个模型配额到顶 = 整个代理看起来挂了」，客户端按 5xx 语义
// 持续重试，每次重试又打上游 MaxRotate 次、继续喂熔断，自愈时间越滚越长。
//
// 本表只做一件事：在限流窗口内跳过「该账号的这个模型」，让同号其他模型照常服务。
// 绝不改账号健康状态、绝不喂熔断器。
package server

import (
	"sync"
	"time"
)

type modelLimitKey struct {
	uid   string
	model string
}

// modelLimitEntry 一次模型级限流的现场：截止时间 + 上游原始响应体
// （原文含重置时间与「可换其他模型」提示，出口直接透传给客户端）。
type modelLimitEntry struct {
	until time.Time
	body  string
}

type modelLimitStore struct {
	mu sync.Mutex
	m  map[modelLimitKey]modelLimitEntry
}

func newModelLimitStore() *modelLimitStore {
	return &modelLimitStore{m: map[modelLimitKey]modelLimitEntry{}}
}

// set 记录一次模型级限流。窗口内重复命中只覆盖截止时间与原文，不重复建键。
// nil 接收者安全：Handler 若绕过 NewHandler 裸构造，限流记忆退化为「不记忆」。
func (s *modelLimitStore) set(uid, model string, until time.Time, body string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[modelLimitKey]modelLimitEntry{}
	}
	s.m[modelLimitKey{uid, model}] = modelLimitEntry{until: until, body: body}
}

// get 返回仍在窗口内的限流记录；已过期则惰性删除并返回 false。
func (s *modelLimitStore) get(uid, model string) (modelLimitEntry, bool) {
	if s == nil {
		return modelLimitEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := modelLimitKey{uid, model}
	e, ok := s.m[k]
	if !ok {
		return modelLimitEntry{}, false
	}
	if !time.Now().Before(e.until) {
		delete(s.m, k)
		return modelLimitEntry{}, false
	}
	return e, true
}

// clear 该模型恢复正常（拿到 200）后清掉记录：窗口可能因上游放宽而提前结束，
// 留着会让下一次请求白跳过一个真能用的号。
func (s *modelLimitStore) clear(uid, model string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, modelLimitKey{uid, model})
}
