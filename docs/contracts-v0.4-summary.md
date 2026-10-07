# Verifier, Writer, and Factual-Check Contracts - v0.4

## Overview

This document summarizes the generic v0.4 contracts for the Verifier, Writer, and final factual-check stages. These contracts provide clear handoff between validated ResearchDossier to editorial output without coupling to Bluesky, Hugo, or Mundo Dolphins-specific style.

## State Enums

### VerificationResult Statuses

| Enum | Description |
|------|-------------|
| `VerificationStatusSupported` | Claim is confirmed by multiple reliable sources |
| `VerificationStatusContradicted` | Evidence shows claim is false or misleading |
| `VerificationStatusUncertain` | Evidence is inconclusive or conflicting |
| `VerificationStatusInsufficientEvidence` | Claim could not be verified |

### EditorialArtifact Types

| Type | Description |
|------|-------------|
| `ArtifactTypeArticle` | Single long-form body with sections |
| `ArtifactTypeThread` | Ordered list of short-form posts |
| `ArtifactTypeHybrid` | Both body and posts (e.g., summary + thread) |

### Warning Severities

| Severity | Description |
|----------|-------------|
| `WarningSeverityLow` | Minor concern that doesn't block publication |
| `WarningSeverityMedium` | Moderate concern that should be addressed |
| `WarningSeverityHigh` | Significant concern requiring resolution |
| `WarningSeverityCritical` | Blocks publication until resolved |

### FactualCheckResult Statuses

| Status | Description |
|--------|-------------|
| `FactualCheckStatusPass` | All factual claims verified and supported |
| `FactualCheckStatusFail` | Critical factual issues block publication |
| `FactualCheckStatusReview` | Non-critical issues; human review recommended |

### Claim Usage Types

| Type | Description |
|------|-------------|
| `ClaimUsageCore` | Claim is a central point |
| `ClaimUsageSupporting` | Claim supports another point |
| `ClaimUsageBackground` | Claim provides context |
| `ClaimUsageCounterpoint` | Claim presents alternative view |

### Unsupported Statement Types

| Type | Description |
|------|-------------|
| `UnsupportedStatementNewFacts` | New facts not in verified claims |
| `UnsupportedStatementInference` | Inference not supported |
| `UnsupportedStatementAssumption` | Assumption presented as fact |
| `UnsupportedStatementOpinion` | Opinion presented as fact |
| `UnsupportedStatementConjecture` | Speculation presented as fact |

### Confidence Levels

| Level | Description |
|-------|-------------|
| `ConfidenceHigh` | Strong confidence in assessment |
| `ConfidenceMedium` | Moderate confidence |
| `ConfidenceLow` | Low confidence |

## Provenance Model

### Pipeline Chain

```
ResearchDossier → VerificationResult → EditorialArtifact → FactualCheckResult
```

### Key Provenance Fields

1. **StableIDs**: Every major entity has a stable ID for tracking:
   - `ResearchDossier.stable_id`
   - `VerificationResult.stable_id`
   - `EditorialArtifact.stable_id`
   - `FactualCheckResult.stable_id`

2. **Input References**: Each stage references its input:
   - `VerificationResult.input_dossier_id` → `ResearchDossier.stable_id`
   - `GenerationMetadata.input_verification_id` → `VerificationResult.stable_id`
   - `FactualCheckResult.input_artifact_id` → `EditorialArtifact.stable_id`

3. **Claim Tracing**: All claims are traced through the pipeline:
   - `VerificationResult.VerificationStatuses[claim_id]`
   - `EditorialArtifact.ClaimReferences[claim_id]`
   - `FactualCheckResult.ClaimVerificationStatus[claim_id]`

4. **Evidence References**: Sources are tracked:
   - `ClaimVerificationDetails.SupportingEvidence[].source_id`
   - `EditorialArtifact.SourceReferences[source_id]`

5. **Location Tracking**: Claims are tracked by location in content:
   - `ClaimUsage.Locations[]` with `LocationType`, `SectionName`, `PostIndex`
   - `Section.ClaimIDs[]`
   - `Post.ClaimIDs[]`

## Deliberate Omissions

### Not in Contracts

The following are deliberately excluded from the contracts package to maintain format neutrality:

1. **Publication APIs**: No Bluesky, Twitter/X, or other social platform specific types
2. **Hugo Front Matter**: No YAML front matter structures
3. **Editorial Style**: No Mundo Dolphins-specific formatting or voice
4. **LLM Prompts**: No prompt templates or generation logic
5. **Character Limits**: No platform-specific character limit enforcement
6. **Formatting Logic**: No HTML, Markdown, or rich text rendering

### Implementation Notes

1. **ConfidenceLevel Standardization**: `ConfidenceLevel` is now defined in `contracts/types.go` and used consistently across `VerificationResult` and `FactualCheckResult`.

2. **Claim ID Tracking in UnsupportedStatements**: Added `ClaimIDs` field to `UnsupportedStatement` to enable better traceability.

3. **Serialization**: All contracts implement JSON serialization with explicit struct tags.

## Coverage Summary

| File | Coverage |
|------|----------|
| `verification_result.go` | 71-100% per method |
| `editorial_artifact.go` | 84-100% per method |
| `factual_check_result.go` | 67-100% per method |
| `types.go` | 90-100% per method |
| **Overall** | **91.4%** |

## Files Modified

1. `internal/contracts/verification_result.go`
   - Standardized `ConfidenceLevel` type
   - Added `EvidenceReference.OriginalClaimID` field

2. `internal/contracts/editorial_artifact.go`
   - All methods already well-tested

3. `internal/contracts/factual_check_result.go`
   - Added `ClaimIDs` to `UnsupportedStatement`
   - Standardized `ConfidenceLevel` type (removed duplicate)
   - Updated `GetUnsupportedClaimIDs()` implementation

4. `internal/contracts/types.go`
   - Added `ConfidenceLevel` type with `IsSatisfied()` method
   - Added `String()` method for `ConfidenceLevel`

5. `internal/contracts/contracts_test.go`
   - Added tests for all previously uncovered methods
   - Updated tests to use standard `ConfidenceLevel`

## Test Results

```bash
$ make check
ok      ... (all packages pass)
+ golangci-lint: 0 issues
+ gosec: No security issues found

$ make coverage
ok      github.com/Mundo-Dolphins/local-newsroom/internal/contracts
coverage: 91.4% of statements
```

## Acceptance Criteria Status

| Criteria | Status |
|----------|--------|
| Core verification/editorial contracts exist and are documented | ✅ |
| Contracts support contradictory/uncertain findings | ✅ |
| Artifact contract can represent long-form and multi-post | ✅ |
| Serialization tests exist | ✅ |
| `make check` passes | ✅ |
| `make coverage` passes (91.4% > 80% target) | ✅ |

## Design Principles Enforced

1. **Format-neutral**: No Bluesky/Hugo/rendering details in core types
2. **Serializable**: All types implement JSON serialization
3. **Explicit enums**: Status fields use explicit enum types, not free-form strings
4. **Provenance preservation**: Dossier claim IDs tracked through all stages
5. **Uncertainty support**: Contradictory/uncertain findings explicitly representable
6. **Testability**: Each stage's contracts enable independent unit testing
