package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tommy-cat/agent/internal/security"
)

// newGuardTestEngine 构建测试安全引擎：task_start 包含"危险"的输入被 deny。
func newGuardTestEngine() *security.Engine {
	eng := security.NewEngine()
	eng.AddPolicy(security.Policy{
		ID:       "deny-dangerous-input",
		Priority: 1,
		Enabled:  true,
		When: security.PolicyCondition{
			ActionType: []string{"task_start"},
			Pattern:    `危险`,
		},
		Then: security.PolicyAction{Effect: security.EffectDeny, Message: "输入包含违禁内容"},
	})
	return eng
}

// okHandler 模拟下游 handler，原样返回 200。
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	// 确认请求体仍然可读（中间件读完必须装回）
	body := make([]byte, r.ContentLength)
	_, _ = r.Body.Read(body)
	w.WriteHeader(http.StatusOK)
})

// TestTaskStartGuard_Deny 验证命中 deny 策略的输入返回 400 中文错误。
func TestTaskStartGuard_Deny(t *testing.T) {
	guard := taskStartGuard(newGuardTestEngine(), okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(`{"message":"帮我执行危险操作"}`))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deny 输入应返回 400，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "违禁内容") {
		t.Errorf("响应应包含中文拦截原因，得到: %s", rec.Body.String())
	}
}

// TestTaskStartGuard_Allow 验证正常输入放行且请求体完整传给下游。
func TestTaskStartGuard_Allow(t *testing.T) {
	guard := taskStartGuard(newGuardTestEngine(), okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat",
		strings.NewReader(`{"message":"帮我查一下天气"}`))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("正常输入应放行（200），得到 %d", rec.Code)
	}
}

// TestTaskStartGuard_OtherRoutes 验证非 chat 路由不受影响。
func TestTaskStartGuard_OtherRoutes(t *testing.T) {
	guard := taskStartGuard(newGuardTestEngine(), okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("其他路由不应被拦截，得到 %d", rec.Code)
	}
}
