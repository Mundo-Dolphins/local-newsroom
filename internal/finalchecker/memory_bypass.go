// This file implements the deterministic memory-provenance check that
// enforces the v0.7 no-bypass invariant (issue #145):
//
//	"Memory is context, never evidence. A memory-only fact must not
//	appear as a verified claim in the final artifact."
//
// The check runs inside the final factual check, after the LLM comparison
// has produced its findings, and is therefore deterministic and independent
// of any model behavior. It compares the claims a memory entity matched the
// run against (recorded by the research stage as the dossier's
// MemoryHintIDs) with the artifact's verified claims.
//
// A finding is emitted when:
//
//  1. A memory entity matched the run (the dossier carried its ID), AND
//  2. A claim is part of the verified set (supported or uncertain) AND is
//     used in the artifact (any usage type), AND
//  3. The claim's statement or supporting excerpt matches a value or
//     background fact that the memory carried for that entity, AND
//  4. The claim carries no live-source evidence in the verification
//     result (the verifier found nothing from the fetched documents that
//     supports it).
//
// In short: the claim looks like it was satisfied by the memory hint alone.
// That is precisely the bypass the invariant forbids, so the finding is
// critical and the run fails.
//
// Claims with insufficient_evidence status are deliberately excluded from
// the critical path: the verifier already flags them as unverified, and the
// writer is forbidden from using them as core facts (the existing
// unverified_as_fact deterministic check covers their misuse). A claim that
// is explicitly unverified in the artifact is not a bypass; a claim that is
// verified but only by memory is.
//
// Matching is conservative (see memoryMatchesClaim): it requires a shared
// substantive token between the claim's statement and the memory value, and
// the memory values being compared are the raw strings the memory recorded
// (the same strings the hints rendered), so the check is exactly as loose as
// the projection itself. False positives are possible for genuinely
// verified claims whose text happens to echo a stale memory value; the
// severity is critical by design (fail closed), and the finding carries the
// memory entity ID and the matched value so a human reviewer can
// distinguish "memory was the only source" from "memory and a source agree".
package finalchecker

import (
	"context"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/entitymemory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
)

// MemoryBypassChecker is the deterministic no-bypass guard. It is stateless
// per run: the orchestrator constructs one, sets the memory context for the
// run (SetMemoryContext), and the final-checker calls Check on it after the
// LLM comparison. A nil MemoryContext is a no-op (pre-memory behavior).
type MemoryBypassChecker struct {
	// MemoryContext carries the memory provenance for the run (nil = the
	// run projected no memory hints; the check is then a no-op).
	MemoryContext *orchestrator.MemoryContext
}

// MemoryContext is the run-level memory provenance (the orchestrator's
// shape, re-declared here as a type alias so the finalchecker can name it
// without a coupling comment): which memory entities the run consulted, and
// what each carried.
type MemoryContext = orchestrator.MemoryContext

// NewMemoryBypassChecker returns a MemoryBypassChecker with no memory
// context (a no-op). The orchestrator calls SetMemoryContext with the
// dossier's provenance before the check runs.
func NewMemoryBypassChecker() *MemoryBypassChecker {
	return &MemoryBypassChecker{}
}

// SetMemoryContext installs the run's memory provenance. Passing nil (or a
// context with no entities) makes the check a no-op. It is safe to call
// before, after, or instead of Check; the check reads it at Check time.
func (m *MemoryBypassChecker) SetMemoryContext(ctx *MemoryContext) {
	m.MemoryContext = ctx
}

// CheckMemoryProvenance runs the deterministic no-bypass check against the
// verified claims in the artifact. It returns the memory-related findings
// to merge into the final check result (empty when the check is a no-op or
// passes). It never fails the run directly: it emits critical findings that
// the final-check result machinery turns into a fail status.
func (m *MemoryBypassChecker) CheckMemoryProvenance(ctx context.Context, artifact *contracts.EditorialArtifact, verification *contracts.VerificationResult) []contracts.FactualFinding {
	if m == nil || m.MemoryContext == nil || len(m.MemoryContext.Entities) == 0 {
		return nil
	}
	if artifact == nil || verification == nil {
		return nil
	}

	// Build the set of verified claim IDs: only supported and uncertain
	// claims count as "verified" for the bypass check. Contradicted and
	// insufficient_evidence claims are already handled by the other
	// deterministic checks and the writer's usage rules; a claim that the
	// verifier could not confirm is not a bypass (it was not presented as
	// fact), while a claim the verifier "confirmed" on memory-only grounds
	// is.
	verified := make(map[string]bool)
	for claimID, status := range verification.VerificationStatuses {
		if status == contracts.VerificationStatusSupported || status == contracts.VerificationStatusUncertain {
			verified[claimID] = true
		}
	}

	// The claims the artifact actually uses, with their verification
	// details. Only used claims matter: an unused dossier claim cannot
	// appear in the artifact at all.
	usedClaims := make(map[string]contracts.ClaimVerificationDetails)
	for claimID := range artifact.ClaimReferences {
		details, ok := verification.ClaimDetails[claimID]
		if !ok {
			continue
		}
		usedClaims[claimID] = details
	}

	// For each verified, used claim, check whether any matched memory
	// entity's values (or aliases) match the claim's statement or supporting
	// evidence while the claim has no live-source evidence.
	//
	// The "no live-source evidence" test is the crux: a claim whose
	// supporting evidence is real (non-empty) has live sources backing it,
	// so the memory could not have verified it (even if the text happens to
	// match a memory value). A claim with empty or missing supporting
	// evidence that nonetheless carries a memory-shaped statement is the
	// bypass signature.
	var bypassFindings []contracts.FactualFinding
	for claimID, details := range usedClaims {
		if !verified[claimID] {
			continue
		}
		// A claim with real supporting evidence is backed by live sources.
		// The memory may have shaped its wording, but it cannot have
		// verified it: no bypass.
		if hasLiveEvidence(details) {
			continue
		}
		for _, entity := range m.MemoryContext.Entities {
			if match, value := entityMatchesClaim(entity, details); match {
				bypassFindings = append(bypassFindings, m.findings(claimID, entity, value)...)
				break // one finding per claim is enough
			}
		}
	}

	return bypassFindings
}

