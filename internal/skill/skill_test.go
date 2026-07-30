package skill

import (
	"strings"
	"testing"
)

// ============================================================
// generateSkillName tests
// ============================================================

func TestGenerateSkillName_Empty(t *testing.T) {
	name := generateSkillName("")
	if name != "unnamed-skill" {
		t.Errorf("empty summary = %q, want unnamed-skill", name)
	}
}

func TestGenerateSkillName_English(t *testing.T) {
	name := generateSkillName("Hello World Test")
	if name != "hello-world-test" {
		t.Errorf("got %q, want hello-world-test", name)
	}
}

func TestGenerateSkillName_SpecialChars(t *testing.T) {
	name := generateSkillName("Hello! World? Test.")
	// special chars become dashes, may produce multiple consecutive dashes
	if !strings.HasPrefix(name, "hello") {
		t.Errorf("name should start with hello, got %q", name)
	}
}

func TestGenerateSkillName_MaxFiveWords(t *testing.T) {
	name := generateSkillName("one two three four five six seven")
	parts := strings.Split(name, "-")
	if len(parts) > 5 {
		t.Errorf("should cap at 5 words, got %d: %q", len(parts), name)
	}
}

// ============================================================
// extractKeywords tests
// ============================================================

func TestExtractKeywords_Empty(t *testing.T) {
	kw := extractKeywords("")
	if kw != nil {
		t.Error("empty text should return nil keywords")
	}
}

func TestExtractKeywords_Basic(t *testing.T) {
	kw := extractKeywords("learn programming and build software")
	if len(kw) == 0 {
		t.Error("should extract keywords from text")
	}
}

func TestExtractKeywords_Stopwords(t *testing.T) {
	kw := extractKeywords("the and is a of in to for")
	if len(kw) != 0 {
		t.Errorf("stopwords only should return empty, got %v", kw)
	}
}

func TestExtractKeywords_ShortWords(t *testing.T) {
	kw := extractKeywords("a b c ab cd ef")
	for _, w := range kw {
		if len(w) < 2 {
			t.Errorf("short word %q should be filtered", w)
		}
	}
}

func TestExtractKeywords_Duplicates(t *testing.T) {
	kw := extractKeywords("test test test test")
	if len(kw) != 1 {
		t.Errorf("duplicates should be removed, got %d: %v", len(kw), kw)
	}
}

// ============================================================
// tokenize tests
// ============================================================

func TestSecureTokenize_Empty(t *testing.T) {
	result := tokenize("")
	if len(result) != 0 {
		t.Error("empty text should return empty slice")
	}
}

func TestSecureTokenize_English(t *testing.T) {
	result := tokenize("hello world programming")
	if len(result) != 3 {
		t.Errorf("got %d tokens, want 3: %v", len(result), result)
	}
}

func TestSecureTokenize_ShortWords(t *testing.T) {
	result := tokenize("a b c ab cd")
	for _, w := range result {
		if len(w) < 2 {
			t.Errorf("short word %q should be filtered", w)
		}
	}
}

func TestSecureTokenize_Punctuation(t *testing.T) {
	result := tokenize("hello, world! how-are_you/doing")
	if len(result) == 0 {
		t.Error("text with punctuation should produce tokens")
	}
}

// ============================================================
// ValidateSkill tests
// ============================================================

func TestValidateSkill_Nil(t *testing.T) {
	g := &Generator{}
	err := g.ValidateSkill(nil)
	if err == nil {
		t.Error("nil skill should return error")
	}
}

func TestValidateSkill_EmptyName(t *testing.T) {
	g := &Generator{}
	s := &Skill{Name: "", Steps: []SkillStep{{Order: 1, Action: "call_tool", ToolName: "test"}}}
	if err := g.ValidateSkill(s); err == nil {
		t.Error("skill with empty name should return error")
	}
}

func TestValidateSkill_EmptySteps(t *testing.T) {
	g := &Generator{}
	s := &Skill{Name: "test-skill"}
	if err := g.ValidateSkill(s); err == nil {
		t.Error("skill with empty steps should return error")
	}
}

func TestValidateSkill_ToolCallNoName(t *testing.T) {
	g := &Generator{}
	s := &Skill{
		Name:  "test",
		Steps: []SkillStep{{Order: 1, Action: "call_tool"}},
	}
	if err := g.ValidateSkill(s); err == nil {
		t.Error("call_tool without ToolName should return error")
	}
}

