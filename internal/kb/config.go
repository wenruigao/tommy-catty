// config.go 定义知识库的配置与加载逻辑。
package kb

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KBConfig 描述一个本地知识库。
type KBConfig struct {
	Name       string   `yaml:"name" json:"name"`               // 知识库名称
	Paths      []string `yaml:"paths" json:"paths"`             // 待索引的目录/文件
	Exclude    []string `yaml:"exclude" json:"exclude"`         // 排除的 glob 模式
	Extensions []string `yaml:"extensions" json:"extensions"`   // 允许的文件扩展名（含点，如 .md）
	Strategy   string   `yaml:"strategy" json:"strategy"`       // 分块策略：auto/heading/paragraph
	MaxTokens  int      `yaml:"max_tokens" json:"max_tokens"`   // 每块最大 token
	Overlap    int      `yaml:"overlap" json:"overlap"`         // 滑动窗口重叠
	MaxFileMB  int      `yaml:"max_file_mb" json:"max_file_mb"` // 单文件大小上限（MB）
	TopK       int      `yaml:"top_k" json:"top_k"`             // 默认检索返回数
}

// DefaultExtensions 默认索引的文本类文件扩展名。
var DefaultExtensions = []string{
	".md", ".markdown", ".txt", ".rst",
	".go", ".py", ".js", ".ts", ".java", ".rs", ".c", ".cpp", ".h",
	".yaml", ".yml", ".json", ".toml",
}

// applyDefaults 填充默认值。
func (c *KBConfig) applyDefaults() {
	if len(c.Extensions) == 0 {
		c.Extensions = DefaultExtensions
	}
	if c.Strategy == "" {
		c.Strategy = string(StrategyAuto)
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 500
	}
	if c.MaxFileMB <= 0 {
		c.MaxFileMB = 5
	}
	if c.TopK <= 0 {
		c.TopK = 5
	}
}

// chunkConfig 转换为分块配置。
func (c *KBConfig) chunkConfig() ChunkConfig {
	return ChunkConfig{
		Strategy:  ChunkStrategy(c.Strategy),
		MaxTokens: c.MaxTokens,
		Overlap:   c.Overlap,
	}
}

// extAllowed 判断扩展名是否在允许列表中。
func (c *KBConfig) extAllowed(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range c.Extensions {
		if strings.ToLower(e) == ext {
			return true
		}
	}
	return false
}

// excluded 判断路径是否匹配任一排除模式。
func (c *KBConfig) excluded(path string) bool {
	for _, pattern := range c.Exclude {
		if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
			return true
		}
		if strings.Contains(path, pattern) {
			return true
		}
	}
	return false
}

// KnowledgeBase 是一个已构建的知识库实例。
type KnowledgeBase struct {
	Config KBConfig
	Index  *Index
}

// BuildKnowledgeBase 扫描配置中的路径并构建内存索引。
func BuildKnowledgeBase(cfg KBConfig) (*KnowledgeBase, error) {
	cfg.applyDefaults()
	idx := NewIndex()

	for _, root := range cfg.Paths {
		info, err := os.Stat(root)
		if err != nil {
			continue // 跳过不存在的路径
		}
		if !info.IsDir() {
			indexFile(idx, root, cfg)
			continue
		}
		_ = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi == nil {
				return nil
			}
			if fi.IsDir() {
				if cfg.excluded(path) {
					return filepath.SkipDir
				}
				return nil
			}
			indexFile(idx, path, cfg)
			return nil
		})
	}

	return &KnowledgeBase{Config: cfg, Index: idx}, nil
}

// indexFile 读取并索引单个文件。
func indexFile(idx *Index, path string, cfg KBConfig) {
	if !cfg.extAllowed(path) || cfg.excluded(path) {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.Size() > int64(cfg.MaxFileMB)*1024*1024 {
		return // 跳过大文件
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	title := filepath.Base(path)
	idx.AddDocument(path, title, string(data), cfg.chunkConfig())
}

// Search 在知识库中检索，返回带来源引用的结果。
func (kb *KnowledgeBase) Search(query string, topK int) []SearchHit {
	if topK <= 0 {
		topK = kb.Config.TopK
	}
	return kb.Index.Search(query, topK)
}

// Stats 返回知识库统计信息。
type Stats struct {
	Name        string
	DocCount    int
	ChunkCount  int
	BuiltAt     time.Time
}
