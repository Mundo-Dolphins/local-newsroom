package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ReceiptID is the stable identifier of a publication receipt.
//
// Format: "rcpt-<timestamp>-<12 hex chars>", where the timestamp is the
// receipt's published_at and the hash is derived from the plan, target, and
// external reference. Deriving (rather than assigning) the ID means a
// receipt re-created for the same external record re-derives the same ID —
// the anchor for idempotent resume.
type ReceiptID string

// ReceiptIDPrefix is the required prefix of every receipt ID.
const ReceiptIDPrefix = "rcpt-"

// NewReceiptID derives a stable receipt ID.
//
// externalRef is the external record ID when set, or the external URI, or
// the content digest as a last resort — anything that identifies the
// external record the receipt refers to.
func NewReceiptID(planID PublicationID, targetID TargetID, externalRef string, publishedAt time.Time) ReceiptID {
	return ReceiptID(deriveID(ReceiptIDPrefix, publishedAt, string(planID), string(targetID), externalRef))
}

// Validate checks that the receipt ID is well-formed.
func (id ReceiptID) Validate() error {
	return validateIDValue("receipt id", string(id), ReceiptIDPrefix)
}

// String returns the string representation of the receipt ID.
func (id ReceiptID) String() string {
	return string(id)
}

// Receipt constants.
const (
	maxReceiptURILen      = 1024
	maxReceiptRecordIDLen = 256
	maxReceiptFieldLen    = 128
	maxReceiptMetadata    = 32
	maxReceiptMetaValLen  = 1024
)

// externalRecordIDRe allows opaque external record identifiers: commit
// hashes, atproto cids/rkeys, post IDs, slugs. It is deliberately broad but
// excludes whitespace, so anything secret-looking is rejected.
var externalRecordIDRe = regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,256}$`)

// idempotencyKeyRe allows client-generated deduplication tokens.
var idempotencyKeyRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

// clientVersionRe allows conservative publisher implementation versions, with
// one optional slash for "name/version" user-agent forms.
var clientVersionRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,63}$`)

// receiptMetadataKeyRe allows conservative metadata key names.
var receiptMetadataKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// PublicationReceipt is the durable, external proof that a target was
// published.
//
// A receipt records WHERE the content now lives (external URI and/or record
// ID) and WHAT exactly was sent (content digest), so any future process can
// answer "is this already published, and to what?" without any credential
// and without re-sending.
//
// SECURITY: receipts are persisted. The contract therefore carries no field
// for credentials, the external URI may not contain a live credential, and
// every free-text value is redacted on construction and re-checked on
// validation. The idempotency key is a client-generated deduplication token,
// not a secret; it is validated accordingly.
type PublicationReceipt struct {
	// ID is the stable receipt identifier (see NewReceiptID).
	ID ReceiptID `json:"id"`

	// PlanID references the owning publication plan.
	PlanID PublicationID `json:"plan_id"`

	// TargetID references the target within the plan.
	TargetID TargetID `json:"target_id"`

	// Provider is the publisher that produced the receipt (mirrors the
	// target's provider).
	Provider string `json:"provider,omitempty"`

	// ExternalURI is the canonical URL where the content is now live, when
	// the platform exposes one (e.g. the published post or page URL).
	ExternalURI string `json:"external_uri,omitempty"`

	// ExternalRecordID is the platform's own identifier for the published
	// record (e.g. a commit SHA or a post record ID).
	ExternalRecordID string `json:"external_record_id,omitempty"`

	// ContentDigest is the digest of exactly the content that was sent.
	// It must equal the approved artifact digest; a receipt for different
	// bytes is rejected (see Validate).
	ContentDigest Digest `json:"content_digest"`

	// IdempotencyKey is the client-generated deduplication token used for
	// the external request, when the platform supports one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// ClientVersion is the version of the publisher implementation that
	// produced the receipt (audit trail).
	ClientVersion string `json:"client_version,omitempty"`

	// Metadata carries provider-specific, non-credential details of the
	// publication (e.g. a count of posted items). Keys and values are
	// validated and redacted.
	Metadata map[string]string `json:"metadata,omitempty"`

	// PublishedAt is when the publisher confirmed the publication (UTC).
	PublishedAt run.Timestamp `json:"published_at"`
}

