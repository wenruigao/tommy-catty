package security

import "testing"

func TestResolveConflictDenyOverride(t *testing.T) {
	decisions := []Decision{
		{Effect: EffectAllow, PolicyID: "p1"},
		{Effect: EffectDeny, PolicyID: "p2", Message: "blocked"},
		{Effect: EffectAllow, PolicyID: "p3"},
	}
	policies := []Policy{
		{ID: "p1", Priority: 10},
		{ID: "p2", Priority: 50},
		{ID: "p3", Priority: 5},
	}
	result := ResolveConflict(decisions, policies)
	if result.Effect != EffectDeny {
		t.Errorf("expected deny-override, got %v", result.Effect)
	}
}

func TestResolveConflictPriority(t *testing.T) {
	decisions := []Decision{
		{Effect: EffectAllow, PolicyID: "p1"},
		{Effect: EffectThrottle, PolicyID: "p2"},
	}
	policies := []Policy{
		{ID: "p1", Priority: 50},
		{ID: "p2", Priority: 10},
	}
	result := ResolveConflict(decisions, policies)
	// p2 has lower priority number → wins
	if result.PolicyID != "p2" {
		t.Errorf("expected p2 (higher priority), got %s", result.PolicyID)
	}
}

func TestResolveConflictApproval(t *testing.T) {
	decisions := []Decision{
		{Effect: EffectAllow, PolicyID: "p1"},
		{Effect: EffectRequireApproval, PolicyID: "p2"},
	}
	policies := []Policy{
		{ID: "p1", Priority: 10},
		{ID: "p2", Priority: 50},
	}
	result := ResolveConflict(decisions, policies)
	if result.Effect != EffectRequireApproval {
		t.Errorf("expected require_approval over allow, got %v", result.Effect)
	}
}

func TestDetectConflicts(t *testing.T) {
	policies := []Policy{
		{ID: "a", Enabled: true, Priority: 10, When: PolicyCondition{ActionType: []string{"task_start"}}, Then: PolicyAction{Effect: EffectAllow}},
		{ID: "b", Enabled: true, Priority: 20, When: PolicyCondition{ActionType: []string{"task_start"}}, Then: PolicyAction{Effect: EffectDeny}},
	}
	conflicts := DetectConflicts(policies)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].PolicyA != "a" || conflicts[0].PolicyB != "b" {
		t.Errorf("unexpected conflict pair: %+v", conflicts[0])
	}
}
