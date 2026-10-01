// Package fixtures provides example data for testing the types package.
package fixtures

import (
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// WebArticle provides a representative example of a fetched and normalized
// web article. This fixture demonstrates the complete flow from Source (raw
// fetch) to Document (cleaned content).
//
// Use this fixture for:
// - Integration tests
// - Example documentation
// - Demonstrating the provenance chain
var WebArticle = WebArticleData{
	Source: types.Source{
		StableID:    "web-article-fixed-content-20240115",
		OriginalURL: "https://example-news.com/technology/ai-breakthrough-2024",
		SourceType:  types.SourceTypeWeb,
		RetrievedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		FetchStatus: &types.FetchStatus{
			HTTPStatus:    ptr(200),
			ContentType:   ptr("text/html; charset=utf-8"),
			ContentLength: ptr[int64](15420),
		},
		Metadata: map[string]string{
			"server":        "nginx/1.18.0",
			"cache-control": "max-age=3600",
			"etag":          "\"3d2f1-18a5c\"",
			"retries":       "0",
		},
	},
	Document: types.Document{
		SourceID:     "web-article-fixed-content-20240115",
		CanonicalURL: ptr("https://example-news.com/technology/ai-breakthrough-2024/"),
		Title:        ptr("AI Breakthrough: New Model Achieves Human-Level Reasoning"),
		PlainText: `AI Breakthrough: New Model Achieves Human-Level Reasoning

By Sarah Chen
Published: January 14, 2024

Scientists at a leading research institute have announced a significant breakthrough in artificial intelligence, unveiling a new model that demonstrates reasoning capabilities comparable to human experts.

The achievement marks a milestone in the field of artificial general intelligence (AGI), though researchers caution that the model still requires significant human oversight.

"This represents meaningful progress toward systems that can understand and reason about complex problems," said Dr. Marcus Wei, lead researcher on the project. "However, we must remain realistic about the current capabilities and limitations."

Key Capabilities

The new model excels at several tasks that previously required human expertise:

- Complex mathematical problem-solving across multiple domains
- Multi-step reasoning with explicit justification of each step
- Cross-referencing information from diverse sources
- Identifying logical fallacies and inconsistencies

The system was evaluated on a standardized benchmark suite covering mathematics, logic puzzles, and scientific reasoning. It achieved scores matching or exceeding human performance on 78% of tasks.

Limitations and Safeguards

Despite the impressive results, the research team emphasizes several important limitations:

The model cannot independently verify facts in real-time and relies on training data cutoff.

Safety mechanisms prevent the system from executing code or making autonomous decisions.

Human oversight remains essential for high-stakes applications.

"Emergent capabilities in large models are unpredictable," noted Dr. Wei. "We implement strict guardrails to prevent unintended behaviors."

Industry Response

The announcement has drawn mixed reactions from the AI community. While many researchers hail it as a major step forward, some express concern about overhyping near-term AGI capabilities.

Industry observers note that practical applications remain limited by cost and infrastructure requirements. Running the model requires specialized hardware and substantial energy resources.

Next Steps

The research team plans to publish detailed methodology and evaluation results in an upcoming peer-reviewed paper. They will also release a subset of the training data under specific licensing terms.

Open-source alternatives are being explored, though the team warns that simplified versions will not match the performance of the full system.

"The path to reliable, beneficial AGI requires both technical innovation and thoughtful governance," Dr. Wei concluded. "We're committed to transparency and safety at every stage."

-- End of Article --`,
		Author: ptr("Sarah Chen"),
		PublishedAt: ptr(time.Date(2024, 1, 14, 16, 45, 0, 0, time.UTC)),
		RetrievedAt: time.Date(2024, 1, 15, 10, 35, 0, 0, time.UTC),
		ExtractionMetadata: map[string]string{
			"word_count":      "287",
			"paragraph_count": "12",
			"extractor":       "html-cleaner-v3.2",
			"extraction_time": "0.342s",
			"sentences_count": "18",
		},
	},
}

// WebArticleData holds both the raw Source and normalized Document for a
// single article, demonstrating the complete provenance chain.
type WebArticleData struct {
	Source   types.Source
	Document types.Document
}

// ptr is a helper for creating pointers to constants.
func ptr[T any](v T) *T {
	return &v
}

// VerifyExported ensures the fixture package exports usable data.
var _ = WebArticle
