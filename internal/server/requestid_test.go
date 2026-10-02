package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/session"
)

func TestRequestIDMiddleware_Generates(t *testing.T) {
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := RequestIDFromContext(r.Context())
		if id == "" {
			t.Error("request_id should be set in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// 响应头应包含 X-Request-ID
	rid := w.Header().Get("X-Request-ID")
	if rid == "" {
		t.Error("X-Request-ID header should be set")
	}
	if len(rid) != 36 { // UUID v4 格式
		t.Errorf("X-Request-ID = %q, expected UUID format", rid)
	}
}

func TestRequestIDMiddleware_Propagates(t *testing.T) {
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := RequestIDFromContext(r.Context())
		if id != "my-trace-id-123" {
			t.Errorf("request_id = %q, want 'my-trace-id-123'", id)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Header.Set("X-Request-ID", "my-trace-id-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("X-Request-ID") != "my-trace-id-123" {
		t.Error("should propagate client-provided X-Request-ID")
	}
}

func TestRequestIDFromContext_Empty(t *testing.T) {
	id := RequestIDFromContext(context.Background())
	if id != "" {
		t.Errorf("expected empty, got %q", id)
	}
}

func TestLoggerFromContext_Fallback(t *testing.T) {
	l := LoggerFromContext(context.Background())
	if l == nil {
		t.Error("should return slog.Default(), not nil")
	}
}

func TestLoggerFromContext_WithMiddleware(t *testing.T) {
	var captured bool
	handler := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := LoggerFromContext(r.Context())
		captured = l != nil
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !captured {
		t.Error("LoggerFromContext should return non-nil logger after middleware")
	}
}

// ============================================================
// Readiness 端点测试
// ============================================================

func TestHandleReadiness_NoGateway(t *testing.T) {
	sm := session.NewSessionManager(session.DefaultManagerConfig(), session.SessionDeps{})
	defer sm.Shutdown()
	h := NewHandler(sm)
	// Gateway 为 nil → not_ready

	req := httptest.NewRequest("GET", "/api/v1/ready", nil)
	w := httptest.NewRecorder()
	h.handleReadiness(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleHealth_Liveness(t *testing.T) {
	sm := session.NewSessionManager(session.DefaultManagerConfig(), session.SessionDeps{})
	defer sm.Shutdown()
	h := NewHandler(sm)

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	h.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("liveness should always return 200, got %d", w.Code)
	}
}
