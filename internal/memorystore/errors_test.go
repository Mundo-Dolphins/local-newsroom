package memorystore

import (
	"context"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// TestErrorTypes exercises the error surface of the store contract.
func TestErrorTypes(t *testing.T) {
	nf := NotFoundError{ID: "mem-1"}
	if !IsNotFound(nf) {
		t.Error("IsNotFound(NotFoundError) = false")
	}
	if got := nf.Error(); got == "" {
		t.Error("NotFoundError.Error() is empty")
	}

	ae := AlreadyExistsError{ID: "mem-1"}
	if !IsAlreadyExists(ae) {
		t.Error("IsAlreadyExists(AlreadyExistsError) = false")
	}
	if got := ae.Error(); got == "" {
		t.Error("AlreadyExistsError.Error() is empty")
	}

	se := SecretDetectedError{ID: "mem-1", Field: "value"}
	if !IsSecretDetected(se) {
		t.Error("IsSecretDetected(SecretDetectedError) = false")
	}
	if got := se.Error(); got == "" {
		t.Error("SecretDetectedError.Error() is empty")
	}

	su := SupersedeError{ID: "mem-1"}
	if !IsSupersedeError(su) {
		t.Error("IsSupersedeError(SupersedeError) = false")
	}
	if got := su.Error(); got == "" {
		t.Error("SupersedeError.Error() is empty")
	}

	iv := InvalidateError{ID: "mem-1"}
	if !IsInvalidateError(iv) {
		t.Error("IsInvalidateError(InvalidateError) = false")
	}
	if got := iv.Error(); got == "" {
		t.Error("InvalidateError.Error() is empty")
	}

	ir := InvalidRecordError{ID: "mem-1", Err: errTest}
	if !IsInvalidRecord(ir) {
		t.Error("IsInvalidRecord(InvalidRecordError) = false")
	}
	if got := ir.Error(); got == "" {
		t.Error("InvalidRecordError.Error() is empty")
	}
	// InvalidRecordError wraps the underlying cause.
	if ir.Unwrap() != errTest {
		t.Error("InvalidRecordError.Unwrap() did not return the cause")
	}

	// Non-matching errors must not be classified.
	if IsNotFound(errTest) || IsAlreadyExists(errTest) || IsSecretDetected(errTest) ||
		IsSupersedeError(errTest) || IsInvalidateError(errTest) || IsInvalidRecord(errTest) {
		t.Error("a plain error must not be classified as any store error")
	}
}

var errTest = errKind{}

type errKind struct{}

func (errKind) Error() string { return "test error" }

// TestFakeStoreAccessors exercises the FakeStore's accessors and lifecycle
// endpoints that the behavioral tests do not touch.
func TestFakeStoreAccessors(t *testing.T) {
	s := NewFakeStore()
	// WithSanitizer returns a new instance (value semantics) — the original
	// is unchanged.
	withSz := s.WithSanitizer(NewProductionSanitizer("k"))
	if withSz == s {
		t.Error("WithSanitizer must return a new store instance")
	}
	_ = withSz

	ctx := context.Background()
	if v, err := s.SchemaVersion(ctx); err != nil || v != currentSchemaVersion {
		t.Errorf("SchemaVersion = %d, %v; want %d", v, err, currentSchemaVersion)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// The store is usable after Close (in-memory; no resources to release).
	rec := buildRecord(t, memory.KindFactual, "")
	if err := s.Write(ctx, rec); err != nil {
		t.Errorf("Write after Close: %v", err)
	}
}
