// Package social is the generic contract for social-platform publishers.
//
// The v0.6 publication contract (internal/publication) is deliberately
// format-agnostic: a "post" target there is an opaque external destination.
// This package adds the *social* shape that several adapters share — a
// thread of text posts, content-addressed record references, and a
// publisher interface — without committing to any one platform's wire
// protocol. Concrete adapters (internal/bluesky, ...) translate between
// this shape and their platform's API.
//
// The package is pure: it defines types, validation, and the resume-state
// encoding that lands in publication receipt metadata. It performs no I/O
// and no network calls.
package social

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
)

// MaxPosts bounds a single thread request.
//
// The bound keeps the resumable receipt state (one bounded metadata pair per
// published post, plus bookkeeping) inside the publication contract's
// metadata bound, and it reflects the reality that newsroom threads are
// short. Adapters may enforce a tighter bound.
const MaxPosts = 30

// Publisher is the generic social-platform publisher contract.
//
// The v0.6 publication contract (internal/publication) is format-agnostic
// and has no shape for "publish a thread of posts to a social platform",
// so the generic interface lives here and concrete adapters
// (internal/bluesky, ...) implement it. The interface is deliberately thin:
// a request (the thread content plus, optionally, the prior receipt's
// resumable state) in, a result (what published, where, and the digest
// binding) out. Platform wire types never cross this boundary — adapters
// translate internally, and the publication contract sees only the
// receipt/dry-run records built from the result.
type Publisher interface {
	// Publish publishes req.Posts as a thread (in publish order) and
	// returns the result. A real run that fails mid-thread returns the
	// partial result (the posts that did publish) together with the
	// error, so the caller can receipt the partial state and resume.
	//
	// When req.DryRun is set the implementation must perform its full
	// local rehearsal — validation and the post order/length report —
	// with ZERO network calls: no login, no write. Blockers are reported
	// in Result.Issues, not as an error.
	Publish(ctx context.Context, req Request) (*Result, error)
}

// ErrDryRunReceipt is returned by Result.Receipt for a dry run: a rehearsal
// published nothing, so it has no receipt to build.
var ErrDryRunReceipt = errors.New("social: cannot build a receipt for a dry run (nothing was published)")

// Feature type constants for text annotations.
//
// These are the three rich-text feature kinds the AT Protocol lexicon
// defines; other platforms map onto the same generic concepts.
const (
	// FeatureLink annotates a span as a hyperlink (field "uri").
	FeatureLink = "link"

	// FeatureMention annotates a span as a user mention (field "did" or
	// "handle", platform-defined).
	FeatureMention = "mention"

	// FeatureTag annotates a span as a hashtag (field "tag").
	FeatureTag = "tag"
)

// Feature is one typed annotation within a Facet.
//
// Fields carries the feature's type-specific values (e.g. a link's "uri").
// Adapters validate which types they support and fail closed on the rest.
type Feature struct {
	// Type is one of the Feature* constants (or a platform-specific type
	// that a specific adapter understands).
	Type string

	// Fields holds the type-specific string values.
	Fields map[string]string
}

// Facet annotates a span of a post's text.
//
// ByteStart and ByteEnd are UTF-8 *byte* offsets into the post text
// (the convention of rich-text facet systems, where offsets address bytes,
// not runes): 0 <= ByteStart < ByteEnd <= len(post.Text in bytes).
type Facet struct {
	// ByteStart is the inclusive start offset in UTF-8 bytes.
	ByteStart int

	// ByteEnd is the exclusive end offset in UTF-8 bytes.
	ByteEnd int

	// Features are the annotations covering the span.
	Features []Feature
}

// RecordRef is a content-addressed reference to a published record: the
// platform's URI for the record plus its content identifier (CID).
//
// Both parts are public, non-credential values; together they identify the
// exact published bytes.
type RecordRef struct {
	// URI is the platform's URI for the published record.
	URI string

	// CID is the content identifier of the published record.
	CID string
}

// ReplyRef names the root and the immediate parent of a reply post.
//
// Threads are chains of replies: every post after the first references the
// thread's root post and the post immediately before it.
type ReplyRef struct {
	// Root is the thread's first post.
	Root RecordRef

	// Parent is the immediately preceding post.
	Parent RecordRef
}

// Post is one post in a thread request.
type Post struct {
	// Text is the post body. Adapters validate platform limits (grapheme
	// count, byte length, forbidden control characters) before any write.
	Text string

	// Langs are BCP-47-ish language tags for the post (optional; adapters
	// bound the count and the tag shape).
	Langs []string

	// Facets are explicit text annotations (optional; adapters validate
	// the offsets and the feature types they support).
	Facets []Facet

	// Reply names an external root/parent pair (optional). v0.6 adapters
	// publish self-contained threads and may reject a non-nil Reply.
	Reply *ReplyRef

	// CreatedAt is the post's creation timestamp (optional; zero means the
	// adapter stamps the publish time).
	CreatedAt time.Time
}

