package publication

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

func TestReceiptIDDerivation(t *testing.T) {
	a := NewReceiptID(pubID("a"), TargetID("t1"), "rec-1", tAt(5))
	b := NewReceiptID(pubID("a"), TargetID("t1"), "rec-1", tAt(5))
	if a != b {
		t.Error("same inputs must derive the same receipt ID")
	}
	if a == "" {
		t.Fatal("derived receipt ID must not be empty")
	}
	if err := a.Validate(); err != nil {
		t.Errorf("derived receipt ID should validate: %v", err)
	}
	if !strings.HasPrefix(string(a), ReceiptIDPrefix) {
		t.Errorf("receipt ID %q must start with %q", a, ReceiptIDPrefix)
	}

	if c := NewReceiptID(pubID("a"), TargetID("t1"), "rec-2", tAt(5)); c == a {
		t.Error("different external references must derive different IDs")
	}
	if c := NewReceiptID(pubID("a"), TargetID("t2"), "rec-1", tAt(5)); c == a {
		t.Error("different targets must derive different IDs")
	}
	if c := NewReceiptID(pubID("b"), TargetID("t1"), "rec-1", tAt(5)); c == a {
		t.Error("different plans must derive different IDs")
	}
}

// pubID is a synthetic but well-formed publication ID for fixtures.
func pubID(s string) PublicationID {
	return PublicationID(deriveID(PublicationIDPrefix, testTime, "run-x", s))
}

func TestReceiptIDValidate(t *testing.T) {
	if err := ReceiptID("nope").Validate(); err == nil {
		t.Error("a wrong prefix must be rejected")
	}
	if err := ReceiptID(ReceiptIDPrefix).Validate(); err == nil {
		t.Error("an empty body must be rejected")
	}
	long := ReceiptID(ReceiptIDPrefix + strings.Repeat("x", 200))
	if err := long.Validate(); err == nil {
		t.Error("an overlong body must be rejected")
	}
	bad := ReceiptID(ReceiptIDPrefix + "20260711T100005-" + strings.Repeat("a", 12) + " bad")
	if err := bad.Validate(); err == nil {
		t.Error("invalid characters must be rejected")
	}
}

// baseReceipt returns a valid receipt for fixture assertions.
func baseReceipt() *PublicationReceipt {
	return NewReceipt(
		pubID("plan-a"), TargetID("bluesky-thread"), "bluesky",
		"https://bsky.app/profile/dolphins.example/post/1",
		"3luchk2f5zrq4c5n3b2v7xjy6h8d9m0a1k4e6t7u8i9o0p1q2r3s",
		digestOf(briefA), "idem-7f3a", "bluesky-publisher/0.6.0",
		map[string]string{"posts": "5"}, tAt(30),
	)
}

func TestNewReceipt(t *testing.T) {
	r := baseReceipt()
	if err := r.Validate(); err != nil {
		t.Fatalf("a well-formed receipt must validate: %v", err)
	}
	if r.ID == "" || r.PlanID == "" || r.TargetID == "" {
		t.Error("identity fields must be set")
	}
	if r.ExternalURI == "" || r.ExternalRecordID == "" {
		t.Error("external references must be preserved")
	}

	// Free-text values are redacted on the way in.
	secret := testToken
	r2 := NewReceipt(pubID("plan-a"), TargetID("t"), "bluesky",
		"https://bsky.app/post/1?"+secret, secret,
		digestOf(briefA), "key "+secret, "v1", nil, tAt(30))
	if !strings.Contains(r2.ExternalURI, run.RedactedPlaceholder) {
		t.Errorf("secret in the URI must be redacted, got %q", r2.ExternalURI)
	}
	if !strings.Contains(r2.ExternalRecordID, run.RedactedPlaceholder) {
		t.Errorf("secret record ID must be redacted, got %q", r2.ExternalRecordID)
	}
	if !strings.Contains(r2.IdempotencyKey, run.RedactedPlaceholder) {
		t.Errorf("secret idempotency key must be redacted, got %q", r2.IdempotencyKey)
	}

	// The external reference for ID derivation falls back URI -> digest.
	withRecord := NewReceipt(pubID("p"), TargetID("t"), "bluesky", "https://x.example/1", "rec-1", digestOf(briefA), "", "", nil, tAt(1))
	noRecord := NewReceipt(pubID("p"), TargetID("t"), "bluesky", "https://x.example/1", "", digestOf(briefA), "", "", nil, tAt(1))
	uriOnly := NewReceipt(pubID("p"), TargetID("t"), "bluesky", "", "", digestOf(briefA), "", "", nil, tAt(1))
	if withRecord.ID == noRecord.ID {
		t.Error("a record ID must influence the derived receipt ID")
	}
	if noRecord.ID == uriOnly.ID {
		t.Error("the external URI must influence the derived receipt ID")
	}
}

