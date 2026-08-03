package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Exporter 将追踪 span 以 JSONL 格式导出到文件（每个 span 一行 JSON）。
type Exporter struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// NewExporter 创建 JSONL 导出器，以追加方式写入指定文件。
func NewExporter(path string) (*Exporter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开 trace 导出文件失败: %w", err)
	}
	return &Exporter{f: f, enc: json.NewEncoder(f)}, nil
}

// Export 将一批 span 逐行写入导出文件。
func (e *Exporter) Export(spans []*Span) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range spans {
		if err := e.enc.Encode(s); err != nil {
			return fmt.Errorf("写入 trace span 失败: %w", err)
		}
	}
	return nil
}

// Close 关闭导出器（刷新并关闭底层文件）。
func (e *Exporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.f.Close()
}
