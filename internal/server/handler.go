package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/metrics"
	"github.com/wenruigao/tommy-catty/internal/security"
	"github.com/wenruigao/tommy-catty/internal/session"
)

// Handler 持有 HTTP API 所需的共享依赖。
type Handler struct {
	SessionMgr *session.SessionManager

	// Meter Token 计量器（网关全局口径），为 /api/v1/usage 提供数据；nil 表示不暴露
	Meter *llm.Meter

	// SecEngine 安全策略引擎，任务完成后评估 task_end 检查点（携带 Cost，供 cost-guard）；nil 则跳过
	SecEngine *security.Engine

	// Gateway LLM 网关（readiness 探针检查供应商可用性）；nil 表示未配置
	Gateway *llm.Gateway

	// MemStore 记忆持久化存储（readiness 探针报告状态）；nil 表示未配置
	MemStore interface{ Close() error }

	// RequestTimeout 单次 chat 请求的最大执行时间（默认 120s；<= 0 不限）
	RequestTimeout time.Duration
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
	mux.HandleFunc("GET /api/v1/ready", h.handleReadiness)
	mux.HandleFunc("GET /api/v1/usage", h.handleUsage)
	mux.HandleFunc("GET /metrics", h.handleMetrics)
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

	// 请求级超时：防止单次 LLM 调用占满连接
	ctx := r.Context()
	if h.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.RequestTimeout)
		defer cancel()
	}

	sess := h.SessionMgr.GetOrCreate(userID)
	result, err := sess.Run(ctx, req.Message)
	if err != nil {
		if err == session.ErrRateLimited || err == session.ErrConcurrencyLimited {
			writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: err.Error()})
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			writeJSON(w, http.StatusGatewayTimeout, errorResponse{Error: "request timeout"})
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

// handleHealth liveness 探针：进程存活即返回 200（K8s livenessProbe）。
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
	})
}

// handleReadiness readiness 探针：检查依赖组件可用性（K8s readinessProbe）。
// 任一关键依赖不可用则返回 503，负载均衡器摘除流量。
func (h *Handler) handleReadiness(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	ready := true

	// 会话管理器
	if h.SessionMgr != nil {
		checks["sessions"] = "ok"
	} else {
		checks["sessions"] = "unavailable"
		ready = false
	}

	// LLM 网关（检查是否有可用供应商）
	if h.Gateway != nil {
		providers := h.Gateway.ListProviders()
		if len(providers) > 0 {
			checks["llm"] = "ok"
		} else {
			checks["llm"] = "no_providers"
			ready = false
		}
	} else {
		checks["llm"] = "unavailable"
		ready = false
	}

	// 记忆存储
	if h.MemStore != nil {
		checks["memstore"] = "ok"
	} else {
		checks["memstore"] = "disabled"
		// memstore 非关键依赖，不影响 readiness
	}

	status := "ready"
	httpStatus := http.StatusOK
	if !ready {
		status = "not_ready"
		httpStatus = http.StatusServiceUnavailable
	}

	writeJSON(w, httpStatus, map[string]interface{}{
		"status":          status,
		"active_sessions": h.SessionMgr.ActiveCount(),
		"checks":          checks,
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

// handleMetrics 暴露 Prometheus 格式指标（供 Prometheus Server 抓取 → Grafana 展示）。
// /metrics 端点无需认证（Prometheus 抓取不支持自定义 Header），
// 指标数据不含敏感信息（仅计数器/瞬时值，不含用户数据或密钥）。
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// 刷新活跃会话数（Gauge 需每次抓取前拉取最新值）
	metrics.SessionActive().Set(float64(h.SessionMgr.ActiveCount()))

	// 执行所有注册的 Gauge 拉取函数 + 刷新运行时指标
	metrics.CollectAll()

	// 编码为 Prometheus exposition format
	body := metrics.DefaultRegistry().Encode()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
