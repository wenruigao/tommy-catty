// Package skill 提供技能的持久化存储功能。
package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Store 是技能的持久化存储，支持内存缓存和 JSON 文件持久化
type Store struct {
	// skills 内存中的技能映射表（ID -> Skill）
	skills map[string]*Skill
	// mu 保护 skills 的读写锁
	mu sync.RWMutex
	// filePath 持久化文件路径
	filePath string
}

// NewStore 创建一个新的技能存储实例
// 如果指定的文件路径存在，则从文件中加载已有技能
func NewStore(filePath string) *Store {
	s := &Store{
		skills:   make(map[string]*Skill),
		filePath: filePath,
	}
	// 尝试从文件加载已有数据
	_ = s.load()
	return s
}

// Save 保存技能到存储中，并持久化到文件
func (s *Store) Save(sk *Skill) error {
	if sk == nil {
		return fmt.Errorf("技能不能为空")
	}
	if sk.ID == "" {
		return fmt.Errorf("技能ID不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.skills[sk.ID] = sk
	return s.persist()
}

// Get 根据ID获取技能
func (s *Store) Get(id string) (*Skill, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sk, ok := s.skills[id]
	return sk, ok
}

// List 返回所有已存储的技能列表
func (s *Store) List() []*Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		result = append(result, sk)
	}
	return result
}

// Delete 根据ID删除技能，并持久化到文件
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.skills[id]; !ok {
		return fmt.Errorf("技能不存在: %s", id)
	}

	delete(s.skills, id)
	return s.persist()
}

// persist 将所有技能写入 JSON 文件（调用时需持有写锁）
func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.skills, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化技能数据失败: %w", err)
	}

	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("写入技能文件失败: %w", err)
	}
	return nil
}

// load 从 JSON 文件中加载技能数据
func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在时不视为错误，使用空存储
			return nil
		}
		return fmt.Errorf("读取技能文件失败: %w", err)
	}

	var skills map[string]*Skill
	if err := json.Unmarshal(data, &skills); err != nil {
		return fmt.Errorf("解析技能数据失败: %w", err)
	}

	s.skills = skills
	return nil
}
