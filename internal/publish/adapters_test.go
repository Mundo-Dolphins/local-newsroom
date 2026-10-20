package publish_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/bluesky"
	"github.com/Mundo-Dolphins/local-newsroom/internal/gitpub"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// ---------------------------------------------------------------------------
// Bluesky adapter: a compact in-test xrpc server (the same pattern the
// bluesky package uses in its own suite).
// ---------------------------------------------------------------------------

const (
	adapterHandle  = "dolphins.example"
	adapterDID     = "did:plc:u5cwb2mwiv2bfq53cjufe6yn"
	adapterEnvName = "PUBLISH_TEST_BLUESKY_PASSWORD"
)

// fakeJWT builds a syntactically valid three-part JWT.
func adapterJWT(offset time.Duration) string {
	exp, _ := json.Marshal(struct {
		Exp int64 `json:"exp"`
	}{Exp: time.Now().Add(offset).Unix()})
	return "eyJhbGciOiJmYWtlIn0." + base64.RawURLEncoding.EncodeToString(exp) + ".fake-signature"
}

func adapterRkey(i int) string {
	const chars = "234567abcdefghijklmnopqrstuvwxyz"
	n := 16*16*16*16*16*16 + i
	s := ""
	for j := 0; j < 13; j++ {
		s = string(chars[n%32]) + s
		n /= 32
	}
	return s
}

func adapterCID(i int) string {
	const chars = "234567abcdefghijklmnopqrstuvwxyz"
	n := 1000 + i
	s := ""
	for j := 0; j < 52; j++ {
		s += string(chars[(((n*(j+7)+i)%32)+32)%32])
		n = n*31 + 7
	}
	return "bafyrei" + s
}

func adapterRef(i int) social.RecordRef {
	return social.RecordRef{
		URI: "at://" + adapterDID + "/app.bsky.feed.post/" + adapterRkey(i),
		CID: adapterCID(i),
	}
}

func adapterSessionBody() string {
	return fmt.Sprintf(
		`{"did":%q,"handle":%q,"accessJwt":%q,"refreshJwt":%q}`,
		adapterDID, adapterHandle,
		adapterJWT(time.Hour), adapterJWT(24*time.Hour),
	)
}

// adapterFake is the minimal AT Protocol xrpc server for the adapter tests.
type adapterFake struct {
	t  *testing.T
	ts *httptest.Server

	mu           sync.Mutex
	recordHits   int
	recordStatus []int // scripted per call index; last entry repeats
}

func newAdapterFake(t *testing.T) *adapterFake {
	fx := &adapterFake{t: t}
	fx.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession", "/xrpc/com.atproto.server.refreshSession":
			_, _ = w.Write([]byte(adapterSessionBody()))
		case "/xrpc/com.atproto.repo.createRecord":
			fx.recordHits++
			status := 200
			if len(fx.recordStatus) > 0 {
				idx := fx.recordHits - 1
				if idx < len(fx.recordStatus) {
					status = fx.recordStatus[idx]
				} else {
					status = fx.recordStatus[len(fx.recordStatus)-1]
				}
			}
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			ref := adapterRef(fx.recordHits)
			_, _ = fmt.Fprintf(w, `{"uri":%q,"cid":%q}`, ref.URI, ref.CID)
		default:
			t.Errorf("unexpected xrpc path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fx.ts.Close)
	return fx
}

func (fx *adapterFake) recordCalls() int {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.recordHits
}

func (fx *adapterFake) newPublisher(t *testing.T) *bluesky.Publisher {
	t.Helper()
	t.Setenv(adapterEnvName, "app-password-plain-value")
	pub, err := bluesky.New(bluesky.Config{
		Handle:      adapterHandle,
		APIEndpoint: fx.ts.URL,
		APIKeyEnv:   adapterEnvName,
		Client:      fx.ts.Client(),
	})
	if err != nil {
		t.Fatalf("bluesky.New: %v", err)
	}
	return pub
}

// TestBlueskyAdapterPublishesOnceAndDeduplicatesOnResume drives the bridge
// directly: a full publish posts every post, and a retry carrying the
// receipt is an exact no-op inside the adapter (the dedup layer that
// protects even if the plan record is ever wrong).
func TestBlueskyAdapterPublishesOnceAndDeduplicatesOnResume(t *testing.T) {
	fx := newAdapterFake(t)
	pub := fx.newPublisher(t)
	bridge := NewBlueskyPublisherForTest(pub, "pub-20260714T120300Z-1b2c3d4e5f60", "thread")

	content, err := publish.ThreadContent("EU AI Act enters into force.", "What it means for local newsrooms.")
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}

	ctx := context.Background()
	receipt, err := bridge.Publish(ctx, content, nil)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if receipt == nil {
		t.Fatal("first publish returned no receipt")
	}
	if fx.recordCalls() != 2 {
		t.Fatalf("createRecord calls = %d, want 2 (both posts)", fx.recordCalls())
	}
	if !receipt.ContentDigest.Matches(string(publication.DigestOf(content))) {
		t.Fatalf("receipt digest %s does not match the canonical content", receipt.ContentDigest)
	}

	// A retry carrying the receipt must not create a single new record.
	again, err := bridge.Publish(ctx, content, receipt)
	if err != nil {
		t.Fatalf("resume publish: %v", err)
	}
	if again == nil {
		t.Fatal("resume publish returned no receipt")
	}
	if got := fx.recordCalls(); got != 2 {
		t.Fatalf("createRecord calls after resume = %d, want still 2 (nothing re-posted)", got)
	}
}

