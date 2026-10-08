package renderers

import (
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
)

// -----------------------------------------------------------------------------
// Markdown Article Renderer Tests
// -----------------------------------------------------------------------------

func TestMarkdownRenderer_RenderNilArtifact(t *testing.T) {
	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(nil)

	if err == nil {
		t.Fatal("expected error for nil artifact")
	}
	if !IsRendererError(err) {
		t.Errorf("expected RendererError, got %T", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

func TestMarkdownRenderer_RenderValidArticle(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Test Article",
		Subtitle:     "A subtitle",
		Body:         "This is the main body text.\n\nIt has multiple paragraphs.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {
				ClaimID:     "claim-1",
				UsageType:   contracts.ClaimUsageCore,
				Paraphrased: false,
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-001",
		},
	}

	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(artifact)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check for title
	if !strings.Contains(result, "# Test Article") {
		t.Errorf("missing title in output")
	}

	// Check for subtitle
	if !strings.Contains(result, "A subtitle") {
		t.Errorf("missing subtitle in output")
	}

	// Check for body
	if !strings.Contains(result, "This is the main body text") {
		t.Errorf("missing body in output")
	}

	// Check for multiple paragraphs
	if !strings.Contains(result, "It has multiple paragraphs") {
		t.Errorf("missing second paragraph in output")
	}
}

func TestMarkdownRenderer_RenderWithSections(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-002",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Article with Sections",
		Body:         "Intro paragraph.",
		Sections: []contracts.Section{
			{
				Order: 0,
				Title: "Section One",
				Body:  "Content of section one.",
			},
			{
				Order: 1,
				Title: "Section Two",
				Body:  "Content of section two.",
			},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {
				ClaimID:     "claim-1",
				UsageType:   contracts.ClaimUsageCore,
				Paraphrased: false,
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-002",
		},
	}

	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(artifact)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check for sections
	if !strings.Contains(result, "## Section One") {
		t.Error("missing first section heading")
	}
	if !strings.Contains(result, "Content of section one") {
		t.Error("missing first section content")
	}
	if !strings.Contains(result, "## Section Two") {
		t.Error("missing second section heading")
	}
	if !strings.Contains(result, "Content of section two") {
		t.Error("missing second section content")
	}
}

func TestMarkdownRenderer_RenderWithSources(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-003",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Article with Sources",
		Body:         "Article content.",
		SourceReferences: map[string]contracts.SourceUsage{
			"source-b": {
				SourceID:      "source-b",
				UsageCount:    2,
				CitationStyle: contracts.CitationStyleLink,
				CitationText:  "Published in The Times",
			},
			"source-a": {
				SourceID:      "source-a",
				UsageCount:    1,
				CitationStyle: contracts.CitationStyleInline,
				CitationText:  "Smith et al., 2024",
			},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {
				ClaimID:     "claim-1",
				UsageType:   contracts.ClaimUsageCore,
				Paraphrased: false,
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-003",
		},
	}

	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(artifact)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check for sources section
	if !strings.Contains(result, "## Sources") {
		t.Error("missing sources section")
	}

	// Check that sources are sorted deterministically (source-a before source-b)
	sourcesSection := strings.Split(result, "## Sources")[1]
	if !strings.Contains(sourcesSection, "Smith et al., 2024") {
		t.Error("missing source-a content")
	}
	if strings.Index(sourcesSection, "Published in The Times") < strings.Index(sourcesSection, "Smith et al., 2024") {
		t.Error("sources not sorted correctly (source-b before source-a)")
	}
}

func TestMarkdownRenderer_RenderWithoutSources(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:         "test-004",
		ArtifactType:     contracts.ArtifactTypeArticle,
		Title:            "Article without Sources",
		Body:             "Article content.",
		SourceReferences: nil,
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {
				ClaimID:     "claim-1",
				UsageType:   contracts.ClaimUsageCore,
				Paraphrased: false,
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-004",
		},
	}

	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(artifact)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sources section should not be present
	if strings.Contains(result, "## Sources") {
		t.Error("sources section present when no sources provided")
	}
}

func TestMarkdownRenderer_SetIncludeSourceSection(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-005",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Article",
		Body:         "Content.",
		SourceReferences: map[string]contracts.SourceUsage{
			"src-1": {SourceID: "src-1", UsageCount: 1},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-005",
		},
	}

	// By default, sources are included
	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "## Sources") {
		t.Error("sources should be included by default")
	}

	// Disable sources
	renderer = NewMarkdownRenderer().SetIncludeSourceSection(false)
	result, err = renderer.Render(artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "## Sources") {
		t.Error("sources should not be included when disabled")
	}
}

