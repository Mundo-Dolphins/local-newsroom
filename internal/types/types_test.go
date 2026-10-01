package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSourceMarshalJSON(t *testing.T) {
	now := time.Now().UTC()
	httpStatus := 200
	contentType := "text/html"
	contentLen := int64(12345)

	source := Source{
		StableID:    "abc123",
		OriginalURL: "https://example.com/article",
		SourceType:  SourceTypeWeb,
		RetrievedAt: now,
		FetchStatus: &FetchStatus{
			HTTPStatus:    &httpStatus,
			ContentType:   &contentType,
			ContentLength: &contentLen,
			FetchError:    nil,
		},
		Metadata: map[string]string{
			"user-agent": "newsroom-fetcher/1.0",
		},
	}

	data, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("Failed to marshal Source: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Verify required fields
	if result["stable_id"] != "abc123" {
		t.Errorf("Expected stable_id to be 'abc123', got %v", result["stable_id"])
	}
	if result["original_url"] != "https://example.com/article" {
		t.Errorf("Expected original_url to be 'https://example.com/article', got %v", result["original_url"])
	}
	if result["source_type"] != "web" {
		t.Errorf("Expected source_type to be 'web', got %v", result["source_type"])
	}

	// Verify FetchStatus fields
	fetchStatus, ok := result["fetch_status"].(map[string]any)
	if !ok {
		t.Fatal("Expected fetch_status to be a map")
	}
	if fetchStatus["http_status"] != float64(200) {
		t.Errorf("Expected http_status to be 200, got %v", fetchStatus["http_status"])
	}
}

func TestSourceUnmarshalJSON(t *testing.T) {
	jsonStr := `
	{
		"stable_id": "test-stable-id-123",
		"original_url": "https://news.example.com/post",
		"source_type": "rss",
		"retrieved_at": "2024-01-15T10:30:00Z",
		"fetch_status": {
			"http_status": 200,
			"content_type": "application/rss+xml",
			"content_length": 4096,
			"fetch_error": null
		},
		"metadata": {
			"feed_name": "Tech News",
			"item_count": "5"
		}
	}
	`

	var source Source
	if err := json.Unmarshal([]byte(jsonStr), &source); err != nil {
		t.Fatalf("Failed to unmarshal Source: %v", err)
	}

	if source.StableID != "test-stable-id-123" {
		t.Errorf("Expected StableID to be 'test-stable-id-123', got %q", source.StableID)
	}
	if source.OriginalURL != "https://news.example.com/post" {
		t.Errorf("Expected OriginalURL to be 'https://news.example.com/post', got %q", source.OriginalURL)
	}
	if source.SourceType != SourceTypeRSS {
		t.Errorf("Expected SourceType to be 'rss', got %q", source.SourceType)
	}
	if source.FetchStatus == nil {
		t.Fatal("Expected FetchStatus to be non-nil")
	}
	if source.FetchStatus.HTTPStatus == nil {
		t.Fatal("Expected FetchStatus.HTTPStatus to be non-nil")
	}
	if *source.FetchStatus.HTTPStatus != 200 {
		t.Errorf("Expected HTTPStatus to be 200, got %v", *source.FetchStatus.HTTPStatus)
	}
	if source.FetchStatus.FetchError != nil {
		t.Errorf("Expected FetchError to be nil, got %q", *source.FetchStatus.FetchError)
	}
	if len(source.Metadata) != 2 {
		t.Errorf("Expected 2 metadata entries, got %d", len(source.Metadata))
	}
}

func TestSourceUnmarshalJSONWithNullFields(t *testing.T) {
	jsonStr := `
	{
		"stable_id": "test-id-456",
		"original_url": "https://news.example.com/test",
		"source_type": "json",
		"retrieved_at": "2024-01-15T12:00:00Z",
		"fetch_status": {
			"http_status": null,
			"content_type": null,
			"fetch_error": "timeout error"
		}
	}
	`

	var source Source
	if err := json.Unmarshal([]byte(jsonStr), &source); err != nil {
		t.Fatalf("Failed to unmarshal Source: %v", err)
	}

	if source.FetchStatus == nil {
		t.Fatal("Expected FetchStatus to be non-nil")
	}
	if source.FetchStatus.HTTPStatus != nil {
		t.Errorf("Expected HTTPStatus to be nil, got %v", *source.FetchStatus.HTTPStatus)
	}
	if source.FetchStatus.FetchError == nil {
		t.Fatal("Expected FetchError to be non-nil")
	}
	if *source.FetchStatus.FetchError != "timeout error" {
		t.Errorf("Expected FetchError to be 'timeout error', got %q", *source.FetchStatus.FetchError)
	}
}

func TestDocumentMarshalJSON(t *testing.T) {
	now := time.Now().UTC()
	published := time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC)
	title := "Test Article Title"
	author := "Jane Doe"
	canonical := "https://news.example.com/canonical-url"

	doc := Document{
		SourceID:     "doc-source-456",
		CanonicalURL: &canonical,
		Title:        &title,
		PlainText:    "This is the extracted plain text content of the article.",
		Author:       &author,
		PublishedAt:  &published,
		RetrievedAt:  now,
		ExtractionMetadata: map[string]string{
			"word_count": "250",
			"extractor":  "html-cleaner-v2",
		},
	}

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Failed to marshal Document: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// Verify required fields
	if result["source_id"] != "doc-source-456" {
		t.Errorf("Expected source_id to be 'doc-source-456', got %v", result["source_id"])
	}
	if result["plain_text"] != "This is the extracted plain text content of the article." {
		t.Errorf("Expected plain_text to match, got %v", result["plain_text"])
	}

	// Verify optional pointer fields
	if result["title"] != "Test Article Title" {
		t.Errorf("Expected title to be 'Test Article Title', got %v", result["title"])
	}
	if result["author"] != "Jane Doe" {
		t.Errorf("Expected author to be 'Jane Doe', got %v", result["author"])
	}
	if result["canonical_url"] != "https://news.example.com/canonical-url" {
		t.Errorf("Expected canonical_url to be 'https://news.example.com/canonical-url', got %v", result["canonical_url"])
	}
}