// Request is the publisher-facing shape of one social publish call.
type Request struct {
	// PlanID is the owning publication plan (receipt provenance).
	// Required.
	PlanID publication.PublicationID

	// TargetID is the publication target within the plan (receipt
	// provenance). Required.
	TargetID publication.TargetID

	// Posts is the thread content in publish order (1..MaxPosts). The
	// first post is the thread root; the rest are replies chained in
	// order.
	Posts []Post

	// PriorReceipt carries the resumable state of a partially published
	// thread (publication receipt metadata naming the already-published
	// posts), so a retry publishes only the remaining posts. Nil (or an
	// empty one) means a fresh thread.
	PriorReceipt *publication.PublicationReceipt

	// DryRun performs the full local rehearsal — validation and the
	// post-order/length report — with ZERO network calls: no login, no
	// write.
	DryRun bool
}

// Validate checks the request's shape (the publication identifiers, the
// post count bound, and the prior receipt's contract validity).
//
// Content validation (text limits, facets, languages) is adapter-specific
// and happens in the adapter.
func (r Request) Validate() error {
	if err := r.PlanID.Validate(); err != nil {
		return fmt.Errorf("plan ID: %w", err)
	}
	if err := r.TargetID.Validate(); err != nil {
		return fmt.Errorf("target ID: %w", err)
	}
	if len(r.Posts) == 0 {
		return errors.New("a social request requires at least one post")
	}
	if len(r.Posts) > MaxPosts {
		return fmt.Errorf("a social request holds %d posts, exceeding the bound of %d", len(r.Posts), MaxPosts)
	}
	if r.PriorReceipt != nil {
		if err := r.PriorReceipt.Validate(); err != nil {
			return fmt.Errorf("prior receipt: %w", err)
		}
	}
	return nil
}

// Result is the outcome of one Publish call (real or dry run).
type Result struct {
	// Provider is the adapter's provider tag (mirrors the target's).
	Provider string

	// DryRun is true for a rehearsal (no network calls of any kind).
	DryRun bool

	// Published holds one RecordRef per successfully published post, in
	// publish order. After a resumed run it includes the posts published
	// in the prior attempt (the full thread so far). Empty when nothing
	// was published (a dry run, or a failure on the first post).
	Published []RecordRef

	// ExternalURI is the canonical URL of the thread's first post, when
	// the platform exposes one (empty in a dry run: the post does not
	// exist yet).
	ExternalURI string

	// ExternalRecordID is the platform's identifier of the first post
	// (empty in a dry run).
	ExternalRecordID string

	// Digest is the canonical digest of the *full requested* content:
	// the post texts NUL-joined in thread order. For a single post this
	// is exactly that post's own digest. It is the full-thread digest
	// even when the run stopped early, so a partial receipt stays
	// digest-bound to the approved artifact (the published prefix is
	// recorded in the receipt metadata instead).
	Digest publication.Digest

	// Details is a human-readable summary (post order and lengths).
	Details string

	// Issues carries dry-run blockers (a real run fails on the same
	// conditions; a dry run reports them without erroring).
	Issues []publication.DryRunIssue
}

// DigestOfPosts returns the canonical digest of a set of posts: the SHA-256
// of the post texts NUL-joined in thread order. For a single post this is
// exactly the post's own digest, so a plan whose artifact is a single post
// binds byte-for-byte; for a thread, the publication artifact for the
// thread target is the same NUL-joined content.
func DigestOfPosts(posts []Post) publication.Digest {
	var buf strings.Builder
	for i, p := range posts {
		if i > 0 {
			buf.WriteByte(0)
		}
		buf.WriteString(p.Text)
	}
	return publication.DigestOf([]byte(buf.String()))
}

// PostsFromTexts builds posts from plain rendered strings (for example the
// output of a thread renderer) in order, with the given language tags on
// each (nil tags leave the posts to the adapter's defaults).
//
// It is the bridge between a deterministic renderer and a publisher: the
// renderer produces the exact post texts, and this function packages them
// for a publish request.
func PostsFromTexts(texts []string, langs []string) []Post {
	posts := make([]Post, len(texts))
	for i, text := range texts {
		posts[i] = Post{
			Text:  text,
			Langs: append([]string(nil), langs...),
		}
	}
	return posts
}

// Receipt builds the publication receipt for a real run.
//
// The receipt names the first post (canonical URL as external URI, platform
// identifier as external record ID), carries the full-content digest, and
// records the resumable per-post state (see PostMetadataFor) plus the
// adapter's extra metadata. It refuses dry runs (ErrDryRunReceipt) and
// validates the built receipt before returning it.
func (r *Result) Receipt(planID publication.PublicationID, targetID publication.TargetID, clientVersion string, publishedAt time.Time, extraMetadata map[string]string) (*publication.PublicationReceipt, error) {
	if r == nil {
		return nil, errors.New("result is nil")
	}
	if r.DryRun {
		return nil, ErrDryRunReceipt
	}
	if len(r.Published) == 0 {
		return nil, errors.New("result carries no published posts; nothing to receipt")
	}
	meta := map[string]string{}
	for k, v := range r.PostMetadata() {
		meta[k] = v
	}
	for k, v := range extraMetadata {
		meta[k] = v
	}
	recordID := r.ExternalRecordID
	externalURI := r.ExternalURI
	rec := publication.NewReceipt(planID, targetID, r.Provider, externalURI, recordID,
		r.Digest, "", clientVersion, meta, publishedAt)
	if err := rec.Validate(); err != nil {
		return nil, fmt.Errorf("built receipt is invalid: %w", err)
	}
	return rec, nil
}

