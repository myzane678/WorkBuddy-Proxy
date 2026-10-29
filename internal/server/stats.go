// stats.go 用量统计（按天 × 账号 × Key × 模型聚合）：供 /admin 运行概览的今日/累计卡片与趋势、模型分布图表。
// 维度树：全站 → 账号(uid) → Key(keyID) → 模型。全站字段同时保留（「全部账号」口径，
// 也兼容升级前只有全站聚合的旧 stats.json）。
// 自本版本启用起累计；落盘 ./data/stats.json（WB2API_STATS_PATH 可覆盖），
// dirty + 2s 防抖异步写，与 reqlog 同款模式；天桶只保留最近 30 天，Total 聚合不受修剪影响。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const statsKeepDays = 30

// 主 Key 与未鉴权请求的 Key 维度标识（附加 Key 用自己的 ID）。
const (
	keyMainID = "__main__"
	keyNoneID = "__none__"
)

// modelStat 单模型的聚合计数。
type modelStat struct {
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Credit   float64 `json:"credit"`
}

// keyStat 单 Key 的聚合计数（含模型分布，保证按 Key 过滤时图表仍有数据）。
type keyStat struct {
	Requests int64                 `json:"requests"`
	Errors   int64                 `json:"errors"`
	InTok    int64                 `json:"in_tok"`
	OutTok   int64                 `json:"out_tok"`
	Credit   float64               `json:"credit"`
	Models   map[string]*modelStat `json:"models,omitempty"`
}

// acctStat 单账号聚合（含 Key 与模型分布）。
type acctStat struct {
	Requests int64                 `json:"requests"`
	Errors   int64                 `json:"errors"`
	InTok    int64                 `json:"in_tok"`
	OutTok   int64                 `json:"out_tok"`
	Credit   float64               `json:"credit"`
	TTFBN    int64                 `json:"ttfb_n,omitempty"`
	TTFBSum  int64                 `json:"ttfb_sum,omitempty"`
	Models   map[string]*modelStat `json:"models,omitempty"`
	Keys     map[string]*keyStat   `json:"keys,omitempty"`
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
	Accounts map[string]*acctStat  `json:"accounts,omitempty"`
}

