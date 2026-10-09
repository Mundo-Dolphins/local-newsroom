package run

import (
	"math"
	"net/url"
	"regexp"
	"strings"
)

// RedactedPlaceholder is substituted for any value that looks like a
// credential. It is guaranteed to never match the secret detectors below, so
// redaction is idempotent.
const RedactedPlaceholder = "[REDACTED]"

// secretKeyWords are key/parameter names whose values must never be recorded.
// In this (normalized) form, " ?" marks a separator that may be a space.
var secretKeyWords = []string{
	`api ?keys?`,
	`access ?keys?`,
	`auth ?tokens?`,
	`tokens?`,
	`secrets?`,
	`passwords?`,
	`passwds?`,
	`authorizations?`,
	`credentials?`,
	`private ?keys?`,
	`auths?`,
}

// secretKeyRe matches a key name that may hold a credential, as a whole word
// of a normalized key name (separators replaced by spaces).
var secretKeyRe = regexp.MustCompile(`(?i)\b(` + strings.Join(secretKeyWords, "|") + `)\b`)

// keySepRe matches key separators that are treated as word boundaries.
var keySepRe = regexp.MustCompile(`[._-]`)

// camelCaseRe splits camelCase boundaries ("apiKey" -> "api Key").
var camelCaseRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// secretAssignKeyRe matches a credential-like key in free text, optionally
// prefixed by an identifier segment (e.g. "omlx_api_key" in an error string).
// It is derived from secretKeyWords with " ?" mapped to a separator class that
// allows '_', '-' or a space ("access key"), so underscore, dot and spaced
// forms are all recognized.
var secretAssignKeyRe = func() string {
	words := make([]string, len(secretKeyWords))
	for i, w := range secretKeyWords {
		words[i] = strings.ReplaceAll(w, " ?", "[-_ ]?")
	}
	return `(?i)\b(?:[a-z0-9]+[._-])?(?:` + strings.Join(words, "|") + `)\b`
}()

// secretAssignRe matches "key = value" / "key: value" assignments in free
// text whose key looks credential-like. The value must be a plausible token
// (>= 6 chars); a leading quote is allowed and any trailing quote stays in
// place (Go's RE2 engine has no backreferences, so quote matching is
// one-sided, which is fine: the value class simply stops at the closing
// quote).
//
// The value class deliberately excludes '&' so that redacting one query
// parameter of a URL never swallows the following parameters.
//
// Groups: 1 = separator, 2 = optional leading quote, 3 = value.
var secretAssignRe = regexp.MustCompile(
	secretAssignKeyRe + `(\s*[:=]\s*)([\"']?)([A-Za-z0-9._~+/=@!#$%^*-]{6,})`)

// bearerRe matches "Bearer <token>" authorization values.
var bearerRe = regexp.MustCompile(`(?i)\b(bearer)\s+([A-Za-z0-9._~+/=-]{6,})`)

// userinfoRe matches "scheme://user:password@host" basic-auth credentials.
var userinfoRe = regexp.MustCompile(`(\bhttps?://[^:/@\s]+):([^@/\s]+)@`)

// urlRe matches http(s) URLs in free text (trailing punctuation is handled
// by redactURLInText).
var urlRe = regexp.MustCompile(`\bhttps?://[^\s"']+`)

// tokenRunRe matches long alphanumeric runs that may be raw credentials
// (tokens, keys, JWT segments) embedded in error text.
var tokenRunRe = regexp.MustCompile(`[A-Za-z0-9._~+/=-]{32,}`)

// IsSecretKey reports whether a configuration key name looks like it may hold
// a credential (API key, token, password, ...).
//
// Key separators ('_', '-', '.') and camelCase boundaries are treated as word
// boundaries, so "omlx_api_key", "omlx.token" and "omlxAPIKey" are all
// recognized.
func IsSecretKey(key string) bool {
	normalized := keySepRe.ReplaceAllString(key, " ")
	normalized = camelCaseRe.ReplaceAllString(normalized, "$1 $2")
	return secretKeyRe.MatchString(normalized)
}

// RedactSecrets returns s with anything that looks like a credential
// replaced by RedactedPlaceholder.
//
// Detected and redacted:
//   - "Bearer <token>" authorization values
//   - "<secret-like-key> = <value>" / "<secret-like-key>: <value>" assignments
//   - basic-auth credentials in URLs (user:password@host)
//   - URL query parameters with secret-like names
//   - long high-entropy token runs (JWTs, random base64 keys, ...)
//
// Redaction is conservative (it errs on the safe side) and idempotent:
// RedactSecrets(RedactSecrets(s)) == RedactSecrets(s), and the result never
// matches LooksLikeSecret.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = bearerRe.ReplaceAllString(s, "$1 "+RedactedPlaceholder)
	s = redactSecretAssignments(s)
	s = userinfoRe.ReplaceAllString(s, "$1:"+RedactedPlaceholder+"@")
	s = redactURLInText(s)
	s = tokenRunRe.ReplaceAllStringFunc(s, func(run string) string {
		if isHighEntropy(run) {
			return RedactedPlaceholder
		}
		return run
	})
	return s
}

