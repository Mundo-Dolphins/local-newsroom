package social_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// testTime is the fixed base time for deterministic fixtures.
var testTime = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

var (
	testRunID = run.NewRunID("feed", "2026-07-11-dolphins", testTime)
)

// planID builds a well-formed publication plan ID.
func planID() publication.PublicationID {
	return publication.NewPublicationID(
		testRunID,
		publication.NewArtifactIdentity("art-001", run.ArtifactKindRendered, "output/thread.txt", []byte("content"), "render/v1"),
		[]publication.TargetID{"bluesky-thread"},
		testTime,
	)
}

// ref builds a content-addressed record reference.
func ref(i int) social.RecordRef {
	return social.RecordRef{
		URI: "at://did:plc:u5cwb2mwiv2bfq53cjufe6yn/app.bsky.feed.post/rkey" + string(rune('0'+i)),
		CID: "bafyreibjifzpqj6o6wcq3hejh7y4z4z2vmiklkvykc57tw3pcbx3kx" + string(rune('0'+i)),
	}
}

func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     social.Request
		wantErr string
	}{
		{name: "valid single post", req: social.Request{PlanID: planID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "hello"}}}},
		{name: "valid thread", req: social.Request{PlanID: planID(), TargetID: "bluesky-thread", Posts: []social.Post{{Text: "a"}, {Text: "b"}}}},
		{name: "empty plan ID", req: social.Request{TargetID: "bluesky-thread", Posts: []social.Post{{Text: "a"}}}, wantErr: "plan ID"},
		{name: "empty target ID", req: social.Request{PlanID: planID(), Posts: []social.Post{{Text: "a"}}}, wantErr: "target ID"},
		{name: "bad target ID", req: social.Request{PlanID: planID(), TargetID: "no spaces!", Posts: []social.Post{{Text: "a"}}}, wantErr: "target ID"},
		{name: "no posts", req: social.Request{PlanID: planID(), TargetID: "bluesky-thread"}, wantErr: "at least one post"},
		{name: "too many posts", req: social.Request{PlanID: planID(), TargetID: "bluesky-thread", Posts: make([]social.Post, social.MaxPosts+1)}, wantErr: "exceeding the bound"},
		{name: "valid prior receipt", req: social.Request{
			PlanID: planID(), TargetID: "bluesky-thread",
			Posts:        []social.Post{{Text: "a"}, {Text: "b"}},
			PriorReceipt: validReceipt(),
		}},
		{name: "invalid prior receipt", req: social.Request{
			PlanID: planID(), TargetID: "bluesky-thread",
			Posts:        []social.Post{{Text: "a"}},
			PriorReceipt: &publication.PublicationReceipt{},
		}, wantErr: "prior receipt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// validReceipt builds a receipt whose resume metadata names one published
// post.
func validReceipt() *publication.PublicationReceipt {
	return publication.NewReceipt(
		planID(), "bluesky-thread", "bluesky",
		"https://bsky.app/profile/dolphins.example/post/rkey0",
		"at://did:plc:u5cwb2mwiv2bfq53cjufe6yn/app.bsky.feed.post/rkey0",
		publication.DigestOf([]byte("a")), "", "bluesky/0.6",
		map[string]string{"post_1": social.EncodePostRef(ref(0))},
		testTime,
	)
}

func TestDigestOfPosts(t *testing.T) {
	single := social.DigestOfPosts([]social.Post{{Text: "one"}})
	if single != publication.DigestOf([]byte("one")) {
		t.Fatalf("single-post digest = %q, want the post's own digest %q", single, publication.DigestOf([]byte("one")))
	}

	multi := social.DigestOfPosts([]social.Post{{Text: "one"}, {Text: "two"}, {Text: "three"}})
	if multi != publication.DigestOf([]byte("one\x00two\x00three")) {
		t.Fatalf("thread digest = %q, want the NUL-joined texts %q", multi, publication.DigestOf([]byte("one\x00two\x00three")))
	}
}

func TestPostMetadataRoundTrip(t *testing.T) {
	refs := []social.RecordRef{ref(0), ref(1), ref(2)}
	res := &social.Result{Provider: "bluesky", Published: refs}
	meta := res.PostMetadata()
	if len(meta) != 3 {
		t.Fatalf("PostMetadata() has %d pairs, want 3", len(meta))
	}
	if meta["post_1"] != social.EncodePostRef(ref(0)) {
		t.Fatalf("post_1 = %q, want %q", meta["post_1"], social.EncodePostRef(ref(0)))
	}

	got, err := social.DecodePostRefs(meta)
	if err != nil {
		t.Fatalf("DecodePostRefs() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("DecodePostRefs() returned %d refs, want 3", len(got))
	}
	for i, want := range refs {
		if got[i] != want {
			t.Fatalf("decoded ref[%d] = %+v, want %+v (order must be publish order)", i, got[i], want)
		}
	}
}

func TestDecodePostRefsEmpty(t *testing.T) {
	got, err := social.DecodePostRefs(nil)
	if err != nil || got != nil {
		t.Fatalf("DecodePostRefs(nil) = (%v, %v), want (nil, nil)", got, err)
	}
	got, err = social.DecodePostRefs(map[string]string{"other": "value"})
	if err != nil || got != nil {
		t.Fatalf("DecodePostRefs(no post pairs) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestDecodePostRefsRejectsGaps(t *testing.T) {
	meta := map[string]string{
		"post_1": social.EncodePostRef(ref(0)),
		"post_3": social.EncodePostRef(ref(2)), // post_2 missing
	}
	if _, err := social.DecodePostRefs(meta); err == nil {
		t.Fatal("DecodePostRefs() with a gap succeeded, want an error")
	}
}

func TestDecodePostRefsRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]string
	}{
		{name: "no separator", meta: map[string]string{"post_1": "value-without-separator"}},
		{name: "empty uri", meta: map[string]string{"post_1": "|cid"}},
		{name: "empty cid", meta: map[string]string{"post_1": "uri|"}},
		{name: "two separators", meta: map[string]string{"post_1": "uri|cid|extra"}},
		{name: "bad index", meta: map[string]string{"post_x": "uri|cid"}},
		{name: "zero index", meta: map[string]string{"post_0": "uri|cid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := social.DecodePostRefs(tt.meta); err == nil {
				t.Fatalf("DecodePostRefs(%v) succeeded, want an error", tt.meta)
			}
		})
	}
}