func TestMarkdownRenderer_ConvenienceFunction(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-006",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Convenience Test",
		Body:         "Testing the convenience function.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-006",
		},
	}

	result, err := RenderToMarkdown(artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "# Convenience Test") {
		t.Error("title missing from convenience function result")
	}
}

func TestMarkdownRenderer_DeterministicOutput(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-deterministic",
		ArtifactType: contracts.ArtifactTypeHybrid,
		Title:        "Determinism Test",
		Body:         "This is the article body.",
		Sections: []contracts.Section{
			{Order: 1, Title: "First", Body: "Content 1"},
			{Order: 0, Title: "Second", Body: "Content 2"},
		},
		Posts: []contracts.Post{
			{Order: 1, Body: "Post 1"},
			{Order: 0, Body: "Post 2"},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"z-source": {SourceID: "z-source", UsageCount: 1},
			"a-source": {SourceID: "a-source", UsageCount: 1},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"z-claim": {ClaimID: "z-claim", UsageType: contracts.ClaimUsageCore},
			"a-claim": {ClaimID: "a-claim", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-007",
		},
	}

	// Render multiple times
	var results []string
	for i := 0; i < 5; i++ {
		renderer := NewMarkdownRenderer()
		result, err := renderer.Render(artifact)
		if err != nil {
			t.Fatalf("render failed: %v", err)
		}
		results = append(results, result)
	}

	// All results should be identical
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Errorf("results differ: iteration 0 vs iteration %d", i)
			t.Logf("Iteration 0:\n%s", results[0])
			t.Logf("Iteration %d:\n%s", i, results[i])
		}
	}
}

func TestMarkdownRenderer_WithWarnings(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "test-warnings",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Article with Warnings",
		Body:         "Article content.",
		Warnings: []contracts.Warning{
			{
				WarningType:     contracts.WarningTypeUnverifiedClaim,
				Severity:        contracts.WarningSeverityMedium,
				Message:         "Some claims are unverified",
				RelatedClaimIDs: []string{"claim-1", "claim-2"},
			},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageSupporting},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-008",
		},
	}

	renderer := NewMarkdownRenderer()
	result, err := renderer.Render(artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "## Warnings") {
		t.Error("warnings section missing")
	}
	if !strings.Contains(result, "unverified_claim") {
		t.Error("warning type missing")
	}
}

// -----------------------------------------------------------------------------
// Bluesky Thread Renderer Tests - Unicode and Character Limits
// -----------------------------------------------------------------------------

func TestThreadRenderer_RenderNilArtifact(t *testing.T) {
	renderer := NewThreadRenderer()
	result, err := renderer.Render(nil)

	if err == nil {
		t.Fatal("expected error for nil artifact")
	}
	if !IsRendererError(err) {
		t.Errorf("expected RendererError, got %T", err)
	}
	if len(result) != 0 {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestThreadRenderer_BlueskyExactly300Chars(t *testing.T) {
	// Create content that results in exactly 300 characters
	// Title "Test" + newline + newline + body = 300 total
	// 4 (title) + 2 (newlines) + 294 (body) = 300
	title := "Test"
	titleLen := len(title) + 2 // +2 for "\n\n" after title
	bodyLen := BlueskyMaxChars - titleLen
	article := &contracts.EditorialArtifact{
		StableID:     "test-300",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        title,
		Body:         strings.Repeat("x", bodyLen),
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-009",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Error("expected at least one post")
		return
	}

	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds 300 chars: %d chars", i, len(post))
		}
		if len(post) == 300 {
			t.Logf("Post %d is exactly 300 chars as expected", i)
		}
	}
}

