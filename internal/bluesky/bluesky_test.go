package bluesky_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/bluesky"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// ctx returns a context for tests (httptest is local; bounded timeouts
// would only add noise).
func ctx() context.Context {
	return context.Background()
}

// ---------------------------------------------------------------------------
// Configuration and credential safety
// ---------------------------------------------------------------------------

func TestNewValidation(t *testing.T) {
	t.Setenv(credentialEnvName, testAppPassword)
	fx := newFakeXRPC(t)

	tests := []struct {
		name    string
		cfg     bluesky.Config
		wantErr string
	}{
		{name: "valid", cfg: bluesky.Config{Handle: testHandle, APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName}},
		{name: "default endpoint", cfg: bluesky.Config{Handle: testHandle, APIKeyEnv: credentialEnvName}},
		{name: "leading @ tolerated", cfg: bluesky.Config{Handle: "@" + testHandle, APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName}},
		{name: "empty handle", cfg: bluesky.Config{Handle: " ", APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName}, wantErr: "handle is required"},
		{name: "single label handle", cfg: bluesky.Config{Handle: "nope", APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName}, wantErr: "not a valid AT Protocol handle"},
		{name: "bad handle label", cfg: bluesky.Config{Handle: "-bad.example", APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName}, wantErr: "not a valid AT Protocol handle"},
		{name: "relative endpoint", cfg: bluesky.Config{Handle: testHandle, APIEndpoint: "/xrpc", APIKeyEnv: credentialEnvName}, wantErr: "absolute http(s) URL"},
		{name: "ftp endpoint", cfg: bluesky.Config{Handle: testHandle, APIEndpoint: "ftp://example.com", APIKeyEnv: credentialEnvName}, wantErr: "absolute http(s) URL"},
		{name: "endpoint with userinfo", cfg: bluesky.Config{Handle: testHandle, APIEndpoint: "https://u:p@example.com", APIKeyEnv: credentialEnvName}, wantErr: "must not embed credentials"},
		{name: "bad env name", cfg: bluesky.Config{Handle: testHandle, APIEndpoint: fx.ts.URL, APIKeyEnv: "bad name"}, wantErr: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := bluesky.New(tt.cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("New() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("New() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewMissingCredential(t *testing.T) {
	t.Setenv(credentialEnvName, "")
	cfg := bluesky.Config{Handle: testHandle, APIEndpoint: "https://bsky.social", APIKeyEnv: credentialEnvName}
	_, err := bluesky.New(cfg)
	if err == nil {
		t.Fatal("New() succeeded with an unset credential variable, want an error")
	}
	if !strings.Contains(err.Error(), credentialEnvName) {
		t.Fatalf("New() error should name the variable, got: %v", err)
	}
	if strings.Contains(err.Error(), testAppPassword) {
		t.Fatalf("New() error leaked the credential: %v", err)
	}
}

func TestConfigHoldsNoSecret(t *testing.T) {
	t.Setenv(credentialEnvName, testAppPassword)
	fx := newFakeXRPC(t)
	pub, err := bluesky.New(bluesky.Config{Handle: testHandle, APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := pub.Config()
	if got.APIKeyEnv != credentialEnvName {
		t.Fatalf("Config().APIKeyEnv = %q, want %q", got.APIKeyEnv, credentialEnvName)
	}
	if got.Handle != testHandle {
		t.Fatalf("Config().Handle = %q, want %q", got.Handle, testHandle)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), testAppPassword) {
		t.Fatalf("Config leaked the credential")
	}
}

// ---------------------------------------------------------------------------
// Login / session
// ---------------------------------------------------------------------------

func TestLoginAndAuthHeader(t *testing.T) {
	fx := newFakeXRPC(t)
	fx.sessionBody = fmt.Sprintf(
		`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		testDID, testHandle, fakeJWT(time.Hour, false), fakeJWT(24*time.Hour, false),
	)
	pub := fx.newPublisher(t, testAppPassword)

	res, err := pub.Publish(ctx(), social.Request{
		PlanID:   testPlanID(),
		TargetID: "bluesky-thread",
		Posts:    []social.Post{{Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(fx.sessionBodies) != 1 {
		t.Fatalf("createSession calls = %d, want 1", len(fx.sessionBodies))
	}
	var sessReq struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := json.Unmarshal([]byte(fx.sessionBodies[0]), &sessReq); err != nil {
		t.Fatalf("createSession body is not JSON: %v", err)
	}
	if sessReq.Identifier != testHandle {
		t.Fatalf("createSession identifier = %q, want %q", sessReq.Identifier, testHandle)
	}
	if sessReq.Password != testAppPassword {
		t.Fatalf("createSession did not carry the credential from the environment")
	}
	var sessResp struct {
		AccessJWT string `json:"accessJwt"`
	}
	if err := json.Unmarshal([]byte(fx.sessionBody), &sessResp); err != nil {
		t.Fatalf("served session body not JSON: %v", err)
	}
	if len(fx.recordAuth) != 1 || fx.recordAuth[0] != "Bearer "+sessResp.AccessJWT {
		t.Fatalf("createRecord Authorization = %q, want the served access JWT bearer", fx.recordAuth)
	}
	if len(res.Published) != 1 {
		t.Fatalf("published %d posts, want 1", len(res.Published))
	}
}

func TestSessionProactiveRotation(t *testing.T) {
	fx := newFakeXRPC(t)
	// The access JWT is already expired: Session() must rotate it with the
	// refresh JWT before the first write.
	fx.sessionBody = fmt.Sprintf(
		`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		testDID, testHandle,
		fakeJWT(-time.Hour, false), // expired
		fakeJWT(24*time.Hour, false),
	)
	// The refresh returns a fresh access JWT with a distinct signature.
	freshAccess := fakeJWT(time.Hour, false) + "x"
	fx.refreshBody = fmt.Sprintf(`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		testDID, testHandle, freshAccess, fakeJWT(48*time.Hour, false))
	pub := fx.newPublisher(t, testAppPassword)

	res, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hello"}}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(fx.refreshBodies) != 1 {
		t.Fatalf("refreshSession calls = %d, want 1", len(fx.refreshBodies))
	}
	if len(fx.recordAuth) != 1 || fx.recordAuth[0] != "Bearer "+freshAccess {
		t.Fatalf("createRecord Authorization = %q, want the rotated JWT", fx.recordAuth)
	}
	_ = res
}

func TestSessionRotationOnUnauthorized(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)

	freshAccess := fakeJWT(time.Hour, false) + "x" // fresh, non-expired
	// First createRecord: 401 (credentials rejected); then success.
	fx.recordStatus = []int{401, 200}
	fx.refreshBody = fmt.Sprintf(`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		testDID, testHandle, freshAccess, fakeJWT(48*time.Hour, false))

	_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hello"}}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(fx.recordBodies) != 2 {
		t.Fatalf("createRecord calls = %d, want 2 (one retry after 401)", len(fx.recordBodies))
	}
	if !strings.Contains(fx.recordAuth[1], "Bearer "+freshAccess) {
		t.Fatalf("retried createRecord Authorization = %q, want the rotated JWT", fx.recordAuth[1])
	}
}

// ---------------------------------------------------------------------------
// Single post
// ---------------------------------------------------------------------------

func TestSinglePost(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)

	req := social.Request{
		PlanID:   testPlanID(),
		TargetID: "bluesky-thread",
		Posts: []social.Post{{
			Text:      postOne,
			Langs:     []string{"en"},
			CreatedAt: testTime,
		}},
	}
	res, err := pub.Publish(ctx(), req)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// The wire request: repo = session DID, collection = the feed post
	// lexicon type, and the record carries $type, text, createdAt, langs.
	if len(fx.recordBodies) != 1 {
		t.Fatalf("createRecord calls = %d, want 1", len(fx.recordBodies))
	}
	repo, collection, record := decodeRecord(t, fx.recordBodies[0])
	if repo != testDID {
		t.Fatalf("repo = %q, want the session DID %q", repo, testDID)
	}
	if collection != "app.bsky.feed.post" {
		t.Fatalf("collection = %q, want app.bsky.feed.post", collection)
	}
	if record["$type"] != "app.bsky.feed.post" {
		t.Fatalf("record $type = %v, want app.bsky.feed.post", record["$type"])
	}
	if record["text"] != postOne {
		t.Fatalf("record text = %v, want the post text", record["text"])
	}
	createdAt, _ := record["createdAt"].(string)
	if createdAt == "" {
		t.Fatal("record has no createdAt")
	}
	if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
		t.Fatalf("createdAt %q is not RFC3339: %v", createdAt, err)
	}
	if langs, _ := record["langs"].([]any); len(langs) != 1 || langs[0] != "en" {
		t.Fatalf("record langs = %v, want [en]", record["langs"])
	}
	if record["reply"] != nil {
		t.Fatalf("root post carries a reply block: %v", record["reply"])
	}

	// The result: the returned uri + cid, the canonical URL, the digest.
	if len(res.Published) != 1 {
		t.Fatalf("published %d posts, want 1", len(res.Published))
	}
	ref := fakeRef(0)
	if res.Published[0] != ref {
		t.Fatalf("published ref = %+v, want %+v", res.Published[0], ref)
	}
	wantURL := "https://bsky.app/profile/" + testHandle + "/post/" + fakeRkey(0)
	if res.ExternalURI != wantURL {
		t.Fatalf("ExternalURI = %q, want %q", res.ExternalURI, wantURL)
	}
	if res.ExternalRecordID != ref.URI {
		t.Fatalf("ExternalRecordID = %q, want %q", res.ExternalRecordID, ref.URI)
	}
	if res.Digest != social.DigestOfPosts(req.Posts) {
		t.Fatalf("Digest = %q, want the content digest", res.Digest)
	}

	// The receipt.
	rec, err := pub.Receipt(req, res, testTime)
	if err != nil {
		t.Fatalf("Receipt: %v", err)
	}
	if rec.ContentDigest != res.Digest {
		t.Fatalf("receipt digest = %q, want the result digest", rec.ContentDigest)
	}
	if rec.Metadata["post_1"] != social.EncodePostRef(ref) {
		t.Fatalf("receipt metadata post_1 = %q", rec.Metadata["post_1"])
	}
	if v := rec.Metadata["did_sha"]; v == "" || len(v) != 12 {
		t.Fatalf("receipt metadata did_sha = %q, want the 12-hex fingerprint", v)
	}
}

// ---------------------------------------------------------------------------
// Thread chaining
// ---------------------------------------------------------------------------

func TestThreadChaining(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)

	res, err := pub.Publish(ctx(), testRequest(false, nil))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(fx.recordBodies) != 3 {
		t.Fatalf("createRecord calls = %d, want 3", len(fx.recordBodies))
	}
	refs := []social.RecordRef{fakeRef(0), fakeRef(1), fakeRef(2)}

	for i, body := range fx.recordBodies {
		_, _, record := decodeRecord(t, body)
		reply, _ := record["reply"].(map[string]any)
		switch i {
		case 0:
			if reply != nil {
				t.Fatalf("post 1 (root) carries a reply block: %v", reply)
			}
		case 1:
			// root = post 1, parent = post 1.
			assertRef(t, reply, "root", refs[0])
			assertRef(t, reply, "parent", refs[0])
		case 2:
			// root = post 1, parent = post 2 (the immediately preceding).
			assertRef(t, reply, "root", refs[0])
			assertRef(t, reply, "parent", refs[1])
		}
	}

	if len(res.Published) != 3 {
		t.Fatalf("published %d posts, want 3", len(res.Published))
	}
	for i, want := range refs {
		if res.Published[i] != want {
			t.Fatalf("published[%d] = %+v, want %+v", i, res.Published[i], want)
		}
	}
}

func assertRef(t *testing.T, reply map[string]any, field string, want social.RecordRef) {
	t.Helper()
	raw, ok := reply[field].(map[string]any)
	if !ok {
		t.Fatalf("reply.%s missing: %v", field, reply)
	}
	if raw["uri"] != want.URI {
		t.Fatalf("reply.%s.uri = %v, want %q", field, raw["uri"], want.URI)
	}
	if raw["cid"] != want.CID {
		t.Fatalf("reply.%s.cid = %v, want %q", field, raw["cid"], want.CID)
	}
}

// ---------------------------------------------------------------------------
// Validation: graphemes, bytes, languages, text integrity
// ---------------------------------------------------------------------------

func TestUnicodeGraphemeValidation(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantErr  bool
		wantCode string
	}{
		{name: "exactly 300 graphemes ok", text: strings.Repeat("a", 300)},
		{name: "301 graphemes rejected", text: strings.Repeat("a", 301), wantErr: true, wantCode: "over_grapheme_limit"},
		{name: "300 multibyte chars ok (900 bytes)", text: strings.Repeat("日", 300)},
		{name: "1000 multibyte chars rejected on graphemes", text: strings.Repeat("日", 1000), wantErr: true, wantCode: "over_grapheme_limit"},
		{name: "3001 ascii bytes rejected on bytes", text: strings.Repeat("b", 3001), wantErr: true, wantCode: "over_byte_limit"},
		{name: "family emojis count once each (120 ok, 3000 bytes)", text: strings.Repeat("👨‍👩‍👧‍👦", 120)},
		{name: "121 family emojis over the byte limit", text: strings.Repeat("👨‍👩‍👧‍👦", 121), wantErr: true, wantCode: "over_byte_limit"},
		{name: "combining marks count once (299 é ok)", text: strings.Repeat("e\u0301", 299)},
		{name: "300 combining é ok", text: strings.Repeat("e\u0301", 300)},
		{name: "301 combining é rejected", text: strings.Repeat("e\u0301", 301), wantErr: true, wantCode: "over_grapheme_limit"},
		{name: "NUL byte rejected", text: "ab\x00cd", wantErr: true, wantCode: "invalid_text"},
		{name: "empty text rejected", text: "   \n  ", wantErr: true, wantCode: "empty_text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFakeXRPC(t)
			pub := fx.newPublisher(t, testAppPassword)
			req := social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: tt.text}}}
			res, err := pub.Publish(ctx(), req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Publish succeeded, want a validation failure")
				}
				if len(fx.recordBodies) != 0 {
					t.Fatalf("a rejected thread made %d write call(s), want none", len(fx.recordBodies))
				}
				if len(fx.sessionBodies) != 0 {
					t.Fatalf("a rejected thread logged in %d time(s), want none (validation is pre-login)", len(fx.sessionBodies))
				}
				if res == nil || len(res.Issues) == 0 {
					t.Fatalf("result issues = %v, want the validation issue(s)", res)
				}
				for _, is := range res.Issues {
					if is.Code == tt.wantCode {
						return
					}
				}
				t.Fatalf("issue codes = %v, want one with %q", issueCodes(res.Issues), tt.wantCode)
			}
			if err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if len(fx.recordBodies) != 1 {
				t.Fatalf("createRecord calls = %d, want 1", len(fx.recordBodies))
			}
		})
	}
}

