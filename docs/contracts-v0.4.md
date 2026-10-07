# Contracts v0.4 — Generic Pipeline Stages

This document describes the v0.4 contracts that define the interfaces between pipeline stages: **Researcher → Verifier → Writer → Factual Checker**.

## Overview

The contracts package (`internal/contracts`) introduces three core contracts that are:

- **Format-neutral**: No Bluesky/Hugo/rendering specifics
- **Serializable**: All types implement JSON serialization
- **Provenance-preserving**: Claim IDs tracked from ResearchDossier through all stages
- **Explicitly enumerated**: Status fields use enums, not free-form strings
- **Testable**: Each contract enables independent unit testing

## Pipeline Flow

```
Researcher ──→ ResearchDossier ──→ Verifier ──→ VerificationResult ──→
                                                              │
Writer ──→ EditorialArtifact ──→ Factual Checker ──→ FactualCheckResult ──→
```

## Contract 1: VerificationResult

**Purpose**: Verifier output that assesses each claim's verifiability.

### Key Fields

| Field | Type | Description |
|-------|------|-------------|
| `stable_id` | string | Unique ID for this verification run |
| `input_dossier_id` | string | References the ResearchDossier.stable_id |
| `verification_statuses` | map[string]VerificationStatus | Claim → status mapping |
| `claim_details` | map[string]ClaimVerificationDetails | Detailed findings per claim |
| `contradictions` | []VerifiedContradiction | Contradictions discovered/confirmed |
| `quality_score` | float64 | 0-100 score for verification quality |

### Verification Status Enum

```go
const (
    VerificationStatusSupported              VerificationStatus = "supported"              // Confirmed by reliable sources
    VerificationStatusContradicted           VerificationStatus = "contradicted"           // Evidence shows claim is false
    VerificationStatusUncertain              VerificationStatus = "uncertain"              // Evidence is inconclusive/conflicting
    VerificationStatusInsufficientEvidence   VerificationStatus = "insufficient_evidence"  // Could not verify due to lack of evidence
)
```

### State Management

- **Supported**: Multiple reliable sources confirm the claim
- **Contradicted**: Evidence shows claim is false or misleading
- **Uncertain**: Evidence is inconclusive or conflicting
- **InsufficientEvidence**: Claim could not be verified

**Support for contradiction/uncertainty**: Explicitly represented via `VerificationStatus` enum and `VerifiedContradiction` type. Each claim's `ClaimVerificationDetails` includes `supporting_evidence` and `conflicting_evidence` lists.

### Provenance Model

- Claim IDs come directly from `ResearchDossier.Claims[].ID`
- Evidence references use `Source.stable_id` from the dossier
- `VerificationResult.StableID` enables reverse lookup to original research

## Contract 2: EditorialArtifact

**Purpose**: Writer output that transforms verified claims into content.

### Key Fields

| Field | Type | Description |
|-------|------|-------------|
| `stable_id` | string | Unique ID for this artifact |
| `artifact_type` | ArtifactType | article, thread, or hybrid |
| `title` | string | Headline (when applicable) |
| `body` | string | Main article content |
| `sections` | []Section | Ordered sections for long-form |
| `posts` | []Post | Ordered posts for threads |
| `claim_references` | map[string]ClaimUsage | Claim → usage mapping |
| `source_references` | map[string]SourceUsage | Source → usage mapping |
| `warnings` | []Warning | Limitations and cautions |

### Artifact Type Enum

```go
const (
    ArtifactTypeArticle ArtifactType = "article"    // Single body with sections
    ArtifactTypeThread  ArtifactType = "thread"     // Ordered posts only
    ArtifactTypeHybrid  ArtifactType = "hybrid"     // Both body/sections AND posts
)
```

### Usage Type Enum

```go
const (
    ClaimUsageCore       ClaimUsageType = "core"       // Central point
    ClaimUsageSupporting ClaimUsageType = "supporting" // Supports another point
    ClaimUsageBackground ClaimUsageType = "background" // Provides context
    ClaimUsageCounterpoint ClaimUsageType = "counterpoint" // Alternative view
)
```

### Format Neutrality

The contract does NOT include:

- Bluesky character limits or thread structure
- Hugo front matter (date, tags, categories)
- Mundo Dolphins writing style guidelines
- Publication API formats

Instead:

- `ArtifactType` indicates format generically
- `Sections` and `Posts` are ordered lists
- `Body` is plain text (markup-free)
- Claims are tracked by ID, not by content

### Support for Long-form and Threads

**Long-form article**:
```go
{
    ArtifactType: ArtifactTypeArticle,
    Body: "Article content",
    Sections: []Section{
        {Order: 0, Title: "Introduction", Body: "..."},
        {Order: 1, Title: "Main", Body: "..."},
    },
}
```

**Multi-post thread**:
```go
{
    ArtifactType: ArtifactTypeThread,
    Posts: []Post{
        {Order: 0, Body: "Post 1", ClaimIDs: [...]},
        {Order: 1, Body: "Post 2", ClaimIDs: [...]},
    },
}
```

**Hybrid**:
```go
{
    ArtifactType: ArtifactTypeHybrid,
    Body: "Summary article",
    Sections: [...],
    Posts: [...] // Tweet thread version
}
```

### Provenance Preservation