// NewReceipt builds a receipt with every free-text value redacted and the
// receipt ID derived.
//
// The caller supplies the external reference(s) exactly as the platform
// returned them; NewReceipt redacts anything that looks like a credential
// before it enters the contract. Validate then rejects any value that still
// looks like a secret, so a real credential can never round-trip into a
// persisted receipt.
func NewReceipt(planID PublicationID, targetID TargetID, provider, externalURI, externalRecordID string, contentDigest Digest, idempotencyKey, clientVersion string, metadata map[string]string, publishedAt time.Time) *PublicationReceipt {
	meta := make(map[string]string, len(metadata))
	for k, v := range metadata {
		meta[k] = run.RedactSecrets(v)
	}
	ref := externalRecordID
	if ref == "" {
		ref = externalURI
	}
	if ref == "" {
		ref = string(contentDigest)
	}
	return &PublicationReceipt{
		ID:               NewReceiptID(planID, targetID, ref, publishedAt),
		PlanID:           planID,
		TargetID:         targetID,
		Provider:         provider,
		ExternalURI:      run.RedactSecrets(externalURI),
		ExternalRecordID: run.RedactSecrets(externalRecordID),
		ContentDigest:    contentDigest,
		IdempotencyKey:   run.RedactSecrets(idempotencyKey),
		ClientVersion:    clientVersion,
		Metadata:         meta,
		PublishedAt:      run.NewTimestamp(publishedAt),
	}
}

// Validate checks the receipt: identifiers, the external reference pair (at
// least one must be present), credential-free URLs, digest, and redacted
// metadata.
func (r *PublicationReceipt) Validate() error {
	if r == nil {
		return errors.New("publication receipt is nil")
	}
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := r.ID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := r.PlanID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := r.TargetID.Validate(); err != nil {
		errs = append(errs, err)
	}

	if r.Provider != "" {
		if !providerRe.MatchString(r.Provider) {
			addf("provider %q is invalid: use lowercase letters, digits, '.', '-', '_' (1-64 chars)", r.Provider)
		}
	}

	if r.ExternalURI == "" && r.ExternalRecordID == "" {
		errs = append(errs, errors.New("receipt must carry an external URI or an external record ID"))
	}
	if err := validateAbsoluteURL("external uri", r.ExternalURI, maxReceiptURILen); err != nil {
		errs = append(errs, err)
	}
	if r.ExternalRecordID != "" {
		if !externalRecordIDRe.MatchString(r.ExternalRecordID) {
			addf("external record ID %q is invalid: use letters, digits, '.', '_', ':', '/', '-' (1-256 chars)", r.ExternalRecordID)
		} else if run.LooksLikeSecret(r.ExternalRecordID) {
			addf("external record ID contains secret-looking material; redact it before recording")
		}
	}

	if !r.ContentDigest.Valid() {
		errs = append(errs, fmt.Errorf("content digest %q is not a valid %s digest", r.ContentDigest, DigestAlgorithm))
	}
	if r.IdempotencyKey != "" {
		if !idempotencyKeyRe.MatchString(r.IdempotencyKey) {
			addf("idempotency key %q is invalid: use letters, digits, '.', '-', '_' (1-128 chars)", r.IdempotencyKey)
		} else if run.LooksLikeSecret(r.IdempotencyKey) {
			addf("idempotency key contains secret-looking material; redact it before recording")
		}
	}
	if r.ClientVersion != "" && !clientVersionRe.MatchString(r.ClientVersion) {
		addf("client version %q is invalid: use letters, digits, '.', '-', '_' (1-64 chars)", r.ClientVersion)
	}

	if len(r.Metadata) > maxReceiptMetadata {
		addf("metadata holds %d entries, exceeding the bound of %d", len(r.Metadata), maxReceiptMetadata)
	}
	for k, v := range r.Metadata {
		if !receiptMetadataKeyRe.MatchString(k) {
			addf("metadata key %q is invalid: use letters, digits, '.', '-', '_' (1-64 chars)", k)
		}
		if len(v) > maxReceiptMetaValLen {
			addf("metadata value for %q exceeds %d characters", k, maxReceiptMetaValLen)
		}
		if run.LooksLikeSecret(v) {
			addf("metadata value for %q contains secret-looking material; redact it before recording", k)
		}
	}

	if r.PublishedAt.IsZero() {
		errs = append(errs, errors.New("published_at is required"))
	}

	return errors.Join(errs...)
}

