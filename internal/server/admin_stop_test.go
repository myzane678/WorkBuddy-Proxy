package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// TestAdminStopMissingScript：stop 脚本不存在时返回 500，且不执行任何脚本
// （安全路径：绝不误杀进程，仅验证 stop API 的防呆与路由）。
func TestAdminStopMissingScript(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-stop-helper.ps1")
	old := os.Getenv("WB2A_STOP_SCRIPT")
	os.Setenv("WB2A_STOP_SCRIPT", missing)
	defer os.Setenv("WB2A_STOP_SCRIPT", old)

	h := NewHandler(Config{Pool: pool.New(""), Upstream: upstream.New()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/api/stop", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}