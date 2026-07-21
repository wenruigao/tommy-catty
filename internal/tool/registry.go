package tool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tommy-cat/agent/internal/llm"
)

// Registry 是工具注册中心，管理所有已注册的工具及其元信息。
// 支持并发安全的注册、查询和调用操作。
type Registry struct {
	tools map[string]ToolMeta
	mu    sync.RWMutex
}

// NewRegistry 创建并返回一个新的工具注册中心实例
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]ToolMeta),
	}
}

// Register 将一个工具注册到注册中心，附带风险等级和超时配置
func (r *Registry) Register(tool Tool, risk RiskLevel, timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = ToolMeta{
		Tool:      tool,
		RiskLevel: risk,
		Timeout:   timeout,
	}
}

// Get 根据名称获取工具的元信息，返回工具及是否存在的布尔值
func (r *Registry) Get(name string) (ToolMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	meta, ok := r.tools[name]
	return meta, ok
}

// List 返回所有已注册工具的元信息列表
func (r *Registry) List() []ToolMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]ToolMeta, 0, len(r.tools))
	for _, meta := range r.tools {
		list = append(list, meta)
	}
	return list
}

// Call 根据名称查找工具，验证参数后在超时限制内执行工具。
// 如果工具未找到、参数验证失败或执行超时，将返回相应错误。
func (r *Registry) Call(ctx context.Context, name string, args map[string]interface{}) (Result, error) {
	r.mu.RLock()
	meta, ok := r.tools[name]
	r.mu.RUnlock()

	if !ok {
		return Result{}, fmt.Errorf("tool not found: %s", name)
	}

	// 验证参数是否符合 schema 定义
	schema := meta.Parameters()
	if err := validateArgs(schema, args); err != nil {
		return Result{}, fmt.Errorf("argument validation failed for tool %q: %w", name, err)
	}

	// 创建带超时的上下文
	execCtx := ctx
	if meta.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, meta.Timeout)
		defer cancel()
	}

	// 执行工具
	result, err := meta.Execute(execCtx, args)
	if err != nil {
		return Result{}, fmt.Errorf("tool %q execution error: %w", name, err)
	}

	return result, nil
}

// ToToolDefs 将所有已注册工具转换为 LLM 可识别的工具定义列表，
// 用于在 LLM API 调用中声明可用工具。
func (r *Registry) ToToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, meta := range r.tools {
		schema := meta.Parameters()

		// 将 JSONSchema 转换为通用 map 结构
		properties := make(map[string]interface{}, len(schema.Properties))
		for propName, prop := range schema.Properties {
			propMap := map[string]interface{}{
				"type":        prop.Type,
				"description": prop.Description,
			}
			if len(prop.Enum) > 0 {
				propMap["enum"] = prop.Enum
			}
			if prop.Default != nil {
				propMap["default"] = prop.Default
			}
			properties[propName] = propMap
		}

		params := map[string]interface{}{
			"type":       schema.Type,
			"properties": properties,
		}
		if len(schema.Required) > 0 {
			params["required"] = schema.Required
		}

		defs = append(defs, llm.ToolDef{
			Name:        meta.Name(),
			Description: meta.Description(),
			Parameters:  params,
		})
	}
	return defs
}

// validateArgs 对工具参数进行基础类型验证。
// 检查必填参数是否存在，以及参数类型是否与 schema 定义匹配。
func validateArgs(schema JSONSchema, args map[string]interface{}) error {
	// 检查必填参数
	for _, req := range schema.Required {
		if _, ok := args[req]; !ok {
			return fmt.Errorf("missing required parameter: %q", req)
		}
	}

	// 检查每个提供的参数类型是否匹配
	for name, value := range args {
		prop, ok := schema.Properties[name]
		if !ok {
			// 允许未定义的额外参数（宽松模式）
			continue
		}
		if err := checkType(name, prop.Type, value); err != nil {
			return err
		}
	}

	return nil
}

// checkType 验证单个参数的值是否匹配声明的类型
func checkType(name, expectedType string, value interface{}) error {
	if value == nil {
		return nil // nil 值跳过类型检查
	}

	switch expectedType {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("parameter %q: expected string, got %T", name, value)
		}
	case "number", "integer":
		switch value.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			// 数值类型均合法
		default:
			return fmt.Errorf("parameter %q: expected number, got %T", name, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("parameter %q: expected boolean, got %T", name, value)
		}
	case "object":
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("parameter %q: expected object, got %T", name, value)
		}
	case "array":
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("parameter %q: expected array, got %T", name, value)
		}
	}
	return nil
}
