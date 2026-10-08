// Package profiles implements a generic editorial profile system for
// configurable style, facts policy, vocabulary, and author-specific instructions.
//
// Profiles are loaded from a configurable directory structure:
//
//	profiles/
//	  common/
//	    style.md          // Common style rules
//	    facts-policy.md   // Factual safety constraints
//	    vocabulary.md     // Vocabulary constraints
//	  authors/
//	    <author>/
//	      style.md        // Author-specific style
//	      examples/       // Optional example outputs
//
// This package does not implement any LLM-specific logic; it only loads,
// validates, and merges profile data for downstream use.
//
// Security: Profile files are treated as untrusted local text:
//   - No template code execution
//   - No shell expansion
//   - Bounded file sizes (max 50KB per file)
//   - Clear file-not-found errors
//
// Precedence:
//
//	engine defaults
//	  -> publication/common profile
//	  -> author profile
//	  -> command-specific overrides
//
// Core factual-safety rules (from facts-policy.md) cannot be disabled by
// any downstream profile or override.
package profiles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxFileSize is the maximum allowed size for profile files (50KB).
	DefaultMaxFileSize = 50 * 1024

	// DefaultMaxExamplesTotal is the maximum total lines across all example files.
	DefaultMaxExamplesTotal = 1000

	// DefaultMaxExamplesPerFile is the maximum lines in a single example file.
	DefaultMaxExamplesPerFile = 200

	// CommonDir is the directory containing common/base instructions.
	CommonDir = "common"

	// AuthorsDir is the directory containing author-specific instructions.
	AuthorsDir = "authors"

	// ExamplesDir is the directory containing optional example outputs.
	ExamplesDir = "examples"

	// DefaultStyleFilename is the default name for style profile files.
	DefaultStyleFilename = "style.md"

	// DefaultFactsPolicyFilename is the default name for facts policy files.
	DefaultFactsPolicyFilename = "facts-policy.md"

	// DefaultVocabularyFilename is the default name for vocabulary files.
	DefaultVocabularyFilename = "vocabulary.md"
)

// Profile represents the complete set of editorial constraints.
//
// Fields are populated in precedence order:
//  1. Engine defaults (immutable core facts, safety)
//  2. Publication common profile (style.md, facts-policy.md, vocabulary.md)
//  3. Author-specific profile (author/style.md, examples/)
//  4. Command-specific overrides
type Profile struct {
	// CoreFactsPolicy contains immutable factual-safety rules.
	// These cannot be disabled by any downstream profile or override.
	CoreFactsPolicy string

	// Style contains writing style constraints.
	Style string

	// Vocabulary contains vocabulary constraints.
	Vocabulary string

	// OutputFormat contains output format instructions.
	OutputFormat string

	// Examples contains example outputs for guidance.
	// Format: fileName -> content (for tracking)
	Examples map[string]string

	// Metadata contains loaded-at information for debugging.
	Metadata ProfileMetadata
}

// ProfileMetadata contains runtime metadata about profile loading.
type ProfileMetadata struct {
	// LoadedAt is when the profile was loaded.
	LoadedAt time.Time

	// SourcePath is the base path from which profiles were loaded.
	SourcePath string

	// CommonFileCount is the number of files loaded from common/.
	CommonFileCount int

	// AuthorFileCount is the number of files loaded from authors/<author>/.
	AuthorFileCount int

	// ExampleFileCount is the number of example files loaded.
	ExampleFileCount int

	// HasOverrides indicates if command-specific overrides were applied.
	HasOverrides bool
}

// MergeResult represents the result of merging a profile with overrides.
type MergeResult struct {
	// Profile is the merged profile.
	Profile Profile

	// Warnings contains non-fatal warnings from merging.
	Warnings []string
}

// Loader handles loading profiles from a directory structure.
type Loader struct {
	basePath             string
	maxFileSize          int64
	maxExamplesTotal     int
	maxExamplesPerFile   int
	mu                   sync.RWMutex
	defaults             defaults
	authorOverride       string
	outputFormatOverride string
}

// defaults holds the immutable engine-level defaults.
type defaults struct {
	coreFactsPolicy string
}

// Config holds configuration for the profile loader.
type Config struct {
	// BasePath is the directory containing the profiles structure.
	// This should not be empty; use "." for current directory.
	BasePath string

	// MaxFileSize is the maximum allowed size for profile files.
	// If 0, DefaultMaxFileSize is used.
	MaxFileSize int64

	// MaxExamplesTotal is the maximum total lines across all examples.
	// If 0, DefaultMaxExamplesTotal is used.
	MaxExamplesTotal int

	// MaxExamplesPerFile is the maximum lines in a single example file.
	// If 0, DefaultMaxExamplesPerFile is used.
	MaxExamplesPerFile int

	// AuthorOverride is the author-specific profile to use.
	// If empty, no author profile is loaded.
	AuthorOverride string

	// OutputFormatOverride is the output format content to use.
	// If empty, output format is loaded from common/output-format.md if present.
	OutputFormatOverride string
}

