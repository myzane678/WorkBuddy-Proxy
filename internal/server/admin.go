// admin.go /admin 状态页 + 管理 API：概览（key/账号/额度）、请求日志、手动签到、改 key、重启。
// 页面 GET /admin 无鉴权（纯静态壳）；/admin/api/* 一律走 Bearer 鉴权。
package server

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"workbuddy2api/internal/scheduler"
)

// AdminExtras admin 依赖（main.go 注入；Scheduler/ConfigPath 可为空，对应能力降级）。
type AdminExtras struct {
	Scheduler  *scheduler.Scheduler
	ConfigPath string
	StartedAt  time.Time
}

//go:embed admin.html
var adminHTML string

// adminExt 由 NewHandler 保存。
var adminExt AdminExtras

// maskKey 掩码展示：保留前 8 后 4。
func maskKey(k string) string {
	if len(k) <= 12 {
		return k
	}
	return k[:8] + "..." + k[len(k)-4:]
}

// registerAdmin 挂载 /admin 相关路由（NewHandler 内调用）。
// registerAdmin 挂载 /admin 相关路由（NewHandler 内调用）。
// GET 类 API 不鉴权：服务只绑 127.0.0.1，且首次打开页面时浏览器还没有 key，
// 放开读接口才能看到状态并自动回填 key；变更类 POST 保留 Bearer 鉴权。
func (h *Handler) registerAdmin(ext AdminExtras) {
	adminExt = ext
	h.mux.HandleFunc("GET /admin", h.adminPage)
	h.mux.HandleFunc("GET /admin/api/overview", h.adminOverview)
	h.mux.HandleFunc("GET /admin/api/requests", h.adminRequests)
	h.mux.HandleFunc("GET /admin/api/stats", h.adminStats)
	h.mux.HandleFunc("POST /admin/api/checkin", h.withAuth(h.adminCheckin))
	h.mux.HandleFunc("POST /admin/api/restart", h.withAuth(h.adminRestart))
	h.mux.HandleFunc("POST /admin/api/stop", h.withAuth(h.adminStop))
	h.mux.HandleFunc("POST /admin/api/key", h.withAuth(h.adminSetKey))
	h.mux.HandleFunc("POST /admin/api/models", h.withAuth(h.adminSetModels))
	h.mux.HandleFunc("POST /admin/api/keys/create", h.withAuth(h.adminCreateKey))
	h.mux.HandleFunc("POST /admin/api/keys/update", h.withAuth(h.adminUpdateKey))
	h.mux.HandleFunc("POST /admin/api/keys/delete", h.withAuth(h.adminDeleteKey))
}

func (h *Handler) adminPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminHTML))
}

// adminOverview 概览：key、账号池、健康、模型（全量+白名单）、签到状态、运行时长。
func (h *Handler) adminOverview(w http.ResponseWriter, r *http.Request) {
	total, healthy, cooling, disabled, inFlightFull := h.cfg.Pool.CountsDetailed()
	accounts := h.cfg.Pool.List()
	all := h.modelList()
	filtered := filterModels(all, h.cfg.Allowlist)
	uptime := time.Duration(0)
	if !adminExt.StartedAt.IsZero() {
		uptime = time.Since(adminExt.StartedAt)
	}
	resp := map[string]any{
		"api_key":        h.cfg.APIKey,
		"api_keys":       h.cfg.APIKeys,
		"accounts":       accounts,
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
		"models":         len(filtered),
		"all_models":     modelIDs(all),
		"allowlist":      h.cfg.Allowlist,
		"uptime_seconds": int64(uptime.Seconds()),
		"has_scheduler":  adminExt.Scheduler != nil,
	}
	if adminExt.Scheduler != nil {
		rep, next := adminExt.Scheduler.CheckinSnapshot()
		resp["checkin"] = rep
		resp["checkin_done"] = rep.CheckedInToday()
		resp["checkin_next"] = next.Format("01-02 15:04")
	}
	writeJSON(w, http.StatusOK, resp)
}

// modelIDs 提取模型 id 列表（供页面勾选白名单）。
func modelIDs(list []map[string]any) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		if id, ok := m["id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// adminRequests 请求日志：?limit=N（默认 20，上限 500）。
func (h *Handler) adminRequests(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= reqlogCapacity {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": recentRequests(limit)})
}

// adminCheckin 立即执行一轮签到 + 余额刷新 + 解冻。
// 同步执行（此前为 fire-and-forget + 前端 4s 轮询）：响应返回时签到已完成，
// 前端拿到结果即可即时把按钮置为「今日已签到」，实时性从"秒级轮询发现"提为"完成即知"。
// 上游调用最坏数秒，本机管理接口可接受。
func (h *Handler) adminCheckin(w http.ResponseWriter, r *http.Request) {
	if adminExt.Scheduler == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "msg": "scheduler unavailable"})
		return
	}
	adminExt.Scheduler.RunCheckinNow()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "checkin done"})
}

