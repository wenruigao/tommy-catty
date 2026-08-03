// Package server 提供多用户 HTTP API 服务，通过认证中间件提取 userID，
// 将请求路由到对应的 UserSession 执行。
package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const userIDKey contextKey = "userID"

// AuthConfig 认证中间件配置。
type AuthConfig struct {
	// Mode 认证模式："header"（内网）| "jwt" | "api_key"
	Mode string
	// APIKey 共享密钥（api_key 模式下必填，验证 X-API-Key 请求头）；
	// 该模式下空密钥将拒绝所有请求（500），不会降级信任 X-User-ID
	APIKey string
	// JWTSecret HS256 签名密钥（jwt 模式下必填）；
	// 该模式下空密钥将拒绝所有请求（500）
	JWTSecret string
}

// AuthMiddleware 从请求中提取用户标识。
// authMode="header" 时信任 X-User-ID 请求头（仅适用于内网部署，需确保外网不可直接访问）。
func AuthMiddleware(authMode string) func(http.Handler) http.Handler {
	return AuthMiddlewareWithConfig(AuthConfig{Mode: authMode})
}

// AuthMiddlewareWithConfig 使用完整配置创建认证中间件。
func AuthMiddlewareWithConfig(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var userID string

			switch cfg.Mode {
			case "jwt":
				// 未配置签名密钥属于服务端配置错误，拒绝请求
				if cfg.JWTSecret == "" {
					http.Error(w, `{"error":"server misconfigured: jwt auth requires a secret"}`, http.StatusInternalServerError)
					return
				}
				userID = extractBearerClaim(r, cfg.JWTSecret)
			case "api_key":
				// 验证共享密钥；未配置密钥属于服务端配置错误，拒绝请求而非降级信任 X-User-ID
				if cfg.APIKey == "" {
					http.Error(w, `{"error":"server misconfigured: api_key auth requires a key"}`, http.StatusInternalServerError)
					return
				}
				apiKey := r.Header.Get("X-API-Key")
				if subtle.ConstantTimeCompare([]byte(apiKey), []byte(cfg.APIKey)) != 1 {
					http.Error(w, `{"error":"unauthorized: invalid API key"}`, http.StatusUnauthorized)
					return
				}
				userID = r.Header.Get("X-User-ID")
			default: // "header" — 仅内网部署使用
				userID = r.Header.Get("X-User-ID")
			}

			if userID == "" {
				http.Error(w, `{"error":"unauthorized: missing user identity"}`, http.StatusUnauthorized)
				return
			}

			// 基本校验：防止注入
			userID = strings.TrimSpace(userID)
			if len(userID) > 128 || strings.ContainsAny(userID, "/\\<>;") {
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

// extractBearerClaim 校验 Authorization: Bearer <token> 中的 HS256 JWT，
// 返回 payload 的 sub 声明作为用户标识；校验失败时返回空串。
func extractBearerClaim(r *http.Request, secret string) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	sub, err := verifyHS256JWT(strings.TrimPrefix(auth, "Bearer "), secret)
	if err != nil {
		return ""
	}
	return sub
}

// jwtHeader JWT 头部声明。
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// jwtPayload JWT 负载声明（仅保留校验所需字段）。
type jwtPayload struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

// verifyHS256JWT 解析并校验 HS256 签名的 JWT：
// 校验 alg 必须为 HS256、HMAC-SHA256 签名、exp 未过期，并返回 sub。
// sub 为空视为无效。
func verifyHS256JWT(token, secret string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("JWT 格式错误：不是三段结构")
	}

	// 解析头部，校验签名算法
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("JWT 头部解码失败")
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return "", errors.New("JWT 头部解析失败")
	}
	if header.Alg != "HS256" {
		return "", fmt.Errorf("不支持的 JWT 签名算法: %s", header.Alg)
	}

	// 校验 HMAC-SHA256 签名
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	expected := mac.Sum(nil)
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("JWT 签名解码失败")
	}
	if !hmac.Equal(sig, expected) {
		return "", errors.New("JWT 签名校验失败")
	}

	// 解析负载
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("JWT 负载解码失败")
	}
	var payload jwtPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", errors.New("JWT 负载解析失败")
	}

	// 校验过期时间（exp 存在时必须未过期）
	if payload.Exp > 0 && time.Now().Unix() >= payload.Exp {
		return "", errors.New("JWT 已过期")
	}
	if payload.Sub == "" {
		return "", errors.New("JWT 缺少 sub 声明")
	}
	return payload.Sub, nil
}
