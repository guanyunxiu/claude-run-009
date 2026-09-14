package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/auth"
	"github.com/livesub/gateway/internal/config"
	"github.com/livesub/gateway/internal/ws"
)

// fakeLookup 测试用令牌反查：<token> 直接映射到 <sessionID>:<role>。
type fakeLookup struct{}

func (fakeLookup) IdentityByToken(token string) (*auth.SessionIdentity, error) {
	switch token {
	case "host-token-x":
		return &auth.SessionIdentity{SessionID: "x", Role: auth.RoleHost}, nil
	case "view-token-x":
		return &auth.SessionIdentity{SessionID: "x", Role: auth.RoleView}, nil
	case "host-token-y":
		return &auth.SessionIdentity{SessionID: "y", Role: auth.RoleHost}, nil
	default:
		return nil, nil
	}
}

// 不依赖外部服务：用 miniredis + 鉴权桩验证路由层行为。
func newTestServer(t *testing.T) *Server {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	cfg := config.Config{
		CORSOrigins:  []string{"*"},
		MaxAudioSize: 1 << 20,
	}
	s := NewServer(cfg, nil, rdb, nil, nil)
	s.SetTokenLookupForTest(fakeLookup{})
	// 注入真实 hub（statusChecker 为 nil 时默认会话 active），供 WS 路由测试。
	s.SetHub(ws.NewHub(rdb, "subtitles", []string{"*"}, nil))
	return s
}

func withBearer(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestCreateSessionInvalidJSON(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// 无令牌访问受保护资源 -> 401。
func TestProtectedRoutesRequireToken(t *testing.T) {
	server := newTestServer(t)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/sessions/x"},
		{http.MethodGet, "/api/v1/sessions/x/subtitles"},
		{http.MethodPost, "/api/v1/sessions/x/chunks?seq=1&startMs=0&endMs=3000"},
		{http.MethodPost, "/api/v1/sessions/x/end"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := withBearer(httptest.NewRequest(tc.method, tc.path, strings.NewReader("0000")), "")
		req.Header.Del("Authorization")
		server.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s -> %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

// 伪造/无效令牌 -> 401。
func TestInvalidTokenRejected(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := withBearer(
		httptest.NewRequest(http.MethodGet, "/api/v1/sessions/x", nil), "nonsense")
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// 观众令牌不能访问主播接口（上传/结束）-> 403。
func TestViewTokenCannotUploadOrEnd(t *testing.T) {
	server := newTestServer(t)

	w := httptest.NewRecorder()
	req := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/end", nil), "view-token-x")
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("end with view token -> %d, want 403", w.Code)
	}

	w2 := httptest.NewRecorder()
	req2 := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/chunks?startMs=0&endMs=3000", strings.NewReader("0000")), "view-token-x")
	req2.Header.Set("Content-Type", "audio/pcm")
	server.Router().ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("upload with view token -> %d, want 403", w2.Code)
	}
}

// A 会话的令牌不能用于 B 会话 -> 401。
func TestTokenScopedToSession(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := withBearer(
		httptest.NewRequest(http.MethodGet, "/api/v1/sessions/y", nil), "view-token-x")
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-session token -> %d, want 401", w.Code)
	}
}

// 未配置 API key 时，列会话接口直接 404（不暴露存在性）。
func TestListSessionsHiddenWithoutAPIKey(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("list sessions -> %d, want 404", w.Code)
	}
}

// 回归：view 令牌必须能通过真实读路由（GET 会话/字幕）。
// 曾因路由同时声明 RoleHost,RoleView 使 wantHost=true 而误拒 view（观众 403 根因）。
func TestViewTokenPassesRealReadRoutes(t *testing.T) {
	server := newTestServer(t)
	paths := []string{
		"/api/v1/sessions/x",
		"/api/v1/sessions/x/subtitles",
		"/api/v1/sessions/x/pipeline-status",
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		req := withBearer(httptest.NewRequest(http.MethodGet, path, nil), "view-token-x")
		server.Router().ServeHTTP(w, req)
		// 鉴权必须放行（db 为 nil 后续可能 500，但绝不能是 401/403）。
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Fatalf("GET %s with view token -> %d, want auth pass", path, w.Code)
		}
	}
}

// view 令牌经 query token（WS 场景）也应通过读路由鉴权。
func TestViewQueryTokenPassesReadRoute(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sessions/x/subtitles/ws?replay=20&token=view-token-x", nil)
	// 鉴权中间件先于 WS handler 执行：即使后续 handler 因夹具未注入 hub 而 panic，
	// Recovery 也会返回 500；关键断言是不能在鉴权层被 401/403 拦截。
	defer func() { _ = recover() }()
	server.Router().ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("WS with view query token -> %d, want auth pass", w.Code)
	}
}

// 鉴权通过后，参数校验才执行：缺 seq 返回 400（而非 401）。
func TestUploadChunkMissingSeq(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/chunks?startMs=0&endMs=3000", strings.NewReader("0000")), "host-token-x")
	req.Header.Set("Content-Type", "audio/pcm")
	server.Router().ServeHTTP(w, req)
	// 鉴权通过，但 db 为 nil：走到 DB 查询会 500（参数校验先于 DB）。
	// 缺 seq 在鉴权后立即 400。
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing seq)", w.Code)
	}
}

func TestUploadChunkInvertedRange(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/chunks?seq=1&startMs=3000&endMs=1000", strings.NewReader("0000")), "host-token-x")
	req.Header.Set("Content-Type", "audio/pcm")
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (endMs<=startMs)", w.Code)
	}
}

func TestUnknownRoute404(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil)
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
