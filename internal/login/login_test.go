package login

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeUpstream 模拟插件登录三端点：state 签发、token 待完成/完成、account 取账号。
func fakeUpstream(t *testing.T, done *bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/plugin/auth/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("platform") != "CLI" {
			t.Errorf("state: unexpected %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]string{"state": "st-1", "authUrl": "https://example.test/authorize"},
		})
	})
	mux.HandleFunc("/v2/plugin/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "st-1" {
			t.Errorf("token: unexpected state %q", r.URL.Query().Get("state"))
		}
		if !*done {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 1001, "msg": "login ing"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"accessToken":  "at-1",
				"refreshToken": "rt-1",
				"expiresIn":    3600,
				"domain":       "copilot.tencent.com",
			},
		})
	})
	mux.HandleFunc("/v2/plugin/login/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Errorf("account: missing bearer, got %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"uid": "u-1", "enterpriseId": "e-1", "nickname": "号码一"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFlowStartThenPendingThenOK(t *testing.T) {
	done := false
	srv := fakeUpstream(t, &done)
	f := New(srv.URL)

	state, authURL, err := f.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if state != "st-1" || authURL != "https://example.test/authorize" {
		t.Fatalf("Start = %q %q", state, authURL)
	}

	if _, err := f.Poll(context.Background(), state); !errors.Is(err, ErrPending) {
		t.Fatalf("Poll 未完成时应返回 ErrPending，得到 %v", err)
	}

	done = true
	res, err := f.Poll(context.Background(), state)
	if err != nil {
		t.Fatalf("Poll done: %v", err)
	}
	if res.AccessToken != "at-1" || res.RefreshToken != "rt-1" || res.ExpiresIn != 3600 {
		t.Fatalf("token 字段不对: %+v", res)
	}
	if res.UID != "u-1" || res.Nickname != "号码一" || res.EnterpriseID != "e-1" {
		t.Fatalf("account 字段不对: %+v", res)
	}
}

func TestFlowUpstream5xxIsErrorNotPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	f := New(srv.URL)
	if _, _, err := f.Start(context.Background()); err == nil {
		t.Fatal("5xx 应返回错误")
	}
	if _, err := f.Poll(context.Background(), "st-1"); err == nil || errors.Is(err, ErrPending) {
		t.Fatalf("5xx 不应视为 pending，得到 %v", err)
	}
}

func TestFlowMissingUIDIsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/plugin/auth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"accessToken": "at-1", "refreshToken": "rt-1", "expiresIn": 60},
		})
	})
	mux.HandleFunc("/v2/plugin/login/account", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := New(srv.URL).Poll(context.Background(), "st-1"); err == nil {
		t.Fatal("拿不到 uid 时应报错，避免落盘无效账号")
	}
}