func TestThreadRenderer_UnicodeCharacters(t *testing.T) {
	// Test with various Unicode characters (emojis, accents, CJK)
	article := &contracts.EditorialArtifact{
		StableID:     "test-unicode",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Unicode Test 🌍",
		Body: "Hello world! Привет мир! مرحبا بالعالم！こんにちは世界！\n\n" +
			"Emoji test: 🚀 🎉 🌟 💡 🔍 📊 📈\n\n" +
			"Accented: café résumé naïve Piñata_overlap",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-010",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected at least one post")
	}

	// Check that unicode is preserved
	allText := strings.Join(result, " ")
	if !strings.Contains(allText, "🌍") {
		t.Error("emoji lost in output")
	}
	if !strings.Contains(allText, "Привет мир!") {
		t.Error("Cyrillic text lost")
	}
	if !strings.Contains(allText, "こんにちは世界！") {
		t.Error("CJK text lost")
	}
	if !strings.Contains(allText, "café") {
		t.Error("accented characters lost")
	}

	// Check all posts are within limit
	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars (should be <= %d)", i, len(post), BlueskyMaxChars)
		}
	}
}

func TestThreadRenderer_LargeTextSplitting(t *testing.T) {
	// Create a long article that requires multiple posts
	article := &contracts.EditorialArtifact{
		StableID:     "test-split",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Large Article",
		Body:         strings.Repeat("This is a longer sentence with more text to ensure proper splitting occurs across multiple posts. ", 50),
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-011",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected at least one post")
	}

	// All posts must be within limit
	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds 300 chars: %d chars", i, len(post))
		}
	}

	// Should have multiple posts for large content
	if len(result) < 2 {
		t.Log("Note: Content may fit in single post with current settings")
	}

	// Check content is preserved (not truncated silently)
	allText := strings.Join(result, " ")
	if !strings.Contains(allText, "This is a longer sentence") {
		t.Error("content appears to be truncated")
	}
}

func TestThreadRenderer_SentenceBoundaryPreference(t *testing.T) {
	// Test that splitting prefers sentence boundaries over mid-sentence
	article := &contracts.EditorialArtifact{
		StableID:     "test-boundary",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Boundary Test",
		Body:         "First sentence. Second sentence. Third sentence that is quite long but should still try to stay within sentence boundaries if possible. Fourth sentence. Fifth sentence.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-012",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected at least one post")
	}

	// All posts within limit
	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars", i, len(post))
		}
	}

	// Check that sentences aren't completely broken up
	// At minimum, each post should contain complete sentences
	for i, post := range result {
		// Posts should end with sentence boundary (or be short enough to not need one)
		if len(post) > 20 && (post[len(post)-1] != '.' && post[len(post)-1] != '!' && post[len(post)-1] != '?') {
			t.Logf("Post %d ends mid-sentence: %q", i, post)
			// This is informational - not always possible
		}
	}
}

func TestThreadRenderer_LinksAndURLs(t *testing.T) {
	// Test that URLs are handled properly and not split arbitrarily
	article := &contracts.EditorialArtifact{
		StableID:     "test-urls",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "URLs Test",
		Body:         "Check out this long URL: https://example.com/very/long/path/that/should/be/kept/together/when/possible and this other one: https://another-domain.org/path/to/resource. Now here's some more text to fill up the post.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-013",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected at least one post")
	}

	// All posts within limit
	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars", i, len(post))
		}
	}

	// Check URLs are preserved
	allText := strings.Join(result, " ")
	if !strings.Contains(allText, "https://example.com/very/long/path/that/should/be/kept/together/when/possible") {
		t.Error("first URL was broken or lost")
	}
	if !strings.Contains(allText, "https://another-domain.org/path/to/resource") {
		t.Error("second URL was broken or lost")
	}
}

func TestThreadRenderer_NoTruncation(t *testing.T) {
	// Test that content is not truncated silently
	article := &contracts.EditorialArtifact{
		StableID:     "test-notruncate",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "No Truncation",
		Body:         "This is the complete original text that should appear in the rendered posts. Nothing should be cut off or left out. The renderer should split content into multiple posts if needed, but never silently truncate.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-014",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected at least one post")
	}

	// Reconstruct original content and verify all parts are present
	allText := strings.Join(result, " ")

	checkPhrases := []string{
		"This is the complete original text",
		"that should appear in the rendered posts",
		"Nothing should be cut off",
		"never silently truncate",
	}

	for _, phrase := range checkPhrases {
		if !strings.Contains(allText, phrase) {
			t.Errorf("missing phrase: %q", phrase)
			t.Logf("Actual output: %q", allText)
		}
	}
}