// ToDryRunResult converts a dry-run Result into the plan-contract rehearsal
// record (RecordDryRun input). The orchestrator verifies the digest binding
// against the plan's artifact.
func (r *Result) ToDryRunResult(at time.Time) *publication.DryRunResult {
	if r == nil {
		return nil
	}
	outcome := publication.DryRunOutcomeOK
	if len(r.Issues) > 0 {
		outcome = publication.DryRunOutcomeFailed
	}
	return publication.NewDryRunResult(at, outcome, r.Digest, r.Details, r.Issues)
}

// PostMetadata is the resumable per-post receipt state: one bounded metadata
// pair per published post, "post_<n>" = "<uri>|<cid>", in publish order.
//
// The encoding is generic (any content-addressed record platform), and the
// values carry public identifiers only: no credentials, no token-shaped
// runs.
func (r *Result) PostMetadata() map[string]string {
	meta := make(map[string]string, len(r.Published))
	for i, ref := range r.Published {
		meta[fmt.Sprintf("post_%d", i+1)] = EncodePostRef(ref)
	}
	return meta
}

// ResumeMetadataPrefix is the metadata key prefix of published posts.
const ResumeMetadataPrefix = "post_"

// EncodePostRef encodes one published post as a bounded "uri|cid" metadata
// value (the '|' separator is outside the character set of both AT URIs
// and base32 CIDs).
func EncodePostRef(ref RecordRef) string {
	return ref.URI + "|" + ref.CID
}

// maxResumePosts bounds the resume state that fits in one receipt's
// metadata (the publication contract allows at most 32 pairs; leaving
// headroom for adapter bookkeeping).
const maxResumePosts = MaxPosts

// DecodePostRefs recovers the published-post references from receipt
// metadata (the inverse of PostMetadata). It requires the post_N indices to
// form the contiguous range 1..N, and rejects values that do not carry
// exactly one "uri|cid" pair with non-empty halves.
//
// Metadata maps are unordered, so the decode sorts by the post index: the
// returned slice is always in publish order. A gap in the indices is an
// error, not a skip: a lost pair would silently break the reply chain.
func DecodePostRefs(metadata map[string]string) ([]RecordRef, error) {
	type indexedRef struct {
		n   int
		ref RecordRef
	}
	var refs []indexedRef
	for k, v := range metadata {
		if !strings.HasPrefix(k, ResumeMetadataPrefix) {
			continue
		}
		n, err := parsePostIndex(k)
		if err != nil {
			return nil, err
		}
		uri, cid, ok := splitPostRef(v)
		if !ok {
			return nil, fmt.Errorf("resume metadata %q: expected exactly one \"uri|cid\" pair, got %q", k, v)
		}
		refs = append(refs, indexedRef{n: n, ref: RecordRef{URI: uri, CID: cid}})
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > maxResumePosts {
		return nil, fmt.Errorf("resume state holds %d posts, exceeding the bound of %d", len(refs), maxResumePosts)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].n < refs[j].n })
	out := make([]RecordRef, 0, len(refs))
	for _, ir := range refs {
		if ir.n != len(out)+1 {
			return nil, fmt.Errorf("resume state has a gap: post %d is missing", len(out)+1)
		}
		out = append(out, ir.ref)
	}
	return out, nil
}

// parsePostIndex parses a "post_<n>" metadata key.
func parsePostIndex(key string) (int, error) {
	body := key[len(ResumeMetadataPrefix):]
	if body == "" {
		return 0, fmt.Errorf("resume metadata key %q is malformed", key)
	}
	n := 0
	for _, c := range body {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("resume metadata key %q: post index must be numeric", key)
		}
		n = n*10 + int(c-'0')
		if n > maxResumePosts*4 {
			return 0, fmt.Errorf("resume metadata key %q: post index %d is out of range", key, n)
		}
	}
	return n, nil
}

// splitPostRef splits a "uri|cid" value; the halves must be non-empty and
// must not themselves contain the separator.
func splitPostRef(v string) (uri, cid string, ok bool) {
	i := strings.IndexByte(v, '|')
	if i <= 0 || i == len(v)-1 {
		return "", "", false
	}
	if strings.IndexByte(v[i+1:], '|') >= 0 {
		return "", "", false
	}
	return v[:i], v[i+1:], true
}
