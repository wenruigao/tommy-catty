package kb

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tokens := tokenize("Hello World, this is Go 语言测试!")
	joined := strings.Join(tokens, " ")
	// 英文小写化，停用词（this/is）被过滤
	if !strings.Contains(joined, "hello") || !strings.Contains(joined, "world") {
		t.Errorf("expected english tokens lowercased, got %q", joined)
	}
	if strings.Contains(joined, "this") || strings.Contains(joined, " is ") {
		t.Errorf("stop words should be filtered, got %q", joined)
	}
	// 中文 unigram
	if !strings.Contains(joined, "语") || !strings.Contains(joined, "言") {
		t.Errorf("expected CJK unigrams, got %q", joined)
	}
}

func TestChunkByHeading(t *testing.T) {
	content := "# Intro\nsome intro text\n\n## Usage\nhow to use it\n\n## API\nthe api details"
	chunks := ChunkDocument(content, "doc.md", DefaultChunkConfig())
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 heading chunks, got %d", len(chunks))
	}
	// 第一个 chunk 的标题应为 Intro
	if chunks[0].Heading != "Intro" {
		t.Errorf("expected first heading 'Intro', got %q", chunks[0].Heading)
	}
}

func TestChunkByParagraph(t *testing.T) {
	content := "first paragraph line one\nfirst paragraph line two\n\nsecond paragraph here"
	chunks := ChunkDocument(content, "notes.txt", DefaultChunkConfig())
	if len(chunks) != 2 {
		t.Fatalf("expected 2 paragraph chunks, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "first paragraph") {
		t.Errorf("unexpected first chunk: %q", chunks[0].Content)
	}
}

func TestSlidingWindowLongChunk(t *testing.T) {
	// 构造一个超长段落（无空行、无标题）
	var words []string
	for i := 0; i < 2000; i++ {
		words = append(words, "word")
	}
	content := strings.Join(words, " ")
	cfg := DefaultChunkConfig()
	cfg.MaxTokens = 100
	cfg.Overlap = 20
	chunks := ChunkDocument(content, "big.txt", cfg)
	if len(chunks) < 2 {
		t.Fatalf("expected sliding window to split long chunk, got %d", len(chunks))
	}
}

func TestIndexSearchBM25(t *testing.T) {
	idx := NewIndex()
	cfg := DefaultChunkConfig()
	idx.AddDocument("a.md", "a.md", "# Go 并发\nGo 使用 goroutine 和 channel 实现并发编程", cfg)
	idx.AddDocument("b.md", "b.md", "# Python 基础\nPython 是一门解释型语言", cfg)
	idx.AddDocument("c.md", "c.md", "# Go 测试\nGo 的 testing 包提供单元测试支持", cfg)

	hits := idx.Search("Go 并发", 5)
	if len(hits) == 0 {
		t.Fatal("expected search hits")
	}
	// 最相关结果应来自 a.md（同时命中 Go 与 并发）
	if hits[0].Path != "a.md" {
		t.Errorf("expected top hit a.md, got %q", hits[0].Path)
	}
	// 分数应降序
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Errorf("hits not sorted desc at %d", i)
		}
	}
}

func TestIndexStats(t *testing.T) {
	idx := NewIndex()
	cfg := DefaultChunkConfig()
	idx.AddDocument("x.txt", "x.txt", "hello world", cfg)
	if idx.DocCount() != 1 {
		t.Errorf("expected 1 doc, got %d", idx.DocCount())
	}
	if idx.ChunkCount() == 0 {
		t.Error("expected non-zero chunk count")
	}
}

func TestManagerBuild(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/doc1.md", "# Title\ncontent about databases and sql")
	writeFile(t, dir+"/doc2.md", "# Other\ncontent about networking")

	m := NewManager()
	built, errs := m.Build([]KBConfig{
		{Name: "test", Paths: []string{dir}, Extensions: []string{".md"}},
	})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(built) != 1 {
		t.Fatalf("expected 1 built, got %d", len(built))
	}
	kb, ok := m.Get("test")
	if !ok {
		t.Fatal("kb not registered")
	}
	hits := kb.Search("databases sql", 3)
	if len(hits) == 0 {
		t.Fatal("expected hits from built kb")
	}
}
