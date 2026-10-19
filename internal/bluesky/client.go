package bluesky

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// This file holds the AT Protocol wire types — the one place in the adapter
// that knows the platform's JSON shapes. Nothing outside this file (and the
// client that calls it) may reference them, so the generic social contract
// and the publication contract stay platform-free.

// createSessionRequest is the xrpc com.atproto.server.createSession input.
type createSessionRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

// createSessionResponse is the createSession / refreshSession output.
//
// Only the fields the adapter needs are decoded; server additions
// (email, active, status) are ignored.
type createSessionResponse struct {
	DID        string `json:"did"`
	Handle     string `json:"handle"`
	AccessJWT  string `json:"accessJwt"`
	RefreshJWT string `json:"refreshJwt"`
}

// refreshSessionRequest is the xrpc com.atproto.server.refreshSession input.
type refreshSessionRequest struct {
	RefreshJwt string `json:"refreshJwt"`
}

// strongRef is an AT Protocol strong reference to a record: the URI plus
// the content identifier of the exact record bytes.
type strongRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// replyRef is the reply block of an app.bsky.feed.post record: threads are
// chains of replies, and every reply names both the thread root and the
// immediate parent (the protocol requires both).
type replyRef struct {
	Root   *strongRef `json:"root,omitempty"`
	Parent *strongRef `json:"parent,omitempty"`
}

// facetIndex is the byte-offset span of a facet (UTF-8 bytes, end
// exclusive).
type facetIndex struct {
	ByteStart int `json:"byteStart"`
	ByteEnd   int `json:"byteEnd"`
}

// facetLink is the link feature of app.bsky.richtext.facet.
type facetLink struct {
	URI string `json:"uri"`
}

// facetMention is the mention feature of app.bsky.richtext.facet.
type facetMention struct {
	DID string `json:"did"`
}

// facetFeature is one union member of app.bsky.richtext.facet#Feature; the
// lexicon discriminates by $type.
type facetFeature struct {
	Type    string        `json:"$type"`
	Link    *facetLink    `json:"link,omitempty"`
	Mention *facetMention `json:"mention,omitempty"`
}

// facet is an element of the post's facets array.
type facet struct {
	Type     string         `json:"$type"`
	Index    facetIndex     `json:"index"`
	Features []facetFeature `json:"features"`
}

// postRecord is the app.bsky.feed.post record written by
// com.atproto.repo.createRecord.
type postRecord struct {
	Type      string    `json:"$type"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Facets    []facet   `json:"facets,omitempty"`
	Reply     *replyRef `json:"reply,omitempty"`
	Langs     []string  `json:"langs,omitempty"`
}

// createRecordRequest is the xrpc com.atproto.repo.createRecord input.
type createRecordRequest struct {
	Repo       string      `json:"repo"`
	Collection string      `json:"collection"`
	Record     *postRecord `json:"record"`
}

// createRecordResponse is the createRecord output: the full repository path
// (uri) and the content identifier (cid) of the created record.
type createRecordResponse struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// ErrSession indicates the session could not be established or rotated.
var ErrSession = errors.New("bluesky: session establishment failed")

// ErrPublish indicates a post creation failure after the session was valid.
var ErrPublish = errors.New("bluesky: post creation failed")

// ErrResume indicates the prior receipt's resumable state was invalid.
var ErrResume = errors.New("bluesky: resumable thread state is invalid")

// ErrAccountMismatch indicates a resumed thread belongs to a different
// account than the one now authenticated.
var ErrAccountMismatch = errors.New("bluesky: the resumed thread belongs to a different account")

// postCollection is the app.bsky.feed.post lexicon type.
const postCollection = "app.bsky.feed.post"

// facetTypeRichText is the rich-text facet lexicon type.
const facetTypeRichText = "app.bsky.richtext.facet"

// facetFeatureTypes is the AT Protocol discrimination set.
const (
	facetTypeLink    = "app.bsky.richtext.facet#link"
	facetTypeMention = "app.bsky.richtext.facet#mention"
)

// postRecordType is the $type of a feed post record.
const postRecordType = "app.bsky.feed.post"

// apiClient is the authenticated data-plane client: it performs the
// createRecord calls with a session obtained from the swappable provider
// and transparently rotates the access JWT on a 401 (one retry).
type apiClient struct {
	endpoint string
	http     *http.Client
	provider sessionProvider
}

func newAPIClient(cfg resolvedConfig, provider sessionProvider, client *http.Client) *apiClient {
	if client == nil {
		client = defaultClient()
	}
	return &apiClient{
		endpoint: cfg.endpoint,
		http:     client,
		provider: provider,
	}
}

// session returns a usable session for the configured account (logging in
// or rotating as needed). This is the only pre-write network call.
func (c *apiClient) session(ctx context.Context) (*session, error) {
	return c.provider.Session(ctx)
}

// createPost writes one post record as the session's account.
//
// On a 401 (expired access JWT) it rotates the session once and retries; a
// second 401 fails the call. All errors are safe to persist.
func (c *apiClient) createPost(ctx context.Context, s *session, rec *postRecord) (social.RecordRef, error) {
	in := createRecordRequest{
		Repo:       s.did,
		Collection: postCollection,
		Record:     rec,
	}
	var out createRecordResponse
	err := xrpcDo(ctx, c.endpoint, c.http, xrpcCreateRecord, in, &out, s.access)
	if err != nil {
		if isUnauthorized(err) {
			next, rerr := c.provider.Refresh(ctx)
			if rerr == nil {
				err = xrpcDo(ctx, c.endpoint, c.http, xrpcCreateRecord, in, &out, next.access)
				s = next
			}
		}
		if err != nil {
			return social.RecordRef{}, fmt.Errorf("%w: %v", ErrPublish, err)
		}
	}
	if strings.TrimSpace(out.URI) == "" || strings.TrimSpace(out.CID) == "" {
		return social.RecordRef{}, fmt.Errorf("%w: the server response is missing the record uri or cid", ErrPublish)
	}
	return social.RecordRef{URI: out.URI, CID: out.CID}, nil
}
