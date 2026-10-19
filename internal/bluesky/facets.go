package bluesky

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// urlTrailPunct is the trailing prose punctuation that belongs to the
// sentence, not the URL. Parentheses are deliberately NOT in this set:
// they are balanced first (a balanced "(...)" pair may be part of a URL),
// and an unmatched closing paren is cut by balanceParens.
const urlTrailPunct = ".,;:!?'\"<>"

// linkFacets generates link facets for every http(s) URL in text, with
// UTF-8 byte offsets — the byte positions the post record will carry.
//
// The scan is deterministic and local: it walks the text's bytes left to
// right, extends a match over URL-safe ASCII, trims trailing prose
// punctuation, and validates the candidate with net/url. Multi-byte UTF-8
// sequences are never inside a URL (a URL is ASCII by construction), so
// the byte offsets are exact: text[byteStart:byteEnd] is the link itself.
//
// covered lists explicit facets the caller supplied (already validated);
// URLs whose span is covered by one are not auto-linked twice.
func linkFacets(text string, covered []social.Facet) []social.Facet {
	b := []byte(text)
	var out []social.Facet

	i := 0
	for i < len(b) && len(out) < maxFacets {
		start := indexOfURLStart(b, i)
		if start < 0 {
			break
		}
		end := extendURL(b, start)
		if end <= start {
			i = start + 1
			continue
		}
		candidate := string(b[start:end])
		// Parenthesis balancing runs BEFORE the punctuation trim: a
		// balanced "(...)" pair stays (it may be part of the URL), an
		// unmatched ")" is cut, and only then is plain trailing
		// punctuation ("...a.", "...a,") trimmed.
		candidate = strings.TrimRight(balanceParens(candidate), urlTrailPunct)
		if u, ok := parseLinkURL(candidate); ok && !coveredBy(b, start, start+len(candidate), covered) {
			out = append(out, social.Facet{
				ByteStart: start,
				ByteEnd:   start + len(candidate),
				Features: []social.Feature{{
					Type:   social.FeatureLink,
					Fields: map[string]string{"uri": u},
				}},
			})
			i = start + len(candidate)
			continue
		}
		i = start + 1
	}
	return out
}

// indexOfURLStart finds the earliest "http://" or "https://" at or after
// i (the two literals do not overlap as substrings, so the earlier of the
// two searches wins).
func indexOfURLStart(b []byte, i int) int {
	best := -1
	for _, lit := range []string{"http://", "https://"} {
		if j := bytes.Index(b[i:], []byte(lit)); j >= 0 && (best < 0 || i+j < best) {
			best = i + j
		}
	}
	return best
}

// urlSafe reports whether a byte may appear inside a URL (RFC 3986:
// unreserved, sub-delims, gen-delims — printable ASCII only, which keeps
// the match out of multi-byte UTF-8 sequences).
func urlSafe(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-_.~?!#$&'()*+,;=%:@/[]", c) >= 0
}

// extendURL extends a URL match left to right while the bytes are URL-safe.
func extendURL(b []byte, start int) int {
	end := start
	for end < len(b) && urlSafe(b[end]) {
		end++
	}
	return end
}

// balanceParens drops unbalanced trailing closing parentheses: a URL like
// "https://example.com/a(b))" keeps "a(b)".
func balanceParens(s string) string {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				// Unmatched closing paren: prose, cut here.
				return s[:i]
			}
			depth--
		}
	}
	if depth > 0 {
		// More openings than closings: the trailing "(...)" prose
		// belongs to the sentence, not the URL — drop from the last
		// unmatched opening onward.
		for i := len(s) - 1; i >= 0; i-- {
			switch s[i] {
			case '(':
				depth--
				if depth < 0 {
					return s[:i]
				}
			case ')':
				depth++
			}
		}
	}
	return s
}

// parseLinkURL validates a candidate URL: absolute http(s), a host, no
// userinfo. It returns the canonical link URI.
func parseLinkURL(candidate string) (string, bool) {
	u, err := url.Parse(candidate)
	if err != nil || u.User != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return candidate, true
}

// coveredBy reports whether the span [start,end) falls inside any of the
// covered facets.
func coveredBy(b []byte, start, end int, covered []social.Facet) bool {
	for _, f := range covered {
		if start >= f.ByteStart && end <= f.ByteEnd {
			return true
		}
	}
	return false
}
