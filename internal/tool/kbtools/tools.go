// Package kbtools 提供本地知识库检索工具：kb_search / kb_read / kb_list。
// 这些工具基于 internal/kb 的内存倒排索引与 BM25 检索。
package kbtools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wenruigao/tommy-catty/internal/kb"
	"github.com/wenruigao/tommy-catty/internal/tool"
)

// ============================================================
// KBSearchTool - 知识库语义检索
// ============================================================

// KBSearchTool 在指定知识库中检索相关片段。
type KBSearchTool struct {
	mgr *kb.Manager
}

// NewKBSearchTool 创建检索工具。
func NewKBSearchTool(mgr *kb.Manager) *KBSearchTool {
	return &KBSearchTool{mgr: mgr}
}

func (t *KBSearchTool) Name() string { return "kb_search" }

func (t *KBSearchTool) Description() string {
	return "在本地知识库中检索与查询最相关的文档片段，返回带来源（文件路径、标题、行号）的排序结果。适用于查阅项目文档、代码说明、笔记等。"
}

func (t *KBSearchTool) Parameters() tool.JSONSchema {
	return tool.JSONSchema{
		Type: "object",
		Properties: map[string]tool.Property{
			"kb": {
				Type:        "string",
				Description: "知识库名称（对应配置中 knowledge_bases 的条目名）",
			},
			"query": {
				Type:        "string",
				Description: "检索关键词或问题",
			},
			"top_k": {
				Type:        "integer",
				Description: "返回结果数量（默认取知识库配置的 top_k）",
			},
		},
		Required: []string{"kb", "query"},
	}
}

func (t *KBSearchTool) Execute(ctx context.Context, args map[string]interface{}) (tool.Result, error) {
	name, ok := args["kb"].(string)
	if !ok || name == "" {
		return tool.Result{Error: "参数 kb 必填"}, nil
	}
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return tool.Result{Error: "参数 query 必填"}, nil
	}

	kbase, ok := t.mgr.Get(name)
	if !ok {
		return tool.Result{Error: fmt.Sprintf("知识库 %q 不存在，可用：%s", name, strings.Join(t.mgr.Names(), ", "))}, nil
	}

	topK := 0
	if v, ok := args["top_k"]; ok {
		switch n := v.(type) {
		case int:
			topK = n
		case float64:
			topK = int(n)
		}
	}

	hits := kbase.Search(query, topK)
	if len(hits) == 0 {
		return tool.Result{
			Output:   "未找到相关结果。",
			Metadata: map[string]interface{}{"kb": name, "hit_count": 0},
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("知识库 %q 检索到 %d 条结果：\n\n", name, len(hits)))
	for i, h := range hits {
		sb.WriteString(fmt.Sprintf("### %d. %s", i+1, h.Title))
		if h.Heading != "" {
			sb.WriteString(fmt.Sprintf(" › %s", h.Heading))
		}
		sb.WriteString(fmt.Sprintf("  (score=%.3f)\n", h.Score))
		loc := h.Path
		if h.Chunk != nil && h.Chunk.StartLine > 0 {
			loc = fmt.Sprintf("%s:%d-%d", h.Path, h.Chunk.StartLine, h.Chunk.EndLine)
		}
		sb.WriteString(fmt.Sprintf("来源: %s\n\n", loc))
		if h.Chunk != nil {
			sb.WriteString(h.Chunk.Content)
			sb.WriteString("\n\n---\n\n")
		}
	}

	return tool.Result{
		Output: sb.String(),
		Metadata: map[string]interface{}{
			"kb":        name,
			"hit_count": len(hits),
		},
	}, nil
}

// ============================================================
// KBReadTool - 读取文档/片段原文
// ============================================================

// KBReadTool 读取知识库中某文档的完整内容或指定片段。
type KBReadTool struct {
	mgr *kb.Manager
}

// NewKBReadTool 创建读取工具。
func NewKBReadTool(mgr *kb.Manager) *KBReadTool {
	return &KBReadTool{mgr: mgr}
}

func (t *KBReadTool) Name() string { return "kb_read" }

func (t *KBReadTool) Description() string {
	return "读取知识库中某文档的完整内容，或按 doc_id + seq 读取单个片段。用于在 kb_search 命中后查看上下文原文。"
}

func (t *KBReadTool) Parameters() tool.JSONSchema {
	return tool.JSONSchema{
		Type: "object",
		Properties: map[string]tool.Property{
			"kb": {
				Type:        "string",
				Description: "知识库名称",
			},
			"doc_id": {
				Type:        "integer",
				Description: "文档 ID（来自 kb_search 结果或 kb_list）",
			},
			"seq": {
				Type:        "integer",
				Description: "片段序号（可选，指定则只返回该片段；缺省返回整篇所有片段）",
			},
		},
		Required: []string{"kb", "doc_id"},
	}
}

