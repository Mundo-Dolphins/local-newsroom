package publication

import (
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// testTime is the fixed base time for deterministic fixtures.
var testTime = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

// testToken is a high-entropy, mixed-class run that run.LooksLikeSecret
// catches: the fixture behind every "a secret must not persist" test.
const testToken = "aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"

// tAt returns testTime advanced by i seconds.
func tAt(i int) time.Time { return testTime.Add(time.Duration(i) * time.Second) }

// testRunID is a well-formed, stable run ID for fixtures.
var testRunID = run.NewRunID("feed", "2026-07-11-dolphins", testTime)

// The canonical artifact bytes. briefB is briefA with one sentence edited —
// the fixture behind every "the artifact changed" test.
const (
	briefA = "# Dolphins 2026 season preview\n\nThe Dolphins opened the season strong."
	briefB = "# Dolphins 2026 season preview\n\nThe Dolphins opened the season steady."
)

func digestOf(content string) Digest { return DigestOf([]byte(content)) }

// artifact builds a well-formed artifact identity for the given content.
func artifact(content string) ArtifactIdentity {
	return NewArtifactIdentity("art-001", run.ArtifactKindRendered, "output/dolphins-preview.md", []byte(content), "render/v1")
}

// passingGate builds passing gate evidence gathered for the given content.
func passingGate(content string) GateEvidence {
	return NewGateEvidence(run.RunStatusSucceeded, contracts.FactualCheckStatusPass, true, digestOf(content), testTime, "final check passed; review READY")
}

// mainTarget is the default single target used by fixtures.
func mainTarget() PublicationTarget {
	return NewTarget("bluesky-thread", "Dolphins thread", TargetTypePost, "bluesky", "at://com.example/app/bsky.feed.post/dolphins-1", nil)
}

// newApprovalFor builds a granted approval for the plan's current artifact.
func newApprovalFor(p *Plan, by string, targets ...TargetID) *PublicationApproval {
	return NewApproval(p.ID, by, ApprovalMethodCLI, p.Artifact.Digest, targets, "looks good", tAt(10))
}

// receiptFor builds a receipt whose content digest matches the plan's artifact.
func receiptFor(p *Plan, targetID TargetID, at time.Time) *PublicationReceipt {
	return NewReceipt(p.ID, targetID, "bluesky",
		"https://bsky.app/profile/dolphins.example/post/1",
		"3luchk2f5zrq4c5n3b2v7xjy6h8d9m0a1k4e6t7u8i9o0p1q2r3s",
		p.Artifact.Digest, "idem-7f3a", "bluesky-publisher/0.6.0",
		map[string]string{"posts": "5"}, at)
}

// errSum builds a validated error summary as a pointer (the contract's
// Error fields are pointers).
func errSum(code, message string, at time.Time) *run.ErrorSummary {
	e := run.NewErrorSummary(code, message, at)
	return &e
}
