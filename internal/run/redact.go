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

// basicAuthRe matches "Authorization: Basic <token>" values. The token is
// base64 userinfo; it is a credential regardless of its length, so the
// minimum is lower than the bearer pass. The "authorization" context word
// keeps ordinary prose about "basic" things untouched.
var basicAuthRe = regexp.MustCompile(`(?i)\b(authorization)\s*([:=])\s*(basic)\s+([A-Za-z0-9+/=._~-]{8,})`)

// pemKeyRe matches a PEM-encoded private key block (RSA, EC, DSA, PKCS#8,
// OpenSSH, PGP, ...): the whole block, header line to footer line, is
// replaced, because the base64 body alone does not identify the block as a
// private key and partial redaction would leave header/footer markers and
// partial key material.
var pemKeyRe = regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

// userinfoRe matches "scheme://user:password@host" basic-auth credentials.
var userinfoRe = regexp.MustCompile(`(\bhttps?://[^:/@\s]+):([^@/\s]+)@`)

// urlRe matches http(s) URLs in free text (trailing punctuation is handled
// by redactURLInText).
var urlRe = regexp.MustCompile(`\bhttps?://[^\s"']+`)

// tokenRunRe matches long alphanumeric runs that may be raw credentials
// (tokens, keys, JWT segments) embedded in free text. '/' is excluded from
// the run class: it is a filesystem path separator, and a path such as
// /var/folders/... would otherwise form one high-entropy "token" and false-
// positive. Base64 segments split by '/' are still caught when each segment
// is long enough on its own, and slash-bearing credential material in the
// realistic forms (Bearer tokens, key assignments, URL queries, PEM blocks)
// is covered by the dedicated detectors above.
var tokenRunRe = regexp.MustCompile(`[A-Za-z0-9._~+=-]{32,}`)

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
//   - "Authorization: Basic <token>" values
//   - PEM private key blocks (BEGIN/END PRIVATE KEY, whole block)
//   - "<secret-like-key> = <value>" / "<secret-like-key>: <value>" assignments
//   - basic-auth credentials in URLs (user:password@host)
//   - URL query parameters with secret-like names
//   - long high-entropy token runs (JWTs, random base64 keys, ...)
//
// Redaction is conservative (it errs on the safe side) and idempotent:
// RedactSecrets(RedactSecrets(s)) == RedactSecrets(s), and the result never
// matches LooksLikeSecret.
func RedactSecrets(s string) string {
	out, _ := RedactSecretsCounted(s)
	return out
}

// RedactSecretsCounted is RedactSecrets together with the number of
// replacement operations performed (one per redacted match). The count is
// metadata for redaction records; it counts substitutions, not characters.
//
// The result of every pass never re-matches a detector (already-redacted
// values are skipped), so the count is stable: RedactSecretsCounted applied
// to its own output performs zero further replacements.
func RedactSecretsCounted(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	count := 0

	// PEM private key blocks first: the base64 body would otherwise be
	// eaten piecemeal by the token-run pass, leaving header/footer lines.
	for _, m := range pemKeyRe.FindAllString(s, -1) {
		if m != RedactedPlaceholder {
			count++
		}
	}
	s = pemKeyRe.ReplaceAllString(s, RedactedPlaceholder)

	// Bearer tokens: count only matches whose token was not already redacted.
	for _, m := range bearerRe.FindAllStringSubmatchIndex(s, -1) {
		if s[m[4]:m[5]] != RedactedPlaceholder { // group 2 = the token
			count++
		}
	}
	s = bearerRe.ReplaceAllString(s, "$1 "+RedactedPlaceholder)

	// Basic-auth tokens: same counting rule as bearer.
	for _, m := range basicAuthRe.FindAllStringSubmatchIndex(s, -1) {
		if s[m[8]:m[9]] != RedactedPlaceholder { // group 4 = the token
			count++
		}
	}
	s = replaceMatches(s, basicAuthRe, func(m []int, src string) string {
		// Preserve everything up to the token (scheme word and separator
		// exactly as written); only the token itself is replaced.
		return src[m[0]:m[8]] + RedactedPlaceholder
	})

	s, n := redactSecretAssignmentsCounted(s)
	count += n

	for _, m := range userinfoRe.FindAllStringSubmatchIndex(s, -1) {
		if s[m[4]:m[5]] != RedactedPlaceholder { // group 2 = the password
			count++
		}
	}
	s = userinfoRe.ReplaceAllString(s, "$1:"+RedactedPlaceholder+"@")

	s, n = redactURLInTextCounted(s)
	count += n

	s = tokenRunRe.ReplaceAllStringFunc(s, func(run string) string {
		if isHighEntropy(run) && run != RedactedPlaceholder {
			count++
			return RedactedPlaceholder
		}
		return run
	})
	return s, count
}

// replaceMatches rewrites every full match of re using fn, which receives
// the SubmatchIndex of one match and the source string and returns the
// replacement. Non-overlapping, left-to-right.
func replaceMatches(s string, re *regexp.Regexp, fn func(m []int, src string) string) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		b.WriteString(fn(m, s))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactSecretAssignmentsCounted redacts the values of credential-like
// key/value assignments, e.g. "api_key = <token>" or "password: <value>",
// and returns the count of the values actually replaced.
//
// Values that already equal the redaction placeholder (or the "bearer" word,
// whose token was handled by the bearer pass) are left untouched, which keeps
// the overall redaction idempotent and free of doubled placeholders;
// already-redacted values are not counted, which keeps repeated application
// at zero count.
func redactSecretAssignmentsCounted(s string) (string, int) {
	matches := secretAssignRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, 0
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	count := 0
	for _, m := range matches {
		valStart, valEnd := m[6], m[7]
		value := s[valStart:valEnd]
		if value == RedactedPlaceholder || strings.EqualFold(value, "bearer") {
			continue
		}
		b.WriteString(s[last:valStart])
		b.WriteString(RedactedPlaceholder)
		last = valEnd
		count++
	}
	b.WriteString(s[last:])
	return b.String(), count
}

// redactURLInTextCounted redacts secret-like query parameters inside URLs
// found in s, and returns the count of the query parameter values actually
// redacted.
//
// Parameter values are replaced verbatim in the raw query string (no re-encoding),
// so the result still parses to the same URL and a second redaction pass is a
// no-op: the placeholder survives query parsing untouched.
func redactURLInTextCounted(s string) (string, int) {
	var count int
	out := urlRe.ReplaceAllStringFunc(s, func(raw string) string {
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
		q, n := redactQueryPairsCounted(u.RawQuery, keys)
		count += n
		u.RawQuery = q
		return u.String() + trailing
	})
	return out, count
}

// redactQueryPairs returns raw with the value of every parameter whose
// (decoded) name is in keys replaced by RedactedPlaceholder. Untouched pairs
// are preserved byte-for-byte, avoiding the percent-encoding that
// url.Values.Encode would apply to the placeholder brackets.
func redactQueryPairs(raw string, keys map[string]bool) string {
	out, _ := redactQueryPairsCounted(raw, keys)
	return out
}

// redactQueryPairsCounted is redactQueryPairs with a count of the values
// actually redacted (already-redacted values are not counted).
func redactQueryPairsCounted(raw string, keys map[string]bool) (string, int) {
	pairs := strings.Split(raw, "&")
	count := 0
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
		count++
	}
	if !changed {
		return raw, 0
	}
	return strings.Join(pairs, "&"), count
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