func (t *KBReadTool) Execute(ctx context.Context, args map[string]interface{}) (tool.Result, error) {
	name, ok := args["kb"].(string)
	if !ok || name == "" {
		return tool.Result{Error: "参数 kb 必填"}, nil
	}
	docID, ok := toInt(args["doc_id"])
	if !ok {
		return tool.Result{Error: "参数 doc_id 必填且为整数"}, nil
	}

	kbase, ok := t.mgr.Get(name)
	if !ok {
		return tool.Result{Error: fmt.Sprintf("知识库 %q 不存在", name)}, nil
	}

	doc, ok := kbase.Index.GetDocument(docID)
	if !ok {
		return tool.Result{Error: fmt.Sprintf("文档 ID %d 不存在", docID)}, nil
	}

	// 指定 seq：只返回单个片段
	if seqVal, present := args["seq"]; present {
		if seq, ok := toInt(seqVal); ok {
			for _, c := range doc.Chunks {
				if c.Seq == seq {
					return tool.Result{
						Output: fmt.Sprintf("# %s (片段 %d, 行 %d-%d)\n\n%s",
							doc.Path, c.Seq, c.StartLine, c.EndLine, c.Content),
						Metadata: map[string]interface{}{"kb": name, "doc_id": docID, "seq": seq},
					}, nil
				}
			}
			return tool.Result{Error: fmt.Sprintf("片段 seq=%d 不存在", seq)}, nil
		}
	}

	// 返回整篇（所有片段拼接）
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s（共 %d 个片段）\n\n", doc.Path, len(doc.Chunks)))
	for _, c := range doc.Chunks {
		sb.WriteString(fmt.Sprintf("## 片段 %d (行 %d-%d)\n", c.Seq, c.StartLine, c.EndLine))
		if c.Heading != "" {
			sb.WriteString(fmt.Sprintf("标题: %s\n", c.Heading))
		}
		sb.WriteString("\n")
		sb.WriteString(c.Content)
		sb.WriteString("\n\n")
	}

	return tool.Result{
		Output: sb.String(),
		Metadata: map[string]interface{}{
			"kb":          name,
			"doc_id":      docID,
			"chunk_count": len(doc.Chunks),
		},
	}, nil
}

// ============================================================
// KBListTool - 列出知识库与文档
// ============================================================

// KBListTool 列出可用知识库及其文档清单。
type KBListTool struct {
	mgr *kb.Manager
}

// NewKBListTool 创建列表工具。
func NewKBListTool(mgr *kb.Manager) *KBListTool {
	return &KBListTool{mgr: mgr}
}

func (t *KBListTool) Name() string { return "kb_list" }

func (t *KBListTool) Description() string {
	return "列出所有可用知识库；若指定 kb 名称，则列出该知识库中已索引的文档（含 doc_id、路径、片段数）。"
}

func (t *KBListTool) Parameters() tool.JSONSchema {
	return tool.JSONSchema{
		Type: "object",
		Properties: map[string]tool.Property{
			"kb": {
				Type:        "string",
				Description: "知识库名称（可选，缺省列出所有知识库）",
			},
		},
	}
}

func (t *KBListTool) Execute(ctx context.Context, args map[string]interface{}) (tool.Result, error) {
	name, _ := args["kb"].(string)

	// 列出所有知识库
	if name == "" {
		names := t.mgr.Names()
		if len(names) == 0 {
			return tool.Result{Output: "当前没有可用的知识库。"}, nil
		}
		var sb strings.Builder
		sb.WriteString("可用知识库：\n\n")
		for _, n := range names {
			kbase, _ := t.mgr.Get(n)
			sb.WriteString(fmt.Sprintf("- **%s**：%d 篇文档，%d 个片段\n",
				n, kbase.Index.DocCount(), kbase.Index.ChunkCount()))
		}
		return tool.Result{Output: sb.String(), Metadata: map[string]interface{}{"count": len(names)}}, nil
	}

	// 列出指定知识库的文档
	kbase, ok := t.mgr.Get(name)
	if !ok {
		return tool.Result{Error: fmt.Sprintf("知识库 %q 不存在", name)}, nil
	}
	docs := kbase.Index.Documents()
	if len(docs) == 0 {
		return tool.Result{Output: fmt.Sprintf("知识库 %q 中没有文档。", name)}, nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("知识库 %q 的文档（共 %d 篇）：\n\n", name, len(docs)))
	for _, d := range docs {
		sb.WriteString(fmt.Sprintf("- [doc_id=%d] %s（%d 片段）\n", d.ID, d.Path, len(d.Chunks)))
	}
	return tool.Result{
		Output:   sb.String(),
		Metadata: map[string]interface{}{"kb": name, "doc_count": len(docs)},
	}, nil
}

// toInt 将 interface{} 安全转换为 int。
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
