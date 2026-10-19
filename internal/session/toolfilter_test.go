package session

import (
	"context"
	"strings"
	"testing"

	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// stubTool 测试用最小工具实现。
type stubTool struct {
	name string
}

func (s *stubTool) Name() string                { return s.name }
func (s *stubTool) Description() string         { return s.name + " 描述" }
func (s *stubTool) Parameters() tool.JSONSchema { return tool.JSONSchema{Type: "object"} }
func (s *stubTool) Execute(_ context.Context, _ map[string]interface{}) (tool.Result, error) {
	return tool.Result{Output: "ran " + s.name}, nil
}

// newTestRegistry 构造含若干工具的注册表。
func newTestRegistry(t *testing.T, names ...string) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	for _, n := range names {
		reg.Register(&stubTool{name: n}, tool.RiskReadOnly, 0)
	}
	return reg
}

func defNames(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// ============================================================
// ToToolDefs 过滤
// ============================================================

func TestRouteToolFilter_ToToolDefs_NoWhitelist(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "web_search", "db_query")
	f := newRouteToolFilter(reg, func() map[string]bool { return nil })

	defs := f.ToToolDefs()
	if len(defs) != 3 {
		t.Errorf("nil whitelist should not restrict, got %d defs", len(defs))
	}
}

func TestRouteToolFilter_ToToolDefs_Filters(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "web_search", "db_query")
	f := newRouteToolFilter(reg, func() map[string]bool {
		return map[string]bool{"code_run": true}
	})

	defs := f.ToToolDefs()
	if len(defs) != 1 || defs[0].Name != "code_run" {
		t.Errorf("expected only code_run, got %v", defNames(defs))
	}
}

func TestRouteToolFilter_ToToolDefs_DynamicSwap(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "web_search")

	var allowed map[string]bool
	f := newRouteToolFilter(reg, func() map[string]bool { return allowed })

	allowed = map[string]bool{"code_run": true}
	if defs := f.ToToolDefs(); len(defs) != 1 || defs[0].Name != "code_run" {
		t.Errorf("first route: got %v", defNames(defs))
	}

	// 路由切换后无需重建过滤器即生效
	allowed = map[string]bool{"web_search": true}
	if defs := f.ToToolDefs(); len(defs) != 1 || defs[0].Name != "web_search" {
		t.Errorf("after swap: got %v", defNames(defs))
	}

	allowed = nil
	if defs := f.ToToolDefs(); len(defs) != 2 {
		t.Errorf("cleared whitelist should not restrict, got %v", defNames(defs))
	}
}

func TestRouteToolFilter_ToToolDefs_NilAllowedFn(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "web_search")
	f := newRouteToolFilter(reg, nil)

	if defs := f.ToToolDefs(); len(defs) != 2 {
		t.Errorf("nil allowedFn should not restrict, got %d", len(defs))
	}
}

// ============================================================
// Call 白名单拦截
// ============================================================

func TestRouteToolFilter_Call_Allowed(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "web_search")
	f := newRouteToolFilter(reg, func() map[string]bool {
		return map[string]bool{"code_run": true}
	})

	res, err := f.Call(context.Background(), "code_run", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Output != "ran code_run" {
		t.Errorf("output = %q", res.Output)
	}
	if res.Error != "" {
		t.Errorf("allowed call should not error: %q", res.Error)
	}
}

func TestRouteToolFilter_Call_Blocked(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "db_query")
	f := newRouteToolFilter(reg, func() map[string]bool {
		return map[string]bool{"code_run": true}
	})

	res, err := f.Call(context.Background(), "db_query", nil)
	// 关键：返回 tool.Result 错误而非 Go error，使 ReAct 循环可观察并自行收敛
	if err != nil {
		t.Fatalf("blocked call must return nil Go error so ReAct can recover, got %v", err)
	}
	if res.Error == "" {
		t.Fatal("blocked call should set Result.Error")
	}
	if !strings.Contains(res.Error, "db_query") {
		t.Errorf("error should name the blocked tool: %q", res.Error)
	}
	if res.Metadata["blocked_by"] != "router_whitelist" {
		t.Errorf("metadata should mark blocked_by=router_whitelist, got %v", res.Metadata)
	}
}

func TestRouteToolFilter_Call_NoWhitelistAllowsAll(t *testing.T) {
	reg := newTestRegistry(t, "code_run", "db_query")
	f := newRouteToolFilter(reg, func() map[string]bool { return nil })

	for _, name := range []string{"code_run", "db_query"} {
		res, err := f.Call(context.Background(), name, nil)
		if err != nil || res.Error != "" {
			t.Errorf("%s should be allowed: err=%v resultErr=%q", name, err, res.Error)
		}
	}
}

func TestRouteToolFilter_Call_NilAllowedFn(t *testing.T) {
	reg := newTestRegistry(t, "code_run")
	f := newRouteToolFilter(reg, nil)

	res, err := f.Call(context.Background(), "code_run", nil)
	if err != nil || res.Error != "" {
		t.Errorf("nil allowedFn should allow all: err=%v resultErr=%q", err, res.Error)
	}
}

// ============================================================
// buildToolWhitelist
// ============================================================

func TestBuildToolWhitelist(t *testing.T) {
	tests := []struct {
		name    string
		tools   []string
		wantNil bool
		wantLen int
	}{
		{"nil slice", nil, true, 0},
		{"empty slice", []string{}, true, 0},
		{"only empty strings", []string{"", ""}, true, 0},
		{"single", []string{"code_run"}, false, 1},
		{"multiple", []string{"a", "b", "c"}, false, 3},
		{"skips empty entries", []string{"a", "", "b"}, false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildToolWhitelist(tt.tools)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil (unrestricted), got %v", got)
				}
				return
			}
			if len(got) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestBuildToolWhitelist_Lookup(t *testing.T) {
	wl := buildToolWhitelist([]string{"code_run", "file_read"})
	if !wl["code_run"] || !wl["file_read"] {
		t.Error("whitelisted tools should be present")
	}
	if wl["db_query"] {
		t.Error("non-whitelisted tool should be absent")
	}
}

// ============================================================
// routeState
// ============================================================

func TestRouteState_Reset(t *testing.T) {
	rs := &routeState{
		agent:     "coder",
		whitelist: map[string]bool{"code_run": true},
	}
	rs.reset()

	if rs.agent != "" {
		t.Errorf("agent = %q, want empty after reset", rs.agent)
	}
	if rs.whitelist != nil {
		t.Errorf("whitelist = %v, want nil after reset", rs.whitelist)
	}
}
