package server

import (
	"errors"
	"sync/atomic"
	"testing"
)

type mockResource struct {
	name    string
	count   atomic.Int32
	failErr error
}

func (m *mockResource) Reload() error {
	m.count.Add(1)
	return m.failErr
}

func (m *mockResource) Name() string { return m.name }

func TestHotReloader_Register_And_ReloadNow(t *testing.T) {
	hr := NewHotReloader()
	r1 := &mockResource{name: "policy"}
	r2 := &mockResource{name: "agent.md"}
	hr.Register(r1)
	hr.Register(r2)

	hr.ReloadNow()

	if r1.count.Load() != 1 {
		t.Errorf("r1 reload count = %d, want 1", r1.count.Load())
	}
	if r2.count.Load() != 1 {
		t.Errorf("r2 reload count = %d, want 1", r2.count.Load())
	}
}

func TestHotReloader_ReloadNow_Multiple(t *testing.T) {
	hr := NewHotReloader()
	r := &mockResource{name: "test"}
	hr.Register(r)

	hr.ReloadNow()
	hr.ReloadNow()
	hr.ReloadNow()

	if r.count.Load() != 3 {
		t.Errorf("reload count = %d, want 3", r.count.Load())
	}
}

func TestHotReloader_ReloadError_DoesNotPanic(t *testing.T) {
	hr := NewHotReloader()
	r := &mockResource{name: "bad", failErr: errors.New("parse error")}
	hr.Register(r)

	// 不应 panic
	hr.ReloadNow()

	if r.count.Load() != 1 {
		t.Errorf("reload count = %d, want 1", r.count.Load())
	}
}

func TestHotReloader_Empty(t *testing.T) {
	hr := NewHotReloader()
	// 无注册资源，不应 panic
	hr.ReloadNow()
}

func TestHotReloader_StartStop(t *testing.T) {
	hr := NewHotReloader()
	r := &mockResource{name: "test"}
	hr.Register(r)

	hr.Start()
	hr.Stop()
	// Stop 后不应 panic
	hr.Stop()
}