func TestThreadLengthBound(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	posts := make([]social.Post, social.MaxPosts+1)
	for i := range posts {
		posts[i] = social.Post{Text: fmt.Sprintf("post %d", i+1)}
	}
	_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: posts})
	if err == nil {
		t.Fatal("Publish succeeded with more than MaxPosts posts, want a validation failure")
	}
	if len(fx.sessionBodies) != 0 || len(fx.recordBodies) != 0 {
		t.Fatal("an overbound thread made network calls, want none")
	}
}

func TestLangsValidation(t *testing.T) {
	tests := []struct {
		name     string
		langs    []string
		wantErr  bool
		wantCode string
	}{
		{name: "two tags ok", langs: []string{"en", "es"}},
		{name: "three tags ok", langs: []string{"en", "es", "pt-BR"}},
		{name: "four tags rejected", langs: []string{"en", "es", "pt", "fr"}, wantErr: true, wantCode: "too_many_langs"},
		{name: "bad tag rejected", langs: []string{"en-US-x"}, wantErr: true, wantCode: "invalid_lang"},
		{name: "empty tag rejected", langs: []string{""}, wantErr: true, wantCode: "invalid_lang"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFakeXRPC(t)
			pub := fx.newPublisher(t, testAppPassword)
			res, err := pub.Publish(ctx(), social.Request{
				PlanID: testPlanID(), TargetID: "bluesky-thread",
				Posts: []social.Post{{Text: "hello", Langs: tt.langs}},
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("Publish succeeded, want a validation failure")
				}
				for _, is := range res.Issues {
					if is.Code == tt.wantCode {
						return
					}
				}
				t.Fatalf("issue codes = %v, want %q", issueCodes(res.Issues), tt.wantCode)
			}
			if err != nil {
				t.Fatalf("Publish: %v", err)
			}
		})
	}
}

