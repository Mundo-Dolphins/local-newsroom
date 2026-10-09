package run

import (
	"strings"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	positive := []string{
		"api_key", "apiKey", "API_KEY", "token", "auth_token", "omlx_api_key",
		"omlx.token", "password", "Authorization", "client_secret", "credentials",
		"auth", "private_key", "accessToken", "omlx.auth",
	}
	for _, key := range positive {
		if !IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q) = false, want true", key)
		}
	}
	negative := []string{
		"model", "temperature", "topic", "timeout", "passphrase",
		"tokenize", "secretary", "authentication_mode", "base_url",
	}
	for _, key := range negative {
		if IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q) = true, want false", key)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bearer token",
			input: "Authorization: Bearer abc123def456",
			want:  "Authorization: Bearer [REDACTED]",
		},
		{
			name:  "api key assignment",
			input: "request failed: api_key = sk-abc123def456ghi",
			want:  "request failed: api_key = [REDACTED]",
		},
		{
			name:  "env style key",
			input: "omlx_api_key=secret123value",
			want:  "omlx_api_key=[REDACTED]",
		},
		{
			name:  "quoted value",
			input: `password: "hunter2secret"`,
			want:  `password: "[REDACTED]"`,
		},
		{
			name:  "url userinfo",
			input: "calling http://user:supersecret@localhost:8000/v1 now",
			want:  "calling http://user:[REDACTED]@localhost:8000/v1 now",
		},
		{
			name:  "url query param",
			input: "see http://api.example.com/v1?api_key=abc123def456&model=llama",
			want:  "see http://api.example.com/v1?api_key=[REDACTED]&model=llama",
		},
		{
			name:  "jwt token",
			input: "token expired: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XH0sc9bOBlV6c5TrPTRc",
			want:  "token expired: [REDACTED]",
		},
		{
			name:  "hex digest preserved",
			input: "digest 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 ok",
			want:  "digest 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 ok",
		},
		{
			name:  "plain prose preserved",
			input: "The EU AI Act enters into force in August 2025.",
			want:  "The EU AI Act enters into force in August 2025.",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "url without userinfo untouched",
			input: "see https://api.example.com/docs for details",
			want:  "see https://api.example.com/docs for details",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactSecrets(tc.input)
			if got != tc.want {
				t.Errorf("RedactSecrets(%q) = %q, want %q", tc.input, got, tc.want)
			}
			// The redacted output must never re-trigger the detectors.
			if LooksLikeSecret(got) {
				t.Errorf("redacted output %q still looks like a secret", got)
			}
			// Redaction must be idempotent.
			if again := RedactSecrets(got); again != got {
				t.Errorf("RedactSecrets is not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestRedactSecretsHighEntropyToken(t *testing.T) {
	// A long mixed-class random run in free text is treated as a credential.
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XH0sc9bOBlV6c5TrPTRc"
	if !LooksLikeSecret(jwt) {
		t.Errorf("a JWT should look like a secret")
	}
	got := RedactSecrets(jwt)
	if got != RedactedPlaceholder {
		t.Errorf("RedactSecrets(jwt) = %q, want %q", got, RedactedPlaceholder)
	}
	// A hex digest is long but single-class-ish: not a credential.
	hex := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	if LooksLikeSecret(hex) {
		t.Errorf("a hex digest should not look like a secret: %q", hex)
	}
	// Ordinary long words are untouched.
	if LooksLikeSecret("internationalization") {
		t.Error("ordinary long words must not be flagged")
	}
}

func TestRedactSecretsShortBearerUntouched(t *testing.T) {
	// A "Bearer" with a too-short value is not a credential.
	got := RedactSecrets("Authorization: Bearer abc")
	if got != "Authorization: Bearer abc" {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestNewModelMetadataRedactsEndpoint(t *testing.T) {
	mm := NewModelMetadata("omlx", "llama3.1:8b", "http://user:supersecret@localhost:8000/v1")
	if mm.Endpoint != "http://user:[REDACTED]@localhost:8000/v1" {
		t.Errorf("endpoint = %q", mm.Endpoint)
	}
	if err := mm.Validate(); err != nil {
		t.Errorf("a redacted endpoint must validate: %v", err)
	}
	if mm.Provider != "omlx" || mm.Model != "llama3.1:8b" {
		t.Errorf("provider/model not preserved: %+v", mm)
	}
}

func TestLooksLikeSecret(t *testing.T) {
	if LooksLikeSecret("") {
		t.Error("empty string is never a secret")
	}
	if !LooksLikeSecret("api_key: supersecretvalue") {
		t.Error("secret-like assignment must be flagged")
	}
	if LooksLikeSecret("api_key: [REDACTED]") {
		t.Error("an already-redacted assignment must not be flagged")
	}
	if LooksLikeSecret("the article discusses AI regulation in Europe") {
		t.Error("ordinary prose must not be flagged")
	}
}

func TestRedactSecretsMultipleAssignments(t *testing.T) {
	input := "api_key = firstsecret1, and token = secondsecret2, and model = llama"
	want := "api_key = [REDACTED], and token = [REDACTED], and model = llama"
	if got := RedactSecrets(input); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIsHighEntropy(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{strings.Repeat("a", 40), false},                                            // single class
		{strings.Repeat("12345678", 5), false},                                      // digits only
		{"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", false}, // hex digest
		{"aB3xK9mQ2vN8pR5tW1yZ4cD7fG0hJ6sL9zQ", true},                               // mixed classes, high entropy
		{"short", false}, // too short
	}
	for _, tc := range cases {
		if got := isHighEntropy(tc.s); got != tc.want {
			t.Errorf("isHighEntropy(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

func TestRedactSecretsQueryParamIdempotent(t *testing.T) {
	once := RedactSecrets("http://api.example.com/v1?api_key=abc123def456&model=llama")
	if !strings.Contains(once, "api_key=[REDACTED]&model=llama") {
		t.Fatalf("query redaction lost the following param: %q", once)
	}
	twice := RedactSecrets(once)
	if once != twice {
		t.Errorf("query-param redaction not idempotent: %q vs %q", once, twice)
	}
}