func TestDocumentUnmarshalJSON(t *testing.T) {
	jsonStr := `
	{
		"source_id": "extracted-doc-789",
		"canonical_url": "https://news.example.com/final-article",
		"title": "Breaking News: Important Event",
		"plain_text": "Lorem ipsum dolor sit amet, consectetur adipiscing elit.",
		"author": "John Smith",
		"published_at": "2024-01-14T14:22:00Z",
		"retrieved_at": "2024-01-15T11:00:00Z",
		"extraction_metadata": {
			"word_count": "150",
			"extractor": "default"
		}
	}
	`

	var doc Document
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		t.Fatalf("Failed to unmarshal Document: %v", err)
	}

	if doc.SourceID != "extracted-doc-789" {
		t.Errorf("Expected SourceID to be 'extracted-doc-789', got %q", doc.SourceID)
	}
	if doc.PlainText != "Lorem ipsum dolor sit amet, consectetur adipiscing elit." {
		t.Errorf("Expected PlainText to match, got %q", doc.PlainText)
	}
	if doc.Title == nil {
		t.Fatal("Expected Title to be non-nil")
	}
	if *doc.Title != "Breaking News: Important Event" {
		t.Errorf("Expected Title to be 'Breaking News: Important Event', got %q", *doc.Title)
	}
	if doc.Author == nil {
		t.Fatal("Expected Author to be non-nil")
	}
	if *doc.Author != "John Smith" {
		t.Errorf("Expected Author to be 'John Smith', got %q", *doc.Author)
	}
	if doc.PublishedAt == nil {
		t.Fatal("Expected PublishedAt to be non-nil")
	}
	if doc.ExtractionMetadata["word_count"] != "150" {
		t.Errorf("Expected word_count to be '150', got %q", doc.ExtractionMetadata["word_count"])
	}
}