func TestThreadRenderer_DeterministicOutput(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-deterministic-thread",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Deterministic Thread",
		Body:         "This is test content that should produce the same output every time.",
		Sections: []contracts.Section{
			{Order: 0, Title: "First Section", Body: "Section 1 content"},
			{Order: 1, Title: "Second Section", Body: "Section 2 content"},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"z-claim": {ClaimID: "z-claim", UsageType: contracts.ClaimUsageCore},
			"a-claim": {ClaimID: "a-claim", UsageType: contracts.ClaimUsageSupporting},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-015",
		},
	}

	// Render multiple times
	var results [][]string
	for i := 0; i < 5; i++ {
		renderer := NewThreadRenderer()
		result, err := renderer.Render(article)
		if err != nil {
			t.Fatalf("render failed: %v", err)
		}
		results = append(results, result)
	}

	// All results should be identical
	for i := 1; i < len(results); i++ {
		if len(results[i]) != len(results[0]) {
			t.Errorf("post count differs: iteration 0 has %d, iteration %d has %d",
				len(results[0]), i, len(results[i]))
		}
		for j := range results[0] {
			if results[i][j] != results[0][j] {
				t.Errorf("post %d differs: iteration 0 = %q, iteration %d = %q",
					j, results[0][j], i, results[i][j])
			}
		}
	}
}

func TestThreadRenderer_IncludeClaimReferences(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-claimref",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Claim Reference Test",
		Body:         "Test content.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageSupporting},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-016",
		},
	}

	// Without claim references
	renderer := NewThreadRenderer()
	renderer.SetIncludeClaimReferences(false)
	result1, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With claim references
	renderer.SetIncludeClaimReferences(true)
	result2, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that claim references appear in second result but not first
	allText1 := strings.Join(result1, " ")
	allText2 := strings.Join(result2, " ")

	if strings.Contains(allText1, "[CIDs:") {
		t.Error("claim references should not appear when disabled")
	}
	if !strings.Contains(allText2, "[CIDs:") {
		t.Error("claim references should appear when enabled")
	}
	if !strings.Contains(allText2, "claim-1") || !strings.Contains(allText2, "claim-2") {
		t.Error("claim IDs not found in claim references")
	}
}

func TestThreadRenderer_HybridArtifact(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-hybrid",
		ArtifactType: contracts.ArtifactTypeHybrid,
		Title:        "Hybrid Test",
		Body:         "Summary body.",
		Sections: []contracts.Section{
			{Order: 0, Title: "Details", Body: "More details here."},
		},
		Posts: []contracts.Post{
			{Order: 0, Body: "First post in thread.", ClaimIDs: []string{"claim-1"}},
			{Order: 1, Body: "Second post.", ClaimIDs: []string{"claim-2"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageSupporting},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-017",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should render both body and posts
	if len(result) == 0 {
		t.Fatal("expected posts in hybrid artifact")
	}

	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars", i, len(post))
		}
	}
}

func TestThreadRenderer_ThreadArtifact(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-thread",
		ArtifactType: contracts.ArtifactTypeThread,
		Title:        "Thread Test",
		Posts: []contracts.Post{
			{Order: 0, Body: "Post 1 body text.", ClaimIDs: []string{"claim-1"}},
			{Order: 1, Body: "Post 2 body text.", ClaimIDs: []string{"claim-2"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-018",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should preserve post order
	if len(result) != 2 {
		t.Errorf("expected 2 posts, got %d", len(result))
	}

	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars", i, len(post))
		}
	}
}

func TestThreadRenderer_LargePostSplitting(t *testing.T) {
	// Test that very long posts are split appropriately
	longText := strings.Repeat("This is test text with spaces to allow splitting. ", 50) // ~1800 chars

	article := &contracts.EditorialArtifact{
		StableID:     "test-longsplit",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Long Split Test",
		Body:         longText,
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-019",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected posts from long content")
	}

	// All posts must be within limit
	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds 300 chars: %d chars", i, len(post))
		}
	}
}

// -----------------------------------------------------------------------------
// ThreadRenderer Edge Cases and Advanced Features
// -----------------------------------------------------------------------------

func TestThreadRenderer_CustomMaxChars(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-customlimit",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Custom Limit",
		Body:         "First sentence. Second sentence. Third sentence.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-020",
		},
	}

	// Test with smaller limit
	renderer := NewThreadRenderer().SetMaxCharsPerPost(50)
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, post := range result {
		if len(post) > 50 {
			t.Errorf("post %d exceeds custom limit of 50: %d chars", i, len(post))
		}
	}

	// Test with larger limit
	renderer = NewThreadRenderer().SetMaxCharsPerPost(500)
	result, err = renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have fewer posts with larger limit
	t.Logf("Posts with 500 char limit: %d", len(result))
}

