// reqlog.go 请求日志环形缓冲 + 防抖落盘：供 /admin/api/requests 查询。
// 仿 zcode-api 的 reqlog 设计：500 条环形缓冲，dirty flag + 2s 防抖异步落盘，
// 路径默认 ./data/request-log.json，可用 WB2API_REQLOG_PATH 覆盖。
package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	reqlogCapacity = 500
	reqlogFlushEvery = 2 * time.Second
)

// RequestEntry 单条请求记录。
type RequestEntry struct {
	Seq    int64  `json:"seq"`
	Time   string `json:"time"` // MM-DD HH:MM:SS
	Model  string `json:"model"`
	Mode   string `json:"mode"` // stream | sync
	Status int    `json:"status"`
	UID    string `json:"uid"` // 前 8 位
	TTFBMs int64  `json:"ttfb_ms"` // -1 = 无（非流式无首字概念）
	InToks int    `json:"in_toks"`  // -1 = usage 缺失
	OutToks int   `json:"out_toks"` // -1 = usage 缺失
	Credit float64 `json:"credit"` // 本次实扣积分（usage.credit），-1 = 缺失
	TotalMs int64 `json:"total_ms"`
}

// reqlogStore 全局日志存储（进程级单例，handler 直接调用）。
type reqlogStore struct {
	mu       sync.Mutex
	buf      []RequestEntry // 定长环形
	next     int            // 下一个写位置
	count    int
	dirty    bool
	path     string
	pathOnce sync.Once
	lastSeq  int64 // 复用 chatSeq 之外独立保存，避免与 stdout 日志耦合
	flushing bool
}

var reqlog = &reqlogStore{}

// reqlogPath 惰性解析落盘路径（默认 cwd/data/request-log.json）。
func (s *reqlogStore) reqlogPath() string {
	s.pathOnce.Do(func() {
		if p := os.Getenv("WB2API_REQLOG_PATH"); p != "" {
			s.path = p
			return
		}
		s.path = filepath.Join("data", "request-log.json")
	})
	return s.path
}

// recordRequest 请求出口调用（chatStat.done 内）。chatLogEnabled=false（测试）时不记录。
func recordRequest(e RequestEntry) {
	if !chatLogEnabled {
		return
	}
	reqlog.mu.Lock()
	if reqlog.buf == nil {
		reqlog.buf = make([]RequestEntry, reqlogCapacity)
	}
	if e.Seq == 0 {
		reqlog.lastSeq++
		e.Seq = reqlog.lastSeq
	} else if e.Seq > reqlog.lastSeq {
		reqlog.lastSeq = e.Seq
	}
	reqlog.buf[reqlog.next] = e
	reqlog.next = (reqlog.next + 1) % reqlogCapacity
	if reqlog.count < reqlogCapacity {
		reqlog.count++
	}
	reqlog.dirty = true
	reqlog.mu.Unlock()
	go reqlog.flushSoon()
}

// flushSoon 防抖：2s 内的并发记录合并为一次落盘。
func (s *reqlogStore) flushSoon() {
	s.mu.Lock()
	if s.flushing {
		s.mu.Unlock()
		return
	}
	s.flushing = true
	s.mu.Unlock()
	time.Sleep(reqlogFlushEvery)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		s.flushing = false
		return
	}
	s.dirty = false
	s.flushing = false
	s.persistLocked()
}

// persistLocked 把缓冲按时间序写入磁盘（调用方持锁）。
func (s *reqlogStore) persistLocked() {
	if s.buf == nil {
		return
	}
	out := make([]RequestEntry, 0, s.count)
	if s.count < reqlogCapacity {
		out = append(out, s.buf[:s.next]...)
	} else {
		out = append(out, s.buf[s.next:]...)
		out = append(out, s.buf[:s.next]...)
	}
	os.MkdirAll(filepath.Dir(s.reqlogPath()), 0o755)
	raw, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.reqlogPath(), raw, 0o600)
}

// loadRequests 启动后首次查询时从磁盘恢复（跨重启保留最近记录）。
func (s *reqlogStore) loadRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf != nil {
		return
	}
	raw, err := os.ReadFile(s.reqlogPath())
	if err != nil {
		s.buf = make([]RequestEntry, reqlogCapacity)
		return
	}
	var loaded []RequestEntry
	if json.Unmarshal(raw, &loaded) != nil {
		s.buf = make([]RequestEntry, reqlogCapacity)
		return
	}
	s.buf = make([]RequestEntry, reqlogCapacity)
	start := 0
	if len(loaded) > reqlogCapacity {
		start = len(loaded) - reqlogCapacity
	}
	for _, e := range loaded[start:] {
		s.buf[s.next] = e
		s.next = (s.next + 1) % reqlogCapacity
		if s.count < reqlogCapacity {
			s.count++
		}
		if e.Seq > s.lastSeq {
			s.lastSeq = e.Seq
		}
	}
}

// recentRequests 返回按时间倒序的最新 n 条记录。
func recentRequests(n int) []RequestEntry {
	reqlog.loadRequests()
	reqlog.mu.Lock()
	defer reqlog.mu.Unlock()
	out := make([]RequestEntry, 0, reqlog.count)
	if reqlog.count < reqlogCapacity {
		// 顺序段 [0, next)，倒序取
		for i := reqlog.next - 1; i >= 0 && len(out) < n; i-- {
			out = append(out, reqlog.buf[i])
		}
	} else {
		for i := reqlog.next - 1; len(out) < n; i-- {
			if i < 0 {
				i = reqlogCapacity - 1
			}
			out = append(out, reqlog.buf[i])
			if i == reqlog.next { // 绕环一周
				break
			}
		}
	}
	return out
}
