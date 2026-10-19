package bluesky_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/bluesky"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// testTime is the fixed base time for deterministic fixtures.
var testTime = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

// testToken is a high-entropy, mixed-class run that run.LooksLikeSecret
// catches: the fixture behind every "a secret must not leak" test.
const testToken = "aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"

const (
	credentialEnvName = "LOCAL_NEWSROOM_BLUESKY_TEST"
	testAppPassword   = "app-password-not-a-secret-looking-value"
	testHandle        = "dolphins.example"
	testDID           = "did:plc:u5cwb2mwiv2bfq53cjufe6yn"
)

// testPlanID builds a well-formed publication plan ID.
func testPlanID() publication.PublicationID {
	return publication.NewPublicationID(
		run.NewRunID("feed", "2026-07-11-dolphins", testTime),
		publication.NewArtifactIdentity("art-001", run.ArtifactKindRendered, "output/thread.txt", []byte("content"), "render/v1"),
		[]publication.TargetID{"bluesky-thread"},
		testTime,
	)
}

// The thread content used across the publish tests.
const (
	postOne   = "Dolphins open the 2026 season strong. Preview: https://example.com/preview."
	postTwo   = "The numbers back it up, and the depth chart is the story."
	postThree = "More: https://example.com/stats and https://example.com/roster."
)

// testRequest builds a full thread request for the standard thread.
func testRequest(dryRun bool, prior *publication.PublicationReceipt) social.Request {
	return social.Request{
		PlanID:       testPlanID(),
		TargetID:     "bluesky-thread",
		Posts:        social.PostsFromTexts([]string{postOne, postTwo, postThree}, nil),
		PriorReceipt: prior,
		DryRun:       dryRun,
	}
}

// fakeJWT builds a structurally valid JWT with the given exp (or no exp).
// When secretish, the signature carries testToken (for redaction tests).
// fakeJWT builds a syntactically valid three-part JWT with an expiry
// relative to the real clock (offset may be negative: an expired token).
// With secretish it carries the test token as its signature, to exercise
// secret redaction in error paths.
func fakeJWT(offset time.Duration, secretish bool) string {
	exp, _ := json.Marshal(struct {
		Exp int64 `json:"exp"`
	}{Exp: time.Now().Add(offset).Unix()})
	sig := "fake-signature"
	if secretish {
		sig = testToken
	}
	return "eyJhbGciOiJmYWtlIn0." + base64.RawURLEncoding.EncodeToString(exp) + "." + sig
}

// fakeRkey builds a deterministic 13-character record key (base32, like a
// real rkey).
func fakeRkey(i int) string {
	const chars = "234567abcdefghijklmnopqrstuvwxyz"
	n := 16*16*16*16*16*16 + i
	s := ""
	for j := 0; j < 13; j++ {
		s = string(chars[n%32]) + s
		n /= 32
	}
	return s
}

// fakeCID builds a deterministic base32 CID (59 characters).
func fakeCID(i int) string {
	const chars = "234567abcdefghijklmnopqrstuvwxyz"
	n := 1000 + i
	s := ""
	for j := 0; j < 52; j++ {
		s += string(chars[(((n*(j+7)+i)%32)+32)%32])
		n = n*31 + 7
	}
	return "bafyrei" + s
}

// fakeRef is the AT Protocol identity of the i-th published post.
func fakeRef(i int) social.RecordRef {
	return social.RecordRef{
		URI: "at://" + testDID + "/app.bsky.feed.post/" + fakeRkey(i),
		CID: fakeCID(i),
	}
}

// refBody is the JSON createRecord response body for the i-th post.
func refBody(i int) string {
	ref := fakeRef(i)
	return fmt.Sprintf(`{"uri":%q,"cid":%q}`, ref.URI, ref.CID)
}