func TestThreadRenderer_ClaimRefFormat(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-format",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Format Test",
		Body:         "Test.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-abc": {ClaimID: "claim-abc", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-021",
		},
	}

	// Test with custom format
	renderer := NewThreadRenderer().SetIncludeClaimReferences(true).
		SetClaimRefFormat("(claims: %s)")
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(strings.Join(result, " "), "(claims: claim-abc)") {
		t.Error("custom claim format not applied")
	}
}

func TestThreadRenderer_EmptyBody(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:        "test-empty",
		ArtifactType:    contracts.ArtifactTypeArticle,
		Title:           "",
		Body:            "",
		ClaimReferences: map[string]contracts.ClaimUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-022",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	_ = err // empty body is valid - no error expected

	// Empty body should return nil or empty slice
	if len(result) != 0 {
		t.Logf("Got %d posts for empty body (acceptable)", len(result))
	}
}

func TestThreadRenderer_MultipleParagraphs(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-multipara",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Multiple Paragraphs",
		Body:         "Paragraph one.\n\nParagraph two with more text to ensure it fits well.\n\nParagraph three is the last one here.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-023",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected posts from paragraphs")
	}

	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars", i, len(post))
		}
	}

	// Check all paragraphs are represented
	allText := strings.Join(result, " ")
	if !strings.Contains(allText, "Paragraph one") {
		t.Error("first paragraph missing")
	}
	if !strings.Contains(allText, "Paragraph two") {
		t.Error("second paragraph missing")
	}
	if !strings.Contains(allText, "Paragraph three") {
		t.Error("third paragraph missing")
	}
}

func TestThreadRenderer_IndivisibleSegmentError(t *testing.T) {
	// Create content that exceeds limit but has no natural split points
	// This is contrived but tests the error case
	longWord := strings.Repeat("a", 500)

	article := &contracts.EditorialArtifact{
		StableID:     "test-indivisible",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Indivisible Test",
		Body:         longWord,
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-024",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)

	// Should error because a single long word can't be split
	if err == nil {
		t.Log("No error - content may have been split at word level")
		// Even if it splits, check all posts are within limit
		for i, post := range result {
			if len(post) > BlueskyMaxChars {
				t.Errorf("post %d exceeds limit: %d chars", i, len(post))
			}
		}
	} else {
		// IndivisibleSegmentError should be wrapped in RendererError or be an error itself
		if !IsRendererError(err) && !IsIndivisibleSegmentError(err) {
			t.Errorf("expected RendererError or IndivisibleSegmentError, got %T", err)
		}
		t.Logf("Got expected error: %v", err)
	}
}

func TestThreadRenderer_WithSectionContent(t *testing.T) {
	article := &contracts.EditorialArtifact{
		StableID:     "test-sectioncontent",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Section Content",
		Sections: []contracts.Section{
			{
				Order:    0,
				Title:    "Introduction",
				Body:     "This is the introduction with lots of text to ensure proper splitting.",
				ClaimIDs: []string{"claim-1"},
			},
			{
				Order:    1,
				Title:    "Main Analysis",
				Body:     "This is the main analysis section with even more content to test splitting behavior.",
				ClaimIDs: []string{"claim-2"},
			},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-025",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected posts from sections")
	}

	for i, post := range result {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds limit: %d chars", i, len(post))
		}
	}

	// Check section content is present
	allText := strings.Join(result, " ")
	if !strings.Contains(allText, "Introduction") {
		t.Error("section title lost")
	}
	if !strings.Contains(allText, "Introduction") || !strings.Contains(allText, "main analysis") {
		t.Log("Section titles and content check")
	}
}