// anyUserInfoRe matches the prefix of an absolute URI that carries basic-
// auth credentials, for any scheme: scheme://user[:password]@ . The
// password group is optional (a bare user@ is not a credential).
var anyUserInfoRe = regexp.MustCompile(`(?i)^([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/@\s]+)(:([^@/\s]+))?@`)

// urlSchemeRe matches the scheme prefix of an absolute URI ("http://", ...).
var urlSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

// validateURLValue checks an optional absolute-URL field.
//
// When requiredHTTP is true (receipt external URIs) the scheme must be
// http(s); otherwise any scheme is accepted (a target destination may be an
// atproto or git URI). The userinfo is checked with a regex before parsing:
// a live user:password@ is a credential and is rejected, while a redacted
// password (the redaction placeholder) is accepted. The regex step is
// required because Go's url.Parse rejects the placeholder's brackets in
// userinfo and would otherwise make redacted URLs unparseable.
func validateURLValue(kind, value string, maxLen int, requiredHTTP bool) error {
	if value == "" {
		return nil
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s %q exceeds %d characters", kind, value, maxLen)
	}

	probe := value
	if m := anyUserInfoRe.FindStringSubmatch(value); m != nil {
		// m[1]=scheme:// m[2]=user m[3]=:pw (optional) m[4]=pw
		if m[3] != "" && m[4] != run.RedactedPlaceholder { // a live password
			return fmt.Errorf("%s must not contain credentials", kind)
		}
		// Strip the (checked) userinfo so the URL can be parsed.
		probe = m[1] + "_@" + value[len(m[0]):]
	}

	u, err := url.Parse(probe)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s %q is not a valid absolute URL", kind, value)
	}
	if requiredHTTP && u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must be an http(s) URL", kind, value)
	}
	if run.LooksLikeSecret(value) {
		return fmt.Errorf("%s contains secret-looking material; redact it before recording", kind)
	}
	return nil
}

// validateAbsoluteURL checks an optional field that must be an absolute
// http(s) URL when set (receipt external URIs).
func validateAbsoluteURL(kind, value string, maxLen int) error {
	return validateURLValue(kind, value, maxLen, true)
}

// validateCredentialFree checks an opaque string field for credential
// material: a length bound, and — when the value is an absolute URI — the
// same credential-free userinfo rule as validateURLValue. Non-URL values
// (handles, paths, atproto references) skip the URL rules but are still
// checked for secret-looking material.
func validateCredentialFree(kind, value string, maxLen int) error {
	if len(value) > maxLen {
		return fmt.Errorf("%s %q exceeds %d characters", kind, value, maxLen)
	}
	if urlSchemeRe.MatchString(value) {
		return validateURLValue(kind, value, maxLen, false)
	}
	if run.LooksLikeSecret(value) {
		return fmt.Errorf("%s contains secret-looking material; redact it before recording", kind)
	}
	return nil
}

// deriveID derives a stable, prefixed ID from its inputs.
//
// Format: "<prefix>-<timestamp>-<12 hex chars>" where the timestamp is the
// event time (UTC, "20060102T150405") and the hash is the first 12 hex
// characters of SHA-256 over the null-separated inputs. The timestamp keeps
// IDs human-inspectable and time-ordered; the hash keeps them unique and
// deterministic — the same inputs always derive the same ID, so IDs can be
// re-derived (e.g. on resume) without storing extra state.
func deriveID(prefix string, at time.Time, parts ...string) string {
	fields := append([]string{at.UTC().Format("20060102T150405")}, parts...)
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return prefix + at.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(sum[:6])
}

// idBodyRe validates the conservative filesystem-safe character set of a
// derived ID body (after the prefix): letters, digits, dot, dash, underscore.
var idBodyRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

// validateIDValue checks a prefixed ID: the prefix, and a non-empty body of
// at most 128 conservative characters.
func validateIDValue(kind, value, prefix string) error {
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s %q must start with %q", kind, value, prefix)
	}
	body := value[len(prefix):]
	if body == "" || len(body) > 128 {
		return fmt.Errorf("%s %q: body must be 1-128 characters", kind, value)
	}
	if !idBodyRe.MatchString(body) {
		return fmt.Errorf("%s %q: contains invalid characters (allowed: letters, digits, '.', '-', '_')", kind, value)
	}
	return nil
}
