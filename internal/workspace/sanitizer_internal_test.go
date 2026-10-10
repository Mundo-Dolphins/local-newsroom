package workspace

import (
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// TestStoreSanitizerAccessors covers the constructor/nil defaulting and
// SetSanitizer switching (the nil-sanitizer branch of Sanitizer).
func TestStoreSanitizerAccessors(t *testing.T) {
	store, err := NewStoreWithSanitizer(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// A nil registered sanitizer behaves as the pattern-only default.
	san := store.Sanitizer()
	if san == nil {
		t.Fatal("Sanitizer() must never return nil")
	}
	if got := san.ExactValues(); len(got) != 0 {
		t.Errorf("default sanitizer has exact values: %v", got)
	}
	// SetSanitizer swaps the policy; nil restores the default.
	store.SetSanitizer(run.NewSanitizer("live-key-1"))
	if !store.Sanitizer().ContainsExactText("a live-key-1 value") {
		t.Error("SetSanitizer did not take effect")
	}
	store.SetSanitizer(nil)
	if got := store.Sanitizer().ExactValues(); len(got) != 0 {
		t.Errorf("restored default has exact values: %v", got)
	}
}
