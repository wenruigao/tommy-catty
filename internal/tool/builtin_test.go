package tool

import (
	"context"
	"testing"
)

func TestShellExec_CheckBlocked_RmRf(t *testing.T) {
	tool := NewShellExecTool()
	if err := tool.checkBlocked("rm -rf /"); err == nil {
		t.Error("rm -rf / should be blocked")
	}
}

func TestShellExec_CheckBlocked_Mkfs(t *testing.T) {
	tool := NewShellExecTool()
	if err := tool.checkBlocked("mkfs.ext4 /dev/sda"); err == nil {
		t.Error("mkfs should be blocked")
	}
}

func TestShellExec_CheckBlocked_Shutdown(t *testing.T) {
	tool := NewShellExecTool()
	if err := tool.checkBlocked("shutdown now"); err == nil {
		t.Error("shutdown should be blocked")
	}
}

func TestShellExec_CheckBlocked_CaseInsensitive(t *testing.T) {
	tool := NewShellExecTool()
	if err := tool.checkBlocked("Rm -Rf /tmp"); err == nil {
		t.Error("case-varied rm -rf should be blocked")
	}
}

func TestShellExec_CheckBlocked_Safe(t *testing.T) {
	tool := NewShellExecTool()
	if err := tool.checkBlocked("echo hello world"); err != nil {
		t.Errorf("safe command should pass: %v", err)
	}
}

func TestShellExec_CheckBlocked_Empty(t *testing.T) {
	tool := NewShellExecTool()
	if err := tool.checkBlocked(""); err != nil {
		t.Errorf("empty command should pass: %v", err)
	}
}

func TestFileRead_ValidatePath(t *testing.T) {
	tool := &FileReadTool{}
	// relative path inside cwd should be valid (no traversal, no allowed dirs specified)
	if err := tool.validatePath("test.txt"); err != nil {
		t.Errorf("relative path should pass: %v", err)
	}
}

func TestFileRead_ValidatePath_AbsolutePath(t *testing.T) {
	tool := &FileReadTool{}
	if err := tool.validatePath("/tmp/test.txt"); err != nil {
		t.Errorf("absolute path without traversal should pass: %v", err)
	}
}

func TestFileWrite_ValidatePath_SystemDir(t *testing.T) {
	tool := &FileWriteTool{}
	if err := tool.validatePath("/etc/config"); err == nil {
		t.Error("system dir /etc should be blocked for writing")
	}
	if err := tool.validatePath("/usr/local/test"); err == nil {
		t.Error("system dir /usr should be blocked for writing")
	}
}

func TestFileWrite_ValidatePath_Safe(t *testing.T) {
	tool := &FileWriteTool{}
	if err := tool.validatePath("/tmp/output.txt"); err != nil {
		t.Errorf("/tmp should be writable: %v", err)
	}
	if err := tool.validatePath("/Users/wenruigao/test.txt"); err != nil {
		t.Errorf("user dir should be writable: %v", err)
	}
}

func TestBuiltinTools_Names(t *testing.T) {
	r := NewRegistry()

	shellTool := NewShellExecTool()
	r.Register(shellTool, RiskDangerous, 30)

	_, ok := r.Get("shell_exec")
	if !ok {
		t.Error("shell_exec should be registered")
	}
}

func TestWebFetchTool_Name(t *testing.T) {
	tool := &WebFetchTool{}
	if tool.Name() != "web_fetch" {
		t.Errorf("Name = %q, want web_fetch", tool.Name())
	}
}

func TestWebSearchTool_Name(t *testing.T) {
	tool := &WebSearchTool{}
	if tool.Name() != "web_search" {
		t.Errorf("Name = %q, want web_search", tool.Name())
	}
}

func TestFileReadTool_Name(t *testing.T) {
	tool := &FileReadTool{}
	if tool.Name() != "file_read" {
		t.Errorf("Name = %q, want file_read", tool.Name())
	}
}

func TestFileWriteTool_Name(t *testing.T) {
	tool := &FileWriteTool{}
	if tool.Name() != "file_write" {
		t.Errorf("Name = %q, want file_write", tool.Name())
	}
}

func TestCodeRunTool_Name(t *testing.T) {
	tool := &CodeRunTool{}
	if tool.Name() != "code_run" {
		t.Errorf("Name = %q, want code_run", tool.Name())
	}
}

func TestShellExecTool_Name(t *testing.T) {
	tool := NewShellExecTool()
	if tool.Name() != "shell_exec" {
		t.Errorf("Name = %q, want shell_exec", tool.Name())
	}
}

func TestBuiltinTools_Descriptions(t *testing.T) {
	tools := []Tool{
		&WebSearchTool{},
		&WebFetchTool{},
		&FileReadTool{},
		&FileWriteTool{},
		NewShellExecTool(),
		&CodeRunTool{},
	}
	for _, tt := range tools {
		if desc := tt.Description(); desc == "" {
			t.Errorf("%s Description should not be empty", tt.Name())
		}
	}
}

func TestBuiltinTools_Parameters(t *testing.T) {
	tools := []Tool{
		&WebSearchTool{},
		&WebFetchTool{},
		&FileReadTool{},
		&FileWriteTool{},
		NewShellExecTool(),
		&CodeRunTool{},
	}
	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			schema := tool.Parameters()
			if schema.Type != "object" {
				t.Errorf("%s parameters type = %q, want object", tool.Name(), schema.Type)
			}
		})
	}
}

func TestWebFetch_Execute_NoURL(t *testing.T) {
	tool := &WebFetchTool{}
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Error("should error without URL")
	}
}

func TestWebSearch_Execute_NoQuery(t *testing.T) {
	tool := &WebSearchTool{}
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Error("should error without query")
	}
}
