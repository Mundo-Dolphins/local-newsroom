package bluesky

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// resumeState is the decoded resumable state of a partially published
// thread: the account that owns the thread and the already-published posts
// in publish order.
type resumeState struct {
	// didSha is the fingerprint of the thread owner (see didSHA); it is
	// what the account-mismatch check on resume compares.
	didSha string

	// did is the owner's full DID, when recoverable from the state (the
	// first post's AT URI); informational, not required.
	did string

	// refs are the already-published posts in publish order.
	refs []social.RecordRef
}

// resumeMetadataKey names the thread owner in receipt metadata.
//
// The stored value is a *digest* of the account DID, not the DID itself:
// long base64url identifiers (did:key) look like secrets to the
// publication contract's redaction heuristics and would be rejected on
// persistence, while the 12-hex digest is a conservative, stable, and
// secret-free fingerprint.
const resumeMetadataKey = "did_sha"

// didKeyPrefix flags identifiers the v0.6 contract cannot safely persist
// in receipt metadata (see Publisher.Receipt).
const didKeyPrefix = "did:key:"

// didSHA returns the receipt-safe fingerprint of a DID: the first 12 hex
// characters of its SHA-256 (a short run of two character classes — never
// flagged by the secret heuristics).
func didSHA(did string) string {
	sum := sha256.Sum256([]byte(did))
	return hex.EncodeToString(sum[:6])
}

// resumeFromReceipt decodes the resumable state from a prior receipt.
//
// It returns nil for a nil receipt or one that carries no post state (a
// fresh thread). An invalid state is an error: publishing on top of a
// corrupt resume state would break the reply chain.
func resumeFromReceipt(rcpt *publication.PublicationReceipt) (*resumeState, error) {
	if rcpt == nil {
		return nil, nil
	}
	refs, err := social.DecodePostRefs(rcpt.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResume, err)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	st := &resumeState{refs: refs}
	if sha, ok := rcpt.Metadata[resumeMetadataKey]; ok {
		st.didSha = sha
	}
	if did, _, ok := parsePostURI(refs[0].URI); ok {
		st.did = did
		if st.didSha == "" {
			st.didSha = didSHA(did)
		}
	}
	return st, nil
}

// matchesAccount reports whether the authenticated account owns the resumed
// thread: the session DID's digest must equal the stored fingerprint.
func (s *resumeState) matchesAccount(sessionDID string) bool {
	if s == nil || s.didSha == "" {
		return true // nothing stored to check against
	}
	return didSHA(sessionDID) == s.didSha
}

// parsePostURI splits a post AT URI ("at://<did>/app.bsky.feed.post/<rkey>")
// into its DID and record key.
func parsePostURI(uri string) (did, rkey string, ok bool) {
	rest, isAt := strings.CutPrefix(uri, "at://")
	if !isAt {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return "", "", false
	}
	did, collection, rkey := parts[0], parts[1], parts[2]
	if did == "" || collection != postCollection || rkey == "" {
		return "", "", false
	}
	return did, rkey, true
}

// canonicalPostURL builds the public bsky.app URL of a post:
// https://bsky.app/profile/<handle-or-did>/post/<rkey>.
//
// The handle is used when known (what the user configured / the session
// reports); otherwise the DID (bsky.app resolves both).
func canonicalPostURL(profile, rkey string) string {
	return "https://bsky.app/profile/" + profile + "/post/" + rkey
}