func TestReceiptValidate(t *testing.T) {
	r := baseReceipt()
	if err := r.Validate(); err != nil {
		t.Fatalf("base receipt must be valid: %v", err)
	}

	cases := map[string]func(r *PublicationReceipt) func(){
		"no external references": func(r *PublicationReceipt) func() {
			r.ExternalURI, r.ExternalRecordID = "", ""
			return func() {
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "external URI or an external record ID") {
					t.Errorf("want a missing-references error, got %v", err)
				}
			}
		},
		"credential in URI": func(r *PublicationReceipt) func() {
			r.ExternalURI = "https://user:pass@example.example/post"
			return func() {
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "credentials") {
					t.Errorf("want a credentials error, got %v", err)
				}
			}
		},
		"non-http URI": func(r *PublicationReceipt) func() {
			r.ExternalURI = "ftp://example.example/post"
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a scheme error, got none")
				}
			}
		},
		"relative URI": func(r *PublicationReceipt) func() {
			r.ExternalURI = "/post"
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a URL error, got none")
				}
			}
		},
		"bad record ID": func(r *PublicationReceipt) func() {
			r.ExternalRecordID = "bad id with spaces"
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a record ID error, got none")
				}
			}
		},
		"secret record ID": func(r *PublicationReceipt) func() {
			r.ExternalRecordID = testToken
			return func() {
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "secret-looking") {
					t.Errorf("want a secret error, got %v", err)
				}
			}
		},
		"bad digest": func(r *PublicationReceipt) func() {
			r.ContentDigest = Digest("sha256:zz")
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a digest error, got none")
				}
			}
		},
		"bad id": func(r *PublicationReceipt) func() {
			r.ID = ReceiptID("nope")
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want an ID error, got none")
				}
			}
		},
		"bad provider": func(r *PublicationReceipt) func() {
			r.Provider = "Bad Provider"
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a provider error, got none")
				}
			}
		},
		"bad idempotency key": func(r *PublicationReceipt) func() {
			r.IdempotencyKey = "key with spaces"
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want an idempotency error, got none")
				}
			}
		},
		"bad client version": func(r *PublicationReceipt) func() {
			r.ClientVersion = "v 1.2"
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a client version error, got none")
				}
			}
		},
		"too much metadata": func(r *PublicationReceipt) func() {
			meta := make(map[string]string)
			for i := 0; i < maxReceiptMetadata+1; i++ {
				meta[string(rune('a'+i%26))+string(rune('0'+i/26))] = "v"
			}
			r.Metadata = meta
			return func() {
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "metadata") {
					t.Errorf("want a metadata bound error, got %v", err)
				}
			}
		},
		"secret metadata value": func(r *PublicationReceipt) func() {
			r.Metadata = map[string]string{"token": testToken}
			return func() {
				if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "secret-looking") {
					t.Errorf("want a secret metadata error, got %v", err)
				}
			}
		},
		"bad metadata key": func(r *PublicationReceipt) func() {
			r.Metadata = map[string]string{"bad key!": "v"}
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a metadata key error, got none")
				}
			}
		},
		"long metadata value": func(r *PublicationReceipt) func() {
			r.Metadata = map[string]string{"k": strings.Repeat("x", maxReceiptMetaValLen+1)}
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a metadata length error, got none")
				}
			}
		},
		"missing published at": func(r *PublicationReceipt) func() {
			r.PublishedAt = run.Timestamp{}
			return func() {
				if err := r.Validate(); err == nil {
					t.Errorf("want a timestamp error, got none")
				}
			}
		},
		"nil receipt": nil,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if mutate == nil {
				if err := (*PublicationReceipt)(nil).Validate(); err == nil {
					t.Error("a nil receipt must not validate")
				}
				return
			}
			r := *baseReceipt()
			mutate(&r)()
		})
	}
}

func TestValidateAbsoluteURL(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true}, // optional
		{"https://bsky.app/post/1", true},
		{"http://example.com/a", true},
		{"HTTPS://EXAMPLE.COM/A", true}, // scheme is case-insensitive
		{"https://example.com:8080/a", true},
		{"https://user:pass@example.com/", false},
		{"https://user@example.com/", true},                                 // a username without a password is not a credential
		{"https://user:" + run.RedactedPlaceholder + "@example.com/", true}, // redacted
		{"ftp://example.com/a", false},
		{"example.com/a", false}, // relative
		{"not a url at all with : in", false},
		{strings.Repeat("x", maxReceiptURILen+1), false},
	}
	for _, c := range cases {
		err := validateAbsoluteURL("external uri", c.value, maxReceiptURILen)
		if (err == nil) != c.want {
			t.Errorf("validateAbsoluteURL(%q) error = %v, want valid=%t", c.value, err, c.want)
		}
	}
}

func TestValidateCredentialFree(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"a handle, path or slug", true},
		{"at://com.example/app/bsky.feed.post/x", true},
		{"git:ssh://example.com/repo.git", true},
		{"https://user:pass@example.com/", false},                           // live credential
		{"https://user:" + run.RedactedPlaceholder + "@example.com/", true}, // redacted
		{strings.Repeat("d", maxDestinationLen+1), false},
		{testToken, false}, // secret-looking
	}
	for _, c := range cases {
		err := validateCredentialFree("destination", c.value, maxDestinationLen)
		if (err == nil) != c.want {
			t.Errorf("validateCredentialFree(%q) error = %v, want valid=%t", c.value, err, c.want)
		}
	}
}
