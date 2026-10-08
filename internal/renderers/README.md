# Renderers Package

Deterministic renderers for `EditorialArtifact` that do not require LLM calls.

## Overview

This package provides two renderers:

1. **MarkdownRenderer** - Renders `EditorialArtifact` to Markdown format
2. **ThreadRenderer** - Renders `EditorialArtifact` to a Bluesky thread (slice of posts)

Both renderers are fully deterministic - given the same input, they produce identical output.

## Markdown Article Renderer

### Features

- Title and subtitle support
- Body content with multiple paragraphs
- Section support with headers
- Optional sources/references section
- Optional warnings section
- Optional metadata header (YAML-style front matter)
- Deterministic sorting of sections, posts, sources, and claim references

### Configuration

```go
renderer := NewMarkdownRenderer()
renderer.SetIncludeSourceSection(true)
renderer.SetSourceSectionTitle("References")
renderer.SetIncludeMetadataHeader(true)
```

### Output Format

```markdown
---
verification_id: v-001
generated_at: 2024-01-15T10:00:00Z
---

# Article Title

Subtitle (optional)

Main body content with multiple paragraphs.

## Section Title

Section content here.

## Sources

- Source citation 1
- Source citation 2
```

### Splitting Rules

The Markdown renderer does not split content - it renders the entire artifact as-is. Sections are sorted by their `Order` field for deterministic output.

## Bluesky Thread Renderer

### Features

- Deterministic splitting to ≤300 characters per post
- Claim reference tracking (optional)
- Natural boundary splitting (paragraphs, sentences, words)
- Preserves order of sections/posts
- Fails clearly when content cannot be split

### Configuration

```go
renderer := NewThreadRenderer()
renderer.SetMaxCharsPerPost(300)
renderer.SetIncludeClaimReferences(true)
renderer.SetClaimRefFormat("[CIDs: %s]")
```

### Output Format

Returns a slice of strings, where each string is a Bluesky post:

```
[
  "First post content [CIDs: claim-1, claim-2]",
  "Second post content with more text",
  "Final post [CIDs: claim-3]"
]
```

### Splitting Rules

The renderer attempts to split content in this priority order:

1. **Paragraph boundaries** - Split by blank lines first
2. **Sentence boundaries** - Split by `.`, `!`, `?` (excluding `...`)
3. **Word boundaries** - Split by commas, semicolons, hyphens, parentheses
4. **Word character boundaries** - Split at any character position if necessary

### Natural Boundary Detection

The renderer detects common abbreviations to avoid splitting on them:
- Titles: Dr., Mr., Mrs., Ms., Prof., Sr., Jr.
- Latin: etc., e.g., i.e., vs., al., et al., cf.
- Measurements: Fig., fig., Vol., vol., pp., p., No., no.
- Organizations: Inc., Ltd., Co., Corp.
- Street: St., Ave., Blvd., Rd.
- Months: Jan., Feb., Mar., Apr., Jun., Jul., Aug., Sep., Sept., Oct., Nov., Dec.

### Claim References

When `IncludeClaimReferences` is enabled, the renderer appends claim references to each post:

- Format: `[CIDs: claim-1,claim-2,claim-3]`
- Claim IDs are sorted alphabetically for determinism
- If a post exceeds the limit even without claim refs, the claim ref is omitted from that post

### Character Count Enforcement

- **Always** checks if content fits before adding to a post
- **Never** truncates content silently
- **Fails clearly** with `IndivisibleSegmentError` when content cannot be split
- The `RendererError` wraps all errors with `Deterministic: true` indicating this is expected input validation, not an LLM issue

### Error Handling

The renderer returns clear errors when:

1. **Invalid artifact** - `Validate()` fails
2. **Unsupported artifact type** - Not Article, Thread, or Hybrid
3. **Indivisible segment** - Content cannot be split to fit within 300 chars
4. **Nil artifact** - Render called with nil

```go
if IsRendererError(err) {
    // Recoverable error - check RendererError.Deterministic
}
if IsIndivisibleSegmentError(err) {
    // Content cannot be split - requires manual intervention
}
```

### Edge Cases

| Case | Behavior |
|------|----------|
| Empty body | Returns nil/empty slice |
| Only title | Title is included in first post |
| Very long single word | May error if word > 300 chars |
| URL in content | URLs preserved, may cause split at other boundaries |
| Unicode/emoji | Characters counted as-is (UTF-8 runes) |
| Abbreviations | Not treated as sentence endings |
| Multiple consecutive spaces | Normalized to single space |
| Sections without body | Still rendered in order |

### Ordering Guarantee

Sections and posts are always rendered in their `Order` field order (0-indexed), not slice order. This ensures:

```go
// Even if slices are defined out of order:
Sections: []Section{
    {Order: 2, ...},  // Third
    {Order: 0, ...},  // First
    {Order: 1, ...},  // Second
}

// Output will always be: First, Second, Third
```

## Usage Examples

### Basic Markdown Rendering

```go
artifact := &contracts.EditorialArtifact{
    // ... artifact data ...
}

renderer := NewMarkdownRenderer()
markdown, err := renderer.Render(artifact)
if err != nil {
    log.Fatal(err)
}
```

### Basic Thread Rendering

```go
artifact := &contracts.EditorialArtifact{
    // ... artifact data ...
}

renderer := NewThreadRenderer().SetIncludeClaimReferences(true)
posts, err := renderer.Render(artifact)
if err != nil {
    log.Fatal(err)
}

for i, post := range posts {
    fmt.Printf("Post %d (%d chars):\n%s\n\n", i, len(post), post)
}
```

### Hybrid Artifact (Article + Thread)

```go
// Hybrid artifacts can have both Body/Sections AND Posts
artifact := &contracts.EditorialArtifact{
    ArtifactType: contracts.ArtifactTypeHybrid,
    Body: "Summary body",
    Sections: []contracts.Section{
        {Order: 0, Title: "Details", Body: "More details"},
    },
    Posts: []contracts.Post{
        {Order: 0, Body: "Thread post 1"},
        {Order: 1, Body: "Thread post 2"},
    },
}

// Thread renderer renders from Posts
posts, _ := renderer.Render(artifact)
```

## Testing

```bash
# Run all renderer tests
go test -v ./internal/renderers/...

# Run with coverage
go test -cover ./internal/renderers/...

# Run specific test
go test -v -run TestThreadRenderer_BlueskyExactly300Chars ./internal/renderers/...
```

## Determinism Verification

All tests include deterministic output checks - rendering the same artifact multiple times produces identical results.

```go
// All results should be identical for the same input
for i := 0; i < 5; i++ {
    result, _ := renderer.Render(artifact)
    // assert all results match
}
```

## Out of Scope

These features are intentionally not included:

- Hugo-specific front matter
- HTML rendering
- Image/video embeds
- Bluesky API publication
- Citation formatting beyond simple source references
- Automatic fact-checking or content verification
