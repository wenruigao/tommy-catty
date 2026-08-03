package trace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExporter_WritesJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.jsonl")
	exp, err := NewExporter(path)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	tr := NewTracer()
	s1 := tr.StartSpan("t1", "span1", map[string]string{"k": "v"})
	s2 := tr.StartSpan("t1", "span2", nil)
	tr.EndSpan(s1, nil)
	tr.EndSpan(s2, nil)

	if err := exp.Export(tr.GetSpans()); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	if err := exp.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开导出文件失败: %v", err)
	}
	defer f.Close()

	var lines []Span
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var s Span
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			t.Fatalf("导出文件包含非法 JSON 行: %v", err)
		}
		lines = append(lines, s)
	}
	if len(lines) != 2 {
		t.Fatalf("应有 2 行 span，得到 %d", len(lines))
	}
	if lines[0].Name != "span1" || lines[1].Name != "span2" {
		t.Errorf("span 名称不正确: %+v", lines)
	}
	if lines[0].Attrs["k"] != "v" {
		t.Errorf("span 属性丢失: %+v", lines[0].Attrs)
	}
}

func TestExporter_Append(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.jsonl")

	tr := NewTracer()
	s := tr.StartSpan("t", "s", nil)
	tr.EndSpan(s, nil)

	// 两次导出应追加为两行
	exp, err := NewExporter(path)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}
	if err := exp.Export(tr.GetSpans()); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	exp.Close()

	exp2, err := NewExporter(path)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}
	if err := exp2.Export(tr.GetSpans()); err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	exp2.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取导出文件失败: %v", err)
	}
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	if count != 2 {
		t.Errorf("追加导出应有 2 行，得到 %d", count)
	}
}

func TestExporter_BadPath(t *testing.T) {
	_, err := NewExporter(filepath.Join(t.TempDir(), "nonexistent-dir", "traces.jsonl"))
	if err == nil {
		t.Error("无效路径应返回错误")
	}
}
