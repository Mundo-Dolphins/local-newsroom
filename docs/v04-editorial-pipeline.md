# v0.4 Editorial Pipeline Documentation

## Overview

v0.4 introduces a complete offline end-to-end editorial pipeline that runs deterministically without requiring external LLM services, oMLX, SearXNG, or live web access. This documentation describes the architecture, stage boundaries, and implementation details.

## E2E Pipeline Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              COMPLETE OFFLINE PIPELINE                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ResearchDossier ──→ Fake Verifier LLM ──→ VerificationResult               │
│                                            │                               │
│                                            ▼                               │
│                              Optional Bounded Follow-Up (Fake)               │
│                                            │                               │
│                                            ▼                               │
│                               Fake Writer LLM                                │
│                                            │                               │
│                                            ▼                               │
│                              EditorialArtifact                               │
│                                            │                               │
│                                            ▼                               │
│                          Deterministic + Fake-LLM Final Checker              │
│                                            │                               │
│                                            ▼                               │
│                                    Renderer                                  │
│                                            │                               │
│                    ┌───────────────────────┴───────────────────────┐        │
│                    ▼                                               ▼        │
│              article.md /        Bluesky thread (≤300 chars/post)       │
│                              Markdown format                            │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Stage Responsibilities

### 1. Researcher → ResearchDossier

**Responsibility:** Collect and synthesize evidence into verifiable claims.

**Input:** Query topics, URLs, archive context
**Output:** `ResearchDossier` with claims, sources, contradictions

**Key Types:**
- `ResearchDossier`: Complete research compilation with claims
- `Claim`: Structured claim with statement and supporting evidence
- `SourceReference`: Evidence source with URL, text, excerpt
- `Contradiction`: Documented conflicting evidence

**v0.4 Note:** Research generation remains unchanged from v0.3. No breaking changes.

### 2. Fake Verifier LLM → VerificationResult

**Responsibility:** Assess each claim's verifiability status.

**Input:** `ResearchDossier`
**Output:** `VerificationResult` with status mappings

**Verification Statuses:**
- `supported`: Multiple reliable sources confirm the claim
- `contradicted`: Evidence shows claim is false or misleading
- `uncertain`: Evidence is inconclusive or conflicting
- `insufficient_evidence`: Claim could not be verified

**Quality Score:** 0-100 based on claim verification quality
**Confidence Levels:** `high`, `medium`, `low`

### 3. Optional Bounded Follow-Up Loop

**Responsibility:** Targeted research for unresolved claims.

**Bounded by:**
- `max_rounds`: Default 1 additional round
- `max_queries_per_claim`: Default 3
- `max_queries_per_round`: Default 10
- `max_sources_per_round`: Default 20
- `source_budget`: Default 100

**When Triggers:** Only claims with `uncertain`, `insufficient_evidence`, or `contradicted` status trigger follow-up. Supported claims never searched.

**v0.4 Implementation:** Fully deterministic fake implementation for offline E2E testing.

### 4. Fake Writer LLM → EditorialArtifact

**Responsibility:** Transform verified claims into publication-ready content.

**Input:** `VerificationResult`
**Output:** `EditorialArtifact` with title, body, sections, posts

**Artifact Types:**
- `article`: Long-form body with sections
- `thread`: Multi-post thread format
- `hybrid`: Both body and posts (e.g., summary + tweet thread)

**Claim Provenance:**
- Each claim tracked via `ClaimReferences`
- Usage type: `core`, `supporting`, `background`, `counterpoint`
- Location tracking: body offsets, section indices, post indices
- Paraphrased flag indicates rewriting occurred

### 5. Deterministic + Fake-LLM Final Checker → FactualCheckResult

**Responsibility:** Gatekeeping verification before publication.

**Input:** `EditorialArtifact`, `VerificationResult`
**Output:** `FactualCheckResult` with pass/fail determination

**Critical Checks:**
- All claimed references exist in `VerificationResult`
- No new facts asserted beyond verified claims
- Numbers, dates, names consistent with source evidence
- No out-of-context usage of source material

**Status Values:**
- `pass`: All factual claims verified, can proceed to publication
- `fail`: Critical issues block publication
- `review`: Non-critical issues need human review

**Altered Details:** Detects changes to numbers, dates, names from source

### 6. Renderer → article.md / Bluesky Thread

**Responsibility:** Format artifacts for consumption.

**Markdown Renderer:**
- Supports article, thread, and hybrid formats
- Preserves sections, sources, warnings
- Deterministic output (sorted lists, stable formatting)

**Thread Renderer:**
- Strict 300-character post limit (Bluesky invariant)
- Sentence-aware splitting
- Unicode character counting
- URL and link preservation
- Claim reference formatting (optional)

**Rendering Options:**
- Include/exclude sources section
- Include claim references in posts
- Custom character limits
- Claim reference format string

