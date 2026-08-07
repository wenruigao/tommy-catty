// usage_test.go 验证 /api/v1/usage 端点。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tommy-cat/agent/internal/llm"
)

// TestHandleUsage 验证用量端点返回计量汇总与预算信息。
func TestHandleUsage(t *testing.T) {
	meter := llm.NewMeter(1000)
	meter.Record(llm.TokenRecord{
		Category:         llm.UsageExecution,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		Model:            "m1",
	})
	h := &Handler{Meter: meter}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, "alice"))
	w := httptest.NewRecorder()
	h.handleUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["enabled"] != true {
		t.Error("enabled should be true")
	}
	summary, _ := body["summary"].(map[string]interface{})
	if summary["total_tokens"].(float64) != 15 {
		t.Errorf("summary.total_tokens: got %v, want 15", summary["total_tokens"])
	}
	byModel, _ := summary["by_model"].(map[string]interface{})
	if byModel["m1"].(float64) != 15 {
		t.Errorf("summary.by_model[m1]: got %v, want 15", byModel["m1"])
	}
	budget, _ := body["daily_budget"].(map[string]interface{})
	if budget["limit"].(float64) != 1000 || budget["used"].(float64) != 15 || budget["exceeded"] != false {
		t.Errorf("daily_budget mismatch: %v", budget)
	}
}

// TestHandleUsage_Disabled 未配置计量器时返回 enabled=false。
func TestHandleUsage_Disabled(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req = req.WithContext(context.WithValue(req.Context(), userIDKey, "alice"))
	w := httptest.NewRecorder()
	h.handleUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != `{"enabled":false}`+"\n" {
		t.Errorf("body: %q", got)
	}
}

// TestHandleUsage_Unauthorized 未认证请求应返回 401。
func TestHandleUsage_Unauthorized(t *testing.T) {
	h := &Handler{Meter: llm.NewMeter(0)}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	w := httptest.NewRecorder()
	h.handleUsage(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", w.Code)
	}
}