// totals 全量累计（天桶修剪后仍准确）。
type totals struct {
	Since    string               `json:"since"` // 首次统计日期 YYYY-MM-DD
	Requests int64                `json:"requests"`
	Errors   int64                `json:"errors"`
	InTok    int64                `json:"in_tok"`
	OutTok   int64                `json:"out_tok"`
	Credit   float64              `json:"credit"`
	Accounts map[string]*acctStat `json:"accounts,omitempty"`
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
// uid 为本次实际服务的账号（换号重试时取最后一次），keyID 为命中的 API Key 身份。
func (s *statsStore) record(uid, keyID, model string, status int, inToks, outToks int, credit float64, ttfbMs int64) {
	if !chatLogEnabled {
		return
	}
	if uid == "" {
		uid = "-"
	}
	if keyID == "" {
		keyID = keyNoneID
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
	bumpModel(&d.Models, model, in, out, cr, isErr)
	if d.Accounts == nil {
		d.Accounts = map[string]*acctStat{}
	}
	acct := d.Accounts[uid]
	if acct == nil {
		acct = &acctStat{}
		d.Accounts[uid] = acct
	}
	bumpAcct(acct, keyID, model, in, out, cr, isErr, ttfbMs)

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
	if s.Total.Accounts == nil {
		s.Total.Accounts = map[string]*acctStat{}
	}
	acctT := s.Total.Accounts[uid]
	if acctT == nil {
		acctT = &acctStat{}
		s.Total.Accounts[uid] = acctT
	}
	bumpAcct(acctT, keyID, model, in, out, cr, isErr, ttfbMs)

	s.pruneLocked(now)
	s.dirty = true
	s.mu.Unlock()
	go s.flushSoon()
}

// bumpModel 累加一个模型桶（map 为 nil 时惰性创建）。
func bumpModel(bucket *map[string]*modelStat, model string, in, out int64, cr float64, isErr bool) {
	if *bucket == nil {
		*bucket = map[string]*modelStat{}
	}
	m := (*bucket)[model]
	if m == nil {
		m = &modelStat{}
		(*bucket)[model] = m
	}
	m.Requests++
	m.InTok += in
	m.OutTok += out
	m.Credit += cr
	if isErr {
		m.Errors++
	}
}

// bumpAcct 把一次请求累加进账号桶（含 Key 与模型两层）。
func bumpAcct(a *acctStat, keyID, model string, in, out int64, cr float64, isErr bool, ttfbMs int64) {
	a.Requests++
	a.InTok += in
	a.OutTok += out
	a.Credit += cr
	if isErr {
		a.Errors++
	}
	if ttfbMs >= 0 {
		a.TTFBN++
		a.TTFBSum += ttfbMs
	}
	bumpModel(&a.Models, model, in, out, cr, isErr)
	if a.Keys == nil {
		a.Keys = map[string]*keyStat{}
	}
	k := a.Keys[keyID]
	if k == nil {
		k = &keyStat{}
		a.Keys[keyID] = k
	}
	k.Requests++
	k.InTok += in
	k.OutTok += out
	k.Credit += cr
	if isErr {
		k.Errors++
	}
	bumpModel(&k.Models, model, in, out, cr, isErr)
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

// ---------------------------------------------------------------------------
// 口径过滤：全站 / 单账号 / 单账号+Key / 全部账号+单 Key
// ---------------------------------------------------------------------------

// statScope 统计查询口径：UID 空 = 全部账号，Key 空 = 全部 Key。
type statScope struct {
	UID string
	Key string
}

// counters 一次口径下的扁平聚合（含模型分布）。
type counters struct {
	Requests int64
	Errors   int64
	InTok    int64
	OutTok   int64
	Credit   float64
	TTFBN    int64
	TTFBSum  int64
	Models   map[string]*modelStat
}

func (a *acctStat) counters() counters {
	if a == nil {
		return counters{}
	}
	return counters{Requests: a.Requests, Errors: a.Errors, InTok: a.InTok, OutTok: a.OutTok,
		Credit: a.Credit, TTFBN: a.TTFBN, TTFBSum: a.TTFBSum, Models: a.Models}
}

func (k *keyStat) counters() counters {
	if k == nil {
		return counters{}
	}
	return counters{Requests: k.Requests, Errors: k.Errors, InTok: k.InTok, OutTok: k.OutTok,
		Credit: k.Credit, Models: k.Models}
}

// merge 把 src 并入 dst（模型按名合并）。
func (c *counters) merge(src counters) {
	c.Requests += src.Requests
	c.Errors += src.Errors
	c.InTok += src.InTok
	c.OutTok += src.OutTok
	c.Credit += src.Credit
	c.TTFBN += src.TTFBN
	c.TTFBSum += src.TTFBSum
	if len(src.Models) == 0 {
		return
	}
	if c.Models == nil {
		c.Models = map[string]*modelStat{}
	}
	for name, m := range src.Models {
		t := c.Models[name]
		if t == nil {
			t = &modelStat{}
			c.Models[name] = t
		}
		t.Requests += m.Requests
		t.Errors += m.Errors
		t.InTok += m.InTok
		t.OutTok += m.OutTok
		t.Credit += m.Credit
	}
}

// viewOfDay 按口径取某天的聚合。
func viewOfDay(d *dayStat, sc statScope) counters {
	if d == nil {
		return counters{}
	}
	if sc.UID == "" && sc.Key == "" {
		return counters{Requests: d.Requests, Errors: d.Errors, InTok: d.InTok, OutTok: d.OutTok,
			Credit: d.Credit, TTFBN: d.TTFBN, TTFBSum: d.TTFBSum, Models: d.Models}
	}
	if sc.UID != "" {
		a := d.Accounts[sc.UID]
		if sc.Key == "" {
			return a.counters()
		}
		return a.Keys[sc.Key].counters()
	}
	// 全部账号 + 指定 Key：跨账号合并同一把 Key 的用量
	var out counters
	for _, a := range d.Accounts {
		if k := a.Keys[sc.Key]; k != nil {
			out.merge(k.counters())
		}
	}
	return out
}

// viewOfTotals 按口径取全时段累计（Total 不受天桶修剪影响）。
func viewOfTotals(t *totals, sc statScope) counters {
	if t == nil {
		return counters{}
	}
	if sc.UID == "" && sc.Key == "" {
		return counters{Requests: t.Requests, Errors: t.Errors, InTok: t.InTok, OutTok: t.OutTok, Credit: t.Credit}
	}
	if sc.UID != "" {
		a := t.Accounts[sc.UID]
		if sc.Key == "" {
			return a.counters()
		}
		return a.Keys[sc.Key].counters()
	}
	var out counters
	for _, a := range t.Accounts {
		if k := a.Keys[sc.Key]; k != nil {
			out.merge(k.counters())
		}
	}
	return out
}

// keyAggs 口径下可选的 Key 列表（全时段），供前端下拉。
func keyAggs(t *totals, sc statScope) []keyAgg {
	if t == nil {
		return []keyAgg{}
	}
	acc := map[string]*keyStat{}
	if sc.UID != "" {
		if a := t.Accounts[sc.UID]; a != nil {
			for id, k := range a.Keys {
				acc[id] = k
			}
		}
	} else {
		for _, a := range t.Accounts {
			for id, k := range a.Keys {
				if cur := acc[id]; cur != nil {
					merged := cur.counters()
					merged.merge(k.counters())
					acc[id] = &keyStat{Requests: merged.Requests, Errors: merged.Errors,
						InTok: merged.InTok, OutTok: merged.OutTok, Credit: merged.Credit, Models: merged.Models}
					continue
				}
				acc[id] = k
			}
		}
	}
	out := make([]keyAgg, 0, len(acc))
	for id, k := range acc {
		out = append(out, keyAgg{ID: id, Requests: k.Requests, Errors: k.Errors,
			InTok: k.InTok, OutTok: k.OutTok, Credit: k.Credit})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BackfillLegacyStats 把升级前没有账号维度的历史统计归到当前唯一账号（只执行一次）。
// 依据：升级前的记录无法追溯账号，只在「恰好一个账号」时归属才不会张冠李戴；
// Key 维度留空——历史请求没有记录是哪把 Key 发的，Key 细分从本版本起算。
func BackfillLegacyStats(uids []string) {
	if len(uids) != 1 {
		return
	}
	uid := uids[0]
	if uid == "" {
		return
	}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.load()
	if len(stats.Total.Accounts) > 0 {
		return // 已迁移过
	}
	if stats.Total.Requests == 0 && len(stats.Days) == 0 {
		return
	}
	stats.Total.Accounts = map[string]*acctStat{uid: {
		Requests: stats.Total.Requests,
		Errors:   stats.Total.Errors,
		InTok:    stats.Total.InTok,
		OutTok:   stats.Total.OutTok,
		Credit:   stats.Total.Credit,
	}}
	for _, d := range stats.Days {
		if len(d.Accounts) > 0 {
			continue
		}
		d.Accounts = map[string]*acctStat{uid: {
			Requests: d.Requests,
			Errors:   d.Errors,
			InTok:    d.InTok,
			OutTok:   d.OutTok,
			Credit:   d.Credit,
			TTFBN:    d.TTFBN,
			TTFBSum:  d.TTFBSum,
			Models:   cloneModels(d.Models),
		}}
	}
	log.Printf("stats: 历史统计（%d 次请求）已归到唯一账号 %s，Key 维度自本版本起算",
		stats.Total.Requests, uid)
	stats.dirty = true
	stats.persistLocked()
}

// cloneModels 深拷贝模型桶（避免迁移后与日桶共享 map 被二次累加）。
func cloneModels(src map[string]*modelStat) map[string]*modelStat {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]*modelStat, len(src))
	for name, m := range src {
		if m == nil {
			continue
		}
		cp := *m
		out[name] = &cp
	}
	return out
}

// ---------------------------------------------------------------------------
// /admin/api/stats 响应
// ---------------------------------------------------------------------------

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

// keyAgg /admin/api/stats 的 Key 聚合行（供前端下拉与按 Key 查看）。
type keyAgg struct {
	ID       string  `json:"id"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	InTok    int64   `json:"in_tok"`
	OutTok   int64   `json:"out_tok"`
	Credit   float64 `json:"credit"`
}

// adminStats 概览图表数据：?uid=&key= 过滤口径；未指定时为全站口径。
func (h *Handler) adminStats(w http.ResponseWriter, r *http.Request) {
	sc := statScope{UID: r.URL.Query().Get("uid"), Key: r.URL.Query().Get("key")}
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stats.load()
	todayKey := time.Now().Format("2006-01-02")

	td := viewOfDay(stats.Days[todayKey], sc)

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
		v := viewOfDay(stats.Days[k], sc)
		a := int64(0)
		if v.TTFBN > 0 {
			a = v.TTFBSum / v.TTFBN
		}
		dayDetails[k] = dayDetail{Date: k, Requests: v.Requests, Errors: v.Errors,
			InTok: v.InTok, OutTok: v.OutTok, Credit: v.Credit,
			TtfbN: v.TTFBN, TtfbAvgMs: a, Models: topModelsAgg(v.Models)}
	}
	if len(dates) > 7 {
		dates = dates[len(dates)-7:]
	}
	days := make([]dayAgg, 0, len(dates))
	for _, k := range dates {
		v := viewOfDay(stats.Days[k], sc)
		days = append(days, dayAgg{Date: k, Requests: v.Requests, Errors: v.Errors,
			InTok: v.InTok, OutTok: v.OutTok, Credit: v.Credit})
	}
	// 累计模型分布：跨天合并（沿用 30 天窗口口径）
	var totalModels counters
	for _, d := range stats.Days {
		totalModels.merge(viewOfDay(d, sc))
	}
	tot := viewOfTotals(&stats.Total, sc)
	writeJSON(w, http.StatusOK, map[string]any{
		"today": td.toTodayMap(todayKey),
		"total": map[string]any{
			"since": stats.Total.Since, "requests": tot.Requests, "errors": tot.Errors,
			"in_tok": tot.InTok, "out_tok": tot.OutTok, "credit": tot.Credit,
		},
		"days":            days,
		"models_today":    topModelsAgg(td.Models),
		"models_total":    topModelsAgg(totalModels.Models),
		"available_dates": allDates,
		"day_details":     dayDetails,
		"keys":            keyAggs(&stats.Total, sc),
		"scope":           map[string]string{"uid": sc.UID, "key": sc.Key},
	})
}

// toTodayMap 今日卡的响应形态（保持既有字段名）。
func (c counters) toTodayMap(date string) map[string]any {
	avg := int64(0)
	if c.TTFBN > 0 {
		avg = c.TTFBSum / c.TTFBN
	}
	return map[string]any{"date": date, "requests": c.Requests, "errors": c.Errors,
		"in_tok": c.InTok, "out_tok": c.OutTok, "credit": c.Credit,
		"ttfb_n": c.TTFBN, "ttfb_avg_ms": avg}
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
