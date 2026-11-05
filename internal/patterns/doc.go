// Package patterns detects repeated editorial feedback patterns across runs.
//
// This package implements issue #148: detecting recurring correction/edit
// patterns such as repeated removal of unsupported superlatives, recurring
// terminology changes, repeated preference for shorter intros, etc.
//
// Core principles:
//   - Deterministic pattern detection: same inputs produce same patterns
//   - Configurable thresholds: minimum occurrences, time windows, grouping
//   - Evidence preservation: keep raw feedback references for review
//   - No automatic profile mutation: patterns are advisory only
//
// Pattern detection workflow:
//  1. Query KindEditorialFeedback records from the memory store
//  2. Filter by category (when specified) and time window
//  3. Group occurrences by key (author/profile/recipe/category)
//  4. Identify patterns: same category + similar feedback within threshold
//  5. Score patterns by occurrence count and recency
//  6. Filter by minimum occurrence threshold
//
// Output: one KindEditorialPattern record per detected pattern, with
// occurrences as evidence references.
package patterns
