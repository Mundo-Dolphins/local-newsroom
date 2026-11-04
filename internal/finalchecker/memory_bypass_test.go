package finalchecker

import (
	"context"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/entitymemory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
)

// memoryEntityBayern is a memory entity whose manager attribute matches the
// claim below.
var memoryEntityBayern = entitymemory.Entity{
	ID:            "team-bayern",
	CanonicalName: "Bayern Munich",
	Type:          entitymemory.TypeTeam,
	Attributes:    []entitymemory.Attribute{{Key: "manager", Value: "Xabi Alonso"}},
}

// TestMemoryBypassNoContext verifies the check is a no-op when no memory
// context was installed (pre-memory behavior).
func TestMemoryBypassNoContext(t *testing.T) {
	c := NewMemoryBypassChecker()
	artifact := &contracts.EditorialArtifact{
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
	}
	vres := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {
				ClaimID:            "claim-1",
				Statement:          "Bayern Munich is managed by Xabi Alonso",
				VerificationStatus: contracts.VerificationStatusSupported,
				SupportingEvidence: []contracts.EvidenceReference{}, // no live evidence
			},
		},
	}
	if findings := c.CheckMemoryProvenance(context.Background(), artifact, vres); len(findings) != 0 {
		t.Errorf("expected no findings with no context, got %d", len(findings))
	}
}

// TestMemoryBypassWithLiveEvidence verifies a claim with real supporting
// evidence is NOT flagged (the memory could not have verified it).
func TestMemoryBypassWithLiveEvidence(t *testing.T) {
	c := NewMemoryBypassChecker()
	c.SetMemoryContext(&orchestrator.MemoryContext{Entities: []entitymemory.Entity{memoryEntityBayern}})
	artifact := &contracts.EditorialArtifact{
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
	}
	vres := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {
				ClaimID:            "claim-1",
				Statement:          "Bayern Munich is managed by Xabi Alonso",
				VerificationStatus: contracts.VerificationStatusSupported,
				SupportingEvidence: []contracts.EvidenceReference{{
					SourceID: "src-1",
					Excerpt:  "Bayern named Xabi Alonso as manager",
				}},
			},
		},
	}
	if findings := c.CheckMemoryProvenance(context.Background(), artifact, vres); len(findings) != 0 {
		t.Errorf("expected no findings (live evidence present), got %d", len(findings))
	}
}

// TestMemoryBypassNoEvidence verifies a verified claim with no live-source
// evidence that matches a memory entity value IS flagged as critical.
func TestMemoryBypassNoEvidence(t *testing.T) {
	c := NewMemoryBypassChecker()
	c.SetMemoryContext(&orchestrator.MemoryContext{Entities: []entitymemory.Entity{memoryEntityBayern}})
	artifact := &contracts.EditorialArtifact{
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
	}
	vres := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {
				ClaimID:            "claim-1",
				Statement:          "Bayern Munich is managed by Xabi Alonso",
				VerificationStatus: contracts.VerificationStatusSupported,
				SupportingEvidence: []contracts.EvidenceReference{}, // no live evidence
			},
		},
	}
	findings := c.CheckMemoryProvenance(context.Background(), artifact, vres)
	if len(findings) == 0 {
		t.Fatal("expected a bypass finding, got none")
	}
	if findings[0].Severity != contracts.WarningSeverityCritical {
		t.Errorf("expected critical severity, got %v", findings[0].Severity)
	}
}

// TestMemoryBypassInsufficientEvidence verifies a claim the verifier could
// not confirm (insufficient_evidence) is NOT flagged: it was never presented
// as verified, so there is no bypass.
func TestMemoryBypassInsufficientEvidence(t *testing.T) {
	c := NewMemoryBypassChecker()
	c.SetMemoryContext(&orchestrator.MemoryContext{Entities: []entitymemory.Entity{memoryEntityBayern}})
	artifact := &contracts.EditorialArtifact{
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageBackground},
		},
	}
	vres := &contracts.VerificationResult{
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusInsufficientEvidence,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {
				ClaimID:            "claim-1",
				Statement:          "Bayern Munich is managed by Xabi Alonso",
				VerificationStatus: contracts.VerificationStatusInsufficientEvidence,
				SupportingEvidence: []contracts.EvidenceReference{},
			},
		},
	}
	if findings := c.CheckMemoryProvenance(context.Background(), artifact, vres); len(findings) != 0 {
		t.Errorf("expected no findings for insufficient_evidence, got %d", len(findings))
	}
}
