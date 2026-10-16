package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestDigestOf(t *testing.T) {
	d := DigestOf([]byte("abc"))

	sum := sha256.Sum256([]byte("abc"))
	want := Digest("sha256:" + hex.EncodeToString(sum[:]))
	if d != want {
		t.Fatalf("DigestOf(abc) = %q, want %q (std sha256)", d, want)
	}
	if len(d) != len("sha256:")+64 {
		t.Errorf("digest length = %d, want %d", len(d), len("sha256:")+64)
	}

	// Deterministic.
	if DigestOf([]byte("abc")) != d {
		t.Error("DigestOf is not deterministic")
	}
	// Content-sensitive: one byte difference means a different digest.
	if DigestOf([]byte("abd")) == d {
		t.Error("different content must yield a different digest")
	}
	// The empty content has a defined, stable digest.
	if DigestOf(nil) != DigestOf([]byte{}) {
		t.Error("empty content digests should be stable")
	}
}

func TestDigestValid(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	canonical := hex.EncodeToString(sum[:])

	if !Digest("sha256:" + canonical).Valid() {
		t.Error("a canonical digest should be valid")
	}
	if !DigestOf([]byte("x")).Valid() {
		t.Error("a freshly computed digest should be valid")
	}
	invalid := []Digest{
		"",
		Digest("sha256:"),                              // empty hash
		Digest("sha256:" + canonical[:63]),             // too short
		Digest("sha256:" + canonical + "0"),            // too long
		Digest("md5:" + canonical[:]),                  // wrong algorithm
		Digest("sha256:" + canonical[:63] + "G"),       // non-hex char
		Digest("SHA256:" + canonical),                  // uppercase prefix
		Digest("sha256:" + strings.ToUpper(canonical)), // uppercase hex
		Digest(" sha256:" + canonical),                 // leading space
		Digest("sha256: "),                             // trailing space
	}
	for _, d := range invalid {
		if d.Valid() {
			t.Errorf("%q should not be valid", d)
		}
	}
}

func TestDigestMatches(t *testing.T) {
	a := DigestOf([]byte("content A"))
	b := DigestOf([]byte("content B"))

	if !a.Matches(string(a)) {
		t.Error("a digest must match itself")
	}
	if a.Matches(string(b)) {
		t.Error("different digests must not match")
	}
	// Matches is exact string equality; it makes no claim about validity.
	// Digest values inside contracts are Valid-checked first (canonical
	// lowercase form), so the comparison is unambiguous in practice.
	if Digest("").Valid() {
		t.Error("an empty digest must not be valid")
	}
	if !Digest("").Matches("") {
		t.Error("string equality is expected")
	}
}
