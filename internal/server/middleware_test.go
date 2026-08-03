package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler 回写从上下文提取的 userID，用于验证认证中间件的注入行为。
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(UserIDFromContext(r.Context())))
})

func newAuthRequest(t *testing.T, cfg AuthConfig, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	AuthMiddlewareWithConfig(cfg)(okHandler).ServeHTTP(rec, req)
	return rec
}

func TestAuthMiddleware_APIKey_Correct(t *testing.T) {
	rec := newAuthRequest(t,
		AuthConfig{Mode: "api_key", APIKey: "s3cret"},
		map[string]string{"X-API-Key": "s3cret", "X-User-ID": "alice"},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "alice" {
		t.Errorf("userID = %q, want alice", rec.Body.String())
	}
}

func TestAuthMiddleware_APIKey_Wrong(t *testing.T) {
	rec := newAuthRequest(t,
		AuthConfig{Mode: "api_key", APIKey: "s3cret"},
		map[string]string{"X-API-Key": "wrong", "X-User-ID": "alice"},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_APIKey_MissingHeader(t *testing.T) {
	rec := newAuthRequest(t,
		AuthConfig{Mode: "api_key", APIKey: "s3cret"},
		map[string]string{"X-User-ID": "alice"},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_APIKey_EmptyServerKey(t *testing.T) {
	// 服务端未配置密钥时拒绝请求（500），不允许降级信任 X-User-ID
	rec := newAuthRequest(t,
		AuthConfig{Mode: "api_key", APIKey: ""},
		map[string]string{"X-User-ID": "alice"},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestAuthMiddleware_HeaderMode(t *testing.T) {
	rec := newAuthRequest(t,
		AuthConfig{Mode: "header"},
		map[string]string{"X-User-ID": "bob"},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "bob" {
		t.Errorf("userID = %q, want bob", rec.Body.String())
	}
}

func TestAuthMiddleware_HeaderMode_MissingUserID(t *testing.T) {
	rec := newAuthRequest(t, AuthConfig{Mode: "header"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// ============================================================
// JWT (HS256) 认证测试
// ============================================================

// signTestJWT 使用给定密钥生成 HS256 签名的测试 JWT。
func signTestJWT(t *testing.T, alg, secret string, exp int64, sub string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{"sub": sub, "exp": exp})
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(h + "." + p))
	s := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return h + "." + p + "." + s
}

func TestAuthMiddleware_JWT_Valid(t *testing.T) {
	token := signTestJWT(t, "HS256", "jwt-secret", time.Now().Add(time.Hour).Unix(), "alice")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "alice" {
		t.Errorf("userID = %q, want alice", rec.Body.String())
	}
}

func TestAuthMiddleware_JWT_BadSignature(t *testing.T) {
	token := signTestJWT(t, "HS256", "wrong-secret", time.Now().Add(time.Hour).Unix(), "alice")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_JWT_Expired(t *testing.T) {
	token := signTestJWT(t, "HS256", "jwt-secret", time.Now().Add(-time.Hour).Unix(), "alice")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_JWT_NonHS256(t *testing.T) {
	// alg 非 HS256（即使签名正确）必须拒绝
	token := signTestJWT(t, "HS512", "jwt-secret", time.Now().Add(time.Hour).Unix(), "alice")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_JWT_EmptySub(t *testing.T) {
	token := signTestJWT(t, "HS256", "jwt-secret", time.Now().Add(time.Hour).Unix(), "")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_JWT_Malformed(t *testing.T) {
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: "jwt-secret"},
		map[string]string{"Authorization": "Bearer not-a-jwt"},
	)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_JWT_EmptyServerSecret(t *testing.T) {
	// 服务端未配置密钥时拒绝请求（500），不允许放行任何 token
	token := signTestJWT(t, "HS256", "any", time.Now().Add(time.Hour).Unix(), "alice")
	rec := newAuthRequest(t,
		AuthConfig{Mode: "jwt", JWTSecret: ""},
		map[string]string{"Authorization": "Bearer " + token},
	)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