## Intermediate Artifact Round-Trips

All artifacts between stages are JSON-serializable:

```
ResearchDossier → ResearchDossierJSON → file → ResearchDossierJSON → ResearchDossier
VerificationResult → VerificationResultJSON → file → VerificationResultJSON → VerificationResult
EditorialArtifact → EditorialArtifactJSON → file → EditorialArtifactJSON → EditorialArtifact
FactualCheckResult → FactualCheckResultJSON → file → FactualCheckResultJSON → FactualCheckResult
```

Round-trip integrity tested and verified:
- All required fields preserved
- Non-nil pointer values maintained
- Map structures preserved
- Time values in RFC3339 format
- Enums as string values

## Profile Model

**Purpose:** Store author/publisher preferences for style, tone, and formatting.

**Directory Structure:**
```
profiles/
  {author-name}/
    common/
      style.md       # Style guide instructions
      tone.md        # Tone preferences
    custom/          # Author-specific overrides
```

**Usage:** Write commands can load profile configuration via `--profile-dir` and `--profile-name`.

**v0.4 Status:** Support added for profile loading; integration with Writer LLM depends on actual LLM implementation.

## Final Factual Checker

### Design Principles

1. **Deterministic:** No randomness; same input always produces same output
2. **Claim-Centric:** Checks based on explicit claim IDs, not vague text matching
3. **Evidence-Aware:** Compares against original source excerpts
4. **Non-Destructive:** Can `allow-failed-output` to continue on non-fatal issues

### Check Categories

1. **Unsupported Statements:** Claims not found in verification
   - `new_facts`: Entirely new assertions
   - `inference`: Logical leaps not in evidence
   - `assumption`: Assumptions presented as facts
   - `opinion`: Opinionated content as fact
   - `conjecture`: Speculation as fact

2. **Altered Details:** Numbers, dates, names changed from source
   - `number`, `date`, `name`, `percentage`, `ratio`
   - Significance: `trivial`, `moderate`, `critical`

3. **Out-of-Context Usage:** Source material misrepresented
   - Missing context
   - Debatable framing
   - Outdated references

### Configuration

```json
{
  "temperature": 0.0,          // Deterministic for testing
  "max_tokens": 4096,
  "language": "en",
  "allow_failed_output": false
}
```

## Article and Bluesky Rendering

### Markdown Renderer

**Format:** Standard Markdown with Hugo-friendly structure

**Components:**
- Title and subtitle (if present)
- Body text (if present)
- Sections (ordered by `order` field)
- Sources section (if enabled)
- Warnings section (if present)

**Example:**
```markdown
# Article Title

Subtitle text.

Body content with sections.

## Section Title

Section body content.

## Sources

- Citation text 1
- Citation text 2

## Warnings

- **[unverified_claim]** Warning message
```

### Thread Renderer (Bluesky)

**Bluesky Max Chars:** 300 characters per post

**Splitting Strategy:**
1. Split by paragraphs first
2. Split by sentences within paragraphs
3. Split by boundaries (`,`, `;`, `:`, `-`) if needed
4. Single words > 300 chars → `IndivisibleSegmentError`

**Character Counting:**
- Unicode-aware (emoji, accented chars count as 1)
- Space normalization before counting
- Newlines replaced with single space

**Claim References:**
- Format: `[CIDs: claim-1,claim-2]` (configurable)
- Appended to each post
- Affects character count

**Example Thread:**
```
This is the first post with content. [CIDs: claim-1]

This is the second post with more content. [CIDs: claim-2, claim-3]

Third post with remaining content. [CIDs: claim-4]
```

## CLI Workflows

### Research Workflow

```bash
newsroom research --topic "Dolphin Conservation" \
  --urls "http://example.org/1 http://example.org/2" \
  --output research-dossier.json
```

### Verification Workflow

```bash
newsroom verify --dossier research-dossier.json \
  --output verification.json
```

### Write Workflow

```bash
newsroom write --dossier verification.json \
  --format article \
  --temperature 0.0 \
  --max-tokens 4096 \
  --output article.json
```

### Final Check Workflow

```bash
newsroom final-check --artifact article.json \
  --verification verification.json \
  --output check.json
```

### Render Workflow

```bash
newsroom render --artifact article.json \
  --format markdown --output article.md
```

```bash
newsroom render --artifact article.json \
  --format thread --output thread.bsky
```

## Persisted Intermediate Artifacts

### Location

Artifacts persisted by default in working directory or output paths specified in command line flags.

### Format

JSON with explicit schema validation before persistence.

### Contents

