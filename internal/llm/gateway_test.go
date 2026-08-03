package llm

import (
	"context"
	"errors"
	"testing"
)

// stubProvider 是用于测试路由逻辑的 LLMProvider 桩实现
type stubProvider struct {
	name  string
	model string
}

func (s *stubProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return ChatResponse{}, nil
}

func (s *stubProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	return nil, nil
}

func (s *stubProvider) Name() string   { return s.name }
func (s *stubProvider) Model() string  { return s.model }
func (s *stubProvider) MaxTokens() int { return 0 }

func TestResolveProvider_ByModelNameHitsNonDefault(t *testing.T) {
	gw := NewGateway()
	gw.Register(&stubProvider{name: "mimo", model: "mimo-v2.5-pro"})
	gw.Register(&stubProvider{name: "deepseek", model: "deepseek-chat"})
	gw.SetDefault("deepseek")

	// 传入模型名应命中配置了该模型的非默认供应商
	p, err := gw.resolveProvider("mimo-v2.5-pro")
	if err != nil {
		t.Fatalf("resolveProvider 出错: %v", err)
	}
	if p.Name() != "mimo" {
		t.Errorf("provider = %q, want %q", p.Name(), "mimo")
	}
}

func TestResolveProvider_EmptyModelFallsBackToDefault(t *testing.T) {
	gw := NewGateway()
	gw.Register(&stubProvider{name: "mimo", model: "mimo-v2.5-pro"})
	gw.Register(&stubProvider{name: "deepseek", model: "deepseek-chat"})
	gw.SetDefault("deepseek")

	p, err := gw.resolveProvider("")
	if err != nil {
		t.Fatalf("resolveProvider 出错: %v", err)
	}
	if p.Name() != "deepseek" {
		t.Errorf("provider = %q, want 默认供应商 %q", p.Name(), "deepseek")
	}
}

func TestResolveProvider_UnknownModelFallsBackToDefault(t *testing.T) {
	gw := NewGateway()
	gw.Register(&stubProvider{name: "mimo", model: "mimo-v2.5-pro"})
	gw.Register(&stubProvider{name: "deepseek", model: "deepseek-chat"})
	gw.SetDefault("deepseek")

	p, err := gw.resolveProvider("no-such-model")
	if err != nil {
		t.Fatalf("resolveProvider 出错: %v", err)
	}
	if p.Name() != "deepseek" {
		t.Errorf("provider = %q, want 回退到默认供应商 %q", p.Name(), "deepseek")
	}
}

func TestResolveProvider_UnknownModelFallsBackToSoleProvider(t *testing.T) {
	gw := NewGateway()
	gw.Register(&stubProvider{name: "mimo", model: "mimo-v2.5-pro"})
	// 未设置默认供应商，且只有一个供应商时直接使用它
	p, err := gw.resolveProvider("no-such-model")
	if err != nil {
		t.Fatalf("resolveProvider 出错: %v", err)
	}
	if p.Name() != "mimo" {
		t.Errorf("provider = %q, want 唯一供应商 %q", p.Name(), "mimo")
	}
}

func TestResolveProvider_NotFound(t *testing.T) {
	gw := NewGateway()
	gw.Register(&stubProvider{name: "mimo", model: "mimo-v2.5-pro"})
	gw.Register(&stubProvider{name: "deepseek", model: "deepseek-chat"})
	// 无默认供应商且多于一个供应商，查无此人应报错
	_, err := gw.resolveProvider("no-such-model")
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("err = %v, want ErrProviderNotFound", err)
	}
}

func TestResolveProvider_DuplicateModelNameDeterministic(t *testing.T) {
	// 两个供应商配置相同 model 名时，按供应商名排序遍历应保证结果确定
	gw := NewGateway()
	gw.Register(&stubProvider{name: "beta", model: "shared-model"})
	gw.Register(&stubProvider{name: "alpha", model: "shared-model"})

	for i := 0; i < 50; i++ {
		p, err := gw.resolveProvider("shared-model")
		if err != nil {
			t.Fatalf("resolveProvider 出错: %v", err)
		}
		if p.Name() != "alpha" {
			t.Fatalf("第 %d 次解析得到 %q, want 字典序最小的 %q（结果不确定）", i, p.Name(), "alpha")
		}
	}
}
