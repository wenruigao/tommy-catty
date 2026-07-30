package tool

import (
	"context"
	"testing"
)

// ============================================================
// checkType tests
// ============================================================

func TestCheckType_Nil(t *testing.T) {
	if err := checkType("name", "string", nil); err != nil {
		t.Errorf("nil value should always pass, got %v", err)
	}
}

func TestCheckType_String_Match(t *testing.T) {
	if err := checkType("name", "string", "hello"); err != nil {
		t.Errorf("string type should match: %v", err)
	}
}

func TestCheckType_String_Mismatch(t *testing.T) {
	if err := checkType("name", "string", 123); err == nil {
		t.Error("int should not match string type")
	}
}

func TestCheckType_Number_Match_float64(t *testing.T) {
	if err := checkType("val", "number", float64(3.14)); err != nil {
		t.Errorf("float64 should match number type: %v", err)
	}
}

func TestCheckType_Number_Match_int(t *testing.T) {
	if err := checkType("val", "number", int(42)); err != nil {
		t.Errorf("int should match number type: %v", err)
	}
}

func TestCheckType_Number_Match_int64(t *testing.T) {
	if err := checkType("val", "integer", int64(42)); err != nil {
		t.Errorf("int64 should match integer type: %v", err)
	}
}

func TestCheckType_Number_Mismatch(t *testing.T) {
	if err := checkType("val", "number", "not-a-number"); err == nil {
		t.Error("string should not match number type")
	}
}

func TestCheckType_Boolean_Match(t *testing.T) {
	if err := checkType("flag", "boolean", true); err != nil {
		t.Errorf("bool should match boolean type: %v", err)
	}
}

func TestCheckType_Boolean_Mismatch(t *testing.T) {
	if err := checkType("flag", "boolean", 1); err == nil {
		t.Error("int should not match boolean type")
	}
}

func TestCheckType_Object_Match(t *testing.T) {
	if err := checkType("obj", "object", map[string]interface{}{"key": "value"}); err != nil {
		t.Errorf("map should match object type: %v", err)
	}
}

func TestCheckType_Object_Mismatch(t *testing.T) {
	if err := checkType("obj", "object", []interface{}{}); err == nil {
		t.Error("slice should not match object type")
	}
}

func TestCheckType_Array_Match(t *testing.T) {
	if err := checkType("arr", "array", []interface{}{1, 2, 3}); err != nil {
		t.Errorf("slice should match array type: %v", err)
	}
}

func TestCheckType_Array_Mismatch(t *testing.T) {
	if err := checkType("arr", "array", "not-array"); err == nil {
		t.Error("string should not match array type")
	}
}

func TestCheckType_Unknown_Type(t *testing.T) {
	if err := checkType("x", "unknown_type", "anything"); err != nil {
		t.Errorf("unknown type should pass: %v", err)
	}
}

// ============================================================
// validateArgs tests
// ============================================================

func TestValidateArgs_EmptyAll(t *testing.T) {
	schema := JSONSchema{Type: "object", Properties: map[string]Property{}}
	args := map[string]interface{}{}
	if err := validateArgs(schema, args); err != nil {
		t.Errorf("empty schema and args should pass: %v", err)
	}
}

func TestValidateArgs_MissingRequired(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"query": {Type: "string"},
		},
		Required: []string{"query"},
	}
	args := map[string]interface{}{}
	if err := validateArgs(schema, args); err == nil {
		t.Error("missing required arg should fail")
	}
}

func TestValidateArgs_AllRequiredPresent(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"query": {Type: "string"},
		},
		Required: []string{"query"},
	}
	args := map[string]interface{}{"query": "hello"}
	if err := validateArgs(schema, args); err != nil {
		t.Errorf("all required present should pass: %v", err)
	}
}

func TestValidateArgs_TypeMismatch(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"count": {Type: "number"},
		},
		Required: []string{"count"},
	}
	args := map[string]interface{}{"count": "not-a-number"}
	if err := validateArgs(schema, args); err == nil {
		t.Error("type mismatch for required arg should fail")
	}
}

func TestValidateArgs_NoRequired(t *testing.T) {
	schema := JSONSchema{
		Type: "object",
		Properties: map[string]Property{
			"optional": {Type: "string"},
			"wrong":    {Type: "number"},
		},
	}
	args := map[string]interface{}{"wrong": "not-a-number"}
	if err := validateArgs(schema, args); err == nil {
		t.Error("type mismatch in optional args should still fail")
	}
}

// ============================================================
// RiskLevel tests
// ============================================================

func TestRiskLevel_String(t *testing.T) {
	tests := []struct {
		level    RiskLevel
		expected string
	}{
		{RiskReadOnly, "read_only"},
		{RiskLowWrite, "low_write"},
		{RiskHighWrite, "high_write"},
		{RiskDangerous, "dangerous"},
		{RiskLevel(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.expected {
			t.Errorf("RiskLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

// ============================================================
// Registry tests
// ============================================================

type mockTool struct {
	name string
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return "mock" }
func (m *mockTool) Parameters() JSONSchema {
	return JSONSchema{Type: "object", Properties: map[string]Property{}}
}
func (m *mockTool) Execute(_ context.Context, _ map[string]interface{}) (Result, error) {
	return Result{Output: "ok"}, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "test_tool"}, RiskReadOnly, 30)

	meta, ok := r.Get("test_tool")
	if !ok {
		t.Fatal("Get should find registered tool")
	}
	if meta.Name() != "test_tool" {
		t.Errorf("Name = %q, want test_tool", meta.Name())
	}
	if meta.RiskLevel != RiskReadOnly {
		t.Errorf("Risk = %v, want read_only", meta.RiskLevel)
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("Get should return false for unknown tool")
	}
}

func TestRegistry_List_Empty(t *testing.T) {
	r := NewRegistry()
	if len(r.List()) != 0 {
		t.Error("List on empty registry should return empty slice")
	}
}

func TestRegistry_List_WithTools(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "a"}, RiskReadOnly, 30)
	r.Register(&mockTool{name: "b"}, RiskLowWrite, 15)
	if len(r.List()) != 2 {
		t.Errorf("List should return 2 tools, got %d", len(r.List()))
	}
}

func TestRegistry_ToToolDefs(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "test"}, RiskReadOnly, 30)
	defs := r.ToToolDefs()
	if len(defs) != 1 {
		t.Fatalf("ToToolDefs should return 1 def, got %d", len(defs))
	}
	if defs[0].Name != "test" {
		t.Errorf("def name = %q, want test", defs[0].Name)
	}
}
