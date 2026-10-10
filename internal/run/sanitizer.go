package run

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"
)

// Sanitizer is the single redaction boundary that data must cross before it
// is persisted.
//
// It combines two detection layers:
//
//  1. Exact values: secret values known at runtime (e.g. the resolved
//     OMLX_API_KEY, SEARXNG_API_KEY, or EMBEDDING_API_KEY) are replaced by
//     RedactedPlaceholder wherever they appear — in prose, in JSON, inside
//     URLs, embedded in longer strings. Exact-value replacement is the only
//     detection that applies to binary content (byte-level scanning).
//  2. Pattern-based detection: the RedactSecrets detectors (bearer tokens,
//     basic-auth tokens, assignments to secret-like keys, URL userinfo and
//     query credentials, PEM private-key blocks, and long high-entropy
//     token runs) apply to text content.
//
// The boundary is conservative by design: it errs on the safe side and may
// redact innocuous material that merely resembles a credential. It never
// logs, returns, or embeds the original secret value: Sanitize and
// SanitizeBytes return only sanitized output plus a replacement count.
//
// A nil *Sanitizer is a valid, pattern-only sanitizer (no exact values).
type Sanitizer struct {
	// exact holds the deduplicated, non-empty exact secret values, sorted
	// longest-first so that contained values are replaced before their
	// containers are (replacement of the longer value first is what makes
	// the count and the result unambiguous).
	exact []string
}

// NewSanitizer returns a sanitizer with the given exact secret values.
//
// Empty values are ignored, duplicates are removed, and the values are
// ordered longest-first. Passing no values (or using a zero-value / nil
// *Sanitizer) yields pattern-only detection.
func NewSanitizer(values ...string) *Sanitizer {
	s := &Sanitizer{}
	for _, v := range values {
		if v == "" {
			continue
		}
		s.addExact(v)
	}
	return s
}

// addExact records v, deduplicating and keeping the longest-first order.
func (s *Sanitizer) addExact(v string) {
	for _, e := range s.exact {
		if e == v {
			return
		}
	}
	s.exact = append(s.exact, v)
	sort.SliceStable(s.exact, func(i, j int) bool {
		return len(s.exact[i]) > len(s.exact[j])
	})
}

// ExactValues returns the configured exact secret values, longest first.
//
// SECURITY: the returned values are live credentials. They exist so that
// callers can audit or reuse the configuration; callers must never persist,
// log, or print them.
func (s *Sanitizer) ExactValues() []string {
	if s == nil || len(s.exact) == 0 {
		return nil
	}
	out := make([]string, len(s.exact))
	copy(out, s.exact)
	return out
}

// Sanitize returns s with every detected secret replaced by
// RedactedPlaceholder, together with the number of replacement operations
// performed (exact-value replacements plus pattern replacements).
//
// The result never contains the original secret values, and
// Sanitize(Sanitize(x)) == Sanitize(x): redaction is idempotent.
func (s *Sanitizer) Sanitize(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	out, exactCount := s.replaceExactText(text)
	out, patternCount := RedactSecretsCounted(out)
	return out, exactCount + patternCount
}

// SanitizeBytes returns data with every detected secret removed, together
// with the number of replacement operations performed.
//
// Text content (valid UTF-8, no NUL bytes) receives the full detection
// (exact values plus pattern detectors). Binary content receives exact-value
// replacement only, at the byte level: the pattern detectors are defined
// for text, so binary blobs without a configured exact value are not
// pattern-scanned. Binary blobs (images, databases, ...) are the only
// content class that may pass through with no change.
func (s *Sanitizer) SanitizeBytes(data []byte) (out []byte, count int) {
	if len(data) == 0 {
		return data, 0
	}
	if isText(data) {
		t, n := s.Sanitize(string(data))
		return []byte(t), n
	}
	return s.replaceExactBytes(data)
}

// CleanText reports whether text is free of detected secrets: it contains no
// configured exact value and does not match the pattern detectors.
//
// Persistence layers use it as the final gate after sanitization: content
// that is not clean is a sanitization failure and must not be written
// (fail closed).
func (s *Sanitizer) CleanText(text string) bool {
	if text == "" {
		return true
	}
	if !s.ContainsExactText(text) {
		return !LooksLikeSecret(text)
	}
	return false
}

// CleanBytes is the byte-level analogue of CleanText: text content is gated
// with the full detection; binary content is clean when it contains no
// configured exact value (pattern detection is not defined for binary).
func (s *Sanitizer) CleanBytes(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if isText(data) {
		return s.CleanText(string(data))
	}
	return !s.ContainsExactBytes(data)
}

// ContainsExactText reports whether text contains any configured exact
// secret value as a substring.
func (s *Sanitizer) ContainsExactText(text string) bool {
	for _, v := range s.exactValues() {
		if strings.Contains(text, v) {
			return true
		}
	}
	return false
}

// ContainsExactBytes reports whether data contains any configured exact
// secret value as a byte substring (works for any encoding that preserves
// the value's raw bytes, e.g. UTF-8 or base64-of-UTF-8 with padding
// aligned — but not UTF-16).
func (s *Sanitizer) ContainsExactBytes(data []byte) bool {
	for _, v := range s.exactValues() {
		if bytes.Contains(data, []byte(v)) {
			return true
		}
	}
	return false
}

// exactValues returns the configured values (nil-safe).
func (s *Sanitizer) exactValues() []string {
	if s == nil {
		return nil
	}
	return s.exact
}

// replaceExactText replaces every occurrence of every configured exact
// value with RedactedPlaceholder, counting the replacements. Values that
// equal the placeholder itself are a degenerate configuration: replacing
// them is a no-op and nothing is counted (the Clean gate still refuses to
// persist content that contains the value).
func (s *Sanitizer) replaceExactText(text string) (string, int) {
	count := 0
	for _, v := range s.exactValues() {
		n := strings.Count(text, v)
		if n == 0 {
			continue
		}
		text = strings.ReplaceAll(text, v, RedactedPlaceholder)
		if v != RedactedPlaceholder {
			count += n
		}
	}
	return text, count
}

// replaceExactBytes is the byte-level analogue of replaceExactText.
func (s *Sanitizer) replaceExactBytes(data []byte) ([]byte, int) {
	count := 0
	for _, v := range s.exactValues() {
		vb := []byte(v)
		n := bytes.Count(data, vb)
		if n == 0 {
			continue
		}
		data = bytes.ReplaceAll(data, vb, []byte(RedactedPlaceholder))
		if v != RedactedPlaceholder {
			count += n
		}
	}
	return data, count
}

// isText reports whether data is text content for the purposes of
// sanitization: valid UTF-8 without NUL bytes. Anything else (raw binary:
// images, SQLite blobs, gzip payloads, NUL-bearing byte strings) is binary.
func isText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) == -1
}