func TestValidateSkill_NonConsecutiveOrder(t *testing.T) {
	g := &Generator{}
	s := &Skill{
		Name: "test",
		Steps: []SkillStep{
			{Order: 1, Action: "do_something"},
			{Order: 3, Action: "do_something"},
		},
	}
	if err := g.ValidateSkill(s); err != nil {
		t.Errorf("non-consecutive but increasing order should be valid: %v", err)
	}
}

func TestValidateSkill_DuplicateOrder(t *testing.T) {
	g := &Generator{}
	s := &Skill{
		Name: "test",
		Steps: []SkillStep{
			{Order: 1, Action: "do_something"},
			{Order: 1, Action: "do_something"},
		},
	}
	if err := g.ValidateSkill(s); err == nil {
		t.Error("duplicate order should return error")
	}
}

func TestValidateSkill_Valid(t *testing.T) {
	g := &Generator{}
	s := &Skill{
		Name: "skill-name",
		Steps: []SkillStep{
			{Order: 1, Action: "do_first"},
			{Order: 2, Action: "do_second"},
		},
	}
	if err := g.ValidateSkill(s); err != nil {
		t.Errorf("valid skill should pass validation: %v", err)
	}
}

// ============================================================
// Matcher scoreSkill tests
// ============================================================

func TestMatcher_ScoreSkill_FullMatch(t *testing.T) {
	m := NewMatcher(nil)
	s := &Skill{
		Name:        "test-skill",
		Description: "searches the web for information",
		Tags:        []string{"utility"},
		Trigger: TriggerRule{
			Keywords: []string{"search", "web"},
		},
	}
	score := m.scoreSkill(s, "web search")
	if score <= 0.5 {
		t.Errorf("full match should have high score, got %f", score)
	}
}

func TestMatcher_ScoreSkill_NoMatch(t *testing.T) {
	m := NewMatcher(nil)
	s := &Skill{
		Name: "test",
		Trigger: TriggerRule{
			Keywords: []string{"python", "code"},
		},
		Tags: []string{"programming"},
	}
	score := m.scoreSkill(s, "web search")
	if score > 0 {
		t.Errorf("no match should have zero score, got %f", score)
	}
}

func TestNewMatcher(t *testing.T) {
	store := NewStore("/tmp/test-skills.json")
	m := NewMatcher(store)
	if m == nil {
		t.Error("NewMatcher should not return nil")
	}
	if m.store != store {
		t.Error("Matcher should reference the store")
	}
}

// ============================================================
// Store tests
// ============================================================

func TestStore_SaveAndGet(t *testing.T) {
	store := NewStore("/tmp/test-store-skills.json")

	s := &Skill{ID: "test-id-1", Name: "test-skill", Steps: []SkillStep{{Order: 1, Action: "test"}}}
	err := store.Save(s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok := store.Get("test-id-1")
	if !ok {
		t.Fatal("Get should find saved skill")
	}
	if got.Name != "test-skill" {
		t.Errorf("Name = %q, want test-skill", got.Name)
	}
}

func TestStore_Save_Nil(t *testing.T) {
	store := NewStore("/tmp/test-store-skills-nil")
	err := store.Save(nil)
	if err == nil {
		t.Error("Save nil should return error")
	}
}

func TestStore_Save_EmptyID(t *testing.T) {
	store := NewStore("/tmp/test-store-skills-empty")
	err := store.Save(&Skill{Name: "test"})
	if err == nil {
		t.Error("Save with empty ID should return error")
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	store := NewStore("/tmp/test-store-skills.json")
	_, ok := store.Get("nonexistent-id")
	if ok {
		t.Error("Get should return false for nonexistent")
	}
}

func TestStore_List(t *testing.T) {
	store := NewStore("/tmp/test-store-skills-list.json")

	store.Save(&Skill{ID: "a", Name: "a", Steps: []SkillStep{{Order: 1, Action: "do"}}})
	store.Save(&Skill{ID: "b", Name: "b", Steps: []SkillStep{{Order: 1, Action: "do"}}})

	list := store.List()
	if len(list) != 2 {
		t.Errorf("List should return 2 skills, got %d", len(list))
	}
}

func TestStore_Delete(t *testing.T) {
	store := NewStore("/tmp/test-store-skills-del.json")

	s := &Skill{ID: "to-delete", Name: "test", Steps: []SkillStep{{Order: 1, Action: "do"}}}
	store.Save(s)

	err := store.Delete(s.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok := store.Get(s.ID)
	if ok {
		t.Error("Get after Delete should return false")
	}
}

func TestStore_Delete_NotFound(t *testing.T) {
	store := NewStore("/tmp/test-store-skills.json")
	err := store.Delete("nonexistent")
	if err == nil {
		t.Error("Delete nonexistent should return error")
	}
}
