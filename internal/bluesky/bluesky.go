// Package bluesky is the v0.6 Bluesky publication adapter.
//
// It publishes an approved thread of posts to a Bluesky account through the
// official AT Protocol xrpc API (com.atproto.server.createSession for the
// session, com.atproto.repo.createRecord with the app.bsky.feed.post
// collection for each post), implements the generic social.Publisher
// interface, and bridges the result into the v0.6 publication contract
// (receipts, dry-run records, digest binding).
//
// Safety model:
//
//   - Authentication: the account's app password is read from an
//     environment variable named in the Config. It lives in memory only —
//     never persisted, logged, or embedded in errors (every error surface
//     runs through run.RedactSecrets). The session mechanism sits behind a
//     small provider interface so a future OAuth flow can replace it
//     without touching the publish pipeline.
//   - Validation: the whole thread is validated locally (grapheme and byte
//     limits, language tags, facet byte offsets) before the first write.
//     The adapter never truncates: truncation would break the digest
//     binding, and a publisher must send exactly what was approved.
//   - Threading: posts are written in order. Post 1 is a root post; each
//     later post carries reply.root (the first post's uri+cid) and
//     reply.parent (the immediately preceding post's uri+cid).
//   - Resumability: every published post's uri+cid accumulates in the
//     result and lands in the receipt metadata ("post_N" = "uri|cid"). A
//     retry with that receipt as PriorReceipt publishes only the remaining
//     posts — never duplicating what the thread already holds.
//   - Dry run: full local validation plus the post order/length report,
//     with zero network calls (no login, no write).
package bluesky

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// Publisher publishes threads to a Bluesky account.
type Publisher struct {
	cfg    resolvedConfig
	client *apiClient
}

// The adapter implements the generic social contract.
var _ social.Publisher = (*Publisher)(nil)

// New builds a publisher from cfg.
//
// It validates the configuration (handle shape, endpoint, credential
// variable name and presence) and installs the password session provider.
// No network call happens here: login is deferred to the first real
// publish, so a misconfigured dry run needs nothing but the content.
func New(cfg Config) (*Publisher, error) {
	resolved, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	client := cfg.Client
	if client == nil {
		client = defaultClient()
	}
	provider := newPasswordSessionProvider(resolved, client)
	return &Publisher{
		cfg:    resolved,
		client: newAPIClient(resolved, provider, client),
	}, nil
}

// Config returns the non-secret configuration (no credential value).
func (p *Publisher) Config() Config {
	return Config{
		Handle:       p.cfg.handle,
		APIEndpoint:  p.cfg.endpoint,
		APIKeyEnv:    p.cfg.apiKeyEnv,
		DefaultLangs: append([]string(nil), p.cfg.defaultLangs...),
	}
}

// Publish implements social.Publisher.
//
// Real run: validate everything (fail closed, before the first write),
// establish the session, then write the posts in order — resuming from
// PriorReceipt's already-published posts when present. On a mid-thread
// failure it returns the partial result (the posts that did publish) plus
// the error; the caller persists a receipt from the partial result and the
// retry resumes without duplication.
//
// Dry run: local validation and the post order/length report only — no
// login, no write, no network of any kind. Blockers land in Result.Issues.
func (p *Publisher) Publish(ctx context.Context, req social.Request) (*social.Result, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("bluesky: %w", err)
	}

	issues := validatePosts(req.Posts)
	res := &social.Result{
		Provider: provider,
		DryRun:   req.DryRun,
		Digest:   social.DigestOfPosts(req.Posts),
	}

	if req.DryRun {
		// Local rehearsal only. Blockers are reported, not fatal.
		res.Issues = issues
		res.Details = dryRunDetails(req.Posts)
		return res, nil
	}

	if len(issues) > 0 {
		// Fail closed before the first write: an invalid thread must
		// not be partially published.
		res.Issues = issues
		return res, contentError(issues)
	}

	resume, err := resumeFromReceipt(req.PriorReceipt)
	if err != nil {
		return res, err
	}

	sess, err := p.client.session(ctx)
	if err != nil {
		return res, err
	}

	// A resumed thread must belong to the authenticated account: posting
	// replies as a different account would silently break the chain.
	if !resume.matchesAccount(sess.did) {
		return res, fmt.Errorf("%w: the receipt's thread was published by a different account (authenticated as %s)", ErrAccountMismatch, sess.handle)
	}

	var prior []social.RecordRef
	if resume != nil {
		prior = resume.refs
	}
	published := append([]social.RecordRef(nil), prior...)
	start := len(published)
	if start >= len(req.Posts) {
		// Everything in the request is already published: an exact
		// retry is a no-op (no network writes).
		fillThreadResult(res, published, sess.handle)
		return res, nil
	}

	for i := start; i < len(req.Posts); i++ {
		ref, err := p.client.createPost(ctx, sess, p.buildRecord(sess, req.Posts[i], replyFor(published, i)))
		if err != nil {
			fillThreadResult(res, published, sess.handle)
			return res, fmt.Errorf("bluesky: post %d of %d: %w", i+1, len(req.Posts), err)
		}
		published = append(published, ref)
	}

	fillThreadResult(res, published, sess.handle)
	return res, nil
}