func TestDecodePostRefsRejectsOverbound(t *testing.T) {
	meta := map[string]string{}
	for i := 0; i <= social.MaxPosts; i++ { // 31 > MaxPosts (30)
		meta[social.ResumeMetadataPrefix+string(rune('0'+i))] = "uri|cid"
	}
	// Use real numeric keys for indices beyond 9.
	meta = map[string]string{}
	for i := 1; i <= social.MaxPosts+1; i++ {
		meta[social.ResumeMetadataPrefix+itoa(i)] = "uri|cid"
	}
	if _, err := social.DecodePostRefs(meta); err == nil {
		t.Fatal("DecodePostRefs() over the bound succeeded, want an error")
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return "1" + string(rune('0'+(i-10))) // only needs 10..31 in this test
}

func TestReceiptFromResult(t *testing.T) {
	refs := []social.RecordRef{ref(0), ref(1)}
	res := &social.Result{
		Provider:         "bluesky",
		ExternalURI:      "https://bsky.app/profile/dolphins.example/post/rkey0",
		ExternalRecordID: refs[0].URI,
		Digest:           social.DigestOfPosts([]social.Post{{Text: "one"}, {Text: "two"}}),
		Published:        refs,
	}
	rec, err := res.Receipt(planID(), "bluesky-thread", "bluesky/0.6", testTime, map[string]string{"did": "did:plc:u5cwb2mwiv2bfq53cjufe6yn"})
	if err != nil {
		t.Fatalf("Receipt() error = %v", err)
	}
	if rec.ExternalRecordID != refs[0].URI {
		t.Fatalf("receipt external record ID = %q, want %q", rec.ExternalRecordID, refs[0].URI)
	}
	if rec.Metadata["post_1"] != social.EncodePostRef(refs[0]) {
		t.Fatalf("receipt metadata post_1 = %q", rec.Metadata["post_1"])
	}
	if rec.Metadata["did"] != "did:plc:u5cwb2mwiv2bfq53cjufe6yn" {
		t.Fatalf("receipt metadata did = %q", rec.Metadata["did"])
	}
	if len(rec.Metadata) != 3 {
		t.Fatalf("receipt metadata has %d pairs, want 3", len(rec.Metadata))
	}

	// The resumable state must round-trip through the receipt.
	got, err := social.DecodePostRefs(rec.Metadata)
	if err != nil {
		t.Fatalf("DecodePostRefs(receipt metadata) error = %v", err)
	}
	if len(got) != 2 || got[0] != refs[0] || got[1] != refs[1] {
		t.Fatalf("decoded resume state = %+v, want %+v", got, refs)
	}
}

func TestReceiptRefusesDryRun(t *testing.T) {
	res := &social.Result{Provider: "bluesky", DryRun: true}
	if _, err := res.Receipt(planID(), "bluesky-thread", "bluesky/0.6", testTime, nil); err != social.ErrDryRunReceipt {
		t.Fatalf("Receipt() for a dry run = %v, want ErrDryRunReceipt", err)
	}
}

func TestReceiptRefusesEmptyPublish(t *testing.T) {
	res := &social.Result{Provider: "bluesky"}
	if _, err := res.Receipt(planID(), "bluesky-thread", "bluesky/0.6", testTime, nil); err == nil {
		t.Fatal("Receipt() with no published posts succeeded, want an error")
	}
}

func TestToDryRunResult(t *testing.T) {
	ok := &social.Result{
		Provider: "bluesky",
		DryRun:   true,
		Digest:   publication.DigestOf([]byte("one")),
		Details:  "1 post(s): 3 graphemes",
	}
	out := ok.ToDryRunResult(testTime)
	if out.Outcome != publication.DryRunOutcomeOK {
		t.Fatalf("outcome = %q, want ok", out.Outcome)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("dry run result invalid: %v", err)
	}

	failed := &social.Result{
		Provider: "bluesky",
		DryRun:   true,
		Digest:   publication.DigestOf([]byte("one")),
		Details:  "1 post(s): 301 graphemes",
		Issues:   []publication.DryRunIssue{publication.NewDryRunIssue("over_grapheme_limit", "post 1 is 301 graphemes (max 300)")},
	}
	out = failed.ToDryRunResult(testTime)
	if out.Outcome != publication.DryRunOutcomeFailed {
		t.Fatalf("outcome = %q, want failed", out.Outcome)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("dry run result invalid: %v", err)
	}
}
