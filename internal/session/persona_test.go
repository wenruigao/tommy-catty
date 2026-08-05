package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tommy-cat/agent/internal/ctxmgr"
	"github.com/tommy-cat/agent/internal/llm"
	"github.com/tommy-cat/agent/internal/tool"
)

// TestBuildSystemPrompt 验证系统提示词组装的结构与优先级标注。
func TestBuildSystemPrompt(t *testing.T) {
	prompt := BuildSystemPrompt("AGENT职责", "基础提示", "人格风格", "用户画像内容")

	for _, want := range []string{"AGENT职责", "基础提示", "人格风格", "用户画像内容", "最高优先级"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("系统提示词应包含 %q，得到:\n%s", want, prompt)
		}
	}
	// agent.md 应排在最前
	if strings.Index(prompt, "AGENT职责") > strings.Index(prompt, "用户画像内容") {
		t.Error("agent.md 内容应排在用户画像之前")
	}

	// 无用户画像时不应出现画像段落
	noProfile := BuildSystemPrompt("AGENT职责", "基础提示", "人格风格", "")
	if strings.Contains(noProfile, "用户画像") {
		t.Error("无用户画像时不应包含画像段落")
	}
}

// TestLoadPersonaFile 验证人格文件读取与兜底逻辑。
func TestLoadPersonaFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(path, []byte("自定义职责"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, err := LoadPersonaFile(path, DefaultAgentMD)
	if err != nil || content != "自定义职责" {
		t.Errorf("应读到文件内容，得到 content=%q err=%v", content, err)
	}

	// 文件不存在时返回兜底文本与错误
	content, err = LoadPersonaFile(filepath.Join(dir, "missing.md"), DefaultAgentMD)
	if err == nil {
		t.Error("文件缺失时应返回错误")
	}
	if content != DefaultAgentMD {
		t.Error("文件缺失时应使用兜底文本")
	}
}

// captureLLM 捕获收到的全部消息（含系统提示词），用于验证 SystemPromptProvider 生效。
type captureLLM struct {
	messages []llm.Message
}

func (m *captureLLM) Chat(_ context.Context, messages []llm.Message, _ []llm.ToolDef) (llm.ChatResponse, error) {
	m.messages = append([]llm.Message(nil), messages...)
	return llm.ChatResponse{Content: "完成", Usage: llm.Usage{TotalTokens: 1}}, nil
}

// TestSessionSystemPromptProvider 验证配置了 persona 依赖后，
// 引擎收到的系统提示词包含 agent.md / soul.md / 用户画像内容。
func TestSessionSystemPromptProvider(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "users")
	if err := os.MkdirAll(filepath.Join(profileDir, "u1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "u1", "user.md"), []byte("偏好简洁回答"), 0o644); err != nil {
		t.Fatal(err)
	}

	llmCap := &captureLLM{}
	deps := SessionDeps{
		LLM:             llmCap,
		Tools:           tool.NewRegistry(),
		MaxIterations:   3,
		SystemPrompt:    "基础行为提示文本",
		MemorySize:      10,
		CtxConfig:       ctxmgr.DefaultConfig(),
		AgentMD:         "AGENT职责文本",
		SoulMD:          "人格风格文本",
		UserProfilesDir: profileDir,
	}

	sess := NewUserSession("u1", deps)
	if _, err := sess.Run(context.Background(), "你好"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if len(llmCap.messages) == 0 || llmCap.messages[0].Role != "system" {
		t.Fatalf("首条消息应为系统提示词，得到: %+v", llmCap.messages)
	}
	sysPrompt := llmCap.messages[0].Content
	for _, want := range []string{"AGENT职责文本", "基础行为提示文本", "人格风格文本", "偏好简洁回答"} {
		if !strings.Contains(sysPrompt, want) {
			t.Errorf("系统提示词应包含 %q，得到:\n%s", want, sysPrompt)
		}
	}
}

// TestSessionSkillHint 验证 SkillHintProvider 返回的提示被拼接到目标之前。
func TestSessionSkillHint(t *testing.T) {
	deps := testDeps()
	deps.SkillHintProvider = func(input string) string {
		if strings.Contains(input, "搜索") {
			return "可参考以下已验证的执行经验：先搜索再总结"
		}
		return ""
	}

	sess := NewUserSession("u1", deps)
	result, err := sess.Run(context.Background(), "帮我搜索 Go 教程")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if len(result.Steps) == 0 {
		t.Fatal("应有执行步骤")
	}
	// mockLLM 回显最后一条 user 消息，验证其中包含拼接后的提示
	final := result.Steps[len(result.Steps)-1].FinalAnswer
	if !strings.Contains(final, "可参考以下已验证的执行经验") || !strings.Contains(final, "帮我搜索 Go 教程") {
		t.Errorf("目标前未拼接 Skill 提示，最终回显: %q", final)
	}

	// 不命中时目标保持原样
	sess2 := NewUserSession("u2", deps)
	result2, err := sess2.Run(context.Background(), "随便聊聊")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	final2 := result2.Steps[len(result2.Steps)-1].FinalAnswer
	if strings.Contains(final2, "执行经验") {
		t.Errorf("未命中时不应拼接提示，最终回显: %q", final2)
	}
}