// redactSecretAssignments redacts the values of credential-like key/value
// assignments, e.g. "api_key = <token>" or "password: <value>".
//
// Values that already equal the redaction placeholder (or the "bearer" word,
// whose token was handled by the bearer pass) are left untouched, which keeps
// the overall redaction idempotent and free of doubled placeholders.
func redactSecretAssignments(s string) string {
	matches := secretAssignRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		valStart, valEnd := m[6], m[7]
		value := s[valStart:valEnd]
		if value == RedactedPlaceholder || strings.EqualFold(value, "bearer") {
			continue
		}
		b.WriteString(s[last:valStart])
		b.WriteString(RedactedPlaceholder)
		last = valEnd
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactURLInText redacts secret-like query parameters inside URLs found in s.
//
// Parameter values are replaced verbatim in the raw query string (no re-encoding),
// so the result still parses to the same URL and a second redaction pass is a
// no-op: the placeholder survives query parsing untouched.
func redactURLInText(s string) string {
	return urlRe.ReplaceAllStringFunc(s, func(raw string) string {
		trailing := ""
		for raw != "" {
			last := raw[len(raw)-1]
			if !strings.ContainsAny(".,;:!?)", string(last)) {
				break
			}
			trailing = string(last) + trailing
			raw = raw[:len(raw)-1]
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return raw + trailing
		}
		keys := make(map[string]bool)
		for key, values := range u.Query() {
			if !IsSecretKey(key) {
				continue
			}
			for _, v := range values {
				if v != RedactedPlaceholder {
					keys[key] = true
					break
				}
			}
		}
		if len(keys) == 0 {
			return raw + trailing
		}
		u.RawQuery = redactQueryPairs(u.RawQuery, keys)
		return u.String() + trailing
	})
}

// redactQueryPairs returns raw with the value of every parameter whose
// (decoded) name is in keys replaced by RedactedPlaceholder. Untouched pairs
// are preserved byte-for-byte, avoiding the percent-encoding that
// url.Values.Encode would apply to the placeholder brackets.
func redactQueryPairs(raw string, keys map[string]bool) string {
	pairs := strings.Split(raw, "&")
	changed := false
	for i, pair := range pairs {
		name, value := pair, ""
		if eq := strings.IndexByte(pair, '='); eq >= 0 {
			name = pair[:eq]
			value = pair[eq+1:]
		}
		decodedName, err := url.QueryUnescape(name)
		if err != nil || !keys[decodedName] {
			continue
		}
		decodedValue, err := url.QueryUnescape(value)
		if err == nil && decodedValue == RedactedPlaceholder {
			continue
		}
		pairs[i] = name + "=" + RedactedPlaceholder
		changed = true
	}
	if !changed {
		return raw
	}
	return strings.Join(pairs, "&")
}

// isHighEntropy reports whether a long token-like run is random enough to be
// treated as a credential.
//
// The run must use at least three character classes (lowercase, uppercase,
// digit, symbol) and have a Shannon entropy of at least 4.5 bits/character.
// This catches random base64/JWT material while leaving hex digests (single
// character class) and ordinary long words untouched.
func isHighEntropy(s string) bool {
	if len(s) < 32 {
		return false
	}
	classes := 0
	lower, upper, digit, symbol := false, false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= '0' && c <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	if lower {
		classes++
	}
	if upper {
		classes++
	}
	if digit {
		classes++
	}
	if symbol {
		classes++
	}
	if classes < 3 {
		return false
	}
	return shannonEntropy(s) >= 4.5
}

// shannonEntropy returns the Shannon entropy (bits per character) of s.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := make(map[byte]float64, 32)
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	total := float64(len(s))
	entropy := 0.0
	for _, count := range freq {
		p := count / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// LooksLikeSecret reports whether s appears to contain a raw credential.
//
// It reports true when RedactSecrets would change the string. It is a
// heuristic: it may flag innocuous text (safe side) but is the gate used to
// reject manifest fields that were not redacted before recording.
func LooksLikeSecret(s string) bool {
	return s != "" && RedactSecrets(s) != s
}
