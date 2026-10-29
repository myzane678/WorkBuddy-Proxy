// Package login 实现 WorkBuddy（CN）插件 OAuth 设备流，供后台「添加账号 / 重新登录」复用。
//
// 协议与 cmd/login 完全一致，四步：
//
//	POST /v2/plugin/auth/state?platform=CLI  → state + authUrl（用户在浏览器完成登录）
//	GET  /v2/plugin/auth/token?state=        → 令牌（未完成时业务 code 非 0，视为 pending）
//	GET  /v2/plugin/login/account?state=     → uid / enterpriseId / nickname
//
// 无 PKCE：state 由服务端签发。每个 Flow 自带独立 cookie jar，多账号登录互不串会话。
package login

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

const (
	// DefaultBaseURL CN 区上游。
	DefaultBaseURL = "https://copilot.tencent.com"
	// 登录端点沿用 CLI 身份：与 chat 的 UA 不同，不要与 upstream 包混用。
	defaultUA     = "CLI/2.63.2 CodeBuddy/2.63.2"
	originReferer = "https://www.codebuddy.cn"
)

// ErrPending 表示用户尚未在浏览器完成登录（token 端点还没有 token）。
var ErrPending = errors.New("login pending")

// Result 一次成功登录得到的凭据与账号信息。
type Result struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Domain       string
	UID          string
	EnterpriseID string
	Nickname     string
}

// Flow 一次登录流程的客户端。BaseURL 可注入（测试用 httptest 假上游）。
type Flow struct {
	BaseURL string
	UA      string
	HTTP    *http.Client
}

// New 构建一个独立登录流程；baseURL 为空时用 CN 默认值。
func New(baseURL string) *Flow {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	jar, _ := cookiejar.New(nil)
	return &Flow{
		BaseURL: strings.TrimRight(baseURL, "/"),
		UA:      defaultUA,
		HTTP:    &http.Client{Timeout: 30 * time.Second, Jar: jar},
	}
}

// apiEnvelope 上游统一信封。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// do 发请求并解信封。code != 0 时返回 apiError（由调用方决定是否当作 pending）。
func (f *Flow) do(ctx context.Context, method, fullURL string, body io.Reader, headers func(*http.Request)) (json.RawMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	f.commonHeaders(req)
	if headers != nil {
		headers(req)
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 {
		return nil, resp.StatusCode, fmt.Errorf("upstream %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, &apiError{status: resp.StatusCode, msg: truncate(string(raw), 160)}
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, &apiError{status: resp.StatusCode, code: env.Code, msg: env.Msg}
	}
	return env.Data, resp.StatusCode, nil
}

func (f *Flow) commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
	req.Header.Set("User-Agent", f.UA)
}

// apiError 上游业务错误（code/msg），保留状态码供调用方区分 4xx/5xx。
type apiError struct {
	status int
	code   int
	msg    string
}

func (e *apiError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("upstream %d", e.status)
	}
	return fmt.Sprintf("upstream %d: %s", e.status, e.msg)
}

// Start 申请登录态并返回浏览器授权链接。
func (f *Flow) Start(ctx context.Context) (state, authURL string, err error) {
	data, _, err := f.do(ctx, http.MethodPost, f.BaseURL+"/v2/plugin/auth/state?platform=CLI", bytes.NewReader([]byte("{}")), nil)
	if err != nil {
		return "", "", err
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		return "", "", fmt.Errorf("auth state: missing state or authUrl")
	}
	return st.State, st.AuthURL, nil
}

// Poll 查一次登录结果。用户尚未在浏览器完成时返回 ErrPending。
func (f *Flow) Poll(ctx context.Context, state string) (*Result, error) {
	if strings.TrimSpace(state) == "" {
		return nil, fmt.Errorf("empty state")
	}
	tokRaw, _, err := f.do(ctx, http.MethodGet, f.BaseURL+"/v2/plugin/auth/token?state="+state, nil, nil)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			// 4xx / 业务 code 非 0：登录未完成或 state 已失效，都按 pending 处理，
			// 由会话 TTL 兜底（与 cmd/login 的语义一致）。
			return nil, ErrPending
		}
		return nil, err
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		return nil, ErrPending
	}

	res := &Result{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		Domain:       tok.Domain,
	}
	// account 端点带 Bearer 取 uid/nickname；失败不致命，但 uid 拿不到就无法落盘。
	acctHeaders := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if acctRaw, _, errAcct := f.do(ctx, http.MethodGet, f.BaseURL+"/v2/plugin/login/account?state="+state, nil, acctHeaders); errAcct == nil {
		var acct struct {
			UID          string `json:"uid"`
			EnterpriseID string `json:"enterpriseId"`
			Nickname     string `json:"nickname"`
		}
		_ = json.Unmarshal(acctRaw, &acct)
		res.UID = acct.UID
		res.EnterpriseID = acct.EnterpriseID
		res.Nickname = acct.Nickname
	}
	if res.UID == "" {
		return nil, fmt.Errorf("登录成功但未取到 uid，请重试")
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
