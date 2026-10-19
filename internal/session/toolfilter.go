package session

import (
	"context"
	"fmt"

	"github.com/wenruigao/tommy-catty/internal/engine"
	"github.com/wenruigao/tommy-catty/internal/llm"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// routeToolFilter 按当前路由结果收窄工具集的 engine.ToolCaller 包装器。
//
// 引擎在每轮 ReAct 迭代都会调用 ToToolDefs()（internal/engine/react.go），
// 因此路由变更后无需重建 Engine 即可生效。白名单外的 Call() 返回带 Error 的
// tool.Result（而非 Go error），交由 ReAct 循环自行观察并收敛——LLM 可能沿用
// 会话历史里见过的工具名发起调用，直接中断整个任务代价过高。
//
// allowedFn 返回 nil 表示不限制（兜底 Agent 或未启用路由）。
type routeToolFilter struct {
	inner     engine.ToolCaller
	allowedFn func() map[string]bool
}

// newRouteToolFilter 创建工具过滤器。allowedFn 为 nil 时等价于不限制。
func newRouteToolFilter(inner engine.ToolCaller, allowedFn func() map[string]bool) *routeToolFilter {
	return &routeToolFilter{inner: inner, allowedFn: allowedFn}
}

// currentAllowed 返回当前白名单；nil 表示不限制。
func (f *routeToolFilter) currentAllowed() map[string]bool {
	if f.allowedFn == nil {
		return nil
	}
	return f.allowedFn()
}

// ToToolDefs 返回白名单内的工具定义，供 LLM 选择。
func (f *routeToolFilter) ToToolDefs() []llm.ToolDef {
	all := f.inner.ToToolDefs()
	allowed := f.currentAllowed()
	if allowed == nil {
		return all
	}

	filtered := make([]llm.ToolDef, 0, len(allowed))
	for _, def := range all {
		if allowed[def.Name] {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

// Call 执行工具调用；白名单外的工具被拒绝并返回可读错误，供 LLM 改用其他工具。
func (f *routeToolFilter) Call(ctx context.Context, name string, args map[string]interface{}) (tool.Result, error) {
	if allowed := f.currentAllowed(); allowed != nil && !allowed[name] {
		return tool.Result{
			Error: fmt.Sprintf("工具 %q 不在当前任务允许的工具范围内，请改用其他可用工具完成任务", name),
			Metadata: map[string]interface{}{
				"blocked_by": "router_whitelist",
			},
		}, nil
	}
	return f.inner.Call(ctx, name, args)
}

// routeState 保存本次 Run 的路由结果。
//
// 由 UserSession.Run() 写入，promptProvider 与 routeToolFilter 读取；
// 三方访问都发生在 Run() 持 s.mu 期间，同用户请求串行执行，故无需额外同步。
type routeState struct {
	// agent 命中的 Agent 名（空表示未路由或路由未启用）
	agent string
	// whitelist 该 Agent 的工具白名单；nil 表示不限制
	whitelist map[string]bool
}

// reset 清空路由状态（每次 Run 开始时调用，避免上一次结果泄漏到本次）。
func (r *routeState) reset() {
	r.agent = ""
	r.whitelist = nil
}

// buildToolWhitelist 将工具名列表转为集合，便于 O(1) 判定。
// 返回 nil 表示不限制（空列表）。
func buildToolWhitelist(tools []string) map[string]bool {
	if len(tools) == 0 {
		return nil
	}
	set := make(map[string]bool, len(tools))
	for _, t := range tools {
		if t != "" {
			set[t] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}