// adminRestart 延迟退出，看门狗 3s 内自动拉起。
func (h *Handler) adminRestart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "restarting (watchdog will respawn)"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
}

// adminStop 停止服务：执行 stop-helper.ps1（杀 watchdog 与自身进程）。
// 与重启不同：停止后不会自动拉起，需手动运行 start-workbuddy.cmd。
// 脚本路径默认相对 cwd，可用 WB2A_STOP_SCRIPT 覆盖。
func (h *Handler) adminStop(w http.ResponseWriter, r *http.Request) {
	script := os.Getenv("WB2A_STOP_SCRIPT")
	if script == "" {
		script = "stop-helper.ps1"
	}
	if _, err := os.Stat(script); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "stop script not found: " + script})
		return
	}
	// 延迟启动脚本，确保响应先回到浏览器；脚本随后杀掉 watchdog 与本进程。
	go func() {
		time.Sleep(500 * time.Millisecond)
		cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
		_ = cmd.Start()
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "stopping via " + script + " (restart with start-workbuddy.cmd)"})
}

// adminSetKey 修改 config.json 的 api_key（JSON 逐字段回写，保留其他配置）。
func (h *Handler) adminSetKey(w http.ResponseWriter, r *http.Request) {
	if adminExt.ConfigPath == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "msg": "config path unavailable"})
		return
	}
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "api_key required"})
		return
	}
	if err := writeConfigField(adminExt.ConfigPath, "api_key", req.APIKey); err != "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err})
		return
	}
	h.cfg.APIKey = req.APIKey // 内存同步生效；新 key 立即可用
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "api_key updated (live)"})
}

// adminSetModels 修改模型白名单；空数组 = 全部放行。
func (h *Handler) adminSetModels(w http.ResponseWriter, r *http.Request) {
	if adminExt.ConfigPath == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "msg": "config path unavailable"})
		return
	}
	var req struct {
		Allowlist []string `json:"allowlist"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "allowlist required"})
		return
	}
	// 去空格去空项
	clean := make([]string, 0, len(req.Allowlist))
	for _, m := range req.Allowlist {
		if m = strings.TrimSpace(m); m != "" {
			clean = append(clean, m)
		}
	}
	if err := writeConfigField(adminExt.ConfigPath, "model_allowlist", clean); err != "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err})
		return
	}
	h.cfg.Allowlist = clean // 即时生效：/v1/models 过滤 + chat 403 门禁
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "allowlist updated (live)", "count": len(clean)})
}

// adminCreateKey 新建附加 API key：随机生成，写 config.json + 内存即时生效。
func (h *Handler) adminCreateKey(w http.ResponseWriter, r *http.Request) {
	if adminExt.ConfigPath == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "msg": "config path unavailable"})
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "body required"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "未命名"
	}
	entry, err := newAPIKeyEntry(req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	list := make([]APIKeyEntry, 0, len(h.cfg.APIKeys)+1)
	list = append(list, h.cfg.APIKeys...)
	list = append(list, entry)
	if err := writeConfigField(adminExt.ConfigPath, "api_keys", list); err != "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err})
		return
	}
	h.cfg.APIKeys = list
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": entry})
}

// adminUpdateKey 启用/禁用附加 API key（id 定位；主 key 不在列表内，不受影响）。
func (h *Handler) adminUpdateKey(w http.ResponseWriter, r *http.Request) {
	if adminExt.ConfigPath == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "msg": "config path unavailable"})
		return
	}
	var req struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "id required"})
		return
	}
	list := make([]APIKeyEntry, len(h.cfg.APIKeys))
	copy(list, h.cfg.APIKeys)
	found := false
	for i := range list {
		if list[i].ID == req.ID {
			list[i].Enabled = req.Enabled
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "key id not found"})
		return
	}
	if err := writeConfigField(adminExt.ConfigPath, "api_keys", list); err != "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err})
		return
	}
	h.cfg.APIKeys = list
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "key updated (live)"})
}

// adminDeleteKey 删除附加 API key，即时失效。
func (h *Handler) adminDeleteKey(w http.ResponseWriter, r *http.Request) {
	if adminExt.ConfigPath == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false, "msg": "config path unavailable"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "id required"})
		return
	}
	list := make([]APIKeyEntry, 0, len(h.cfg.APIKeys))
	removed := false
	for _, k := range h.cfg.APIKeys {
		if k.ID == req.ID {
			removed = true
			continue
		}
		list = append(list, k)
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "key id not found"})
		return
	}
	if err := writeConfigField(adminExt.ConfigPath, "api_keys", list); err != "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": err})
		return
	}
	h.cfg.APIKeys = list
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "key deleted (live)"})
}

// writeConfigField 读 config.json → 更新单字段 → 原样回写（保留其他字段）。返回 "" 成功。
func writeConfigField(path, field string, value any) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "read config: " + err.Error()
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "parse config: " + err.Error()
	}
	m[field] = value
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "marshal config"
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "write config: " + err.Error()
	}
	return ""
}