// errors
var (
	ErrPathEmpty        = errors.New("profile path cannot be empty")
	ErrPathNotDirectory = errors.New("profile path is not a directory")
	ErrPathNotExists    = errors.New("profile path does not exist")
	ErrMaxFileSize      = errors.New("profile file exceeds maximum size")
	ErrMaxExamples      = errors.New("example files exceed maximum line count")
	ErrFileNotFound     = errors.New("required profile file not found")
	ErrCoreFactPolicy   = errors.New("core facts policy cannot be modified")
	ErrInvalidPath      = errors.New("profile path contains invalid characters")
	ErrSecurity         = errors.New("profile contains potentially unsafe content")
)

// NewLoader creates a new profile loader with the given configuration.
//
// Returns an error if the configuration is invalid.
func NewLoader(cfg Config) (*Loader, error) {
	if cfg.BasePath == "" {
		return nil, ErrPathEmpty
	}

	// Normalize path
	cfg.BasePath = filepath.Clean(cfg.BasePath)

	// Validate path exists
	info, err := os.Stat(cfg.BasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrPathNotExists, cfg.BasePath)
		}
		return nil, fmt.Errorf("stat profile path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrPathNotDirectory, cfg.BasePath)
	}

	// Validate path is not outside allowed tree (basic security)
	absPath, err := filepath.Abs(cfg.BasePath)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path: %w", err)
	}

	// Check for .. in path (prevents path traversal)
	if strings.Contains(absPath, "..") {
		return nil, ErrInvalidPath
	}

	// Set defaults
	if cfg.MaxFileSize <= 0 {
		cfg.MaxFileSize = DefaultMaxFileSize
	}
	if cfg.MaxExamplesTotal <= 0 {
		cfg.MaxExamplesTotal = DefaultMaxExamplesTotal
	}
	if cfg.MaxExamplesPerFile <= 0 {
		cfg.MaxExamplesPerFile = DefaultMaxExamplesPerFile
	}

	// Define immutable engine-level defaults
	defaults := defaults{
		coreFactsPolicy: buildCoreFactsPolicy(),
	}

	return &Loader{
		basePath:             absPath,
		maxFileSize:          cfg.MaxFileSize,
		maxExamplesTotal:     cfg.MaxExamplesTotal,
		maxExamplesPerFile:   cfg.MaxExamplesPerFile,
		defaults:             defaults,
		authorOverride:       cfg.AuthorOverride,
		outputFormatOverride: cfg.OutputFormatOverride,
	}, nil
}

// Load loads and merges all available profiles.
//
// The merge order is:
//  1. Engine defaults (immutable core facts)
//  2. Publication common profile (common/style.md, common/facts-policy.md, common/vocabulary.md)
//  3. Author-specific profile (authors/<author>/style.md, examples/)
//  4. Command-specific overrides (already applied in constructor)
//
// Returns the merged Profile or an error if loading fails.
func (l *Loader) Load(ctx context.Context) (Profile, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	profile := Profile{
		Examples: make(map[string]string),
	}

	// Step 1: Start with engine defaults
	// These will be merged with common profile (which preserves defaults in mergeCoreFactPolicy)

	// Step 2: Load common profile
	commonProfile, _, err := l.loadCommonProfile()
	if err != nil {
		return Profile{}, fmt.Errorf("loading common profile: %w", err)
	}

	profile.CoreFactsPolicy = commonProfile.CoreFactsPolicy
	profile.Style = commonProfile.Style
	profile.Vocabulary = commonProfile.Vocabulary
	profile.Metadata.CommonFileCount = commonProfile.Metadata.CommonFileCount

	// Merge examples from common profile
	for k, v := range commonProfile.Examples {
		profile.Examples[k] = v
	}

	// Step 3: Load author profile if specified
	if l.authorOverride != "" {
		authorProfile, _, err := l.loadAuthorProfile(l.authorOverride)
		if err != nil {
			return Profile{}, fmt.Errorf("loading author profile: %w", err)
		}
		profile.Style = mergeStyle(profile.Style, authorProfile.Style)
		profile.Metadata.AuthorFileCount = authorProfile.Metadata.AuthorFileCount

		// Merge examples from author profile
		for k, v := range authorProfile.Examples {
			profile.Examples[k] = v
		}
	}

	// Step 4: Apply command-specific overrides
	if l.outputFormatOverride != "" {
		profile.OutputFormat = l.outputFormatOverride
	}

	// Update metadata (preserve CommonFileCount from common profile)
	profile.Metadata = ProfileMetadata{
		LoadedAt:         time.Now(),
		SourcePath:       l.basePath,
		CommonFileCount:  profile.Metadata.CommonFileCount, // Preserve count from earlier
		AuthorFileCount:  profile.Metadata.AuthorFileCount,
		ExampleFileCount: len(profile.Examples),
		HasOverrides:     l.outputFormatOverride != "" || l.authorOverride != "",
	}

	return profile, nil
}