// Receipt builds the publication receipt for a (possibly partial) real
// result: the first post's canonical URL and AT URI, the full-content
// digest, and the resumable per-post state in metadata (the generic
// "post_N" = "uri|cid" pairs plus the thread owner's DID fingerprint).
//
// It refuses dry runs (ErrDryRunReceipt) and validates the built receipt.
// The same receipt serves SucceedTarget (a full thread) and PartialTarget
// (a mid-thread failure); a retry passes it back as PriorReceipt.
//
// One documented limitation: accounts identified by did:key cannot get a
// resumable receipt in v0.6 — the long base64url identifier looks like a
// secret to the publication contract and would be redacted away (and with
// it the resume state). Such calls fail with a clear error instead of
// persisting a redacted, non-resumable receipt; did:plc and did:web
// accounts (all Bluesky Social and the great majority of PDS accounts) are
// fully supported.
func (p *Publisher) Receipt(req social.Request, res *social.Result, publishedAt time.Time) (*publication.PublicationReceipt, error) {
	if res == nil {
		return nil, fmt.Errorf("bluesky: result is nil")
	}
	extra := map[string]string{}
	if len(res.Published) > 0 {
		did, _, ok := parsePostURI(res.Published[0].URI)
		if ok && strings.HasPrefix(did, didKeyPrefix) {
			return nil, fmt.Errorf("%w: the account's %s identifier cannot be persisted in a receipt under the v0.6 publication contract, so the thread's resumable state cannot be stored; use a did:plc/did:web account for resumable publishing", ErrResume, didKeyPrefix[:len(didKeyPrefix)-1])
		}
		if ok {
			extra[resumeMetadataKey] = didSHA(did)
		}
	}
	return res.Receipt(req.PlanID, req.TargetID, clientVersion, publishedAt, extra)
}

// replyFor builds the reply block of post i of the thread: nil for the
// root post, otherwise the strong references to the thread's first post
// (root) and the immediately preceding post (parent).
func replyFor(published []social.RecordRef, i int) *social.ReplyRef {
	if i == 0 {
		return nil
	}
	return &social.ReplyRef{
		Root:   published[0],
		Parent: published[i-1],
	}
}

// buildRecord translates one social post (validated) into the AT Protocol
// record that will be written.
func (p *Publisher) buildRecord(sess *session, post social.Post, reply *social.ReplyRef) *postRecord {
	createdAt := post.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	rec := &postRecord{
		Type:      postRecordType,
		Text:      post.Text,
		CreatedAt: createdAt.UTC(),
	}

	langs := post.Langs
	if len(langs) == 0 {
		langs = p.cfg.defaultLangs
	}
	if len(langs) > 0 {
		rec.Langs = append([]string(nil), langs...)
	}

	facets := mergeFacets(post.Text, post.Facets)
	if len(facets) > 0 {
		rec.Facets = facets
	}

	if reply != nil {
		rec.Reply = &replyRef{
			Root:   &strongRef{URI: reply.Root.URI, CID: reply.Root.CID},
			Parent: &strongRef{URI: reply.Parent.URI, CID: reply.Parent.CID},
		}
	}
	return rec
}

// mergeFacets combines the caller's explicit facets (already validated)
// with the deterministic link facets generated for the text, skipping URLs
// an explicit facet already covers.
func mergeFacets(text string, explicit []social.Facet) []facet {
	var out []facet
	for _, f := range explicit {
		out = append(out, convertFacet(f))
	}
	for _, f := range linkFacets(text, explicit) {
		out = append(out, convertFacet(f))
	}
	if len(out) > maxFacets {
		out = out[:maxFacets]
	}
	return out
}

// convertFacet translates a generic social facet to the AT Protocol wire
// shape (v0.6 emits link features; mention features are validated out
// upstream, but the converter handles the union for completeness).
func convertFacet(f social.Facet) facet {
	feat := facet{
		Type:  facetTypeRichText,
		Index: facetIndex{ByteStart: f.ByteStart, ByteEnd: f.ByteEnd},
	}
	for _, fr := range f.Features {
		switch fr.Type {
		case social.FeatureLink:
			feat.Features = append(feat.Features, facetFeature{
				Type: facetTypeLink,
				Link: &facetLink{URI: fr.Fields["uri"]},
			})
		case social.FeatureMention:
			feat.Features = append(feat.Features, facetFeature{
				Type:    facetTypeMention,
				Mention: &facetMention{DID: fr.Fields["did"]},
			})
		}
	}
	return feat
}

// fillThreadResult sets the result's published state and external
// references (the thread's first post).
func fillThreadResult(res *social.Result, published []social.RecordRef, profile string) {
	res.Published = published
	if len(published) == 0 {
		return
	}
	res.ExternalRecordID = published[0].URI
	if _, rkey, ok := parsePostURI(published[0].URI); ok {
		res.ExternalURI = canonicalPostURL(profile, rkey)
	}
}

// dryRunDetails renders the post order and lengths for a rehearsal.
func dryRunDetails(posts []social.Post) string {
	total := 0
	parts := make([]string, len(posts))
	for i, p := range posts {
		g := graphemeCount(p.Text)
		total += g
		parts[i] = fmt.Sprintf("post %d: %d graphemes / %d bytes", i+1, g, len([]byte(p.Text)))
	}
	if len(posts) == 1 {
		return fmt.Sprintf("1 post (%d graphemes total); %s", total, parts[0])
	}
	return fmt.Sprintf("%d posts (%d graphemes total); %s", len(posts), total, strings.Join(parts, "; "))
}

// contentError renders the validation failure as a publish error. The
// issue messages are redacted by construction (NewDryRunIssue redacts).
func contentError(issues []publication.DryRunIssue) error {
	msgs := make([]string, 0, len(issues))
	for _, is := range issues {
		msgs = append(msgs, is.Message)
	}
	if len(msgs) > 3 {
		msgs = append(msgs[:3], fmt.Sprintf("... and %d more issue(s)", len(issues)-3))
	}
	return fmt.Errorf("bluesky: the thread fails validation before any write: %s", strings.Join(msgs, "; "))
}