// fakeXRPC is the in-test AT Protocol xrpc server.
//
// It captures every request (bodies and authorization headers) and plays
// scripted responses per endpoint; unset scripts use success defaults.
type fakeXRPC struct {
	t  *testing.T
	ts *httptest.Server

	mu sync.Mutex

	hits          int
	sessionBodies []string
	refreshBodies []string
	recordBodies  []string
	recordAuth    []string

	// Scripted behavior (applied per call index; the last entry repeats).
	// A zero status means 200; an empty body means the default success
	// body for that endpoint.
	sessionStatus int
	sessionBody   string
	refreshStatus int
	refreshBody   string
	recordStatus  []int
	recordBody    []string
}

func newFakeXRPC(t *testing.T) *fakeXRPC {
	fx := &fakeXRPC{t: t, sessionStatus: 200, refreshStatus: 200}
	fx.ts = httptest.NewServer(http.HandlerFunc(fx.handle))
	t.Cleanup(fx.ts.Close)
	return fx
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(data)
}

func (fx *fakeXRPC) handle(w http.ResponseWriter, r *http.Request) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.hits++

	switch r.URL.Path {
	case "/xrpc/com.atproto.server.createSession":
		fx.sessionBodies = append(fx.sessionBodies, readBody(fx.t, r))
		status := fx.sessionStatus
		if status == 0 {
			status = 200
		}
		body := fx.sessionBody
		if status == 200 && body == "" {
			body = sessionSuccessBody()
		}
		if status != 200 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	case "/xrpc/com.atproto.server.refreshSession":
		fx.refreshBodies = append(fx.refreshBodies, readBody(fx.t, r))
		status := fx.refreshStatus
		if status == 0 {
			status = 200
		}
		body := fx.refreshBody
		if status == 200 && body == "" {
			body = sessionSuccessBody()
		}
		if status != 200 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	case "/xrpc/com.atproto.repo.createRecord":
		idx := len(fx.recordBodies)
		fx.recordBodies = append(fx.recordBodies, readBody(fx.t, r))
		fx.recordAuth = append(fx.recordAuth, r.Header.Get("Authorization"))

		status, body := 200, ""
		if idx < len(fx.recordStatus) {
			status = fx.recordStatus[idx]
		} else if len(fx.recordStatus) > 0 {
			status = fx.recordStatus[len(fx.recordStatus)-1]
		}
		if idx < len(fx.recordBody) {
			body = fx.recordBody[idx]
		} else if len(fx.recordBody) > 0 {
			body = fx.recordBody[len(fx.recordBody)-1]
		}
		if body == "" && status == 200 {
			ref := fakeRef(idx)
			body = fmt.Sprintf(`{"uri":%q,"cid":%q}`, ref.URI, ref.CID)
		}
		if status != 200 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(body))
	default:
		fx.t.Errorf("unexpected xrpc path %q", r.URL.Path)
		w.WriteHeader(404)
	}
}

// sessionSuccessBody returns the createSession/refreshSession success JSON.
func sessionSuccessBody() string {
	return fmt.Sprintf(
		`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		testDID, testHandle,
		fakeJWT(time.Hour, false),
		fakeJWT(24*time.Hour, false),
	)
}

// newPublisher builds a publisher pointed at fx with the given password.
func (fx *fakeXRPC) newPublisher(t *testing.T, password string) *bluesky.Publisher {
	t.Helper()
	t.Setenv(credentialEnvName, password)
	cfg := bluesky.Config{
		Handle:      testHandle,
		APIEndpoint: fx.ts.URL,
		APIKeyEnv:   credentialEnvName,
		Client:      fx.ts.Client(),
	}
	pub, err := bluesky.New(cfg)
	if err != nil {
		t.Fatalf("bluesky.New: %v", err)
	}
	return pub
}

// decodeRecord decodes a captured createRecord body into its parts.
func decodeRecord(t *testing.T, body string) (repo, collection string, record map[string]any) {
	t.Helper()
	var req struct {
		Repo       string         `json:"repo"`
		Collection string         `json:"collection"`
		Record     map[string]any `json:"record"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("createRecord body is not JSON: %v: %s", err, body)
	}
	return req.Repo, req.Collection, req.Record
}