// loadCommonProfile loads the common/base profile files.
func (l *Loader) loadCommonProfile() (Profile, []string, error) {
	profile := Profile{
		Examples:        make(map[string]string),
		CoreFactsPolicy: l.defaults.coreFactsPolicy, // Initialize with engine defaults
	}

	var warnings []string

	// Load style.md
	stylePath := filepath.Join(l.basePath, CommonDir, DefaultStyleFilename)
	if content, err := l.loadFileSafe(stylePath); err != nil {
		if !os.IsNotExist(err) {
			return Profile{}, warnings, err
		}
		// style.md is optional at common level
	} else {
		profile.Style = content
		profile.Metadata.CommonFileCount++
	}

	// Load facts-policy.md
	factsPath := filepath.Join(l.basePath, CommonDir, DefaultFactsPolicyFilename)
	if content, err := l.loadFileSafe(factsPath); err != nil {
		if !os.IsNotExist(err) {
			return Profile{}, warnings, err
		}
		// facts-policy.md is optional
	} else {
		// Validate that core facts cannot be removed
		if !containsCoreFact(content) {
			warnings = append(warnings, "common/facts-policy.md does not reference core facts; some safety rules may be missing")
		}
		profile.CoreFactsPolicy = mergeCoreFactPolicy(l.defaults.coreFactsPolicy, content)
		profile.Metadata.CommonFileCount++
	}

	// Load vocabulary.md
	vocabPath := filepath.Join(l.basePath, CommonDir, DefaultVocabularyFilename)
	if content, err := l.loadFileSafe(vocabPath); err != nil {
		if !os.IsNotExist(err) {
			return Profile{}, warnings, err
		}
		// vocabulary.md is optional
	} else {
		profile.Vocabulary = content
		profile.Metadata.CommonFileCount++
	}

	// Load examples
	examplesPath := filepath.Join(l.basePath, CommonDir, ExamplesDir)
	if exampleFiles, err := l.loadExamples(examplesPath, "common/examples/"); err != nil {
		if os.IsNotExist(err) {
			// Examples directory doesn't exist, this is fine
		} else if errors.Is(err, ErrMaxExamples) {
			// Validation errors (line count) should fail the load
			return Profile{}, warnings, fmt.Errorf("loading common examples: %w", err)
		} else {
			// Other errors (e.g., permission denied) - convert to warning
			warnings = append(warnings, fmt.Sprintf("loading common examples: %v", err))
		}
	} else {
		profile.Examples = exampleFiles
		if len(exampleFiles) > 0 {
			profile.Metadata.ExampleFileCount = len(exampleFiles)
		}
	}

	return profile, warnings, nil
}

// loadAuthorProfile loads an author-specific profile.
func (l *Loader) loadAuthorProfile(authorName string) (Profile, []string, error) {
	profile := Profile{
		Examples: make(map[string]string),
	}

	var warnings []string

	// Validate author name (no path traversal, only safe characters)
	if !isValidAuthorName(authorName) {
		return Profile{}, warnings, fmt.Errorf("invalid author name: %s", authorName)
	}

	authorPath := filepath.Join(l.basePath, AuthorsDir, authorName)

	// Load author/style.md
	stylePath := filepath.Join(authorPath, DefaultStyleFilename)
	if content, err := l.loadFileSafe(stylePath); err != nil {
		if !os.IsNotExist(err) {
			return Profile{}, warnings, err
		}
		// Author style.md is optional
	} else {
		profile.Style = content
	}

	// Load examples
	examplesPath := filepath.Join(authorPath, ExamplesDir)
	if exampleFiles, err := l.loadExamples(examplesPath, authorName+"/examples/"); err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("loading author %s examples: %v", authorName, err))
		}
	} else {
		for k, v := range exampleFiles {
			profile.Examples[k] = v
		}
		if len(exampleFiles) > 0 {
			profile.Metadata.ExampleFileCount = len(exampleFiles)
		}
	}

	// Only count as loaded if style.md or examples were found
	if profile.Style != "" || len(profile.Examples) > 0 {
		profile.Metadata.AuthorFileCount = 1
	}

	return profile, warnings, nil
}

