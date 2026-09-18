// stats.go 用量统计（按天×模型聚合）：供 /admin 运行概览的今日/累计卡片与趋势、模型分布图表。
// 自本版本启用起累计；落盘 ./data/stats.json（WB2API_STATS_PATH 可覆盖），
// dirty + 2s 防抖异步写，与 reqlog 同款模式；天桶只保留最近 30 天，Total 聚合不受修剪影响。
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const statsKeepDays = 30

// modelStat 单模型的聚合计数。
type modelStat struct {
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Credit   float64 `json:"credit"`
}

// dayStat 单日聚合（本地时区）。
type dayStat struct {
	Date     string                `json:"date"`
	Requests int64                 `json:"requests"`
	Errors   int64                 `json:"errors"`
	InTok    int64                 `json:"in_tok"`
	OutTok   int64                 `json:"out_tok"`
	Credit   float64               `json:"credit"`
	TTFBN    int64                 `json:"ttfb_n"`  // 有首字耗时的流式请求数
	TTFBSum  int64                 `json:"ttfb_sum"` // 首字耗时合计 ms
	Models   map[string]*modelStat `json:"models"`
}

// totals 全量累计（天桶修剪后仍准确）。
type totals struct {
	Since    string  `json:"since"` // 首次统计日期 YYYY-MM-DD
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Credit   float64 `json:"credit"`
}

// statsStore 进程级单例，handler 与 chatStat.done 直接调用。
type statsStore struct {
	mu       sync.Mutex
	Total    totals              `json:"total"`
	Days     map[string]*dayStat `json:"days"`
	dirty    bool
	flushing bool
	loaded   bool
	path     string
	pathOnce sync.Once
}

var stats = &statsStore{}

// statsPath 惰性解析落盘路径（默认 cwd/data/stats.json）。
func (s *statsStore) statsPath() string {
	s.pathOnce.Do(func() {
		if p := os.Getenv("WB2API_STATS_PATH"); p != "" {
			s.path = p
			return
		}
		s.path = filepath.Join("data", "stats.json")
	})
	return s.path
}

// load 惰性从磁盘恢复（跨重启保留）；调用方需持锁。
func (s *statsStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	raw, err := os.ReadFile(s.statsPath())
	if err != nil {
		return
	}
	// json 只会覆盖导出字段，mu/pathOnce/loaded 等不受影响
	_ = json.Unmarshal(raw, s)
}

// record 请求出口调用（chatStat.done 内）；token/积分 <0 视为 usage 缺失，按 0 计入。
func (s *statsStore) record(model string, status int, inToks, outToks int, credit float64, ttfbMs int64) {
	if !chatLogEnabled {
		return
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	s.mu.Lock()
	s.load()
	if s.Days == nil {
		s.Days = map[string]*dayStat{}
	}
	d := s.Days[day]
	if d == nil {
		d = &dayStat{Date: day, Models: map[string]*modelStat{}}
		s.Days[day] = d
	}
	if d.Models == nil {
		d.Models = map[string]*modelStat{}
	}
	in, out := tokOrZero(inToks), tokOrZero(outToks)
	cr := credit
	if cr < 0 {
		cr = 0
	}
	isErr := status >= 400
	d.Requests++
	d.InTok += in
	d.OutTok += out
	d.Credit += cr
	if isErr {
		d.Errors++
	}
	if ttfbMs >= 0 {
		d.TTFBN++
		d.TTFBSum += ttfbMs
	}
	m := d.Models[model]
	if m == nil {
		m = &modelStat{}
		d.Models[model] = m
	}
	m.Requests++
	m.InTok += in
	m.OutTok += out
	m.Credit += cr
	if isErr {
		m.Errors++
	}
	s.Total.Requests++
	s.Total.InTok += in
	s.Total.OutTok += out
	s.Total.Credit += cr
	if isErr {
		s.Total.Errors++
	}
	if s.Total.Since == "" {
		s.Total.Since = day
	}
	s.pruneLocked(now)
	s.dirty = true
	s.mu.Unlock()
	go s.flushSoon()
}

// pruneLocked 天桶超过上限时丢弃最旧的（调用方持锁）。
func (s *statsStore) pruneLocked(now time.Time) {
	if len(s.Days) <= statsKeepDays {
		return
	}
	cut := now.AddDate(0, 0, -statsKeepDays+1).Format("2006-01-02")
	for k := range s.Days {
		if k < cut {
			delete(s.Days, k)
		}
	}
}

// flushSoon 防抖：2s 内的并发记录合并为一次落盘（与 reqlog 同款）。
func (s *statsStore) flushSoon() {
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

// persistLocked 把统计写入磁盘（调用方持锁）。
func (s *statsStore) persistLocked() {
	os.MkdirAll(filepath.Dir(s.statsPath()), 0o755)
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.statsPath(), raw, 0o600)
}

// tokOrZero usage 缺失(-1)按 0 计。
func tokOrZero(n int) int64 {
	if n < 0 {
		return 0
	}
	return int64(n)
}

// modelAgg /admin/api/stats 的模型聚合行。
type modelAgg struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Credit   float64 `json:"credit"`
}