// TestBlueskyAdapterPartialFailureReturnsPartialReceipt pins the
// receipt-with-error mapping: a mid-thread failure returns the partial
// receipt together with the error, so the engine records a resumable
// partial success.
func TestBlueskyAdapterPartialFailureReturnsPartialReceipt(t *testing.T) {
	fx := newAdapterFake(t)
	fx.recordStatus = []int{200, 500}
	pub := fx.newPublisher(t)
	bridge := NewBlueskyPublisherForTest(pub, "pub-20260714T120300Z-1b2c3d4e5f60", "thread")

	content, err := publish.ThreadContent("First post goes up.", "Second post fails.", "Third never runs.")
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}

	receipt, err := bridge.Publish(context.Background(), content, nil)
	if err == nil {
		t.Fatal("expected an error for the failing post")
	}
	if receipt == nil {
		t.Fatal("a partial thread must return its partial receipt (the published post), not nothing")
	}
}

// NewBlueskyPublisherForTest constructs the adapter with a *bluesky.Publisher
// in tests (the production constructor is named NewBlueskyPublisher and has
// the same shape; this alias keeps the test file free of an unused-import
// trap if the adapter signature ever widens).
func NewBlueskyPublisherForTest(pub *bluesky.Publisher, planID publication.PublicationID, targetID publication.TargetID) publish.Publisher {
	return publish.NewBlueskyPublisher(pub, planID, targetID)
}

// ---------------------------------------------------------------------------
// Git adapter: a temporary real repository (the same pattern gitpub uses).
// ---------------------------------------------------------------------------

var testGitEnv = []string{
	"LC_ALL=C",
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_TERMINAL_PROMPT=0",
	"PATH=" + os.Getenv("PATH"),
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed "git" plus literal test args.
	cmd.Env = testGitEnv
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# test site\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "README.md")
	gitRun(t, root, "-c", "user.name=Setup", "-c", "user.email=setup@example.com", "commit", "-q", "-m", "initial")
	return root
}

// TestGitAdapterCommitsOnceAndSkipsRepeat drives the bridge twice with the
// same bytes: exactly one new commit, and the retry's receipt is
// digest-bound to the same content (the adapter's own idempotency).
func TestGitAdapterCommitsOnceAndSkipsRepeat(t *testing.T) {
	root := newTestRepo(t)

	pub, err := gitpub.New(context.Background(), gitpub.Config{
		RepoRoot:       root,
		TargetDir:      ".",
		Branch:         "newsroom/publish",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom-bot@example.com",
	})
	if err != nil {
		t.Fatalf("gitpub.New: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	bridge := publish.NewGitPublisher(pub, "pub-20260714T120300Z-1b2c3d4e5f60", "site",
		"content/news/20260714-ai-act.md", "EU AI Act enters into force", "run-20260714T120000Z-000000000000")

	content := []byte("# EU AI Act\n\nThe Act enters into force.\n")
	ctx := context.Background()

	first, err := bridge.Publish(ctx, content, nil)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if first == nil {
		t.Fatal("first publish returned no receipt")
	}
	if got := gitRun(t, root, "rev-list", "--count", "newsroom/publish"); got != "2" {
		t.Fatalf("commits = %s, want 2 (initial + the publication)", got)
	}
	if !first.ContentDigest.Matches(string(publication.DigestOf(content))) {
		t.Fatalf("git receipt digest %s must equal the single file's digest", first.ContentDigest)
	}

	// Retry with the same bytes: no new commit, same proven content.
	again, err := bridge.Publish(ctx, content, first)
	if err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if again == nil {
		t.Fatal("retry returned no receipt")
	}
	if got := gitRun(t, root, "rev-list", "--count", "newsroom/publish"); got != "2" {
		t.Fatalf("commits after retry = %s, want still 2 (no duplicate commit)", got)
	}
	if again.ExternalRecordID != first.ExternalRecordID {
		t.Fatalf("retry receipt record %q differs from the first %q", again.ExternalRecordID, first.ExternalRecordID)
	}
}
