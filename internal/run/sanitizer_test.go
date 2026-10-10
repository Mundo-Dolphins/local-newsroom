package run

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewSanitizerNormalizesValues(t *testing.T) {
	s := NewSanitizer("", "short", "", "longer-value", "short")
	got := s.ExactValues()
	// Deduplicated, non-empty, longest first; equal lengths keep input order.
	want := []string{"longer-value", "short"}
	if len(got) != len(want) {
		t.Fatalf("ExactValues = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExactValues = %v, want %v", got, want)
		}
	}
}

func TestSanitizeExactValueInBareProse(t *testing.T) {
	// "opensesame" is below the pattern detection bar (short, two classes,
	// no key context): only the exact-value path can catch it.
	secret := "opensesame"
	text := "the passphrase in the note was opensesame and nothing else"

	// Pattern-only sanitizer leaves it alone (documented detection boundary).
	zero := &Sanitizer{}
	if out, _ := zero.Sanitize(text); out != text {
		t.Errorf("pattern-only sanitizer modified benign text: %q", out)
	}
	var nilSan *Sanitizer
	if out, _ := nilSan.Sanitize(text); out != text {
		t.Errorf("nil sanitizer modified benign text: %q", out)
	}

	s := NewSanitizer(secret)
	out, n := s.Sanitize(text)
	if strings.Contains(out, secret) {
		t.Errorf("exact-value sanitizer left the secret in output: %q", out)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	if !strings.Contains(out, RedactedPlaceholder) {
		t.Errorf("output missing placeholder: %q", out)
	}
}

func TestSanitizeMultipleOccurrences(t *testing.T) {
	s := NewSanitizer("tok-1")
	out, n := s.Sanitize("tok-1 mid tok-1 end tok-1")
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	if strings.Count(out, RedactedPlaceholder) != 3 {
		t.Errorf("placeholder count = %d, want 3: %q", strings.Count(out, RedactedPlaceholder), out)
	}
}

func TestSanitizeIdempotent(t *testing.T) {
	s := NewSanitizer("opensesame", "k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW")
	in := "one: opensesame two: k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW three: Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dGVzb25vbmRldG9rZW50dmFsdWU"
	out1, n1 := s.Sanitize(in)
	if n1 == 0 {
		t.Fatalf("first pass performed no replacements")
	}
	out2, n2 := s.Sanitize(out1)
	if out2 != out1 {
		t.Errorf("redaction is not idempotent:\n%q\n%q", out1, out2)
	}
	if n2 != 0 {
		t.Errorf("second pass count = %d, want 0", n2)
	}
}

func TestSanitizeNeverEchoesSecret(t *testing.T) {
	secrets := []string{
		"opensesame",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dGVzb25vbmRldG9rZW50dmFsdWU",
		"password: S3crystals9",
		"k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW",
	}
	for _, secret := range secrets {
		s := NewSanitizer(secrets...)
		out, _ := s.Sanitize("x " + secret + " y")
		if strings.Contains(out, secret) {
			t.Errorf("sanitized output still contains the secret: %q", out)
		}
	}
}

func TestSanitizeBytesTextAndBinary(t *testing.T) {
	s := NewSanitizer("opensesame")

	// Text bytes get the full treatment.
	text := []byte("note: Authorization: Bearer k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW")
	out, n := s.SanitizeBytes(text)
	if n == 0 {
		t.Errorf("text content was not redacted")
	}
	if bytes.Contains(out, []byte("k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW")) {
		t.Errorf("bearer token survived: %q", out)
	}

	// Binary bytes get exact-value replacement only.
	bin := append([]byte{0x00}, []byte("opensesame")...)
	bin = append(bin, 0xff)
	out, n = s.SanitizeBytes(bin)
	if n != 1 {
		t.Errorf("binary count = %d, want 1", n)
	}
	if bytes.Contains(out, []byte("opensesame")) {
		t.Errorf("exact value survived in binary: % x", out)
	}
	if out[0] != 0x00 || out[len(out)-1] != 0xff {
		t.Errorf("binary framing was damaged: % x", out)
	}

	// Binary without any configured value passes through untouched.
	plain := []byte{0x00, 0x01, 0x02, 0xff}
	out, n = s.SanitizeBytes(plain)
	if n != 0 || !bytes.Equal(out, plain) {
		t.Errorf("binary without exact values was modified: % x (n=%d)", out, n)
	}
}

func TestSanitizeEdgeInputs(t *testing.T) {
	s := NewSanitizer("opensesame")
	// Empty inputs are clean no-ops.
	if out, n := s.Sanitize(""); out != "" || n != 0 {
		t.Errorf("Sanitize(\"\") = %q, %d", out, n)
	}
	if out, n := s.SanitizeBytes(nil); out != nil || n != 0 {
		t.Errorf("SanitizeBytes(nil) = %v, %d", out, n)
	}
	if !s.CleanText("") || !s.CleanBytes(nil) || !s.CleanBytes([]byte{}) {
		t.Error("empty input must be clean")
	}
	// A nil sanitizer is pattern-only and reports no exact values.
	var nilSan *Sanitizer
	if got := nilSan.ExactValues(); got != nil {
		t.Errorf("nil ExactValues = %v, want nil", got)
	}
	if !s.CleanText("clean prose") {
		t.Error("clean prose must be clean")
	}
	if s.ContainsExactText("") || s.ContainsExactBytes(nil) {
		t.Error("empty input must not contain exact values")
	}
}

func TestCleanGates(t *testing.T) {
	s := NewSanitizer("opensesame")
	cases := []struct {
		name string
		in   string
		want bool // clean?
	}{
		{"clean prose", "the EU AI Act enters into force", true},
		{"bare exact value", "value opensesame here", false},
		{"bearer token", "Authorization: Bearer k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW", false},
		{"pem block", "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----", false},
		{"redacted placeholder", "Authorization: Bearer " + RedactedPlaceholder, true},
	}
	for _, c := range cases {
		if got := s.CleanText(c.in); got != c.want {
			t.Errorf("CleanText(%q) = %v, want %v", c.name, got, c.want)
		}
	}

	// Byte gates: text vs binary.
	if s.CleanBytes([]byte("value opensesame here")) {
		t.Error("CleanBytes reported text with exact value as clean")
	}
	if s.CleanBytes([]byte("clean bytes \x01\x02")) == false {
		t.Error("CleanBytes reported clean text as dirty")
	}
	bbin := append([]byte{0x00}, []byte("opensesame")...)
	if s.CleanBytes(bbin) {
		t.Error("CleanBytes reported binary containing exact value as clean")
	}
	if !s.CleanBytes([]byte{0x00, 0x01, 0xff}) {
		t.Error("CleanBytes reported binary without exact values as dirty")
	}
	if !s.CleanBytes(nil) || !s.CleanBytes([]byte{}) {
		t.Error("CleanBytes of empty data must be clean")
	}
}

func TestSanitizeSanitizesItsOwnGate(t *testing.T) {
	// The exhaustiveness invariant the persistence boundary relies on: after
	// Sanitize, the bytes must pass the Clean gate. If a future detector is
	// added to CleanText/LooksLikeSecret but not to the Sanitize pipeline,
	// this test fails and the mismatch is caught at the unit level.
	secrets := []string{
		"opensesame",
		"k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW",
		"password: S3crystals9",
		"Authorization: Basic Zm9vYmFyYmF6Zm9vYmFy",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7\n-----END RSA PRIVATE KEY-----",
		"https://user:S3cret@host/path?api_key=abcdef123456",
	}
	for _, in := range secrets {
		s := NewSanitizer(secrets...)
		out, _ := s.Sanitize(in)
		if !s.CleanText(out) {
			t.Errorf("Sanitize output still fails the clean gate for input %q:\n%q", in, out)
		}
	}
}
