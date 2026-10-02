package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

type requestIDKey struct{}

// RequestIDMiddleware 为每个请求注入 X-Request-ID：
//   - 客户端携带则沿用（支持链路追踪透传）
//   - 未携带则生成 UUID v4
//
// ID 注入 context（供下游 handler 和日志使用）并写入响应头。
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}

		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		w.Header().Set("X-Request-ID", id)

		// 将 request_id 注入 slog 默认属性（每次请求独立 handler）
		logger := slog.With("request_id", id)
		ctx = slogContextKey(ctx, logger)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext 从 context 提取 X-Request-ID。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// --- slog context 集成 ---

type slogCtxKey struct{}

func slogContextKey(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, slogCtxKey{}, l)
}

// LoggerFromContext 从 context 提取带 request_id 的 slog.Logger。
// 未找到时返回 slog.Default()。
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(slogCtxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
