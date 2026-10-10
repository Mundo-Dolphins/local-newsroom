package run

import (
	"strings"
	"testing"
)

func TestRedactSecretsBasicAuth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"basic token redacted, prefix preserved",
			"request failed: Authorization: Basic Zm9vYmFyYmF6Zm9vYmFy",
			"request failed: Authorization: Basic " + RedactedPlaceholder,
		},
		{
			"case and separator variants",
			"authorization=Basic aW5kZWVlZw==",
			"authorization=Basic " + RedactedPlaceholder,
		},
		{
			"short token left alone (below the 8-char minimum)",
			"Authorization: Basic ab",
			"Authorization: Basic ab",
		},
		{
			"prose about basic things untouched",
			"the basic approach works",
			"the basic approach works",
		},
	}
	for _, c := range cases {
		if got := RedactSecrets(c.in); got != c.want {
			t.Errorf("%s: RedactSecrets(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRedactSecretsPEMBlock(t *testing.T) {
	block := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----"
	out := RedactSecrets("key file: " + block + " end")
	want := "key file: " + RedactedPlaceholder + " end"
	if out != want {
		t.Errorf("PEM block not fully redacted: %q", out)
	}

	rsa := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7\n-----END RSA PRIVATE KEY-----"
	if got := RedactSecrets(rsa); got != RedactedPlaceholder {
		t.Errorf("RSA block not fully redacted: %q", got)
	}

	// Public keys are not credentials: untouched.
	pub := "-----BEGIN PUBLIC KEY-----\nMFwwDQYJKoZd\n-----END PUBLIC KEY-----"
	if got := RedactSecrets(pub); got != pub {
		t.Errorf("public key was redacted: %q", got)
	}
}

func TestRedactSecretsCounted(t *testing.T) {
	in := "log: Authorization: Bearer tok1234567890 then password = abc123def456 and " +
		"-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"
	out, n := RedactSecretsCounted(in)
	if n != 3 {
		t.Errorf("count = %d, want 3 (bearer + assignment + PEM): %q", n, out)
	}
	// Idempotent: a second pass finds nothing new.
	if _, n2 := RedactSecretsCounted(out); n2 != 0 {
		t.Errorf("second pass count = %d, want 0: %q", n2, out)
	}
	// A string with no credential material reports zero.
	if _, n3 := RedactSecretsCounted("the EU AI Act enters into force"); n3 != 0 {
		t.Errorf("count for benign text = %d, want 0", n3)
	}
}

// TestRedactSecretsFilePathNoFalsePositive pins the token-run class
// narrowing: ordinary filesystem paths (macOS temp roots, Linux XDG paths)
// must not be mistaken for raw tokens. Regression guard: the run manifest
// carries the workspace path, and the manifest gate must not reject ordinary
// runs because of it.
func TestRedactSecretsFilePathNoFalsePositive(t *testing.T) {
	paths := []string{
		"/var/folders/06/s_cvnk_x64z17lbwp3nkn6b00000gn/T/tmpAbC123/001",
		"/home/javi/.local/share/newsroom/workspace/runs/run-20260714T120000Z-e865cbb4262a",
		"sources/raw-1.md and output/article.md were written",
	}
	for _, p := range paths {
		if out := RedactSecrets(p); out != p {
			t.Errorf("path was modified by redaction: %q -> %q", p, out)
		}
		if LooksLikeSecret(p) {
			t.Errorf("LooksLikeSecret(%q) = true, want false", p)
		}
	}
}

// TestRedactSecretsBareRandomToken pins that long, slash-free random tokens
// are still caught after the token-run class narrowed (no '/').
func TestRedactSecretsBareRandomToken(t *testing.T) {
	token := "k7Qx9mW2vN8pR5tW1yZ4cD7fG0hJ3kL9mP2xQ7vW" // 40 chars, three classes
	out := RedactSecrets("token " + token + " found in log")
	if strings.Contains(out, token) {
		t.Errorf("bare random token was not redacted: %q", out)
	}
	// A single-class run of the same length is not a credential.
	word := "aessiondessionlizationinternationalization"
	if got := RedactSecrets(word); got != word {
		t.Errorf("single-class run was redacted: %q", got)
	}
}
