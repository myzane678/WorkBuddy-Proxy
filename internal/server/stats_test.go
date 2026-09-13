// stats_test.go 用量统计的聚合、落盘恢复与修剪回归测试（不触碰全局 stats 单例）。
package server

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStats 独立实例：开启 chatLog（TestMain 默认关），落盘指向临时目录。
func newTestStats(t *testing.T) *statsStore {
	t.Helper()
	withChatLog(t)
	s := &statsStore{}
	s.pathOnce.Do(func() { s.path = filepath.Join(t.TempDir(), "stats.json") })
	return s
}

// TestStatsRecordAggregates 验证日桶/Total/模型桶聚合与 usage 缺失按 0 计。
func TestStatsRecordAggregates(t *testing.T) {
	s := newTestStats(t)
	today := time.Now().Format("2006-01-02")
	s.record("m1", 200, 100, 10, 0.5, 1200)
	s.record("m1", 200, 50, 5, -1, -1)   // usage 缺失 → token/积分按 0
	s.record("m2", 500, 30, 3, 0.2, 300) // 失败请求
	d := s.Days[today]
	if d == nil || d.Requests != 3 || d.Errors != 1 {
		t.Fatalf("日桶聚合错误: %+v", d)
	}
	if d.InTok != 180 || d.OutTok != 18 {
		t.Fatalf("token 聚合错误: in=%d out=%d", d.InTok, d.OutTok)
	}
	if d.Credit < 0.69 || d.Credit > 0.71 {
		t.Fatalf("积分聚合错误: %v", d.Credit)
	}
	if d.TTFBN != 2 || d.TTFBSum != 1500 {
		t.Fatalf("首字聚合错误: n=%d sum=%d", d.TTFBN, d.TTFBSum)
	}
	if s.Total.Requests != 3 || s.Total.InTok != 180 || s.Total.Since != today {
		t.Fatalf("Total 聚合错误: %+v", s.Total)
	}
	m1 := d.Models["m1"]
	if m1 == nil || m1.Requests != 2 || m1.InTok != 150 || m1.Errors != 0 {
		t.Fatalf("模型桶聚合错误: %+v", m1)
	}
	if top := topModels(d); len(top) != 2 || top[0].Model != "m1" {
		t.Fatalf("topModels 排序错误: %+v", top)
	}
}

// TestStatsPersistReload 落盘后新实例恢复，累计与天桶一致。
func TestStatsPersistReload(t *testing.T) {
	s := newTestStats(t)
	s.record("m1", 200, 100, 10, 0.5, 800)
	s.mu.Lock()
	s.persistLocked()
	s.mu.Unlock()
	var s2 statsStore
	s2.pathOnce.Do(func() { s2.path = s.path })
	s2.mu.Lock()
	s2.load()
	s2.mu.Unlock()
	if s2.Total.Requests != 1 || s2.Total.InTok != 100 || s2.Total.Since == "" {
		t.Fatalf("恢复 Total 错误: %+v", s2.Total)
	}
	today := time.Now().Format("2006-01-02")
	if s2.Days[today] == nil || s2.Days[today].Models["m1"] == nil {
		t.Fatalf("恢复天桶错误: %+v", s2.Days)
	}
}

// TestStatsPrune 天桶超上限时只保留最近 N 天。
func TestStatsPrune(t *testing.T) {
	s := newTestStats(t)
	now := time.Now()
	s.mu.Lock()
	s.Days = map[string]*dayStat{}
	for i := 0; i < statsKeepDays+5; i++ {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		s.Days[day] = &dayStat{Date: day, Models: map[string]*modelStat{}}
	}
	s.pruneLocked(now)
	s.mu.Unlock()
	if len(s.Days) != statsKeepDays {
		t.Fatalf("修剪后天数错误: %d", len(s.Days))
	}
}
