package tool

import (
	"strings"
	"testing"
)

func TestRegistryResolvesAndRefusesDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewRead()); err != nil {
		t.Fatalf("register read: %v", err)
	}
	if err := r.Register(NewFind()); err != nil {
		t.Fatalf("register find: %v", err)
	}

	if _, ok := r.Resolve("read"); !ok {
		t.Error("read must resolve")
	}
	if _, ok := r.Resolve("write"); ok {
		t.Error("an unregistered name must not resolve")
	}

	err := r.Register(NewRead())
	if err == nil || !strings.Contains(err.Error(), `tool "read" already registered`) {
		t.Errorf("duplicate registration = %v", err)
	}
}

func TestNamesAreSortedForPrompts(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(NewFind())
	_ = r.Register(NewRead())
	names := r.Names()
	if len(names) != 2 || names[0] != "find" || names[1] != "read" {
		t.Errorf("names = %v", names)
	}
}

// TestForAllowlistHidesDisallowedTools checks the worker-ant filter: a
// tool outside the card's allowlist is not in the sub-registry at all,
// so the model never sees it and cannot call it (doc 04 section 12.1).
func TestForAllowlistHidesDisallowedTools(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(NewRead())
	_ = r.Register(NewFind())
	_ = r.Register(bareTool{}) // stands in for a mutating tool

	sub := r.ForAllowlist([]string{"read", "find", "no_such_tool"})
	if _, ok := sub.Resolve("read"); !ok {
		t.Error("an allowed tool must survive the filter")
	}
	if _, ok := sub.Resolve("bare"); ok {
		t.Error("a tool outside the allowlist must not exist for the worker")
	}
	if got := sub.Names(); len(got) != 2 {
		t.Errorf("sub names = %v, want read and find only", got)
	}
}
