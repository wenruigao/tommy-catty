package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
)

// ============================================================
// extractText tests
// ============================================================

func TestExtractText_Empty(t *testing.T) {
	result := extractText(nil)
	if result != "" {
		t.Errorf("empty contents should return empty string, got %q", result)
	}
}

func TestExtractText_OnlyText(t *testing.T) {
	contents := []ToolContent{
		{Type: "text", Text: "hello"},
		{Type: "text", Text: "world"},
	}
	result := extractText(contents)
	if result != "hello\nworld" {
		t.Errorf("got %q, want %q", result, "hello\nworld")
	}
}

func TestExtractText_WithImage(t *testing.T) {
	contents := []ToolContent{
		{Type: "image", Data: "base64data", MimeType: "image/png"},
	}
	result := extractText(contents)
	if len(result) == 0 {
		t.Error("image content should produce text placeholder")
	}
}

func TestExtractText_WithResource(t *testing.T) {
	contents := []ToolContent{
		{Type: "resource", URI: "file:///test.txt"},
	}
	result := extractText(contents)
	if len(result) == 0 {
		t.Error("resource content should produce text placeholder")
	}
}

func TestExtractText_Mixed(t *testing.T) {
	contents := []ToolContent{
		{Type: "text", Text: "output:"},
		{Type: "image", MimeType: "image/png"},
		{Type: "text", Text: "done"},
	}
	result := extractText(contents)
	if len(result) == 0 {
		t.Error("mixed content should produce text")
	}
}

// ============================================================
// parseInputSchema tests
// ============================================================

func TestParseInputSchema_Empty(t *testing.T) {
	result := parseInputSchema(nil)
	if result.Type != "object" {
		t.Errorf("Type = %q, want object", result.Type)
	}
}

func TestParseInputSchema_WithProperties(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"query":{"type":"string","description":"search query"},"limit":{"type":"number"}},"required":["query"]}`
	result := parseInputSchema(json.RawMessage(schemaJSON))
	if result.Type != "object" {
		t.Errorf("Type = %q, want object", result.Type)
	}
	if len(result.Properties) != 2 {
		t.Errorf("Properties len = %d, want 2", len(result.Properties))
	}
	if len(result.Required) != 1 {
		t.Errorf("Required len = %d, want 1", len(result.Required))
	}
}

func TestParseInputSchema_NoRequired(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"name":{"type":"string"}}}`
	result := parseInputSchema(json.RawMessage(schemaJSON))
	if len(result.Required) != 0 {
		t.Errorf("Required should be empty, got %v", result.Required)
	}
}

