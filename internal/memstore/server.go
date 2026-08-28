package memstore

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/wenruigao/tommy-catty/internal/memory"
)

// Server 记忆存储 REST 服务端：把任意 Store 后端暴露为 /memstore/v1 HTTP 接口，
// 供 remote 后端客户端访问（cmd/memstore 的 HTTP 层，多实例集中存放）。
type Server struct {
	store Store
	token string // Bearer 令牌，空则不鉴权（仅限本机/内网测试）
}

// NewServer 创建服务端；token 非空时所有业务接口要求 Authorization: Bearer <token>。
func NewServer(store Store, token string) *Server {
	return &Server{store: store, token: token}
}

// Handler 返回挂载全部路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/memstore/v1/healthz", s.handleHealth)
	mux.HandleFunc("/memstore/v1/users/", s.handleUsers)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUsers 解析 /memstore/v1/users/{uid}/... 并分发：
//   - PUT    /users/{uid}/memories/{id}   保存记忆
//   - GET    /users/{uid}/memories        最近记忆（?limit=N）
//   - POST   /users/{uid}/memories/search 关键词检索
//   - DELETE /users/{uid}/memories        清空记忆
//   - PUT    /users/{uid}/profile         保存画像
//   - GET    /users/{uid}/profile         读取画像
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if s.token != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+s.token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
	}

	rest := strings.TrimPrefix(r.URL.Path, "/memstore/v1/users/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invalid path"})
		return
	}
	userID := parts[0]
	ctx := r.Context()

	switch {
	case parts[1] == "memories" && r.Method == http.MethodPut && len(parts) == 3:
		var dto MemoryDTO
		if err := readJSON(r, &dto); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if dto.ID == "" || dto.ID != parts[2] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id mismatch"})
			return
		}
		dto.UserID = userID
		if err := s.store.SaveMemory(ctx, fromDTO(dto)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case parts[1] == "memories" && r.Method == http.MethodGet && len(parts) == 2:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 20
		}
		entries, err := s.store.RecentMemories(ctx, userID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": toDTOList(entries)})

	case parts[1] == "memories" && r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "search":
		var req struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		entries, err := s.store.SearchMemories(ctx, userID, req.Query, req.TopK)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": toDTOList(entries)})

	case parts[1] == "memories" && r.Method == http.MethodDelete && len(parts) == 2:
		if err := s.store.DeleteMemories(ctx, userID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case parts[1] == "profile" && r.Method == http.MethodPut && len(parts) == 2:
		var req struct {
			Content string `json:"content"`
		}
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.store.SaveProfile(ctx, userID, req.Content); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case parts[1] == "profile" && r.Method == http.MethodGet && len(parts) == 2:
		content, err := s.store.LoadProfile(ctx, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"content": content})

	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown endpoint"})
	}
}

func toDTOList(entries []memory.MemoryEntry) []MemoryDTO {
	out := make([]MemoryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toDTO(e))
	}
	return out
}

func readJSON(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