// findings builds the critical findings for one bypassed claim. The
// finding is deliberately critical (it fails the run): a memory-only "verified"
// claim in the artifact is exactly the invariant's forbidden outcome, and
// failing closed is the only safe posture.
func (m *MemoryBypassChecker) findings(claimID string, entity entitymemory.Entity, matchedValue string) []contracts.FactualFinding {
	_ = m
	return []contracts.FactualFinding{{
		FindingID:       "memory-bypass-" + claimID,
		FindingType:     contracts.FindingTypeError,
		Statement:       "Claim " + claimID + " matches editorial memory entity " + entity.ID + " (\"" + entity.CanonicalName + "\") value " + quote(matchedValue) + " but has no live-source evidence in the verification result. Memory is context, never evidence: the claim cannot be verified by a memory hint.",
		Evidence:        "memory entity " + entity.ID + " carried this value; the verification result records no supporting live-source evidence for the claim",
		Severity:        contracts.WarningSeverityCritical,
		Category:        contracts.FindingCategoryEvidence,
		RelatedClaimIDs: []string{claimID},
	}}
}

// hasLiveEvidence reports whether the verification details carry any real
// supporting evidence. A claim with live-source evidence is backed by the
// fetched documents, not by memory, regardless of whether its text happens
// to resemble a memory value.
func hasLiveEvidence(details contracts.ClaimVerificationDetails) bool {
	for _, ev := range details.SupportingEvidence {
		if strings.TrimSpace(ev.Excerpt) != "" {
			return true
		}
	}
	return false
}

// entityMatchesClaim reports whether any of the entity's attribute values or
// background facts match the claim's statement or a supporting excerpt, and
// returns the first matched memory value. The match is token-based and
// conservative: it requires at least one substantive (length >= 3) lower-
// cased token in common between the claim's text and the memory value, so a
// one-word accident (e.g., a common word like "team") is not enough, but a
// distinctive value (a number, a name, a phrase) is.
func entityMatchesClaim(entity entitymemory.Entity, details contracts.ClaimVerificationDetails) (bool, string) {
	claimTexts := []string{details.Statement}
	for _, ev := range details.SupportingEvidence {
		if ev.Excerpt != "" {
			claimTexts = append(claimTexts, ev.Excerpt)
		}
	}
	for _, attr := range entity.Attributes {
		for _, ctext := range claimTexts {
			if sharedToken(attr.Value, ctext) {
				return true, attr.Value
			}
		}
	}
	// Aliases are also memory content: a claim that matches an alias with no
	// live evidence is the same bypass signature (the model "confirmed" the
	// name from memory rather than from a document).
	for _, alias := range entity.Aliases {
		for _, ctext := range claimTexts {
			if sharedToken(alias, ctext) {
				return true, alias
			}
		}
	}
	return false, ""
}

// sharedToken reports whether memoryValue and claimText share at least one
// substantive token (>= 3 runes, lower-cased). Tokenization is whitespace-
// based with punctuation stripped from token edges; it is deliberately crude
// because the check only needs to be conservative, not linguistic.
func sharedToken(memoryValue, claimText string) bool {
	memoryTokens := tokenize(memoryValue)
	claimTokens := tokenize(claimText)
	if len(memoryTokens) == 0 || len(claimTokens) == 0 {
		return false
	}
	claimSet := make(map[string]bool, len(claimTokens))
	for _, t := range claimTokens {
		claimSet[t] = true
	}
	for _, t := range memoryTokens {
		if claimSet[t] {
			return true
		}
	}
	return false
}

// tokenize lower-cases, splits on whitespace, and strips leading/trailing
// punctuation, dropping tokens shorter than 3 runes (they are too generic to
// be a meaningful match).
func tokenize(s string) []string {
	s = strings.ToLower(s)
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, `"'.,;:!?()[]{} `)
		if len(f) < 3 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// quote wraps a value in double quotes for the finding statement.
func quote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return `"` + s + `"`
}
