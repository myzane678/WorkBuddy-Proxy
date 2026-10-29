package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// fakeLoginUpstream 假上游：state 直接签发，token 由 done 控制待完成/完成，account 返回固定账号。
func fakeLoginUpstream(t *testing.T, done *atomic.Bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/plugin/auth/state", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]string{"state": "st-1", "authUrl": "https://example.test/authorize"},
		})
	})
	mux.HandleFunc("/v2/plugin/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if !done.Load() {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1001, "msg": "login ing"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"accessToken": "at-new", "refreshToken": "rt-new", "expiresIn": 86400, "domain": "copilot.tencent.com",
			},
		})
	})
	mux.HandleFunc("/v2/plugin/login/account", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"uid": "u-1", "enterpriseId": "e-1", "nickname": "号码一"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func adminReq(t *testing.T, h *Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON（%d）: %s", rec.Code, rec.Body.String())
	}
	return out
}

// TestAdminLoginFlowAddsAndReenablesAccount 覆盖 start → pending → ok 全链路：
// 凭据落盘、池内出现账号、旧 disabled 被清、expires_at 透出。
func TestAdminLoginFlowAddsAndReenablesAccount(t *testing.T) {
	var done atomic.Bool
	srv := fakeLoginUpstream(t, &done)

	dir := t.TempDir()
	p := pool.New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "u-1", Nickname: "号码一"})
	p.Disable("u-1", "12153 session dead")

	h := NewHandler(Config{Pool: p, Upstream: upstream.New(), APIKey: "test-key", AuthDir: dir, Region: "cn", LoginBase: srv.URL})

	rec := adminReq(t, h, "POST", "/admin/api/login/start", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	start := decode(t, rec)
	id, _ := start["id"].(string)
	if id == "" || start["auth_url"] != "https://example.test/authorize" {
		t.Fatalf("start 响应不对: %v", start)
	}

	rec = adminReq(t, h, "GET", "/admin/api/login/poll?id="+id, nil)
	if got := decode(t, rec)["status"]; got != "pending" {
		t.Fatalf("未登录时应 pending，得到 %v", got)
	}

	done.Store(true)
	rec = adminReq(t, h, "GET", "/admin/api/login/poll?id="+id, nil)
	poll := decode(t, rec)
	if poll["status"] != "ok" || poll["uid"] != "u-1" {
		t.Fatalf("登录完成响应不对: %v", poll)
	}
	if exp, _ := poll["expires_at"].(float64); exp <= 0 {
		t.Fatalf("expires_at 未透出: %v", poll)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "workbuddy-u-1.json"))
	if err != nil {
		t.Fatalf("凭据未落盘: %v", err)
	}
	a, err := auth.Parse(raw)
	if err != nil || a.AccessToken != "at-new" || a.RefreshToken != "rt-new" || a.UID != "u-1" {
		t.Fatalf("落盘内容不对: %+v err=%v", a, err)
	}

	st, ok := p.Status("u-1")
	if !ok {
		t.Fatal("池内应有该账号")
	}
	if st.Disabled || st.Reason != "" {
		t.Fatalf("重登后应清除旧禁用标记: %+v", st)
	}
	if st.ExpiresAt <= 0 {
		t.Fatalf("expires_at 未进状态: %+v", st)
	}
	if p.Pick() == nil {
		t.Fatal("重登后账号应可被选中")
	}
}

func TestAdminAccountEnableDisable(t *testing.T) {
	p := pool.New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "u-1"})
	h := NewHandler(Config{Pool: p, Upstream: upstream.New(), APIKey: "test-key", AuthDir: t.TempDir(), Region: "cn"})

	if rec := adminReq(t, h, "POST", "/admin/api/accounts/disable", map[string]string{"uid": "u-1"}); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if st, _ := p.Status("u-1"); !st.Disabled || st.Reason != "手动禁用" {
		t.Fatalf("应已禁用: %+v", st)
	}

	if rec := adminReq(t, h, "POST", "/admin/api/accounts/enable", map[string]string{"uid": "u-1"}); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if st, _ := p.Status("u-1"); st.Disabled || st.Reason != "" {
		t.Fatalf("应已启用: %+v", st)
	}

	if rec := adminReq(t, h, "POST", "/admin/api/accounts/enable", map[string]string{"uid": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("未知账号应 404，得到 %d", rec.Code)
	}
}

func TestAdminAccountDeleteMovesFileAndSyncs(t *testing.T) {
	dir := t.TempDir()
	p := pool.New("")
	defer p.Close()
	a := &auth.Auth{
		UID: "u-1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1,
		FilePath: filepath.Join(dir, "workbuddy-u-1.json"),
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("准备凭据失败: %v", err)
	}
	p.Add(a)

	h := NewHandler(Config{Pool: p, Upstream: upstream.New(), APIKey: "test-key", AuthDir: dir, Region: "cn"})
	if rec := adminReq(t, h, "POST", "/admin/api/accounts/delete", map[string]string{"uid": "u-1"}); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(dir, "workbuddy-u-1.json")); !os.IsNotExist(err) {
		t.Fatalf("原文件应已移走: %v", err)
	}
	trash, _ := os.ReadDir(filepath.Join(dir, ".deleted"))
	if len(trash) != 1 {
		t.Fatalf("应软删除到 .deleted，实际 %d 个文件", len(trash))
	}
	if _, ok := p.Status("u-1"); ok {
		t.Fatal("池内应已剔除该账号")
	}
	if p.AuthByUID("u-1") != nil {
		t.Fatal("AuthByUID 应查不到已删账号")
	}
}
