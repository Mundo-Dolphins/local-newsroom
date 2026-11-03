package memory

import (
	"strings"
	"testing"
)

// Tests for the record-level enum-branch validation that shares a code path
// for all enum fields: unknown values fail, recognized values pass.

func TestKindValidationBranches(t *testing.T) {
	m := factualRecord()
	m.Kind = "bogus"
	if err := m.Validate(); err == nil {
		t.Error("unknown kind must fail")
	}
}

func TestScopeValidationBranches(t *testing.T) {
	m := factualRecord()
	m.Scope = "galaxy"
	if err := m.Validate(); err == nil {
		t.Error("unknown scope must fail")
	}
}

func TestConfidenceValidationBranches(t *testing.T) {
	m := factualRecord()
	m.Confidence = "maybe"
	if err := m.Validate(); err == nil {
		t.Error("unknown confidence must fail")
	}
}

func TestVolatilityValidationBranches(t *testing.T) {
	m := factualRecord()
	m.Volatility = "sometimes"
	if err := m.Validate(); err == nil {
		t.Error("unknown volatility must fail")
	}
}

func TestExpirationStatusValidationBranches(t *testing.T) {
	m := factualRecord()
	m.Expiration = Expiration{InvalidationStatus: "zombie"}
	if err := m.Validate(); err == nil {
		t.Error("unknown invalidation status must fail")
	}
}

func TestInvalidationStatusEmptyBranch(t *testing.T) {
	m := factualRecord()
	m.Expiration = Expiration{}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "invalidation_status") {
		t.Errorf("empty invalidation status must fail, got %v", err)
	}
}

func TestSupersededWithoutTargetBranch(t *testing.T) {
	m := factualRecord()
	m.Expiration = Expiration{InvalidationStatus: InvalidationSuperseded}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "superseded_by") {
		t.Errorf("superseded without target must fail, got %v", err)
	}
}
