package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/security"
	"github.com/tommy-cat/agent/internal/session"
)

// Handler 持有 HTTP API 所需的共享依赖。
type Handler struct {
	SessionMgr *session.SessionManager

	// Meter Token 计量器（网关全局口径），为 /api/v1/usage 提供数据；nil 表示不暴露
	Meter *llm.Meter

	// SecEngine 安全策略引擎，任务完成后评估 task_end 检查点（携带 Cost，供 cost-guard）；nil 则跳过
	SecEngine *security.Engine
}

// NewHandler 创建 HTTP handler。
func NewHandler(sm *session.SessionManager) *Handler {
	return &Handler{SessionMgr: sm}
}

// RegisterRoutes 注册所有 API 路由到给定的 mux。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/chat", h.handleChat)
	mux.HandleFunc("GET /api/v1/history", h.handleHistory)
	mux.HandleFunc("POST /api/v1/clear", h.handleClear)
	mux.HandleFunc("GET /api/v1/health", h.handleHealth)
	mux.HandleFunc("GET /api/v1/usage", h.handleUsage)
}

// --- Request/Response types ---

type chatRequest struct {
	Message string `json:"message"`
}

type chatResponse struct {
	TaskID     string   `json:"task_id"`
	Answer     string   `json:"answer"`
	Steps      int      `json:"steps"`
	TokenUsage int      `json:"token_usage"`
	DurationMs int64    `json:"duration_ms"`
	ToolsUsed  []string `json:"tools_used,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type historyResponse struct {
	Messages []messageItem `json:"messages"`
}

type messageItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// --- Handlers ---

// handleChat 处理用户任务请求。
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "message is required"})
		return
	}

	sess := h.SessionMgr.GetOrCreate(userID)
	result, err := sess.Run(r.Context(), req.Message)
	if err != nil {
		if err == session.ErrRateLimited {
			writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: "rate limit exceeded"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	// 构建响应
	resp := chatResponse{
		TaskID:     result.TaskID,
		Steps:      len(result.Steps),
		TokenUsage: result.TokenUsage,
		DurationMs: result.EndTime.Sub(result.StartTime).Milliseconds(),
	}

	if len(result.Steps) > 0 {
		lastStep := result.Steps[len(result.Steps)-1]
		resp.Answer = lastStep.FinalAnswer
	}

	// 收集使用的工具
	for _, step := range result.Steps {
		if step.Action != "" {
			resp.ToolsUsed = append(resp.ToolsUsed, step.Action)
		}
	}

	// task_end 策略评估（携带 Cost 估算，供 cost-guard 等成本策略；与 CLI 模式口径一致）
	if h.SecEngine != nil {
		h.SecEngine.Evaluate(security.Checkpoint{
			Type:      "task_end",
			UserID:    userID,
			Cost:      float64(result.TokenUsage) * llm.CostPerToken,
			Timestamp: time.Now(),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleHistory 获取用户对话历史。
func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	sess, ok := h.SessionMgr.Get(userID)
	if !ok {
		writeJSON(w, http.StatusOK, historyResponse{Messages: []messageItem{}})
		return
	}

	msgs := sess.GetHistory(limit)
	items := make([]messageItem, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, messageItem{Role: m.Role, Content: m.Content})
	}

	writeJSON(w, http.StatusOK, historyResponse{Messages: items})
}

// handleClear 清空用户记忆。
func (h *Handler) handleClear(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}

	if sess, ok := h.SessionMgr.Get(userID); ok {
		sess.ClearMemory()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// handleHealth 健康检查。
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "ok",
		"active_sessions": h.SessionMgr.ActiveCount(),
	})
}

// handleUsage 返回网关级 Token 用量与预算（全局口径；per-user 频控配额由会话层独立执行）。
func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	if UserIDFromContext(r.Context()) == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "unauthorized"})
		return
	}
	if h.Meter == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
		return
	}
	summary := h.Meter.Summary()
	used, limit, exceeded := h.Meter.CheckBudget()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":         true,
		"summary":         summary,
		"cache_hit_ratio": summary.CacheHitRatio(),
		"daily_budget": map[string]interface{}{
			"used":     used,
			"limit":    limit,
			"exceeded": exceeded,
		},
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
