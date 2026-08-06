package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// TestAuthMiddleware_APIKey_FixedUserID 验证 api_key 模式配置固定身份后，
// 忽略客户端传入的 X-User-ID（防止同一密钥持有者互相冒充）。
func TestAuthMiddleware_APIKey_FixedUserID(t *testing.T) {
	rec := newAuthRequest(t,
		AuthConfig{Mode: "api_key", APIKey: "s3cret", UserID: "service-bot"},
		map[string]string{"X-API-Key": "s3cret", "X-User-ID": "mallory"},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "service-bot" {
		t.Errorf("userID = %q, want 固定身份 service-bot（应忽略客户端 X-User-ID）", rec.Body.String())
	}
}

// signTestJWTNoExp 生成不含 exp 声明的 HS256 JWT（用于验证缺省过期被拒绝）。
func signTestJWTNoExp(t *testing.T, secret, sub string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"sub": sub})
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(h + "." + p))
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// TestAuthMiddleware_JWT_MissingExp 验证缺少 exp 声明的 JWT 被拒绝
// （否则缺省即永不过期）。
func TestAuthMiddleware_JWT_MissingExp(t *testing.T) {
	token := signTestJWTNoExp(t, "jwt-secret", "alice")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401（缺少 exp 的 JWT 必须拒绝）", rec.Code)
	}
}
