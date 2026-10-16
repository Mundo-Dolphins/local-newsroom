package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// Digest is a content digest with an explicit algorithm prefix, e.g.
// "sha256:9f2c…".
//
// Publication contracts never carry the artifact *content*; they carry its
// digest. The digest is the binding primitive for the whole v0.6 safety
// model: the gate evidence, the plan, and every approval all agree on the
// exact bytes a human saw, and a publisher may only be invoked when the
// bytes it is about to send hash to the approved digest. If the artifact
// changes after approval, the digests diverge and the approval is stale —
// no amount of re-serialization can hide that.
type Digest string

// DigestAlgorithm is the hashing algorithm of every publication digest.
//
// SHA-256 is the single supported algorithm: the explicit prefix keeps old
// records readable if a future schema version ever introduces another one.
const DigestAlgorithm = "sha256"

// digestRe is the canonical digest form: algorithm prefix + 64 lowercase hex
// characters. Hex digests use at most two character classes, so they never
// trip the high-entropy secret detectors in internal/run.
var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// DigestOf computes the canonical publication digest of data (the exact
// bytes persisted in the run directory).
func DigestOf(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest(DigestAlgorithm + ":" + hex.EncodeToString(sum[:]))
}

// Valid reports whether the digest is in canonical form.
func (d Digest) Valid() bool {
	return digestRe.MatchString(string(d))
}

// Matches reports whether the digest equals other exactly (canonical,
// case-sensitive comparison).
func (d Digest) Matches(other string) bool {
	return string(d) == other
}

// String returns the string representation of the digest.
func (d Digest) String() string {
	return string(d)
}