// loadFileSafe loads a file with security constraints.
func (l *Loader) loadFileSafe(path string) (string, error) {
	// Resolve the absolute path and verify it's within the base path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrSecurity, path)
	}

	// Clean the path to remove any .. components
	cleanedPath := filepath.Clean(absPath)
	baseAbs, _ := filepath.Abs(l.basePath)

	// Ensure the cleaned path starts with the base path
	if !strings.HasPrefix(cleanedPath, baseAbs+string(os.PathSeparator)) && cleanedPath != baseAbs {
		return "", fmt.Errorf("%w: path traversal detected: %s", ErrSecurity, path)
	}

	info, err := os.Stat(cleanedPath)
	if err != nil {
		return "", err
	}

	// Check file size
	if info.Size() > l.maxFileSize {
		return "", fmt.Errorf("%w: %s (size: %d, limit: %d)",
			ErrMaxFileSize, cleanedPath, info.Size(), l.maxFileSize)
	}

	// Read file content
	content, err := os.ReadFile(cleanedPath)
	if err != nil {
		return "", err
	}

	// No template code execution - just return plain text
	return string(content), nil
}

// loadExamples loads example files from a directory.
func (l *Loader) loadExamples(examplesPath, relPrefix string) (map[string]string, error) {
	// Directory is optional
	if _, err := os.Stat(examplesPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Validate is a directory
	info, err := os.Stat(examplesPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", examplesPath)
	}

	// Find all example files
	var exampleFiles []string
	err = filepath.Walk(examplesPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Only process files, skip subdirectories
		if filepath.Ext(info.Name()) != "" {
			relPath, _ := filepath.Rel(examplesPath, path)
			exampleFiles = append(exampleFiles, relPath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(exampleFiles) == 0 {
		return nil, nil
	}

	// Load all examples, track line counts
	examples := make(map[string]string)
	var totalLines int

	for _, relPath := range exampleFiles {
		fullPath := filepath.Join(examplesPath, relPath)
		relPathWithPrefix := filepath.Join(relPrefix, relPath)

		content, err := l.loadFileSafe(fullPath)
		if err != nil {
			return nil, err
		}

		// Check line count for this file
		lines := strings.Count(content, "\n") + 1
		if lines > l.maxExamplesPerFile {
			return nil, fmt.Errorf("%w in %s: %d lines (max: %d)",
				ErrMaxExamples, relPathWithPrefix, lines, l.maxExamplesPerFile)
		}

		totalLines += lines
		if totalLines > l.maxExamplesTotal {
			return nil, fmt.Errorf("%w: %d total lines (max: %d)",
				ErrMaxExamples, totalLines, l.maxExamplesTotal)
		}

		examples[relPathWithPrefix] = content
	}

	return examples, nil
}

// buildCoreFactsPolicy constructs the immutable engine-level facts policy.
func buildCoreFactsPolicy() string {
	return `## Core Factual Safety Rules (Immutable)

These rules are enforced by the engine and cannot be disabled by any profile or override:

1. **Never introduce factual information absent from the dossier.** 
   You must not invent facts, statistics, dates, names, quotations, or context.

2. **Do not present contradicted or insufficiently evidenced claims as established facts.** 
   If a claim has verification status "contradicted" or "insufficient_evidence", 
   you must not state it as true.

3. **Frame uncertain material explicitly as uncertain.** 
   Claims with "uncertain" status must be presented with appropriate uncertainty 
   language (e.g., "according to...", "sources suggest...", "it appears that...").

4. **Preserve all names, dates, scores, and statistics exactly** 
   as they appear in verified claims. Do not round, approximate, or modify numerical values.

5. **Do not invent quotations.** 
   Never put words in quotes unless they come directly from the source evidence in the dossier.

6. **All factual claims must be traceable to verified evidence.** 
   Every significant factual statement should have an associated claim reference.

These rules are non-negotiable and apply to all output regardless of profile content.
`
}

// containsCoreFact checks if the profile content acknowledges core facts.
func containsCoreFact(content string) bool {
	// Simple heuristic: check for at least one of the core rules
	indicators := []string{
		"never introduce factual",
		"contradicted",
		"insufficient evidence",
		"uncertain",
		"verify",
		"evidence",
	}
	contentLower := strings.ToLower(content)
	for _, indicator := range indicators {
		if strings.Contains(contentLower, indicator) {
			return true
		}
	}
	return false
}

// mergeStyle merges two style strings, with later taking precedence.
func mergeStyle(base, override string) string {
	if override == "" {
		return base
	}
	if base == "" {
		return override
	}
	return base + "\n\n" + override
}

// mergeCoreFactPolicy merges core facts policy, preserving immutability.
func mergeCoreFactPolicy(defaults, override string) string {
	if override == "" {
		return defaults
	}
	if defaults == "" {
		return override
	}
	// Always keep the engine defaults as the foundation
	return defaults + "\n\n---\n\n" + override
}

// isValidAuthorName validates that an author name is safe to use in paths.
func isValidAuthorName(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}
	// Only allow alphanumeric, hyphen, underscore
	for _, c := range name {
		//nolint:staticcheck // This form is clearer than applying De Morgan's law
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
