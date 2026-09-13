package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/config"
)

// 不依赖外部服务：用 miniredis + nil db/store 只验证纯参数校验与路由层行为。
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
	return NewServer(cfg, nil, rdb, nil, nil)
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

func TestUploadChunkMissingSeq(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/chunks?startMs=0&endMs=3000", strings.NewReader("0000"))
	req.Header.Set("Content-Type", "audio/pcm")
	server.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing seq)", w.Code)
	}
}

func TestUploadChunkInvertedRange(t *testing.T) {
	server := newTestServer(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/chunks?seq=1&startMs=3000&endMs=1000", strings.NewReader("0000"))
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
