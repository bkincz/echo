package jsruntime_test

import (
	"testing"

	"github.com/echo-ssr/echo/internal/jsruntime"
)

// Find and Require use sync.Once so only one path can be exercised per binary
// invocation. These tests cover the happy path (Node.js present in PATH).
// The "node not found" path is exercised indirectly by cmd/echo tests via
// PATH="" environment manipulation.

func TestFind_returnsNodeWhenAvailable(t *testing.T) {
	rt := jsruntime.Find()
	if rt == "" {
		t.Skip("Node.js not available in PATH — skipping")
	}
	if rt != "node" {
		t.Errorf("Find() = %q, want %q", rt, "node")
	}
}

func TestRequire_succeedsWhenNodeAvailable(t *testing.T) {
	rt, err := jsruntime.Require()
	if rt == "" {
		t.Skip("Node.js not available in PATH — skipping")
	}
	if err != nil {
		t.Fatalf("Require() unexpected error: %v", err)
	}
	if rt != "node" {
		t.Errorf("Require() = %q, want %q", rt, "node")
	}
}

func TestRequire_returnsConsistentResult(t *testing.T) {
	// Calling Find and Require multiple times should always return the same result.
	a := jsruntime.Find()
	b := jsruntime.Find()
	if a != b {
		t.Errorf("Find() not idempotent: %q vs %q", a, b)
	}

	r1, e1 := jsruntime.Require()
	r2, e2 := jsruntime.Require()
	if r1 != r2 {
		t.Errorf("Require() runtime not idempotent: %q vs %q", r1, r2)
	}
	if (e1 == nil) != (e2 == nil) {
		t.Errorf("Require() error not idempotent: %v vs %v", e1, e2)
	}
}