// dayAgg /admin/api/stats 的天聚合行（趋势图用）。
type dayAgg struct {
	Date     string  `json:"date"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Credit   float64 `json:"credit"`
}

// dayDetail 单日完整明细（含模型分布），供概览页日历选择历史日期后整体切换。
type dayDetail struct {
	Date      string     `json:"date"`
	Requests  int64      `json:"requests"`
	Errors    int64      `json:"errors"`
	InTok     int64      `json:"in_tok"`
	OutTok    int64      `json:"out_tok"`
	Credit    float64    `json:"credit"`
	TtfbN     int64      `json:"ttfb_n"`
	TtfbAvgMs int64      `json:"ttfb_avg_ms"`
	Models    []modelAgg `json:"models"`
}

// adminStats 概览图表数据：今日汇总、累计汇总、最近 7 天趋势、模型分布（今日+累计）。
func (h *Handler) adminStats(w http.ResponseWriter, r *http.Request) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.load()
	todayKey := time.Now().Format("2006-01-02")
	td := stats.Days[todayKey]
	today := map[string]any{"date": todayKey, "requests": int64(0), "errors": int64(0),
		"in_tok": int64(0), "out_tok": int64(0), "credit": 0.0, "ttfb_n": int64(0), "ttfb_avg_ms": int64(0)}
	if td != nil {
		avg := int64(0)
		if td.TTFBN > 0 {
			avg = td.TTFBSum / td.TTFBN
		}
		today = map[string]any{"date": td.Date, "requests": td.Requests, "errors": td.Errors,
			"in_tok": td.InTok, "out_tok": td.OutTok, "credit": td.Credit,
			"ttfb_n": td.TTFBN, "ttfb_avg_ms": avg}
	}
	dates := make([]string, 0, len(stats.Days))
	for k := range stats.Days {
		dates = append(dates, k)
	}
	sort.Strings(dates)
	// available_dates + day_details：天桶全量（30 天内）按日历选择历史日期用。
	// 本地单用户接口，一次带全量换前端切换零延迟；体积 = 天数 × 模型数，几十 KB 量级。
	allDates := dates
	dayDetails := make(map[string]dayDetail, len(allDates))
	for _, k := range allDates {
		d := stats.Days[k]
		avg := int64(0)
		if d.TTFBN > 0 {
			avg = d.TTFBSum / d.TTFBN
		}
		dayDetails[k] = dayDetail{Date: k, Requests: d.Requests, Errors: d.Errors,
			InTok: d.InTok, OutTok: d.OutTok, Credit: d.Credit,
			TtfbN: d.TTFBN, TtfbAvgMs: avg, Models: topModelsAgg(d.Models)}
	}
	if len(dates) > 7 {
		dates = dates[len(dates)-7:]
	}
	days := make([]dayAgg, 0, len(dates))
	for _, k := range dates {
		d := stats.Days[k]
		days = append(days, dayAgg{Date: k, Requests: d.Requests, Errors: d.Errors,
			InTok: d.InTok, OutTok: d.OutTok, Credit: d.Credit})
	}
	// 累计模型分布：跨天合并
	totalModels := map[string]*modelStat{}
	for _, d := range stats.Days {
		for name, m := range d.Models {
			t := totalModels[name]
			if t == nil {
				t = &modelStat{}
				totalModels[name] = t
			}
			t.Requests += m.Requests
			t.Errors += m.Errors
			t.InTok += m.InTok
			t.OutTok += m.OutTok
			t.Credit += m.Credit
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"today":           today,
		"total":           stats.Total,
		"days":            days,
		"models_today":    topModels(td),
		"models_total":    topModelsAgg(totalModels),
		"available_dates": allDates,
		"day_details":     dayDetails,
	})
}

// topModels 单日模型聚合列表（日为空返回空数组）。
func topModels(d *dayStat) []modelAgg {
	if d == nil {
		return []modelAgg{}
	}
	return topModelsAgg(d.Models)
}

// topModelsAgg 模型聚合列表，按 tokens 降序。
func topModelsAgg(m map[string]*modelStat) []modelAgg {
	out := make([]modelAgg, 0, len(m))
	for name, v := range m {
		out = append(out, modelAgg{Model: name, Requests: v.Requests, Errors: v.Errors,
			InTok: v.InTok, OutTok: v.OutTok, Credit: v.Credit})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].InTok+out[i].OutTok > out[j].InTok+out[j].OutTok
	})
	return out
}