func TestDocumentRoundTrip(t *testing.T) {
	published := time.Date(2024, 1, 15, 12, 30, 0, 0, time.UTC)
	now := time.Date(2024, 1, 15, 13, 0, 0, 0, time.UTC)
	title := "Round Trip Test"
	author := "Test Author"
	canonical := "https://example.com/canonical"

	original := Document{
		SourceID:     "round-trip-id",
		CanonicalURL: &canonical,
		Title:        &title,
		PlainText:    "This document should round-trip correctly through JSON.",
		Author:       &author,
		PublishedAt:  &published,
		RetrievedAt:  now,
		ExtractionMetadata: map[string]string{
			"test_flag": "true",
		},
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Unmarshal
	var restored Document
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Compare
	if restored.SourceID != original.SourceID {
		t.Errorf("SourceID mismatch: expected %q, got %q", original.SourceID, restored.SourceID)
	}
	if restored.PlainText != original.PlainText {
		t.Errorf("PlainText mismatch: expected %q, got %q", original.PlainText, restored.PlainText)
	}
	if restored.Title == nil || *restored.Title != *original.Title {
		t.Errorf("Title mismatch: expected %q, got %v", *original.Title, restored.Title)
	}
	if restored.Author == nil || *restored.Author != *original.Author {
		t.Errorf("Author mismatch: expected %q, got %v", *original.Author, restored.Author)
	}
	if restored.CanonicalURL == nil || *restored.CanonicalURL != *original.CanonicalURL {
		t.Errorf("CanonicalURL mismatch: expected %q, got %v", *original.CanonicalURL, restored.CanonicalURL)
	}
	if restored.PublishedAt == nil || !restored.PublishedAt.Equal(*original.PublishedAt) {
		t.Errorf("PublishedAt mismatch: expected %v, got %v", *original.PublishedAt, restored.PublishedAt)
	}
	if len(restored.ExtractionMetadata) != len(original.ExtractionMetadata) {
		t.Errorf("ExtractionMetadata length mismatch: expected %d, got %d", len(original.ExtractionMetadata), len(restored.ExtractionMetadata))
	}
}

func TestDocumentWithMissingOptionalFields(t *testing.T) {
	// Test that missing optional fields are treated as unknown (nil)
	jsonStr := `
	{
		"source_id": "minimal-doc",
		"plain_text": "Content without optional fields",
		"retrieved_at": "2024-01-15T10:00:00Z"
	}
	`

	var doc Document
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		t.Fatalf("Failed to unmarshal minimal document: %v", err)
	}

	// These should all be nil (unknown), not empty
	if doc.CanonicalURL != nil {
		t.Errorf("Expected CanonicalURL to be nil, got %q", *doc.CanonicalURL)
	}
	if doc.Title != nil {
		t.Errorf("Expected Title to be nil, got %q", *doc.Title)
	}
	if doc.Author != nil {
		t.Errorf("Expected Author to be nil, got %q", *doc.Author)
	}
	if doc.PublishedAt != nil {
		t.Errorf("Expected PublishedAt to be nil, got %v", *doc.PublishedAt)
	}
	if len(doc.ExtractionMetadata) != 0 {
		t.Errorf("Expected empty ExtractionMetadata, got %v", doc.ExtractionMetadata)
	}
}

func TestDocumentWithNullFields(t *testing.T) {
	// Test that explicitly null fields are treated as unknown (nil)
	now := time.Now().UTC()
	jsonStr := `
	{
		"source_id": "null-vals-doc",
		"canonical_url": null,
		"title": null,
		"plain_text": "Content",
		"author": null,
		"published_at": null,
		"retrieved_at": "` + now.Format(time.RFC3339) + `",
		"extraction_metadata": {}
	}
	`

	var doc Document
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		t.Fatalf("Failed to unmarshal document with nulls: %v", err)
	}

	// All explicitly null fields should be nil (unknown)
	if doc.CanonicalURL != nil {
		t.Errorf("Expected CanonicalURL to be nil (null), got %q", *doc.CanonicalURL)
	}
	if doc.Title != nil {
		t.Errorf("Expected Title to be nil (null), got %q", *doc.Title)
	}
	if doc.Author != nil {
		t.Errorf("Expected Author to be nil (null), got %q", *doc.Author)
	}
	if doc.PublishedAt != nil {
		t.Errorf("Expected PublishedAt to be nil (null), got %v", *doc.PublishedAt)
	}
}

func TestDocumentWithEmptyStrings(t *testing.T) {
	// Test that empty strings are preserved when explicitly set
	now := time.Now().UTC()
	jsonStr := `
	{
		"source_id": "empty-strings-doc",
		"canonical_url": null,
		"title": "",
		"plain_text": "Content",
		"author": null,
		"published_at": null,
		"retrieved_at": "` + now.Format(time.RFC3339) + `",
		"extraction_metadata": {
			"note": "title was empty but present"
		}
	}
	`

	var doc Document
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		t.Fatalf("Failed to unmarshal document with empty strings: %v", err)
	}

	// CanonicalURL should be nil (unknown - explicitly null in JSON)
	if doc.CanonicalURL != nil {
		t.Errorf("Expected CanonicalURL to be nil, got %q", *doc.CanonicalURL)
	}

	// Title should point to empty string (explicitly empty)
	if doc.Title == nil {
		t.Error("Expected Title to be non-nil (empty string explicitly set)")
	} else if *doc.Title != "" {
		t.Errorf("Expected Title to be empty string, got %q", *doc.Title)
	}

	// Author should be nil (unknown - explicitly null)
	if doc.Author != nil {
		t.Errorf("Expected Author to be nil, got %q", *doc.Author)
	}
}

