package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubLookup map[string]*SessionIdentity

func (s stubLookup) IdentityByToken(token string) (*SessionIdentity, error) {
	return s[token], nil
}

func newRouter(lookup TokenLookup, apiKey string, allowQuery bool, roles ...Role) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1/sessions/:id", Middleware(lookup, apiKey, allowQuery, roles...))
	g.GET("/subtitles", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/api/v1/sessions/:id/end",
		Middleware(lookup, apiKey, allowQuery, roles...), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
	return r
}

func do(r *gin.Engine, method, path string, bearer string, queryToken string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if queryToken != "" {
		q := req.URL.Query()
		q.Set("token", queryToken)
		req.URL.RawQuery = q.Encode()
	}
	r.ServeHTTP(w, req)
	return w
}

func lookup() stubLookup {
	return stubLookup{
		"host-tok": {SessionID: "s1", Role: RoleHost},
		"view-tok": {SessionID: "s1", Role: RoleView},
	}
}

func TestMissingToken401(t *testing.T) {
	r := newRouter(lookup(), "", false, RoleHost, RoleView)
	if w := do(r, http.MethodGet, "/api/v1/sessions/s1/subtitles", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

func TestInvalidToken401(t *testing.T) {
	r := newRouter(lookup(), "", false, RoleHost, RoleView)
	if w := do(r, http.MethodGet, "/api/v1/sessions/s1/subtitles", "bogus", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", w.Code)
	}
}

func TestHostSatisfiesViewRoute(t *testing.T) {
	r := newRouter(lookup(), "", false, RoleView)
	if w := do(r, http.MethodGet, "/api/v1/sessions/s1/subtitles", "host-tok", ""); w.Code != http.StatusOK {
		t.Fatalf("host on view-only route got %d want 200", w.Code)
	}
}

func TestViewRejectedOnHostRoute(t *testing.T) {
	r := newRouter(lookup(), "", false, RoleHost)
	if w := do(r, http.MethodPost, "/api/v1/sessions/s1/end", "view-tok", ""); w.Code != http.StatusForbidden {
		t.Fatalf("view on host-only route got %d want 403", w.Code)
	}
}

func TestHostAcceptedOnHostRoute(t *testing.T) {
	r := newRouter(lookup(), "", false, RoleHost)
	if w := do(r, http.MethodPost, "/api/v1/sessions/s1/end", "host-tok", ""); w.Code != http.StatusOK {
		t.Fatalf("host got %d want 200", w.Code)
	}
}

func TestTokenCannotCrossSessions(t *testing.T) {
	r := newRouter(lookup(), "", false, RoleHost, RoleView)
	if w := do(r, http.MethodGet, "/api/v1/sessions/other/subtitles", "host-tok", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-session got %d want 401", w.Code)
	}
}

func TestQueryTokenAllowedOnlyWhenEnabled(t *testing.T) {
	// WS/读路径允许 query token。
	rAllow := newRouter(lookup(), "", true, RoleView)
	if w := do(rAllow, http.MethodGet, "/api/v1/sessions/s1/subtitles", "", "view-tok"); w.Code != http.StatusOK {
		t.Fatalf("query token (enabled) got %d want 200", w.Code)
	}
	// 写路径禁止 query token，只认 Authorization 头。
	rDeny := newRouter(lookup(), "", false, RoleHost)
	if w := do(rDeny, http.MethodPost, "/api/v1/sessions/s1/end", "", "host-tok"); w.Code != http.StatusUnauthorized {
		t.Fatalf("query token (disabled) got %d want 401", w.Code)
	}
}

func TestGlobalAPIKeyActsAsHost(t *testing.T) {
	r := newRouter(lookup(), "secret-key", false, RoleHost)
	if w := do(r, http.MethodPost, "/api/v1/sessions/s1/end", "secret-key", ""); w.Code != http.StatusOK {
		t.Fatalf("api key got %d want 200", w.Code)
	}
	// 错误的 key 被拒。
	if w := do(r, http.MethodPost, "/api/v1/sessions/s1/end", "wrong", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key got %d want 401", w.Code)
	}
}

func TestNewTokenPairDistinctAndNonEmpty(t *testing.T) {
	host, view, err := NewTokenPair()
	if err != nil {
		t.Fatalf("token pair: %v", err)
	}
	if host == "" || view == "" || host == view {
		t.Fatalf("bad pair host=%q view=%q", host, view)
	}
	if len(host) != 48 || len(view) != 48 {
		t.Fatalf("token length host=%d view=%d want 48", len(host), len(view))
	}
}

func TestGlobalAPIKeyOnly404WhenUnconfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/sessions", GlobalAPIKeyOnly(""), func(c *gin.Context) { c.Status(200) })
	if w := do(r, http.MethodGet, "/sessions", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unconfigured got %d want 404", w.Code)
	}
}