func TestDefaultLangs(t *testing.T) {
	t.Setenv(credentialEnvName, testAppPassword)
	fx := newFakeXRPC(t)
	pub, err := bluesky.New(bluesky.Config{
		Handle: testHandle, APIEndpoint: fx.ts.URL, APIKeyEnv: credentialEnvName,
		Client:       fx.ts.Client(),
		DefaultLangs: []string{"es"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hola"}}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, _, record := decodeRecord(t, fx.recordBodies[0])
	if langs, _ := record["langs"].([]any); len(langs) != 1 || langs[0] != "es" {
		t.Fatalf("record langs = %v, want the default [es]", record["langs"])
	}
}

func TestExternalReplyRejected(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	res, err := pub.Publish(ctx(), social.Request{
		PlanID:   testPlanID(),
		TargetID: "bluesky-thread",
		Posts: []social.Post{{
			Text:  "hello",
			Reply: &social.ReplyRef{Root: fakeRef(0), Parent: fakeRef(0)},
		}},
	})
	if err == nil {
		t.Fatal("Publish succeeded with an external reply reference, want a validation failure")
	}
	if len(fx.recordBodies) != 0 {
		t.Fatal("an external-reply request made write calls, want none")
	}
	for _, is := range res.Issues {
		if is.Code == "external_reply" {
			return
		}
	}
	t.Fatalf("issue codes = %v, want external_reply", issueCodes(res.Issues))
}

func issueCodes(issues []publication.DryRunIssue) []string {
	out := make([]string, len(issues))
	for i, is := range issues {
		out[i] = is.Code
	}
	return out
}

// ---------------------------------------------------------------------------
// Facets
// ---------------------------------------------------------------------------

func TestFacetByteOffsets(t *testing.T) {
	// Multi-byte characters before and inside the text: the facet must
	// address UTF-8 bytes, so text[byteStart:byteEnd] is the URL itself.
	text := "Événement à suivre: https://example.com/a?x=1. Fin."
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	if _, err := pub.Publish(ctx(), social.Request{
		PlanID: testPlanID(), TargetID: "bluesky-thread",
		Posts: []social.Post{{Text: text}},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, _, record := decodeRecord(t, fx.recordBodies[0])
	facets, _ := record["facets"].([]any)
	if len(facets) != 1 {
		t.Fatalf("facets = %d, want 1: %v", len(facets), facets)
	}
	facet := facets[0].(map[string]any)
	index := facet["index"].(map[string]any)
	start := int(index["byteStart"].(float64))
	end := int(index["byteEnd"].(float64))
	if got := text[start:end]; got != "https://example.com/a?x=1" {
		t.Fatalf("text[%d:%d] = %q, want the URL (trailing dot not part of it)", start, end, got)
	}
	feature := facet["features"].([]any)[0].(map[string]any)
	if feature["$type"] != "app.bsky.richtext.facet#link" {
		t.Fatalf("feature $type = %v, want the link feature", feature["$type"])
	}
	if feature["link"].(map[string]any)["uri"] != "https://example.com/a?x=1" {
		t.Fatalf("link uri = %v", feature["link"])
	}
}

func TestFacetsMultipleURLs(t *testing.T) {
	text := "One https://a.example/1 and two https://b.example/2?x=(y) end."
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	if _, err := pub.Publish(ctx(), social.Request{
		PlanID: testPlanID(), TargetID: "bluesky-thread",
		Posts: []social.Post{{Text: text}},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, _, record := decodeRecord(t, fx.recordBodies[0])
	facets := record["facets"].([]any)
	if len(facets) != 2 {
		t.Fatalf("facets = %d, want 2: %v", len(facets), facets)
	}
	first := facets[0].(map[string]any)["index"].(map[string]any)
	if got := text[int(first["byteStart"].(float64)):int(first["byteEnd"].(float64))]; got != "https://a.example/1" {
		t.Fatalf("first facet covers %q, want the first URL", got)
	}
	second := facets[1].(map[string]any)["index"].(map[string]any)
	if got := text[int(second["byteStart"].(float64)):int(second["byteEnd"].(float64))]; got != "https://b.example/2?x=(y)" {
		t.Fatalf("second facet covers %q, want the second URL with balanced parens", got)
	}
}

func TestExplicitFacetPreventsDuplicate(t *testing.T) {
	text := "See https://example.com/x."
	// An explicit link facet over the URL: the auto-linker must not
	// generate a duplicate for the covered span.
	explicit := []social.Facet{{
		ByteStart: strings.Index(text, "https://example.com/x"),
		ByteEnd:   len(text) - 1, // exclude the trailing dot
		Features:  []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "https://example.com/x"}}},
	}}
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	if _, err := pub.Publish(ctx(), social.Request{
		PlanID: testPlanID(), TargetID: "bluesky-thread",
		Posts: []social.Post{{Text: text, Facets: explicit}},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, _, record := decodeRecord(t, fx.recordBodies[0])
	if n := len(record["facets"].([]any)); n != 1 {
		t.Fatalf("facets = %d, want exactly 1 (the explicit one, no duplicate)", n)
	}
}

func TestMentionFacetDeferred(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	text := "hi @someone.example"
	res, err := pub.Publish(ctx(), social.Request{
		PlanID: testPlanID(), TargetID: "bluesky-thread",
		Posts: []social.Post{{
			Text: text,
			Facets: []social.Facet{{
				ByteStart: 3,
				ByteEnd:   len(text),
				Features:  []social.Feature{{Type: social.FeatureMention, Fields: map[string]string{"did": testDID}}},
			}},
		}},
	})
	if err == nil {
		t.Fatal("Publish succeeded with a mention facet, want the deferred-facet rejection")
	}
	if len(fx.recordBodies) != 0 {
		t.Fatal("a mention-facet request made write calls, want none")
	}
	for _, is := range res.Issues {
		if is.Code == "deferred_facet" {
			return
		}
	}
	t.Fatalf("issue codes = %v, want deferred_facet", issueCodes(res.Issues))
}

func TestInvalidFacetOffsetsRejected(t *testing.T) {
	tests := []struct {
		name   string
		facets []social.Facet
	}{
		{name: "end beyond text", facets: []social.Facet{{ByteStart: 0, ByteEnd: 1000, Features: []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "https://e.example"}}}}}},
		{name: "zero-width", facets: []social.Facet{{ByteStart: 3, ByteEnd: 3, Features: []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "https://e.example"}}}}}},
		{name: "negative start", facets: []social.Facet{{ByteStart: -1, ByteEnd: 3, Features: []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "https://e.example"}}}}}},
		{name: "overlapping", facets: []social.Facet{
			{ByteStart: 0, ByteEnd: 10, Features: []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "https://e.example"}}}},
			{ByteStart: 5, ByteEnd: 15, Features: []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "https://e.example"}}}},
		}},
		{name: "unknown feature", facets: []social.Facet{{ByteStart: 0, ByteEnd: 3, Features: []social.Feature{{Type: "bold"}}}}},
		{name: "non-http link", facets: []social.Facet{{ByteStart: 0, ByteEnd: 3, Features: []social.Feature{{Type: social.FeatureLink, Fields: map[string]string{"uri": "ftp://e.example"}}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFakeXRPC(t)
			pub := fx.newPublisher(t, testAppPassword)
			_, err := pub.Publish(ctx(), social.Request{
				PlanID: testPlanID(), TargetID: "bluesky-thread",
				Posts: []social.Post{{Text: "some text here", Facets: tt.facets}},
			})
			if err == nil {
				t.Fatal("Publish succeeded, want a facet validation failure")
			}
			if len(fx.recordBodies) != 0 {
				t.Fatal("an invalid-facet request made write calls, want none")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Failure and resume
// ---------------------------------------------------------------------------

func TestFirstPostFailure(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	fx.recordStatus = []int{500, 200, 200}
	fx.recordBody = []string{
		fmt.Sprintf(`{"error":"InternalServerError","message":"boom %s"}`, testToken), ``, ``,
	}

	res, err := pub.Publish(ctx(), testRequest(false, nil))
	if err == nil {
		t.Fatal("Publish succeeded, want a first-post failure")
	}
	if !errors.Is(err, bluesky.ErrPublish) {
		t.Fatalf("error = %v, want ErrPublish", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaked the token: %v", err)
	}
	if res == nil || len(res.Published) != 0 {
		t.Fatalf("published = %v, want none after a first-post failure", res.Published)
	}
	// Nothing may land in a receipt built from the empty result.
	if _, rerr := pub.Receipt(testRequest(false, nil), res, testTime); rerr == nil {
		t.Fatal("Receipt succeeded with zero published posts, want an error")
	}
}

func TestMidThreadFailureAndResumableRetry(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)

	// Post 3 fails on the first attempt.
	fx.recordStatus = []int{200, 200, 400, 200, 200}
	fx.recordBody = []string{``, ``, `{"error":"TooMany","message":"rate limit"}`, ``, ``}

	req := testRequest(false, nil)
	fullDigest := social.DigestOfPosts(req.Posts)

	// Attempt 1: two posts land, the third fails.
	res, err := pub.Publish(ctx(), req)
	if err == nil {
		t.Fatal("Publish succeeded, want a mid-thread failure")
	}
	if len(res.Published) != 2 {
		t.Fatalf("published = %d, want 2 (the partial state)", len(res.Published))
	}
	if len(fx.recordBodies) != 3 {
		t.Fatalf("createRecord calls = %d, want 3 (two success + one failure)", len(fx.recordBodies))
	}

	// The partial receipt: digest-bound to the FULL approved content, with
	// the per-post state in metadata.
	rec, err := pub.Receipt(req, res, testTime)
	if err != nil {
		t.Fatalf("Receipt (partial): %v", err)
	}
	if rec.ContentDigest != fullDigest {
		t.Fatalf("partial receipt digest = %q, want the full-content digest (the approved artifact)", rec.ContentDigest)
	}
	if rec.ExternalRecordID != fakeRef(0).URI {
		t.Fatalf("receipt external record ID = %q, want the first post's AT URI", rec.ExternalRecordID)
	}
	if len(rec.Metadata) != 3 { // post_1, post_2, did_sha
		t.Fatalf("receipt metadata = %v (want 3 pairs)", rec.Metadata)
	}

	// Attempt 2: resume from the receipt — only the third post is written,
	// chained onto the two that already exist.
	fx.recordStatus = []int{200, 200, 400, 200}
	fx.recordBody = []string{``, ``, `{"error":"TooMany","message":"rate limit"}`, refBody(2)}
	before := len(fx.recordBodies)
	res2, err := pub.Publish(ctx(), social.Request{
		PlanID:       req.PlanID,
		TargetID:     req.TargetID,
		Posts:        req.Posts,
		PriorReceipt: rec,
	})
	if err != nil {
		t.Fatalf("resumed Publish: %v", err)
	}
	newCalls := len(fx.recordBodies) - before
	if newCalls != 1 {
		t.Fatalf("resume made %d createRecord calls, want exactly 1 (no duplication)", newCalls)
	}
	_, _, record := decodeRecord(t, fx.recordBodies[len(fx.recordBodies)-1])
	reply := record["reply"].(map[string]any)
	assertRef(t, reply, "root", fakeRef(0))
	assertRef(t, reply, "parent", fakeRef(1))
	if len(res2.Published) != 3 {
		t.Fatalf("resumed published = %d, want the full 3", len(res2.Published))
	}
	for i, want := range []social.RecordRef{fakeRef(0), fakeRef(1), fakeRef(2)} {
		if res2.Published[i] != want {
			t.Fatalf("resumed published[%d] = %+v, want %+v", i, res2.Published[i], want)
		}
	}

	// The final receipt carries the whole thread.
	rec2, err := pub.Receipt(req, res2, testTime)
	if err != nil {
		t.Fatalf("Receipt (full): %v", err)
	}
	if len(rec2.Metadata) != 4 { // post_1..post_3, did_sha
		t.Fatalf("full receipt metadata = %v (want 4 pairs)", rec2.Metadata)
	}

	// Attempt 3: an exact retry with the full receipt is a no-op.
	before = len(fx.recordBodies)
	res3, err := pub.Publish(ctx(), social.Request{
		PlanID:       req.PlanID,
		TargetID:     req.TargetID,
		Posts:        req.Posts,
		PriorReceipt: rec2,
	})
	if err != nil {
		t.Fatalf("exact-retry Publish: %v", err)
	}
	if n := len(fx.recordBodies) - before; n != 0 {
		t.Fatalf("exact retry made %d write calls, want none", n)
	}
	if len(res3.Published) != 3 {
		t.Fatalf("exact retry published = %d, want 3 (the existing thread)", len(res3.Published))
	}
}

// didSHA is the test-side twin of the adapter's DID fingerprint.
func didSHA(did string) string {
	sum := sha256.Sum256([]byte(did))
	return hex.EncodeToString(sum[:6])
}

// TestResumeAccountMismatch checks that resuming another account's thread
// fails before any write.
func TestResumeAccountMismatch(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	// A receipt whose thread was published by a different account.
	otherDid := "did:plc:aaaaaaaabbbbbbbbdddddddddeeeeeeee"
	req := testRequest(false, nil)
	rec := publication.NewReceipt(
		req.PlanID, req.TargetID, "bluesky",
		"https://bsky.app/profile/other.example/post/r",
		"at://"+otherDid+"/app.bsky.feed.post/rkey0",
		fullThreadDigest(req), "", "bluesky/0.6",
		map[string]string{
			"post_1":  social.EncodePostRef(social.RecordRef{URI: "at://" + otherDid + "/app.bsky.feed.post/rkey0", CID: fakeCID(0)}),
			"did_sha": didSHA(otherDid),
		},
		testTime,
	)
	res, err := pub.Publish(ctx(), social.Request{
		PlanID: req.PlanID, TargetID: req.TargetID, Posts: req.Posts, PriorReceipt: rec,
	})
	if err == nil {
		t.Fatal("Publish succeeded resuming another account's thread, want an account mismatch")
	}
	if !errors.Is(err, bluesky.ErrAccountMismatch) {
		t.Fatalf("error = %v, want ErrAccountMismatch", err)
	}
	if len(fx.recordBodies) != 0 {
		t.Fatal("an account mismatch made write calls, want none")
	}
	_ = res
}

func fullThreadDigest(req social.Request) publication.Digest {
	return social.DigestOfPosts(req.Posts)
}

func TestReceiptMetadataBounds(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	posts := make([]social.Post, social.MaxPosts)
	for i := range posts {
		posts[i] = social.Post{Text: fmt.Sprintf("post %d of %d", i+1, social.MaxPosts)}
	}
	req := social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: posts}
	res, err := pub.Publish(ctx(), req)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rec, err := pub.Receipt(req, res, testTime)
	if err != nil {
		t.Fatalf("Receipt: %v (metadata = %d pairs)", err, len(rec.Metadata))
	}
	if len(rec.Metadata) > 32 {
		t.Fatalf("receipt metadata = %d pairs, exceeding the contract bound of 32", len(rec.Metadata))
	}
	// The resume state must round-trip.
	refs, err := social.DecodePostRefs(rec.Metadata)
	if err != nil {
		t.Fatalf("DecodePostRefs: %v", err)
	}
	if len(refs) != social.MaxPosts {
		t.Fatalf("decoded %d resume refs, want %d", len(refs), social.MaxPosts)
	}
}

func TestResumeRejectsCorruptState(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	req := testRequest(false, nil)
	rec := publication.NewReceipt(
		req.PlanID, req.TargetID, "bluesky",
		"https://bsky.app/profile/"+testHandle+"/post/r",
		fakeRef(0).URI, fullThreadDigest(req), "", "bluesky/0.6",
		map[string]string{"post_1": social.EncodePostRef(fakeRef(0)), "post_3": social.EncodePostRef(fakeRef(2))}, // gap
		testTime,
	)
	if _, err := pub.Publish(ctx(), social.Request{
		PlanID: req.PlanID, TargetID: req.TargetID, Posts: req.Posts, PriorReceipt: rec,
	}); err == nil {
		t.Fatal("Publish succeeded with a gapped resume state, want ErrResume")
	}
}

// ---------------------------------------------------------------------------
// Auth redaction
// ---------------------------------------------------------------------------

func TestAuthRedaction(t *testing.T) {
	// 1. A login failure that echoes the password must redact it.
	t.Run("login failure redacts password", func(t *testing.T) {
		fx := newFakeXRPC(t)
		pub := fx.newPublisher(t, testToken) // the credential IS the test token
		fx.sessionStatus = 400
		fx.sessionBody = fmt.Sprintf(`{"error":"InvalidRequest","message":"rejected %s"}`, testToken)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if err == nil {
			t.Fatal("Publish succeeded, want a login failure")
		}
		if !errors.Is(err, bluesky.ErrSession) {
			t.Fatalf("error = %v, want ErrSession", err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("error leaked the credential: %v", err)
		}
	})

	// 2. A write failure that echoes the access JWT must redact it.
	t.Run("write failure redacts jwt", func(t *testing.T) {
		fx := newFakeXRPC(t)
		pub := fx.newPublisher(t, testAppPassword)
		fx.recordStatus = []int{500}
		fx.recordBody = []string{fmt.Sprintf(`{"error":"InternalServerError","message":"jwt %s expired"}`, testToken)}
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if err == nil {
			t.Fatal("Publish succeeded, want a write failure")
		}
		if strings.Contains(err.Error(), testToken) {
			t.Fatalf("error leaked the JWT: %v", err)
		}
	})

	// 3. Receipt metadata never carries credential-shaped values: the
	//    receipt built from a successful run must survive Validate with no
	//    redaction of its resume state.
	t.Run("receipt stays intact", func(t *testing.T) {
		fx := newFakeXRPC(t)
		pub := fx.newPublisher(t, testAppPassword)
		req := testRequest(false, nil)
		res, err := pub.Publish(ctx(), req)
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		rec, err := pub.Receipt(req, res, testTime)
		if err != nil {
			t.Fatalf("Receipt: %v", err)
		}
		for _, v := range rec.Metadata {
			if strings.Contains(v, run.RedactedPlaceholder) {
				t.Fatalf("receipt metadata was redacted: %v", rec.Metadata)
			}
		}
		if strings.Contains(rec.ExternalRecordID, run.RedactedPlaceholder) {
			t.Fatalf("external record ID was redacted: %q", rec.ExternalRecordID)
		}
	})
}

// ---------------------------------------------------------------------------
// Dry run
// ---------------------------------------------------------------------------

func TestDryRunMakesNoNetworkCalls(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)

	res, err := pub.Publish(ctx(), testRequest(true, nil))
	if err != nil {
		t.Fatalf("dry run Publish: %v", err)
	}
	if fx.hits != 0 {
		t.Fatalf("a dry run made %d HTTP call(s), want zero (no login, no write)", fx.hits)
	}
	if !res.DryRun {
		t.Fatal("result.DryRun = false, want true")
	}
	if len(res.Published) != 0 {
		t.Fatalf("a dry run reports %d published posts, want none", len(res.Published))
	}
	// The report shows the post order and lengths.
	if !strings.Contains(res.Details, "3 posts") || !strings.Contains(res.Details, "post 1:") || !strings.Contains(res.Details, "post 3:") {
		t.Fatalf("dry run details = %q, want the post order and lengths", res.Details)
	}
	if res.ExternalURI != "" || res.ExternalRecordID != "" {
		t.Fatalf("a dry run claims an external location: %q %q", res.ExternalURI, res.ExternalRecordID)
	}

	// The rehearsal record binds the same digest as a real run would.
	drr := res.ToDryRunResult(testTime)
	if drr.Outcome != publication.DryRunOutcomeOK {
		t.Fatalf("dry run outcome = %q, want ok", drr.Outcome)
	}
	if err := drr.Validate(); err != nil {
		t.Fatalf("dry run result invalid: %v", err)
	}
	realDigest := social.DigestOfPosts(testRequest(false, nil).Posts)
	if drr.ContentDigest != realDigest {
		t.Fatalf("dry run digest = %q, want the real content digest %q", drr.ContentDigest, realDigest)
	}

	// And it refuses a receipt.
	if _, err := pub.Receipt(testRequest(true, nil), res, testTime); err != social.ErrDryRunReceipt {
		t.Fatalf("Receipt for a dry run = %v, want ErrDryRunReceipt", err)
	}
}

func TestDryRunReportsBlockers(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	req := testRequest(true, nil)
	req.Posts[1].Text = strings.Repeat("x", 301)

	res, err := pub.Publish(ctx(), req)
	if err != nil {
		t.Fatalf("dry run must not error on content blockers: %v", err)
	}
	if fx.hits != 0 {
		t.Fatalf("a dry run made %d HTTP call(s), want zero", fx.hits)
	}
	drr := res.ToDryRunResult(testTime)
	if drr.Outcome != publication.DryRunOutcomeFailed {
		t.Fatalf("dry run outcome = %q, want failed", drr.Outcome)
	}
	found := false
	for _, is := range drr.Issues {
		if is.Code == "over_grapheme_limit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dry run issues = %v, want over_grapheme_limit", issueCodes(drr.Issues))
	}
	if err := drr.Validate(); err != nil {
		t.Fatalf("dry run result invalid: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Malformed API responses
// ---------------------------------------------------------------------------

func TestMalformedResponses(t *testing.T) {
	t.Run("session missing did", func(t *testing.T) {
		fx := newFakeXRPC(t)
		fx.sessionBody = fmt.Sprintf(`{"handle":%q,"accessJwt":%q}`, testHandle, fakeJWT(time.Hour, false))
		pub := fx.newPublisher(t, testAppPassword)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if !errors.Is(err, bluesky.ErrSession) {
			t.Fatalf("error = %v, want ErrSession", err)
		}
		if len(fx.recordBodies) != 0 {
			t.Fatal("writes attempted despite a broken session")
		}
	})

	t.Run("session missing access jwt", func(t *testing.T) {
		fx := newFakeXRPC(t)
		fx.sessionBody = fmt.Sprintf(`{"did":%q,"handle":%q}`, testDID, testHandle)
		pub := fx.newPublisher(t, testAppPassword)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if !errors.Is(err, bluesky.ErrSession) {
			t.Fatalf("error = %v, want ErrSession", err)
		}
	})

	t.Run("session 401", func(t *testing.T) {
		fx := newFakeXRPC(t)
		fx.sessionStatus = 401
		fx.sessionBody = `{"error":"Unauthorized","message":"bad credentials"}`
		pub := fx.newPublisher(t, testAppPassword)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if !errors.Is(err, bluesky.ErrSession) {
			t.Fatalf("error = %v, want ErrSession", err)
		}
	})

	t.Run("record missing cid", func(t *testing.T) {
		fx := newFakeXRPC(t)
		fx.recordBody = []string{fmt.Sprintf(`{"uri":%q}`, fakeRef(0).URI)}
		pub := fx.newPublisher(t, testAppPassword)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if !errors.Is(err, bluesky.ErrPublish) {
			t.Fatalf("error = %v, want ErrPublish", err)
		}
	})

	t.Run("record non-JSON 200", func(t *testing.T) {
		fx := newFakeXRPC(t)
		fx.recordBody = []string{`this is not json`}
		pub := fx.newPublisher(t, testAppPassword)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if !errors.Is(err, bluesky.ErrPublish) {
			t.Fatalf("error = %v, want ErrPublish", err)
		}
	})

	t.Run("record 400 with envelope", func(t *testing.T) {
		fx := newFakeXRPC(t)
		fx.recordStatus = []int{400}
		fx.recordBody = []string{`{"error":"InvalidRequest","message":"text too long"}`}
		pub := fx.newPublisher(t, testAppPassword)
		_, err := pub.Publish(ctx(), social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}})
		if !errors.Is(err, bluesky.ErrPublish) {
			t.Fatalf("error = %v, want ErrPublish", err)
		}
		if !strings.Contains(err.Error(), "InvalidRequest") {
			t.Fatalf("error should carry the xrpc code: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// did:key receipts
// ---------------------------------------------------------------------------

func TestDidKeyReceiptRejected(t *testing.T) {
	fx := newFakeXRPC(t)
	// A did:key account (long base64url identifier).
	didKey := "did:key:z6MkhaXgBZq7iFK3kM4pQ2rS5tU8vW1yE6gH9jK2mN3oP4qR"
	fx.sessionBody = fmt.Sprintf(`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		didKey, testHandle, fakeJWT(time.Hour, false), fakeJWT(24*time.Hour, false))
	fx.recordBody = []string{fmt.Sprintf(`{"uri":"at://%s/app.bsky.feed.post/%s","cid":%q}`, didKey, fakeRkey(0), fakeCID(0))}

	pub := fx.newPublisher(t, testAppPassword)
	req := social.Request{PlanID: testPlanID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hi"}}}
	res, err := pub.Publish(ctx(), req)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_, err = pub.Receipt(req, res, testTime)
	if err == nil {
		t.Fatal("Receipt succeeded for a did:key account, want the documented v0.6 rejection")
	}
	if !strings.Contains(err.Error(), "did:key") {
		t.Fatalf("rejection should name the limitation: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

func TestRecordBuildIsDeterministic(t *testing.T) {
	fx := newFakeXRPC(t)
	pub := fx.newPublisher(t, testAppPassword)
	post := social.Post{
		Text:      postOne,
		Langs:     []string{"en"},
		CreatedAt: testTime,
	}
	for i := 0; i < 2; i++ {
		req := social.Request{
			PlanID: testPlanID(), TargetID: "bluesky-thread",
			Posts: []social.Post{post},
		}
		if _, err := pub.Publish(ctx(), req); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	// The records must be byte-identical (same createdAt, same facets) —
	// the digest binding and audit trail depend on determinism.
	if a, b := fx.recordBodies[0], fx.recordBodies[1]; a != b {
		t.Fatalf("record builds differ across identical inputs:\n%s\nvs\n%s", a, b)
	}
}