func TestSourceTypeConstants(t *testing.T) {
	tests := []struct {
		name  string
		value SourceType
	}{
		{"Web", SourceTypeWeb},
		{"RSS", SourceTypeRSS},
		{"JSON", SourceTypeJSON},
		{"CSV", SourceTypeCSV},
		{"PDF", SourceTypePDF},
		{"Unknown", SourceTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Source{
				StableID:    "test",
				OriginalURL: "https://example.com",
				SourceType:  tt.value,
				RetrievedAt: time.Now().UTC(),
			}

			data, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			var result map[string]any
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if result["source_type"] != string(tt.value) {
				t.Errorf("Expected source_type to be %q, got %q", tt.value, result["source_type"])
			}
		})
	}
}

func TestFetchStatusSerialization(t *testing.T) {
	t.Run("Successful Fetch", func(t *testing.T) {
		status := 200
		contentType := "text/html"
		contentLen := int64(5432)

		fs := FetchStatus{
			HTTPStatus:    &status,
			ContentType:   &contentType,
			ContentLength: &contentLen,
			FetchError:    nil,
		}

		data, err := json.Marshal(fs)
		if err != nil {
			t.Fatalf("Failed to marshal FetchStatus: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if result["http_status"] != float64(200) {
			t.Errorf("Expected http_status 200, got %v", result["http_status"])
		}
		if _, ok := result["fetch_error"]; ok {
			t.Error("fetch_error should not be present when nil")
		}
	})

	t.Run("Failed Fetch", func(t *testing.T) {
		status := 404
		errMsg := "Not Found"

		fs := FetchStatus{
			HTTPStatus: &status,
			FetchError: &errMsg,
		}

		data, err := json.Marshal(fs)
		if err != nil {
			t.Fatalf("Failed to marshal FetchStatus: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if result["http_status"] != float64(404) {
			t.Errorf("Expected http_status 404, got %v", result["http_status"])
		}
		if result["fetch_error"] != "Not Found" {
			t.Errorf("Expected fetch_error to be 'Not Found', got %v", result["fetch_error"])
		}
	})

	t.Run("No Response Received", func(t *testing.T) {
		errMsg := "connection timeout"

		fs := FetchStatus{
			FetchError: &errMsg,
		}

		data, err := json.Marshal(fs)
		if err != nil {
			t.Fatalf("Failed to marshal FetchStatus: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if _, ok := result["http_status"]; ok {
			t.Error("http_status should not be present when nil")
		}
		if result["fetch_error"] != "connection timeout" {
			t.Errorf("Expected fetch_error to be 'connection timeout', got %v", result["fetch_error"])
		}
	})

	t.Run("Partial Status", func(t *testing.T) {
		// Some fields present, others null
		status := 200
		fs := FetchStatus{
			HTTPStatus: &status,
			ContentType: nil,
			ContentLength: nil,
		}

		data, err := json.Marshal(fs)
		if err != nil {
			t.Fatalf("Failed to marshal: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if result["http_status"] != float64(200) {
			t.Errorf("Expected http_status 200, got %v", result["http_status"])
		}
		if _, ok := result["content_type"]; ok {
			t.Error("content_type should not be present when nil")
		}
	})
}

func TestPointerToHelper(t *testing.T) {
	// Test string
	s := PointerTo("hello")
	if s == nil || *s != "hello" {
		t.Error("PointerTo(string) failed")
	}

	// Test int
	n := PointerTo(42)
	if n == nil || *n != 42 {
		t.Error("PointerTo(int) failed")
	}

	// Test float64
	f := PointerTo(3.14)
	if f == nil || *f != 3.14 {
		t.Error("PointerTo(float64) failed")
	}

	// Test bool
	b := PointerTo(true)
	if b == nil || !*b {
		t.Error("PointerTo(bool) failed")
	}

	// Test int64
	l := PointerTo(int64(1024))
	if l == nil || *l != 1024 {
		t.Error("PointerTo(int64) failed")
	}

	// Test time.Time
	tm := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	tp := PointerTo(tm)
	if tp == nil || !(*tp).Equal(tm) {
		t.Error("PointerTo(time.Time) failed")
	}
}
