package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livesub/gateway/internal/ingest"
)

// view 令牌不能启动拉流（host 专属）-> 403。
func TestCreateIngestRequiresHost(t *testing.T) {
	s := newTestServer(t)
	s.SetIngestManager(ingest.NewManager(nil, nil))
	body := `{"kind":"hls","source":"http://x/index.m3u8"}`
	w := httptest.NewRecorder()
	req := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/ingests", strings.NewReader(body)), "view-token-x")
	req.Header.Set("Content-Type", "application/json")
	s.Router().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("view create ingest -> %d, want 403", w.Code)
	}
}

// 无 token -> 401。
func TestIngestListRequiresToken(t *testing.T) {
	s := newTestServer(t)
	s.SetIngestManager(ingest.NewManager(nil, nil))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/x/ingests", nil)
	s.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("list ingests no token -> %d, want 401", w.Code)
	}
}

// webrtc 来源应被明确拒绝（400 校验），而不是静默走 ffmpeg。
func TestCreateIngestRejectsWebRTC(t *testing.T) {
	s := newTestServer(t)
	// db 为 nil 时会在会话检查阶段失败，但 kind 校验在其之前，应先返回 400。
	s.SetIngestManager(ingest.NewManager(nil, nil))
	body := `{"kind":"webrtc","source":"wss://sfu/x"}`
	w := httptest.NewRecorder()
	req := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/ingests", strings.NewReader(body)), "host-token-x")
	req.Header.Set("Content-Type", "application/json")
	s.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("webrtc ingest -> %d, want 400 (not implemented)", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp["error"].(string), "webrtc") {
		t.Fatalf("error should mention webrtc, got %v", resp["error"])
	}
}

// 缺少 source -> 400。
func TestCreateIngestRequiresSource(t *testing.T) {
	s := newTestServer(t)
	s.SetIngestManager(ingest.NewManager(nil, nil))
	w := httptest.NewRecorder()
	req := withBearer(httptest.NewRequest(http.MethodPost,
		"/api/v1/sessions/x/ingests", strings.NewReader(`{"kind":"rtmp"}`)), "host-token-x")
	req.Header.Set("Content-Type", "application/json")
	s.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing source -> %d want 400", w.Code)
	}
}

// URL 自动推断 kind：.m3u8 -> hls，rtmp:// -> rtmp，无法识别 -> 400。
func TestNormalizeKindInference(t *testing.T) {
	cases := []struct {
		kind, url string
		want      ingest.SourceKind
		ok        bool
	}{
		{"", "rtmp://live.example/app/key", ingest.KindRTMP, true},
		{"", "https://example.com/live/index.m3u8", ingest.KindHLS, true},
		{"hls", "https://example.com/x", ingest.KindHLS, true},
		{"", "https://example.com/audio.webm", "", false},
		{"bogus", "rtmp://x", "", false},
	}
	for _, c := range cases {
		k, err := ingest.NormalizeKind(c.kind, c.url)
		if c.ok && (err != nil || k != c.want) {
			t.Errorf("NormalizeKind(%q,%q)=(%q,%v) want %q", c.kind, c.url, k, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("NormalizeKind(%q,%q) expected error", c.kind, c.url)
		}
	}
}
