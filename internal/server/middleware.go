// Package server 提供多用户 HTTP API 服务，通过认证中间件提取 userID，
// 将请求路由到对应的 UserSession 执行。
package server

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const userIDKey contextKey = "userID"

// AuthMiddleware 从请求中提取用户标识。
// authMode="header" 时信任 X-User-ID 请求头（适用于内网部署）；
// 后续可扩展 JWT / OAuth2 验证。
func AuthMiddleware(authMode string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var userID string

			switch authMode {
			case "jwt":
				// TODO: 解析 JWT token 提取 sub claim
				userID = extractBearerClaim(r)
			default: // "header"
				userID = r.Header.Get("X-User-ID")
			}

			if userID == "" {
				http.Error(w, `{"error":"unauthorized: missing user identity"}`, http.StatusUnauthorized)
				return
			}

			// 基本校验：防止路径注入
			userID = strings.TrimSpace(userID)
			if len(userID) > 128 || strings.ContainsAny(userID, "/\\<>") {
				http.Error(w, `{"error":"invalid user id"}`, http.StatusBadRequest)
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext 从请求上下文中提取 userID。
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userIDKey).(string); ok {
		return v
	}
	return ""
}

// extractBearerClaim 从 Authorization: Bearer <token> 中提取用户标识。
// 当前为占位实现，后续接入真正的 JWT 解析库。
func extractBearerClaim(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	// TODO: 使用 jwt 库解析 token，返回 claims["sub"]
	// 暂降级为直接使用 token 字符串作为 userID（开发阶段）
	token := strings.TrimPrefix(auth, "Bearer ")
	if len(token) > 64 {
		token = token[:64]
	}
	return token
}
