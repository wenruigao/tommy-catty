// index.go 实现内存倒排索引与 BM25 排序。
package kb

import (
	"math"
	"sort"
	"sync"
)

// Document 表示一个已索引的文档。
type Document struct {
	ID     int    // 文档唯一 ID
	Path   string // 文件路径
	Title  string // 文档标题（文件名或首个标题）
	Chunks []Chunk
}

// posting 记录某个词项在某个 chunk 中的出现次数。
type posting struct {
	docID int
	seq   int // chunk 序号
	tf    int // 词频
}

// Index 是内存倒排索引 + BM25 检索引擎。
type Index struct {
	mu sync.RWMutex

	docs    map[int]*Document
	chunks  map[int]map[int]*Chunk // docID -> seq -> chunk
	posting map[string][]posting   // term -> postings

	// chunk 长度（token 数），用于 BM25 归一化
	chunkLen    map[chunkKey]int
	avgChunkLen float64
	totalChunks int
	nextDocID   int

	// BM25 参数
	k1 float64
	b  float64
}

// chunkKey 唯一标识一个 chunk。
type chunkKey struct {
	docID int
	seq   int
}

// NewIndex 创建空索引。
func NewIndex() *Index {
	return &Index{
		docs:     make(map[int]*Document),
		chunks:   make(map[int]map[int]*Chunk),
		posting:  make(map[string][]posting),
		chunkLen: make(map[chunkKey]int),
		k1:       1.5,
		b:        0.75,
	}
}

// AddDocument 索引一个文档（先分块再建倒排）。
func (idx *Index) AddDocument(path, title, content string, cfg ChunkConfig) int {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	docID := idx.nextDocID
	idx.nextDocID++

	chunks := ChunkDocument(content, path, cfg)
	for i := range chunks {
		chunks[i].DocID = docID
	}

	doc := &Document{ID: docID, Path: path, Title: title, Chunks: chunks}
	idx.docs[docID] = doc
	idx.chunks[docID] = make(map[int]*Chunk)

	for _, c := range chunks {
		idx.indexChunk(docID, c.Seq, c)
	}

	idx.recomputeStats()
	return docID
}

// indexChunk 将单个 chunk 加入倒排索引（调用方须持锁）。
func (idx *Index) indexChunk(docID, seq int, c Chunk) {
	idx.chunks[docID][seq] = &c
	key := chunkKey{docID, seq}

	tokens := tokenize(c.Content)
	idx.chunkLen[key] = len(tokens)

	// 统计词频
	tf := make(map[string]int)
	for _, t := range tokens {
		tf[t]++
	}
	for term, count := range tf {
		idx.posting[term] = append(idx.posting[term], posting{docID: docID, seq: seq, tf: count})
	}
}

// recomputeStats 重算平均 chunk 长度等统计量（调用方须持锁）。
func (idx *Index) recomputeStats() {
	total := 0
	for _, l := range idx.chunkLen {
		total += l
	}
	idx.totalChunks = len(idx.chunkLen)
	if idx.totalChunks > 0 {
		idx.avgChunkLen = float64(total) / float64(idx.totalChunks)
	} else {
		idx.avgChunkLen = 0
	}
}

// SearchHit 表示一条检索结果。
type SearchHit struct {
	DocID   int
	Seq     int
	Path    string
	Title   string
	Heading string
	Score   float64
	Chunk   *Chunk
}

// Search 执行 BM25 检索，返回按分数降序的前 topK 个 chunk。
func (idx *Index) Search(query string, topK int) []SearchHit {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if topK <= 0 {
		topK = 5
	}
	if idx.totalChunks == 0 {
		return nil
	}

	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return nil
	}

	scores := make(map[chunkKey]float64)
	N := float64(idx.totalChunks)

	for _, term := range queryTerms {
		posts := idx.posting[term]
		if len(posts) == 0 {
			continue
		}
		// IDF：log(1 + (N - df + 0.5) / (df + 0.5))
		df := float64(len(posts))
		idf := math.Log(1 + (N-df+0.5)/(df+0.5))

		for _, p := range posts {
			key := chunkKey{p.docID, p.seq}
			dl := float64(idx.chunkLen[key])
			tf := float64(p.tf)
			// BM25 词项得分
			num := tf * (idx.k1 + 1)
			den := tf + idx.k1*(1-idx.b+idx.b*dl/idx.avgChunkLen)
			scores[key] += idf * num / den
		}
	}

	hits := make([]SearchHit, 0, len(scores))
	for key, score := range scores {
		c := idx.chunks[key.docID][key.seq]
		doc := idx.docs[key.docID]
		hits = append(hits, SearchHit{
			DocID:   key.docID,
			Seq:     key.seq,
			Path:    doc.Path,
			Title:   doc.Title,
			Heading: c.Heading,
			Score:   score,
			Chunk:   c,
		})
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Seq < hits[j].Seq
	})

	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits
}

// DocCount 返回已索引文档数。
func (idx *Index) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// ChunkCount 返回已索引 chunk 数。
func (idx *Index) ChunkCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.totalChunks
}

// GetDocument 返回指定文档。
func (idx *Index) GetDocument(docID int) (*Document, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	d, ok := idx.docs[docID]
	return d, ok
}

// Documents 返回所有文档（按 ID 升序）。
func (idx *Index) Documents() []*Document {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	docs := make([]*Document, 0, len(idx.docs))
	for _, d := range idx.docs {
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return docs
}
