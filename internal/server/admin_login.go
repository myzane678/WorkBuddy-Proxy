// admin_login.go 后台内嵌登录与账号管理：走 WorkBuddy 插件 OAuth 设备流，
// 浏览器完成后由服务端落盘 auths/workbuddy-<uid>.json 并热加入账号池；同时提供
// 账号的启用/禁用/删除（软删除到 auths/.deleted/，可人工恢复）。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/login"
)

// loginSessionTTL 一次登录会话的有效期：超过即失效，前端需重新点「添加账号」。
const loginSessionTTL = 15 * time.Minute

// uidPattern 上游返回的 uid 直接参与文件名拼装，必须限定字符集（防目录穿越）。
var uidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// loginSession 一次待完成的登录：独立 cookie jar（多账号互不串会话）。
type loginSession struct {
	mu        sync.Mutex // 同一会话的 poll 串行，避免并发落盘/重复加号
	flow      *login.Flow
	state     string
	createdAt time.Time
	result    *login.Result // 成功结果（幂等返回）
	errMsg    string        // 终态失败原因
}

type loginSessions struct {
	mu sync.Mutex
	m  map[string]*loginSession
}

func newLoginSessions() *loginSessions {
	return &loginSessions{m: map[string]*loginSession{}}
}

func (s *loginSessions) new(flow *login.Flow, state string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	s.mu.Lock()
	s.pruneLocked(time.Now())
	s.m[id] = &loginSession{flow: flow, state: state, createdAt: time.Now()}
	s.mu.Unlock()
	return id
}

func (s *loginSessions) get(id string) *loginSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	return s.m[id]
}

func (s *loginSessions) drop(id string) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

func (s *loginSessions) pruneLocked(now time.Time) {
	for id, sess := range s.m {
		if now.Sub(sess.createdAt) > loginSessionTTL {
			delete(s.m, id)
		}
	}
}

func (h *Handler) authDir() string {
	if strings.TrimSpace(h.cfg.AuthDir) != "" {
		return h.cfg.AuthDir
	}
	return "./auths"
}

func (h *Handler) region() string {
	if strings.TrimSpace(h.cfg.Region) != "" {
		return h.cfg.Region
	}
	return "cn"
}

// adminLoginStart 申请登录链接，返回给前端展示（用户需在浏览器完成登录）。
func (h *Handler) adminLoginStart(w http.ResponseWriter, r *http.Request) {
	flow := login.New(h.cfg.LoginBase)
	state, authURL, err := flow.Start(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "msg": "获取授权链接失败：" + err.Error()})
		return
	}
	id := h.logins.new(flow, state)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "auth_url": authURL})
}

// adminLoginPoll 由前端每 2s 轮询一次：pending / ok / failed。
// ok 时落盘 + 热加入账号池 + 清掉旧的禁用标记（upsertLocked 会保留 disabled）。
func (h *Handler) adminLoginPoll(w http.ResponseWriter, r *http.Request) {
	sess := h.logins.get(r.URL.Query().Get("id"))
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "status": "failed", "msg": "登录会话不存在或已过期，请重新点击添加账号"})
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.result != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "status": "ok", "uid": sess.result.UID,
			"nickname": sess.result.Nickname, "expires_at": sess.expiresAt(),
		})
		return
	}
	if sess.errMsg != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": "failed", "msg": sess.errMsg})
		return
	}

	res, err := sess.flow.Poll(r.Context(), sess.state)
	if errors.Is(err, login.ErrPending) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "pending"})
		return
	}
	if err != nil {
		sess.errMsg = err.Error()
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": "failed", "msg": sess.errMsg})
		return
	}
	if err := h.saveLoginAuth(res); err != nil {
		sess.errMsg = err.Error()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "status": "failed", "msg": sess.errMsg})
		return
	}
	sess.result = res
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": "ok", "uid": res.UID,
		"nickname": res.Nickname, "expires_at": sess.expiresAt(),
	})
}

// expiresAt 由登录结果换算 access token 到期时间（Unix 秒，0=未知）。
func (s *loginSession) expiresAt() int64 {
	if s.result == nil || s.result.ExpiresIn <= 0 {
		return 0
	}
	return s.createdAt.Add(time.Duration(s.result.ExpiresIn) * time.Second).Unix()
}

// saveLoginAuth 落盘 + 热加入池。同 uid 重登会覆盖文件并清掉旧禁用标记。
func (h *Handler) saveLoginAuth(res *login.Result) error {
	if !uidPattern.MatchString(res.UID) {
		return fmt.Errorf("上游返回的 uid 不合法：%q", res.UID)
	}
	dir := h.authDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 auth 目录失败：%w", err)
	}
	expires := int64(0)
	if res.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second).Unix()
	}
	a := &auth.Auth{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    expires,
		Domain:       res.Domain,
		UID:          res.UID,
		EnterpriseID: res.EnterpriseID,
		Nickname:     res.Nickname,
		FilePath:     filepath.Join(dir, "workbuddy-"+res.UID+".json"),
	}
	if err := a.SaveAtomic(); err != nil {
		return fmt.Errorf("保存凭据失败：%w", err)
	}
	h.cfg.Pool.Add(a)
	h.cfg.Pool.Enable(res.UID)
	return nil
}

// adminAccountEnable / adminAccountDisable 手动切换账号状态。
func (h *Handler) adminAccountEnable(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.accountUID(w, r)
	if !ok {
		return
	}
	h.cfg.Pool.Enable(uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "已启用", "uid": uid})
}

func (h *Handler) adminAccountDisable(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.accountUID(w, r)
	if !ok {
		return
	}
	h.cfg.Pool.Disable(uid, "手动禁用")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "已禁用", "uid": uid})
}

// adminAccountDelete 软删除：凭据文件移入 auths/.deleted/（不在 LoadDir 的扫描范围），
// 再与目录对齐让池剔除该账号；需要时人工移回即可恢复。
func (h *Handler) adminAccountDelete(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.accountUID(w, r)
	if !ok {
		return
	}
	file := ""
	if a := h.cfg.Pool.AuthByUID(uid); a != nil {
		file = a.FilePath
	}
	if file == "" {
		file = filepath.Join(h.authDir(), "workbuddy-"+uid+".json")
	}
	trash := filepath.Join(h.authDir(), ".deleted")
	if err := os.MkdirAll(trash, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "创建回收目录失败：" + err.Error()})
		return
	}
	dest := filepath.Join(trash, filepath.Base(file)+"."+time.Now().Format("20060102-150405"))
	if _, err := os.Stat(file); err == nil {
		if err := os.Rename(file, dest); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "移动凭据文件失败：" + err.Error()})
			return
		}
	}
	auths, err := auth.LoadDir(h.authDir(), h.region())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "重新扫描账号目录失败：" + err.Error()})
		return
	}
	h.cfg.Pool.SyncToDir(auths)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": "已删除（可从 auths/.deleted 恢复）", "uid": uid})
}

// accountUID 解析请求体里的 uid 并确认账号存在（uid 只用于池内查找，不接受路径）。
func (h *Handler) accountUID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		UID string `json:"uid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "请求体解析失败"})
		return "", false
	}
	uid := strings.TrimSpace(req.UID)
	if uid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少 uid"})
		return "", false
	}
	if h.cfg.Pool.AuthByUID(uid) == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "账号不存在"})
		return "", false
	}
	return uid, true
}
