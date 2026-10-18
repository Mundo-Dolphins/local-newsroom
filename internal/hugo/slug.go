package hugo

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// slugMaxLen bounds the alphabetic part of a generated slug (in runes).
const slugMaxLen = 80

// slugHashLen is the length of the disambiguating hash suffix appended when
// a slug is truncated (hex characters).
const slugHashLen = 12

// Slugify derives a deterministic, filesystem-safe slug from a title.
//
// Rules:
//
//   - Unicode-aware case folding (Spanish, German, Turkish-adjacent titles
//     slugify without surprise: "Los Dolphins" -> "los-dolphins").
//   - Unicode letters and digits are kept, so accented and CJK titles keep
//     their characters ("cañón" -> "cañón").
//   - Every other character class (spaces, punctuation, symbols) collapses
//     to a single "-"; leading and trailing dashes are trimmed.
//   - Slugs longer than slugMaxLen runes are truncated on a word boundary
//     and suffixed with hashSlugHex(title), so two titles sharing a long
//     prefix still map to different slugs.
//   - A title with no letters or digits at all (e.g. pure emoji or
//     punctuation) yields "slug-<16 hex chars of the title hash>" — never
//     empty, never a traversal.
//
// The same input always yields the same slug; the function never reads the
// environment, the clock, or random state.
func Slugify(title string) string {
	runes := make([]rune, 0, len(title))
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			runes = append(runes, r)
			lastDash = false
		case len(runes) > 0 && !lastDash:
			// Any non-alphanumeric after content becomes one dash. (A
			// leading separator adds nothing: no rune was emitted yet.)
			runes = append(runes, '-')
			lastDash = true
		}
	}
	s := strings.TrimRight(string(runes), "-")
	if s == "" {
		return "slug-" + hashSlugHex(title, 16)
	}
	if len([]rune(s)) > slugMaxLen {
		s = truncateRunes(s, slugMaxLen)
		s = strings.TrimRight(s, "-")
		s += "-" + hashSlugHex(title, slugHashLen)
	}
	return s
}

// truncateRunes cuts s to at most n runes, preferring word boundaries ("-"
// is the only separator a slug contains): whole words are kept while the
// total still fits, so the cut never splits a word. A single word longer
// than the bound falls back to a hard cut. It never splits a rune.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	var out []rune
	for i := 0; i < len(runes); {
		j := i
		for j < len(runes) && runes[j] != '-' {
			j++
		}
		word := runes[i:j]
		cost := len(word)
		if len(out) > 0 {
			cost++ // the separating dash counts toward the bound
		}
		if len(out)+cost > n {
			break
		}
		if len(out) > 0 {
			out = append(out, '-')
		}
		out = append(out, word...)
		i = j + 1
	}
	if len(out) == 0 {
		// The first word alone exceeds the bound: hard cut.
		return string(runes[:n])
	}
	return string(out)
}

// hashSlugHex returns the first n hex characters of the SHA-256 of title.
func hashSlugHex(title string, n int) string {
	sum := sha256.Sum256([]byte(title))
	return hex.EncodeToString(sum[:])[:n]
}