- `GenerationMetadata.input_verification_id` references the `VerificationResult`
- `ClaimReferences` uses claim IDs from `VerificationResult.ClaimDetails`
- `SourceReferences` uses source IDs from evidence chains

## Contract 3: FactualCheckResult

**Purpose**: Final gate before publication. Verifies the artifact against verified claims.

### Key Fields

| Field | Type | Description |
|-------|------|-------------|
| `stable_id` | string | Unique ID for this check |
| `input_artifact_id` | string | References EditorialArtifact.stable_id |
| `claim_ids` | []string | Claims referenced in artifact |
| `claim_verification_status` | map[string]VerificationStatus | Claim → verification |
| `unsupported_statements` | []UnsupportedStatement | Unverified factual claims |
| `altered_details` | []AlteredDetail | Numbers/dates/names differ from source |
| `overall_status` | FactualCheckStatus | pass, fail, or review |
| `recommendations` | []Recommendation | Actionable fixes |

### Overall Status Enum

```go
const (
    FactualCheckStatusPass  FactualCheckStatus = "pass"   // Can publish
    FactualCheckStatusFail  FactualCheckStatus = "fail"   // Block publication
    FactualCheckStatusReview FactualCheckStatus = "review" // Human review recommended
)
```

### Unsupported Statement Type Enum

```go
const (
    UnsupportedStatementNewFacts     UnsupportedStatementType = "new_facts"
    UnsupportedStatementInference    UnsupportedStatementType = "inference"
    UnsupportedStatementAssumption   UnsupportedStatementType = "assumption"
    UnsupportedStatementOpinion      UnsupportedStatementType = "opinion"
    UnsupportedStatementConjecture   UnsupportedStatementType = "conjecture"
)
```

### Altered Detail Type Enum

```go
const (
    AlteredDetailTypeNumber  AlteredDetailType = "number"
    AlteredDetailTypeDate    AlteredDetailType = "date"
    AlteredDetailTypeName    AlteredDetailType = "name"
    AlteredDetailTypePercentage AlteredDetailType = "percentage"
    AlteredDetailTypeRatio   AlteredDetailType = "ratio"
)
```

### Actionable Findings

The contract includes `Recommendation` with:

- `recommendation_type`: fix, remove, add, clarify, verify, review
- `priority`: low, medium, high, critical
- `effort_estimate`: quick, moderate, substantial, significant
- `related_finding_ids`: Links to specific findings

### Pass Requirements

A result passes if there are **no**:

- Critical/high unsupported statements
- Critical altered details
- Critical error findings

This ensures that only well-verified content proceeds to publication.

## Deliberate Omissions

The following are intentionally NOT in v0.4 contracts:

| Omission | Reason | Where it Lives |
|----------|--------|----------------|
| Bluesky character limits | Format-specific | Writer implementation |
| Hugo front matter | Format-specific | Hugo adapter |
| Mundo Dolphins writing style | Brand-specific | Writer prompts |
| Publication APIs | Integration-specific | Pipeline orchestration |
| LLM prompts | Implementation-specific | prompts/ directory |
| Token budgets | Cost-specific | Writer config |
| Thread collapse rules | Format-specific | Bluesky adapter |

These will be handled by:

1. **Prompt layer** (LLM generation parameters)
2. **Adapter layer** (Format-specific serialization)
3. **Orchestrator layer** (Pipeline workflow)

## Implementation Notes

### Validation

All three contracts have `Validate()` methods that check:

- ID uniqueness within collections
- Referential integrity (claim IDs exist, sources exist)
- Enum validity (status values are known)
- Data consistency (token counts add up, etc.)

### Serialization

All types are JSON-serializable with:

- Explicit type names (not bare strings)
- CamelCase JSON keys via struct tags
- Pointer types for optional fields where needed

### Test Coverage

- 79.1% code coverage for `internal/contracts`
- 85+ test cases covering:
  - Valid struct creation
  - Validation failures
  - JSON serialization
  - Provenance chain (full pipeline simulation)
  - Edge cases (empty collections, invalid states)

## Migration Path

To move from research to publication:

```go
// 1. Researcher outputs ResearchDossier
dossier := researcher.ResearchDossier{...}
dossier.Validate() // Ensures claim-to-source integrity

// 2. Verifier produces VerificationResult
verifyResult := Verifier{...}.Verify(dossier)
verifyResult.Validate() // Ensures status consistency

// 3. Writer produces EditorialArtifact
artifact := Writer{...}.Write(verifyResult)
artifact.Validate() // Ensures claim references exist

// 4. Factual Checker produces FactualCheckResult
checkResult := FactualChecker{...}.Check(artifact, verifyResult)
checkResult.Validate()
if checkResult.OverallStatus == contracts.FactualCheckStatusFail {
    // Block publication, return recommendations
}
```

## Summary

The v0.4 contracts provide:

| Property | Status |
|----------|--------|
| Format-neutral | ✓ No Bluesky/Hugo coupling |
| Serializable | ✓ JSON-serializable with tests |
| Explicit enums | ✓ No free-form status strings |
| Provenance | ✓ Claim IDs tracked end-to-end |
| Uncertainty support | ✓ Contradicted/Uncertain/Insufficient states |
| Testability | ✓ 79.1% coverage, independent tests |
| Long-form + threads | ✓ Three artifact types |
| Format validation | ✓ All contracts have Validate() |