```json
{
  "stable_id": "artifact-001",
  "artifact_type": "article",
  "title": "Article Title",
  "body": "Body content",
  "sections": [...],
  "posts": [...],
  "claim_references": {
    "claim-id-1": {
      "claim_id": "claim-id-1",
      "usage_type": "core",
      "paraphrased": false,
      "locations": [...]
    }
  },
  "source_references": {...},
  "generation_metadata": {
    "input_verification_id": "ver-001",
    "generated_at": "2024-01-15T12:00:00Z"
  },
  "warnings": [...]
}
```

### Lifecycle

```
1. ResearchDossier saved after research phase
2. VerificationResult saved after verification phase
3. EditorialArtifact saved after write phase
4. FactualCheckResult saved after final check
5. Rendered output saved (markdown/BSKY)
```

## Explicit Non-Goals

### Automatic Publication

**Status:** Not implemented

v0.4 does not include:
- Automatic Hugo blog publishing
- Automatic Bluesky post publishing
- Automatic social media scheduling

Artifacts are rendered and saved as files for manual review and publication.

### Real LLM Integration

**Status:** Offline-first, fake LLMs only

v0.4 uses deterministic fake LLM implementations for all tests:
- No actual API calls to external services
- All "LLM" responses are hardcoded and deterministic
- No oMLX, OpenAI, or other provider dependencies

### Real Search

**Status:** Fake planning only

Search queries are generated deterministically but not actually executed:
- No SearXNG integration
- No actual web searches
- Search results simulated for offline testing

### Live Data Fetching

**Status:** Archive-only during tests

During offline E2E tests:
- No live web fetching
- No RSS feed subscriptions
- No JSON API calls
- Archive is pre-populated or memory-based

## Testing Strategy

### E2E Test Coverage

| Scenario | Test Name | Expected Result |
|----------|-----------|-----------------|
| All claims supported | `TestE2E_v04_PositiveAllClaimsSupported` | Article succeeds |
| Contradiction handling | `TestE2E_v04_ContradictionHandler` | Contradiction documented |
| Uncertain claim | `TestE2E_v04_UncertainClaimQualified` | Claim remains qualified |
| Writer extra fact | `TestE2E_v04_WriterIntroducesExtraFact` | Final checker fails |
| Writer changes stat | `TestE2E_v04_WriterChangesStatistic` | Final checker fails |
| Article rendering | `TestE2E_v04_ArticleRendering` | Valid markdown |
| Bluesky limit | `TestE2E_v04_Bluesky300CharInvariant` | All posts ≤300 chars |
| Profile inheritance | `TestE2E_v04_ProfileInheritance` | Profile loaded |
| Round-trip | `TestE2E_v04_IntermediateArtifactRoundTrip` | JSON round-trip works |
| v0.3 RAG | `TestE2E_v04_v03ResearchRAGUnaffected` | v0.3 unaffected |

### Test Commands

```bash
# Run all v0.4 tests
go test ./tests/e2e_v04_test.go -v

# Run with coverage
go test -coverprofile=coverage-v04.out ./tests/e2e_v04_test.go

# Run all tests
make test
make coverage
```

## v0.4 Limitations

### Known Limitations

1. **No Actual LLM Calls**
   - All tests use fake deterministic responses
   - Real-world behavior untested until v0.5+
   - Prompt engineering not validated

2. **No Live Archive Operations**
   - Tests use memory-based SQLite store
   - No actual web archiving
   - No real-time document updates

3. **No Profile Validation**
   - Profile loading works but not integrated with Writer
   - Style guidance not enforced during generation

4. **No Search Execution**
   - Planner generates queries but doesn't execute them
   - No real search results
   - No actual document fetching

5. **No Publication Pipeline**
   - Artifacts saved as files only
   - No automated publishing workflows
   - No Hugo theme integration

### Future Enhancements (v0.5+)

- Integration with oMLX or other local LLM providers
- Live archive update on research runs
- Real search and fetching for v0.4 E2E tests
- Automated Hugo publishing
- Bluesky API integration (manual approval only)
- Multi-profile support with inheritance

## Version Compatibility

### v0.4 Breaking Changes from v0.3

**None:** v0.4 is fully backward compatible with v0.3.

### v0.3 Features Preserved

- Archive ingestion and storage
- Semantic retrieval with embeddings
- Chunk-based document processing
- Embedding metadata tracking
- RAG context for research
- v0.2 research/RAG compatibility

### New v0.4 Features

- Complete offline E2E testing
- Deterministic fake LLM implementations
- Structured verification results
- Editorial artifact contracts
- Final factual checker
- Renderer modules
- Profile configuration model

## Conclusion

v0.4 provides a comprehensive, testable offline pipeline for editorial content generation. All components are implemented with deterministic fake LLMs, enabling full E2E testing without external dependencies. The architecture supports future integration with real LLM services while maintaining complete offline test coverage.

The explicit non-goals ensure v0.4 focuses on pipeline correctness and artifact quality rather than publication automation, which is reserved for v0.5+.