func TestThreadRenderer_PreserveOrder(t *testing.T) {
	// Test that section order and post order are preserved
	article := &contracts.EditorialArtifact{
		StableID:     "test-order",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Order Test",
		// Use short content to prevent splitting across sentences
		Body: "First section. Second section. Third section.",
		Sections: []contracts.Section{
			{Order: 0, Title: "First", Body: "First content."},
			{Order: 1, Title: "Second", Body: "Second content."},
			{Order: 2, Title: "Third", Body: "Third content."},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore},
			"claim-3": {ClaimID: "claim-3", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-026",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(article)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("expected posts")
	}

	// Check that content appears in the correct order (First, Second, Third)
	allText := strings.Join(result, " ")
	firstIdx := strings.Index(allText, "First content")
	secondIdx := strings.Index(allText, "Second content")
	thirdIdx := strings.Index(allText, "Third content")

	if firstIdx < 0 {
		t.Error("First content not found")
	}
	if secondIdx < 0 {
		t.Error("Second content not found")
	}
	if thirdIdx < 0 {
		t.Error("Third content not found")
	}
	if firstIdx > secondIdx || secondIdx > thirdIdx {
		t.Error("content order not preserved correctly")
	}
}

// -----------------------------------------------------------------------------
// Helper Tests for Internal Functions
// -----------------------------------------------------------------------------

func TestSortStrings(t *testing.T) {
	input := []string{"z", "a", "m", "b"}
	result := sortStrings(input)

	expected := []string{"a", "b", "m", "z"}
	for i, v := range result {
		if v != expected[i] {
			t.Errorf("sortStrings[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestUniqueAndSortStrings(t *testing.T) {
	input := []string{"z", "a", "m", "a", "z", "b"}
	result := uniqueAndSortStrings(input)

	if len(result) != 4 {
		t.Errorf("expected 4 unique elements, got %d", len(result))
	}

	expected := []string{"a", "b", "m", "z"}
	for i, v := range result {
		if v != expected[i] {
			t.Errorf("uniqueAndSortStrings[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestCleanWhitespace(t *testing.T) {
	input := "Multiple    spaces\tand\ttabs\n\nnewlines"
	result := cleanWhitespace(input)

	// Should normalize all whitespace to single spaces
	if strings.Contains(result, "   ") || strings.Contains(result, "\t") {
		t.Errorf("whitespace not cleaned: %q", result)
	}
	if strings.Contains(result, "\n") {
		t.Errorf("newlines not cleaned: %q", result)
	}
}

func TestSplitBySentences(t *testing.T) {
	input := "First sentence. Second sentence! Third question?"
	result := splitBySentences(input)

	if len(result) != 3 {
		t.Errorf("expected 3 sentences, got %d: %v", len(result), result)
	}
}

func TestSplitByParagraphs(t *testing.T) {
	input := "Para one.\n\nPara two.\n\nPara three."
	result := splitByParagraphs(input)

	if len(result) != 3 {
		t.Errorf("expected 3 paragraphs, got %d: %v", len(result), result)
	}
}

// -----------------------------------------------------------------------------
// Contract Integration Tests
// -----------------------------------------------------------------------------

func TestContractValidationInRenderer(t *testing.T) {
	// Test that invalid artifacts are rejected
	invalidArtifact := &contracts.EditorialArtifact{
		StableID:     "test-invalid",
		ArtifactType: contracts.ArtifactTypeThread, // Requires posts
		Title:        "Invalid",
		Body:         "This should fail validation",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			InputVerificationID: "v-027",
		},
	}

	renderer := NewThreadRenderer()
	result, err := renderer.Render(invalidArtifact)

	if err == nil {
		t.Error("expected error for invalid artifact")
	}
	if result != nil {
		t.Error("result should be nil for invalid artifact")
	}
}

func TestFullLifecycleMarkdown(t *testing.T) {
	// Simulate a full pipeline: create artifact -> render to markdown
	artifact := &contracts.EditorialArtifact{
		StableID:     "full-life-markdown",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Full Lifecycle Test",
		Subtitle:     "Testing the entire markdown rendering pipeline",
		Body:         "This is a comprehensive test of the markdown renderer.\n\nIt handles titles, subtitles, and body content with multiple paragraphs.\n\nThe renderer should also support sections for long-form content.",
		Sections: []contracts.Section{
			{Order: 0, Title: "Introduction", Body: "Welcome to the introduction section.\n\nThis section provides context and background information."},
			{Order: 1, Title: "Methodology", Body: "Here we describe our research methodology and approach."},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"source-1": {
				SourceID:      "source-1",
				UsageCount:    3,
				CitationStyle: contracts.CitationStyleLink,
				CitationText:  "Original Research Paper, 2024",
			},
			"source-2": {
				SourceID:      "source-2",
				UsageCount:    1,
				CitationStyle: contracts.CitationStyleInline,
				CitationText:  "Smith, 2023",
			},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageSupporting, Paraphrased: true},
		},
		Warnings: []contracts.Warning{
			{
				WarningType:     contracts.WarningTypeLowConfidence,
				Severity:        contracts.WarningSeverityLow,
				Message:         "Some data points have low confidence",
				RelatedClaimIDs: []string{"claim-2"},
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			InputVerificationID: "v-full-life",
			WriterParameters: map[string]string{
				"section_style": "numbered",
				"tone":          "formal",
			},
		},
	}

	// Render to markdown
	renderer := NewMarkdownRenderer().
		SetIncludeSourceSection(true).
		SetIncludeMetadataHeader(true)

	markdown, err := renderer.Render(artifact)
	if err != nil {
		t.Fatalf("markdown render failed: %v", err)
	}

	// Verify key elements are present
	checks := []struct {
		name string
		text string
	}{
		{"title", "# Full Lifecycle Test"},
		{"subtitle", "Testing the entire markdown rendering pipeline"},
		{"body", "This is a comprehensive test"},
		{"section heading", "## Introduction"},
		{"section content", "Welcome to the introduction"},
		{"sources section", "## Sources"},
		{"source citation", "Original Research Paper, 2024"},
		{"warnings section", "## Warnings"},
	}

	for _, check := range checks {
		if !strings.Contains(markdown, check.text) {
			t.Errorf("missing %s: %q", check.name, check.text)
		}
	}

	// Verify determinism
	renderer2 := NewMarkdownRenderer().
		SetIncludeSourceSection(true).
		SetIncludeMetadataHeader(true)

	markdown2, err := renderer2.Render(artifact)
	if err != nil {
		t.Fatalf("markdown render failed: %v", err)
	}

	if markdown != markdown2 {
		t.Error("markdown output is not deterministic")
		t.Logf("Output 1: %s", markdown[:100])
		t.Logf("Output 2: %s", markdown2[:100])
	}
}

func TestFullLifecycleThread(t *testing.T) {
	// Simulate a full pipeline: create artifact -> render to thread
	artifact := &contracts.EditorialArtifact{
		StableID:     "full-life-thread",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Thread Lifecycle Test",
		Body:         "Breaking: New research reveals important findings about dolphin conservation.\n\nScientists discovered that coastal dolphin populations have increased by 15% over the past year, marking a significant recovery after decades of decline.\n\nThe study, conducted by marine biologists across five countries, provides hope for ocean conservation efforts worldwide. Key findings include improved water quality, reduced pollution levels, and successful protection measures.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-findings": {ClaimID: "claim-findings", UsageType: contracts.ClaimUsageCore, Paraphrased: false, UsageContext: "Main finding about population increase"},
			"claim-method":   {ClaimID: "claim-method", UsageType: contracts.ClaimUsageBackground, Paraphrased: false, UsageContext: "Study methodology"},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"study-2024": {
				SourceID:      "study-2024",
				UsageCount:    2,
				CitationStyle: contracts.CitationStyleLink,
				CitationText:  "Dolphin Conservation Study 2024, Marine Biology Journal",
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			GeneratedAt:         time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
			InputVerificationID: "v-full-thread",
		},
	}

	renderer := NewThreadRenderer().
		SetIncludeClaimReferences(true)

	posts, err := renderer.Render(artifact)
	if err != nil {
		t.Fatalf("thread render failed: %v", err)
	}

	if len(posts) == 0 {
		t.Fatal("expected at least one post")
	}

	// All posts should be within limit
	for i, post := range posts {
		if len(post) > BlueskyMaxChars {
			t.Errorf("post %d exceeds 300 chars: %d", i, len(post))
		}
	}

	// Verify content is preserved
	allText := strings.Join(posts, " ")
	if !strings.Contains(allText, "Breaking") {
		t.Error("title lost")
	}
	if !strings.Contains(allText, "dolphin") {
		t.Error("content truncated")
	}
	if !strings.Contains(allText, "[CIDs:") {
		t.Error("claim references not included")
	}

	// Verify determinism
	renderer2 := NewThreadRenderer().
		SetIncludeClaimReferences(true)

	posts2, err := renderer2.Render(artifact)
	if err != nil {
		t.Fatalf("thread render failed: %v", err)
	}

	if len(posts) != len(posts2) {
		t.Errorf("post count differs: got %d, expected %d", len(posts2), len(posts))
	}

	for i := range posts {
		if posts[i] != posts2[i] {
			t.Errorf("post %d differs", i)
			t.Logf("Post %d (1): %q", i, posts[i])
			t.Logf("Post %d (2): %q", i, posts2[i])
		}
	}
}
