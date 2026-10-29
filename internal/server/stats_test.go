// stats_test.go 用量统计的聚合、落盘恢复与修剪回归测试（不触碰全局 stats 单例）。
package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
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
	s.record("u1", keyMainID, "m1", 200, 100, 10, 0.5, 1200)
	s.record("u1", keyMainID, "m1", 200, 50, 5, -1, -1)   // usage 缺失 → token/积分按 0
	s.record("u1", keyMainID, "m2", 500, 30, 3, 0.2, 300) // 失败请求
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
	s.record("u1", keyMainID, "m1", 200, 100, 10, 0.5, 800)
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

// TestAdminStatsHistoryDayDetails /admin/api/stats 必须暴露 available_dates 与 day_details
// （概览页日历选择历史日期的数据源），且 day_details 带模型明细与首字均值。
func TestAdminStatsHistoryDayDetails(t *testing.T) {
	s := newTestStats(t)
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	s.record("u1", keyMainID, "m1", 200, 100, 10, 0.5, 800)
	s.mu.Lock()
	s.Days[yesterday] = &dayStat{Date: yesterday, Requests: 2, InTok: 7, OutTok: 3,
		Models: map[string]*modelStat{"m1": {Requests: 2, InTok: 7, OutTok: 3}}}
	s.mu.Unlock()

	old := stats
	stats = s
	defer func() { stats = old }()
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/api/stats", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		AvailableDates []string             `json:"available_dates"`
		DayDetails     map[string]dayDetail `json:"day_details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resp not json: %v", err)
	}
	if len(resp.AvailableDates) != 2 || resp.AvailableDates[0] != yesterday || resp.AvailableDates[1] != today {
		t.Fatalf("available_dates 错误: %v", resp.AvailableDates)
	}
	yd, ok := resp.DayDetails[yesterday]
	if !ok || yd.Requests != 2 || yd.InTok != 7 || yd.OutTok != 3 {
		t.Fatalf("day_details[%s] 错误: %+v", yesterday, yd)
	}
	if len(yd.Models) != 1 || yd.Models[0].Model != "m1" || yd.Models[0].Requests != 2 {
		t.Fatalf("day_details 模型明细错误: %+v", yd.Models)
	}
	td := resp.DayDetails[today]
	if td.TtfbN != 1 || td.TtfbAvgMs != 800 {
		t.Fatalf("day_details[%s] 首字统计错误: %+v", today, td)
	}
}

// TestStatsScopeByAccountAndKey 账号与 Key 两个维度都能独立取数，
// 且「全部账号 + 指定 Key」是跨账号合并。
func TestStatsScopeByAccountAndKey(t *testing.T) {
	s := newTestStats(t)
	s.record("u1", keyMainID, "m1", 200, 100, 10, 0.5, 800)
	s.record("u1", "k2", "m1", 200, 50, 5, 0.25, -1)
	s.record("u2", keyMainID, "m2", 200, 7, 3, 0.1, -1)

	old := stats
	stats = s
	defer func() { stats = old }()
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})})

	get := func(q string) map[string]any {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/api/stats"+q, nil))
		if rec.Code != 200 {
			t.Fatalf("stats%s code=%d body=%s", q, rec.Code, rec.Body)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("stats%s not json: %v", q, err)
		}
		return m
	}
	num := func(m map[string]any, obj, field string) float64 {
		o, _ := m[obj].(map[string]any)
		f, _ := o[field].(float64)
		return f
	}

	if got := num(get(""), "total", "requests"); got != 3 {
		t.Fatalf("全站口径应 3 次请求，得到 %v", got)
	}
	u1 := get("?uid=u1")
	if got := num(u1, "total", "requests"); got != 2 {
		t.Fatalf("u1 累计应 2 次，得到 %v", got)
	}
	if got := num(u1, "today", "requests"); got != 2 {
		t.Fatalf("u1 今日应 2 次，得到 %v", got)
	}
	if got := num(u1, "today", "in_tok"); got != 150 {
		t.Fatalf("u1 今日输入 token 应 150，得到 %v", got)
	}
	u1k2 := get("?uid=u1&key=k2")
	if got := num(u1k2, "total", "requests"); got != 1 {
		t.Fatalf("u1+k2 应 1 次，得到 %v", got)
	}
	if got := num(u1k2, "total", "in_tok"); got != 50 {
		t.Fatalf("u1+k2 输入 token 应 50，得到 %v", got)
	}
	mainOnly := get("?key=" + keyMainID)
	if got := num(mainOnly, "total", "requests"); got != 2 {
		t.Fatalf("主 Key 跨账号应 2 次，得到 %v", got)
	}
	if got := num(mainOnly, "total", "in_tok"); got != 107 {
		t.Fatalf("主 Key 跨账号输入 token 应 107，得到 %v", got)
	}
	keys, _ := u1["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("u1 应有 2 把 Key 的用量，得到 %v", u1["keys"])
	}
	// 日历明细同样按口径过滤
	dd, _ := u1["day_details"].(map[string]any)
	today := time.Now().Format("2006-01-02")
	td, _ := dd[today].(map[string]any)
	if f, _ := td["in_tok"].(float64); f != 150 {
		t.Fatalf("day_details 未按账号口径过滤: %v", td)
	}
}

// TestBackfillLegacyStats 升级前的全站统计归到唯一账号，且幂等（重复调用不翻倍）。
func TestBackfillLegacyStats(t *testing.T) {
	s := newTestStats(t)
	today := time.Now().Format("2006-01-02")
	s.mu.Lock()
	s.Total = totals{Since: today, Requests: 9, Errors: 1, InTok: 900, OutTok: 90, Credit: 1.5}
	s.Days = map[string]*dayStat{today: {Date: today, Requests: 9, InTok: 900, OutTok: 90, Credit: 1.5,
		TTFBN: 9, TTFBSum: 900, Models: map[string]*modelStat{"m1": {Requests: 9, InTok: 900, OutTok: 90}}}}
	s.mu.Unlock()

	old := stats
	stats = s
	defer func() { stats = old }()
	BackfillLegacyStats([]string{"u1"})
	BackfillLegacyStats([]string{"u1"}) // 幂等

	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.Total.Accounts["u1"]
	if a == nil || a.Requests != 9 || a.InTok != 900 || a.Errors != 1 {
		t.Fatalf("历史累计归拢错误: %+v", a)
	}
	if len(a.Keys) != 0 {
		t.Fatalf("历史数据没有 Key 归属，应为空: %+v", a.Keys)
	}
	d := s.Days[today]
	ad := d.Accounts["u1"]
	if ad == nil || ad.Requests != 9 || ad.Models["m1"] == nil {
		t.Fatalf("历史日桶归拢错误: %+v", d.Accounts)
	}
	ad.Models["m1"].Requests++
	if d.Models["m1"].Requests != 9 {
		t.Fatal("迁移后的账号模型桶不应与日桶共享内存")
	}
}

// TestBackfillLegacyStatsSkipsMultiAccount 多账号时无法判断历史归属，不迁移。
func TestBackfillLegacyStatsSkipsMultiAccount(t *testing.T) {
	s := newTestStats(t)
	s.mu.Lock()
	s.Total = totals{Since: "2026-01-01", Requests: 3}
	s.mu.Unlock()
	old := stats
	stats = s
	defer func() { stats = old }()
	BackfillLegacyStats([]string{"u1", "u2"})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Total.Accounts) != 0 {
		t.Fatalf("多账号不应归拢: %+v", s.Total.Accounts)
	}
}
