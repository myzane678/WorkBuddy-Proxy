// Package scheduler 定时任务：每日签到（09/21点）+ token keepalive（22点）。
// 签到成功后重新查余额，余额 > 0 的冷却账号自动解冻。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// CheckinResult 单账号一轮签到的结果。
type CheckinResult struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	OK       bool   `json:"ok"`     // 签到请求本身无错误（含"今天已签到"）
	Msg      string `json:"msg"`    // 上游返回信息 / 失败原因
	Remain   int64  `json:"remain"` // 签到后余额
}

// CheckinReport 一轮签到（手动或定时）的汇总。
type CheckinReport struct {
	Time    string          `json:"time"`
	Trigger string          `json:"trigger"` // manual | scheduled
	Results []CheckinResult `json:"results"`
}

// Config 调度器依赖。
type Config struct {
	Pool           *pool.Pool
	Upstream       *upstream.Client
	CheckinHours   []int // 默认 [9, 21]
	KeepaliveHours []int // 默认 [22]
}

// Scheduler 调度器。
type Scheduler struct {
	cfg Config
	mu  sync.Mutex
	last CheckinReport // 最近一轮签到（内存 + data/checkin-state.json 落盘）
}

// New 构建。
func New(cfg Config) *Scheduler {
	if len(cfg.CheckinHours) == 0 {
		cfg.CheckinHours = []int{9, 21}
	}
	if len(cfg.KeepaliveHours) == 0 {
		cfg.KeepaliveHours = []int{22}
	}
	s := &Scheduler{cfg: cfg}
	s.loadState()
	return s
}

// nextFire 返回 now 之后最近的一个整点触发时间；hours 为本地小时（0-23）。
func nextFire(now time.Time, hours []int) time.Time {
	var earliest time.Time
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// Run 主循环，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	all := append(append([]int{}, s.cfg.CheckinHours...), s.cfg.KeepaliveHours...)
	for {
		next := nextFire(time.Now(), all)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			h := time.Now().Hour()
			if contains(s.cfg.CheckinHours, h) {
				s.runCheckin("scheduled")
			}
			if contains(s.cfg.KeepaliveHours, h) {
				s.RunKeepaliveNow()
			}
		}
	}
}

func contains(hours []int, h int) bool {
	for _, v := range hours {
		if v == h {
			return true
		}
	}
	return false
}

// RunCheckinNow 手动触发一轮签到（admin 页"立即签到"按钮）。
func (s *Scheduler) RunCheckinNow() {
	s.runCheckin("manual")
}

// runCheckin 对所有账号执行签到 + 余额刷新 + 解冻，并记录结果供 /admin 展示。
// 冷却中的账号也参与（签到就是为了解冻它们）；禁用的跳过。
func (s *Scheduler) runCheckin(trigger string) {
	rep := CheckinReport{Time: time.Now().Format("15:04:05"), Trigger: trigger}
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshToken == "" {
			continue
		}
		res := CheckinResult{UID: st.UID, Nickname: st.Nickname, OK: true}
		if err := s.cfg.Upstream.DailyCheckin(a); err != nil {
			res.Msg = err.Error()
			// 上游把"今天已签到"作为业务 400 返回：对用户而言是成功态，不算失败。
			if strings.Contains(res.Msg, "已签到") {
				res.Msg = "今天已签到"
			} else {
				res.OK = false
				// 其他业务错误也继续走余额查询
			}
		} else {
			res.Msg = "签到成功"
		}
		remain, err := s.cfg.Upstream.UserResource(a)
		if err != nil {
			res.Remain = -1
		} else {
			res.Remain = remain
			s.cfg.Pool.SetCredits(st.UID, remain) // 回写积分：/status 展示 + 三因子权重依赖
			s.cfg.Pool.ReenableIfCredits(st.UID, remain)
		}
		rep.Results = append(rep.Results, res)
	}
	s.mu.Lock()
	s.last = rep
	s.mu.Unlock()
	s.persistState()
}

// CheckinSnapshot 供 /admin 展示：最近一轮签到 + 下次自动签到时间。
func (s *Scheduler) CheckinSnapshot() (CheckinReport, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, nextFire(time.Now(), s.cfg.CheckinHours)
}

// statePath 签到状态落盘路径（服务 cwd 相对）。
func (s *Scheduler) statePath() string { return filepath.Join("data", "checkin-state.json") }

// persistState 落盘最近签到记录（跨重启保留显示）。
func (s *Scheduler) persistState() {
	s.mu.Lock()
	raw, err := json.Marshal(s.last)
	s.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.statePath()), 0o755)
	_ = os.WriteFile(s.statePath(), raw, 0o600)
}

// loadState 启动时恢复最近签到记录。
func (s *Scheduler) loadState() {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		return
	}
	var rep CheckinReport
	if json.Unmarshal(raw, &rep) == nil && len(rep.Results) > 0 {
		s.last = rep
	}
}

// RunKeepaliveNow 立即对所有账号刷新 token；session 死亡的自动禁用。
func (s *Scheduler) RunKeepaliveNow() {
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshToken == "" {
			continue
		}
		if err := s.cfg.Upstream.RefreshToken(a); err != nil {
			log.Printf("keepalive %s: %v", st.UID, err)
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
				s.cfg.Pool.Disable(st.UID, "12153 session dead")
			}
			continue
		}
		if err := a.SaveAtomic(); err != nil {
			log.Printf("keepalive %s save: %v", st.UID, err)
		}
	}
}