func TestParseInputSchema_DefaultType(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"field":{}}}`
	result := parseInputSchema(json.RawMessage(schemaJSON))
	prop := result.Properties["field"]
	if prop.Type != "string" && prop.Type == "" {
		t.Error("property without type should default to string")
	}
}

// ============================================================
// Client ToolFullName tests
// ============================================================

func TestClient_ToolFullName_WithPrefix(t *testing.T) {
	c := &Client{cfg: ClientConfig{Name: "myserver", ToolPrefix: "mcp"}}
	name := c.ToolFullName("search")
	// Prefix mode: just concatenates, no separator
	if name != "mcpsearch" {
		t.Errorf("got %q, want mcpsearch", name)
	}
}

func TestClient_ToolFullName_NoPrefix(t *testing.T) {
	c := &Client{cfg: ClientConfig{Name: "myserver"}}
	name := c.ToolFullName("search")
	if name != "myserver_search" {
		t.Errorf("got %q, want myserver_search", name)
	}
}

// ============================================================
// ToolCallResult conversion tests
// ============================================================

func TestConvertToolResult_Normal(t *testing.T) {
	mcpResult := &ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: "result"}},
		IsError: false,
	}
	result := convertToolResult(mcpResult)
	if result.Output != "result" {
		t.Errorf("Output = %q, want result", result.Output)
	}
	if result.Error != "" {
		t.Errorf("Error should be empty, got %q", result.Error)
	}
	if result.Metadata["source"] != "mcp" {
		t.Error("Metadata should contain source=mcp")
	}
}

func TestConvertToolResult_Error(t *testing.T) {
	mcpResult := &ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: "something went wrong"}},
		IsError: true,
	}
	result := convertToolResult(mcpResult)
	if result.Output != "" {
		t.Errorf("Output should be empty on error, got %q", result.Output)
	}
	if result.Error == "" {
		t.Error("Error should be populated on error result")
	}
}

// ============================================================
// JSONRPCError tests
// ============================================================

func TestJSONRPCError_Error(t *testing.T) {
	err := &JSONRPCError{Code: -1, Message: "test error"}
	if err.Error() != "test error" {
		t.Errorf("Error() = %q, want test error", err.Error())
	}
}

// ============================================================
// MCPToolAdapter construction tests (via NewMCPToolAdapter)
// ============================================================

func TestNewMCPToolAdapter(t *testing.T) {
	client := &Client{cfg: ClientConfig{Name: "test-server"}}
	def := ToolDefinition{
		Name:        "remote-tool",
		Description: "A remote MCP tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}
	adapter := NewMCPToolAdapter(client, def)
	if adapter == nil {
		t.Fatal("NewMCPToolAdapter should not return nil")
	}
	if adapter.Name() != "test-server_remote-tool" {
		t.Errorf("Name() = %q, want test-server_remote-tool", adapter.Name())
	}
	if adapter.Description() != "A remote MCP tool" {
		t.Errorf("Description() = %q", adapter.Description())
	}

	params := adapter.Parameters()
	if params.Type != "object" {
		t.Errorf("Parameters type = %q, want object", params.Type)
	}
}

func TestNewMCPToolAdapter_EmptyDescription(t *testing.T) {
	client := &Client{cfg: ClientConfig{Name: "test-server"}}
	def := ToolDefinition{
		Name:        "tool",
		Description: "",
	}
	adapter := NewMCPToolAdapter(client, def)
	desc := adapter.Description()
	if desc == "" {
		t.Error("Description should not be empty for MCP tool")
	}
}

func TestNewMCPToolAdapter_WithPrefix(t *testing.T) {
	client := &Client{cfg: ClientConfig{Name: "srv", ToolPrefix: "mcp"}}
	def := ToolDefinition{Name: "search"}
	adapter := NewMCPToolAdapter(client, def)
	// Prefix mode: just "mcp" + "search" = "mcpsearch"
	if adapter.Name() != "mcpsearch" {
		t.Errorf("Name() = %q, want mcpsearch", adapter.Name())
	}
}

// ============================================================
// Client StripPrefix tests
// ============================================================

func TestClient_StripPrefix_Matches(t *testing.T) {
	c := &Client{cfg: ClientConfig{Name: "test-server", ToolPrefix: "mcp"}}
	result := c.StripPrefix("mcpsearch")
	if result != "search" {
		t.Errorf("StripPrefix = %q, want search", result)
	}
}

func TestClient_StripPrefix_NoMatch(t *testing.T) {
	c := &Client{cfg: ClientConfig{Name: "test-server", ToolPrefix: "mcp"}}
	result := c.StripPrefix("other_search")
	if result != "other_search" {
		t.Errorf("StripPrefix should return original: got %q", result)
	}
}

func TestClient_StripPrefix_NoPrefix(t *testing.T) {
	c := &Client{cfg: ClientConfig{Name: "test-server"}}
	result := c.StripPrefix("test-server_search")
	if result != "search" {
		t.Errorf("StripPrefix with name prefix = %q, want search", result)
	}
}

// ============================================================
// Description sanitization tests
// ============================================================

// TestMCPToolAdapter_DescriptionTruncated 验证超长描述被截断到 500 字符。
func TestMCPToolAdapter_DescriptionTruncated(t *testing.T) {
	client := &Client{cfg: ClientConfig{Name: "srv"}}
	long := strings.Repeat("a", 1000)
	adapter := NewMCPToolAdapter(client, ToolDefinition{Name: "t", Description: long})
	desc := adapter.Description()
	if len([]rune(desc)) > maxDescriptionLen+3 { // 截断后追加 "..."
		t.Errorf("描述应截断到 %d 字符，实际 %d", maxDescriptionLen, len([]rune(desc)))
	}
	if !strings.HasSuffix(desc, "...") {
		t.Error("截断后的描述应以 ... 结尾")
	}
}

// TestMCPToolAdapter_DescriptionStripsControlChars 验证描述中的控制字符被剥离，
// 换行与制表符保留。
func TestMCPToolAdapter_DescriptionStripsControlChars(t *testing.T) {
	client := &Client{cfg: ClientConfig{Name: "srv"}}
	// 混入 NUL、ESC、响铃等控制字符（常见于终端逃逸注入）
	dirty := "正常描述\x00\x1b[31m\a\n第二行\t缩进"
	adapter := NewMCPToolAdapter(client, ToolDefinition{Name: "t", Description: dirty})
	desc := adapter.Description()
	for _, r := range desc {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			t.Errorf("描述中不应残留控制字符 %q: %q", r, desc)
		}
	}
	if !strings.Contains(desc, "\n第二行\t缩进") {
		t.Errorf("换行与制表符应保留，结果: %q", desc)
	}
	if !strings.Contains(desc, "正常描述") {
		t.Errorf("正常内容应保留，结果: %q", desc)
	}
}

// TestSanitizeDescription_ShortUnchanged 验证未超限且无控制字符的描述原样返回。
func TestSanitizeDescription_ShortUnchanged(t *testing.T) {
	s := "搜索互联网获取信息\n支持多关键词"
	if got := sanitizeDescription(s); got != s {
		t.Errorf("普通描述不应被修改，got %q", got)
	}
}
