package tests

// v0.6 offline publication release-gate end-to-end tests.
//
// This file drives the actual `newsroom` binary as an external process through
// the complete v0.6 publication chain:
//
//	run (with a publication preset from an external editorial bundle)
//	  -> publish dry-run (zero external side effects)
//	  -> digest-bound approval
//	  -> Hugo staging -> temporary git checkout commit
//	  -> Bluesky thread against a fake AT Protocol server
//	  -> persisted receipts -> resume after partial failure -> idempotent repeat
//
// Everything is local and deterministic: a fake OpenAI-compatible LLM (shared
// with the v0.5 suite), a fixture article server, a temporary git checkout,
// and a fake xrpc server. No real oMLX, SearXNG, web, git remote, or Bluesky
// is ever contacted.
//
// Scenario map (release-gate requirements):
//
//   1. Hugo-only success (stage, commit, receipt)        TestE2E_v06_HugoOnlyPublish
//   2. Bluesky single-post success                       TestE2E_v06_BlueskySinglePost
//   3. Multi-post thread with reply references           TestE2E_v06_BlueskyThread
//   4. Multi-target publication (Hugo + Bluesky)         TestE2E_v06_MultiTarget
//   5. Publish blocked when the run is not READY         TestE2E_v06_NotReadyBlocksPublish
//   6. Publish blocked without approval                  TestE2E_v06_ApprovalRequired
//   7. Stale approval after the content changes          TestE2E_v06_StaleApproval
//   8. Partial failure, resume, no duplicates            TestE2E_v06_PartialFailureResume
//   9. Git dirty tree fails safely                       TestE2E_v06_GitDirtyTree
//  10. Repeated publish creates no duplicates            TestE2E_v06_RepeatPublishNoDups
//  11. Credentials never persisted                       TestE2E_v06_SecretSweep
//  12. External bundle resolution (manifest)             TestE2E_v06_ExternalBundle
//  13. v0.5 run workflow unaffected                      TestE2E_v06_V05RunUnaffected

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ---------------------------------------------------------------------------
// Shared v0.6 constants
// ---------------------------------------------------------------------------

const (
	// v6Password is the Bluesky app password. It exists only in the child
	// process environment (under v6EnvVar) — never in any config file, run
	// artifact, receipt, or error message. The secret-sweep scenario asserts
	// exactly that.
	v6Password = "e2e-app-password-7f3a9c"

	// v6EnvVar is the environment variable name referenced by the bluesky.yaml
	// api_key field (the env-reference-only secret rule).
	v6EnvVar = "NEWSROOM_E2E_BSKY_PASSWORD"

	// v6Handle is a syntactically valid AT Protocol handle (dotted labels).
	v6Handle = "e2e-tester.bsky.social"

	// v6Did is the account DID the fake PDS reports. It deliberately is NOT a
	// did:key (those are rejected for resumable publication receipts) and is
	// free-form enough to assert the did_sha fingerprint binding.
	v6Did = "did:plc:e2e000000000000000000001"
)

// v6Posts are the thread posts the writer artifact carries; each is well
// under the 300-grapheme post limit.
var v6Posts = []string{
	"The city council approved the 2027 budget on Tuesday night.",
	"Renewable capacity is now the largest source of new generation.",
	"Officials expect grid spending to rise steadily through 2030.",
}

// v6didSHA is the receipt's account fingerprint: the first 12 hex characters
// of SHA-256 of the account DID (internal/bluesky receipt binding).
func v6didSHA(did string) string {
	sum := sha256.Sum256([]byte(did))
	return hex.EncodeToString(sum[:])[:12]
}

// ---------------------------------------------------------------------------
// Fake AT Protocol (xrpc) server
// ---------------------------------------------------------------------------

// v6FakeAT is a stateful fake AT Protocol server implementing the three
// xrpc methods the bluesky adapter uses: createSession, refreshSession, and
// createRecord. It enforces the adapter's bearer authentication, records
// every post (text and reply references) in publish order, and supports
// deterministic failure injection for the partial-failure scenario.
type v6FakeAT struct {
	mu        sync.Mutex
	did       string
	handle    string
	password  string
	accessJWT string

	sessions  int
	refreshes int
	failNext  int // fail the next N createRecord calls with HTTP 500
	failAt    int // fail exactly the N-th createRecord call (1-based), once
	records   []v6ATRecord
}

// v6ATRecord is one accepted post on the fake server.
type v6ATRecord struct {
	Text   string
	Root   string // reply.root URI ("" for the root post)
	Parent string // reply.parent URI ("" for the root post)
}

// v6fakeJWT builds a structurally valid three-part JWT with an exp claim
// (the session layer only decodes exp; the signature is never verified).
func v6fakeJWT(exp int64, iss string) string {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return b64(`{"alg":"HS256","typ":"JWT"}`) + "." +
		b64(fmt.Sprintf(`{"exp":%d,"iss":"%s"}`, exp, iss)) + "." +
		b64("fake-signature")
}

func newV6FakeAT(t *testing.T) (*v6FakeAT, string) {
	t.Helper()
	f := &v6FakeAT{did: v6Did, handle: v6Handle, password: v6Password}
	f.accessJWT = v6fakeJWT(time.Now().Add(24*time.Hour).Unix(), f.did)
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *v6FakeAT) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/xrpc/com.atproto.server.createSession":
		var req struct {
			Identifier string `json:"identifier"`
			Password   string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			req.Identifier != f.handle || req.Password != f.password {
			f.xerror(w, http.StatusUnauthorized, "AuthenticationRequired", "invalid credentials")
			return
		}
		f.mu.Lock()
		f.sessions++
		f.mu.Unlock()
		f.sessionResponse(w)

	case "/xrpc/com.atproto.server.refreshSession":
		f.mu.Lock()
		f.refreshes++
		f.mu.Unlock()
		f.sessionResponse(w)

	case "/xrpc/com.atproto.repo.createRecord":
		f.mu.Lock()
		now := len(f.records) + 1
		fail := f.failNext > 0 || f.failAt == now
		if f.failNext > 0 {
			f.failNext--
		}
		if f.failAt == now {
			f.failAt = 0
		}
		f.mu.Unlock()
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+f.accessJWT {
			f.xerror(w, http.StatusUnauthorized, "AuthenticationRequired", "invalid token")
			return
		}
		if fail {
			f.xerror(w, http.StatusInternalServerError, "FakeError", "injected record failure")
			return
		}
		var req struct {
			Repo       string `json:"repo"`
			Collection string `json:"collection"`
			Record     *struct {
				Text  string `json:"text"`
				Reply *struct {
					Root   *v6Ref `json:"root"`
					Parent *v6Ref `json:"parent"`
				} `json:"reply"`
			} `json:"record"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			req.Repo != f.did || req.Collection != "app.bsky.feed.post" || req.Record == nil {
			f.xerror(w, http.StatusBadRequest, "InvalidRequest", "unexpected record")
			return
		}
		rec := v6ATRecord{Text: req.Record.Text}
		if req.Record.Reply != nil {
			if req.Record.Reply.Root != nil {
				rec.Root = req.Record.Reply.Root.URI
			}
			if req.Record.Reply.Parent != nil {
				rec.Parent = req.Record.Reply.Parent.URI
			}
		}
		f.mu.Lock()
		n := len(f.records)
		f.records = append(f.records, rec)
		rkey := fmt.Sprintf("3q8fake%03d", n+1)
		f.mu.Unlock()
		resp := map[string]string{
			"uri": "at://" + f.did + "/app.bsky.feed.post/" + rkey,
			"cid": "bafyreibfake" + rkey,
		}
		_ = json.NewEncoder(w).Encode(resp)

	default:
		f.xerror(w, http.StatusNotFound, "NotFound", "unknown xrpc method")
	}
}

func (f *v6FakeAT) sessionResponse(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"did":        f.did,
		"handle":     f.handle,
		"accessJwt":  f.accessJWT,
		"refreshJwt": f.accessJWT,
	})
}

func (f *v6FakeAT) xerror(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

type v6Ref struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

// FailNext makes the next n createRecord calls fail with HTTP 500 (the
// partial-failure scenario). Zero resets the injector.
func (f *v6FakeAT) FailNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}

// FailAt makes exactly the n-th createRecord call (1-based, counting the
// records accepted so far) fail with HTTP 500: earlier posts succeed, the
// n-th fails, later posts are not attempted (the client stops on error).
func (f *v6FakeAT) FailAt(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = n
}

func (f *v6FakeAT) SessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions
}

func (f *v6FakeAT) RefreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

func (f *v6FakeAT) RecordCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func (f *v6FakeAT) Records() []v6ATRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]v6ATRecord, len(f.records))
	copy(out, f.records)
	return out
}

// fakePostURI is the canonical URI the fake assigns to the n-th record
// (1-based, matching the server's own counter).
func (f *v6FakeAT) fakePostURI(n int) string {
	return "at://" + f.did + "/app.bsky.feed.post/" + fmt.Sprintf("3q8fake%03d", n)
}

// ---------------------------------------------------------------------------
// v0.6 test environment
// ---------------------------------------------------------------------------

// v6Env is one scenario's fully local environment: the shared v0.5 process
// environment (binary, workspace, fake LLM, article server) plus the v0.6
// publication surface (external editorial bundle, temporary git checkout,
// fake AT Protocol server, and the child environment carrying the app
// password).
type v6Env struct {
	t       *testing.T
	b       *bEnv
	bundle  string // editorial bundle root (publications/<name>/...)
	site    string // Hugo checkout (git repository)
	gitHome string // isolated HOME for the test-side git invocations
	at      *v6FakeAT
	env     []string
}

func newV6Env(t *testing.T) *v6Env {
	t.Helper()
	b := newBEnv(t)
	v := &v6Env{
		t:       t,
		b:       b,
		bundle:  t.TempDir(),
		site:    t.TempDir(),
		gitHome: t.TempDir(),
	}
	v6gitSetup(t, v.site, v.gitHome)
	at, atURL := newV6FakeAT(t)
	v.at = at
	v.env = append(b.childEnv(), v6EnvVar+"="+v6Password)
	v.writePresets(atURL)
	return v
}

// writePresets writes the three editorial presets into the bundle:
// site-only (Hugo), thread-only (Bluesky), and dual (Hugo + Bluesky).
func (v *v6Env) writePresets(atURL string) {
	v.t.Helper()
	hugoYAML := fmt.Sprintf("destination: %s\nbase_url: http://localhost:1313\ndrafts: true\n", v.site)
	blueskyYAML := fmt.Sprintf("handle: %s\napi_endpoint: %s\napi_key:\n  env: %s\n", v6Handle, atURL, v6EnvVar)
	files := map[string]string{
		filepath.Join("publications", "site-only", "publication.yaml"):   "schema_version: 1\nrenderer: article\nhugo: hugo.yaml\n",
		filepath.Join("publications", "site-only", "hugo.yaml"):          hugoYAML,
		filepath.Join("publications", "thread-only", "publication.yaml"): "schema_version: 1\nrenderer: thread\nbluesky: bluesky.yaml\n",
		filepath.Join("publications", "thread-only", "bluesky.yaml"):     blueskyYAML,
		filepath.Join("publications", "dual", "publication.yaml"):        "schema_version: 1\nrenderer: hybrid\nhugo: hugo.yaml\nbluesky: bluesky.yaml\n",
		filepath.Join("publications", "dual", "hugo.yaml"):               hugoYAML,
		filepath.Join("publications", "dual", "bluesky.yaml"):            blueskyYAML,
	}
	for rel, data := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(v.bundle, rel)), 0o755); err != nil {
			v.t.Fatalf("creating %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(v.bundle, rel), []byte(data), 0o644); err != nil {
			v.t.Fatalf("writing %s: %v", rel, err)
		}
	}
}

// run executes `newsroom run ...` (one fresh run) and returns the exit code
// and combined output. publication == "" skips the publication flags
// (the v0.5 default behavior).
func (v *v6Env) run(t *testing.T, format, publication string) (int, string) {
	t.Helper()
	args := []string{"run", "--topic", bTopic, "--workspace-root", v.b.wsRoot,
		"--llm-base-url", v.b.llmURL, "--llm-model", bFakeModel, "--format", format}
	for _, u := range v.b.articleURLs {
		args = append(args, "--url", u)
	}
	if publication != "" {
		args = append(args, "--publication", publication, "--bundle-root", v.bundle)
	}
	return v.exec(args...)
}

// publish executes `newsroom publish <runID> ...` and returns the exit code
// and combined output.
func (v *v6Env) publish(runID string, extra ...string) (int, string) {
	v.t.Helper()
	args := append([]string{"publish", runID, "--workspace-root", v.b.wsRoot}, extra...)
	return v.exec(args...)
}

// publishExplicit is publish with the bundle flags passed explicitly (the
// operator surface; the manifest also records them after a run).
func (v *v6Env) publishExplicit(runID, publication string, extra ...string) (int, string) {
	v.t.Helper()
	args := []string{"publish", runID, "--workspace-root", v.b.wsRoot,
		"--publication", publication, "--bundle-root", v.bundle}
	args = append(args, extra...)
	return v.exec(args...)
}

// exec runs the binary with a piped (immediately closed) stdin — a pipe is
// not a character device, so the child must take the non-interactive path —
// and the v0.6 child environment (hermetic + the app password variable).
func (v *v6Env) exec(args ...string) (int, string) {
	v.t.Helper()
	cmd := exec.Command(v.b.binary, args...)
	cmd.Dir = v.b.workDir
	cmd.Env = v.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		v.t.Fatalf("stdin pipe for %s: %v", args[0], err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		v.t.Fatalf("starting %s: %v", args[0], err)
	}
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		code := 0
		if waitErr != nil {
			var ee *exec.ExitError
			if errors.As(waitErr, &ee) {
				code = ee.ExitCode()
			} else {
				v.t.Fatalf("command %s: %v\n%s", args[0], waitErr, out.String())
			}
		}
		return code, out.String()
	case <-time.After(180 * time.Second):
		_ = cmd.Process.Kill()
		v.t.Fatalf("command %s timed out after 180s:\n%s", args[0], out.String())
		return 0, ""
	}
}

// ---------------------------------------------------------------------------
// Temporary git checkout helpers (test-side git; the child runs its own
// isolated git client internally)
// ---------------------------------------------------------------------------

// v6gitSetup initializes the checkout: a "main" branch with a committed
// content/ tree and repo-local commit identity.
func v6gitSetup(t *testing.T, dir, home string) {
	t.Helper()
	mustGit := func(args ...string) {
		if _, err := gitExec(t, dir, home, args...); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}
	mustGit("init", "-b", "main")
	mustGit("config", "user.name", "E2E Newsroom")
	mustGit("config", "user.email", "e2e@newsroom.invalid")
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
		t.Fatalf("creating content dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "content", ".gitkeep"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("writing .gitkeep: %v", err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "initial content tree")
}

// gitExec runs git in the checkout with an isolated environment (no system
// or global config) and returns the trimmed combined output.
func gitExec(t *testing.T, dir, home string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// git is gitExec with trimmed output (for branch names, counts, hashes).
func (v *v6Env) git(args ...string) (string, error) {
	out, err := gitExec(v.t, v.site, v.gitHome, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is gitExec without trimming (for byte-exact file content).
func (v *v6Env) gitRaw(args ...string) (string, error) {
	return gitExec(v.t, v.site, v.gitHome, args...)
}

func (v *v6Env) gitMust(args ...string) string {
	out, err := v.git(args...)
	if err != nil {
		v.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// branchExists reports whether the named branch exists in the checkout.
func (v *v6Env) branchExists(name string) bool {
	if _, err := v.git("rev-parse", "--verify", "refs/heads/"+name); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Canned LLM responses (v0.6 write artifacts)
// ---------------------------------------------------------------------------

// v6writeJSON is the writer stage response for one scenario: the canonical
// EditorialArtifact the write stage accepts, in the format the scenario
// publishes. Posts (thread/hybrid) are plain and well under the 300-grapheme
// limit; sections/posts reference only claims present in the reference maps.
func v6writeJSON(artifactType contracts.ArtifactType, posts ...string) string {
	now, _ := time.Parse(time.RFC3339, bBaseTime)
	a := contracts.EditorialArtifact{
		StableID:     "b-artifact-1",
		ArtifactType: artifactType,
		Title:        "Council Approves Budget as Renewable Capacity Grows",
		Subtitle:     "Budget and energy milestones land in the same week",
		Body:         bClaimOne + " " + bClaimTwo,
		Sections: []contracts.Section{
			{Order: 0, Title: "The budget", Body: bClaimOne, ClaimIDs: []string{"claim-1"}},
			{Order: 1, Title: "The grid", Body: bClaimTwo, ClaimIDs: []string{"claim-2"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore},
			"claim-2": {ClaimID: "claim-2", UsageType: contracts.ClaimUsageCore},
		},
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "b-verification-1", GeneratedAt: now,
		},
	}
	if len(posts) > 0 {
		a.Posts = make([]contracts.Post, len(posts))
		for i, p := range posts {
			a.Posts[i] = contracts.Post{Order: i, Body: p}
		}
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// v6checkJSON is the final factual check response (pass or fail). The
// checker recomputes the overall status from findings (it does not trust
// the LLM's own status field), so a failing check must carry a critical
// error finding for the merged result to actually fail.
func v6checkJSON(status contracts.FactualCheckStatus, summary string) string {
	now, _ := time.Parse(time.RFC3339, bBaseTime)
	c := contracts.FactualCheckResult{
		StableID: "b-check-1", InputArtifactID: "b-artifact-1", CheckedAt: now,
		ClaimIDs: []string{"claim-1", "claim-2"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
			"claim-2": contracts.VerificationStatusSupported,
		},
		OverallStatus:   status,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         summary,
	}
	if status != contracts.FactualCheckStatusPass {
		c.FactualFindings = []contracts.FactualFinding{{
			FindingID: "finding-det-1", FindingType: contracts.FindingTypeError,
			Statement: "the budget figure conflicts with the source document",
			Evidence:  "the source states a different amount", Severity: contracts.WarningSeverityCritical,
			Category: contracts.FindingCategoryClaim, RelatedClaimIDs: []string{"claim-1"},
		}}
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// ---------------------------------------------------------------------------
// Publish JSON report (the command's machine-readable contract)
// ---------------------------------------------------------------------------

type v6Report struct {
	Run struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"run"`
	Ready *struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	} `json:"ready"`
	Preset *struct {
		Name       string `json:"name"`
		Hash       string `json:"hash"`
		BundleRoot string `json:"bundle_root"`
	} `json:"preset"`
	Mode      string         `json:"mode"`
	Published bool           `json:"published"`
	Error     string         `json:"error"`
	Plans     []v6ReportPlan `json:"plans"`
}

type v6ReportPlan struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Artifact struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"artifact"`
	Approval *struct {
		Status         string `json:"status"`
		ArtifactDigest string `json:"artifact_digest"`
	} `json:"approval"`
	Targets []v6ReportTarget `json:"targets"`
	Errors  []string         `json:"errors"`
}

type v6ReportTarget struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Error       string            `json:"error"`
	Receipt     json.RawMessage   `json:"receipt"`
	PreviewOK   *bool             `json:"preview_ok"`
	PreviewMeta map[string]string `json:"preview_metadata"`
}

// v6Receipt is the persisted publication receipt (the report and the plan
// document embed the same shape).
type v6Receipt struct {
	ID               string            `json:"id"`
	Provider         string            `json:"provider"`
	ExternalURI      string            `json:"external_uri"`
	ExternalRecordID string            `json:"external_record_id"`
	ContentDigest    string            `json:"content_digest"`
	Metadata         map[string]string `json:"metadata"`
}

// v6PlanDoc is the on-disk plan document (publication/plans/<id>.json).
type v6PlanDoc struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Artifact struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
	} `json:"artifact"`
	Targets []struct {
		ID      string          `json:"id"`
		Status  string          `json:"status"`
		Receipt json.RawMessage `json:"receipt"`
		DryRun  *struct {
			Outcome       string `json:"outcome"`
			ContentDigest string `json:"content_digest"`
			Details       string `json:"details"`
		} `json:"dry_run"`
	} `json:"targets"`
}

func parseV6Report(t *testing.T, out string) *v6Report {
	t.Helper()
	// The report is the single JSON object the command writes; the decoder
	// reads exactly one value and ignores any trailing text the process
	// appended to the same stream.
	var rep v6Report
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&rep); err != nil {
		t.Fatalf("parsing publish JSON report: %v\n%s", err, out)
	}
	return &rep
}

func decodeV6Receipt(t *testing.T, raw json.RawMessage) *v6Receipt {
	t.Helper()
	var r v6Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parsing receipt %s: %v", raw, err)
	}
	return &r
}

// runDirAbs returns the absolute directory of the run in the workspace.
func (v *v6Env) runDirAbs(runID string) string {
	return filepath.Join(v.b.wsRoot, "runs", runID)
}

// loadManifest reads the persisted manifest for the scenario run.
func (v *v6Env) loadManifest(runID string) *run.Manifest {
	return bLoadManifest(v.t, v.b.wsRoot, runID)
}

// ---------------------------------------------------------------------------
// Scenario 1: Hugo-only publication (success path)
// ---------------------------------------------------------------------------

func TestE2E_v06_HugoOnlyPublish(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeArticle))

	code, out := v.run(t, "article", "site-only")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()
	m := v.loadManifest(runID)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", m.Status)
	}
	if m.Publication == nil || m.Publication.ID != "site-only" {
		t.Fatalf("run recorded publication = %+v, want site-only", m.Publication)
	}

	// 1) Dry run: zero external side effects.
	code, out = v.publishExplicit(runID, "site-only", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	if rep.Ready == nil || !rep.Ready.Ready {
		t.Fatalf("preview reports not ready (%+v)", rep.Ready)
	}
	if len(rep.Plans) != 1 {
		t.Fatalf("preview plans = %d, want 1", len(rep.Plans))
	}
	plan := rep.Plans[0]
	digest := plan.Artifact.Digest
	if digest == "" {
		t.Fatalf("preview plan has no artifact digest")
	}
	if plan.Status != "planned" {
		t.Fatalf("preview plan status = %q, want planned", plan.Status)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].Status != "planned" {
		t.Fatalf("preview target = %+v, want one planned target", plan.Targets)
	}
	branch := "publication/" + plan.Targets[0].ID // gitpub derives publication/<target>
	if v.branchExists(branch) {
		t.Fatalf("dry run created git branch %q (must have no side effects)", branch)
	}
	if v.at.RecordCount() != 0 {
		t.Fatalf("dry run made network calls (%d records)", v.at.RecordCount())
	}

	// The rehearsal is recorded on the persisted plan document (the audit
	// record of what the adapter validated before approval).
	doc := v.readPlanDoc(t, runID, plan.ID)
	if doc.Targets[0].DryRun == nil || doc.Targets[0].DryRun.Outcome != "ok" {
		t.Fatalf("preview did not record a successful dry run on the plan: %+v", doc.Targets[0].DryRun)
	}

	// 2) Approval bound to the exact digest, then the publish cycle.
	code, out = v.publishExplicit(runID, "site-only", "--approve", "--digest", digest, "--format", "json")
	if code != 0 {
		t.Fatalf("publish exit = %d, want 0:\n%s", code, out)
	}
	rep = parseV6Report(t, out)
	if len(rep.Plans) != 1 {
		t.Fatalf("publish plans = %d, want 1", len(rep.Plans))
	}
	plan = rep.Plans[0]
	if plan.Status != "succeeded" {
		t.Fatalf("plan status = %q, want succeeded (errors: %v)", plan.Status, plan.Errors)
	}
	if plan.Targets[0].Status != "succeeded" {
		t.Fatalf("target status = %q, want succeeded (error: %s)", plan.Targets[0].Status, plan.Targets[0].Error)
	}
	rec := decodeV6Receipt(t, plan.Targets[0].Receipt)
	if rec.Provider != "git" {
		t.Fatalf("receipt provider = %q, want git", rec.Provider)
	}
	if rec.ContentDigest != digest {
		t.Fatalf("receipt content digest = %q, want the approved %q", rec.ContentDigest, digest)
	}
	if rec.ExternalRecordID == "" {
		t.Fatalf("git receipt has no external record id (commit SHA)")
	}
	if rec.Metadata["branch"] != branch {
		t.Fatalf("receipt branch = %q, want %q", rec.Metadata["branch"], branch)
	}
	if rep.Run.Status != string(run.RunStatusSucceeded) {
		t.Fatalf("run status in report = %q", rep.Run.Status)
	}

	// 3) The checkout: one commit on the publication branch, and the
	// committed bytes are exactly the approved (staged) bytes.
	if got := v.gitMust("rev-parse", "--abbrev-ref", "HEAD"); got != branch {
		t.Fatalf("checkout is on branch %q, want %q", got, branch)
	}
	if got := v.gitMust("rev-list", "--count", "main..HEAD"); got != "1" {
		t.Fatalf("commits ahead of main = %q, want 1", got)
	}
	if got := v.gitMust("rev-parse", "HEAD"); got != rec.ExternalRecordID {
		t.Fatalf("repo HEAD = %q, want the receipt's record id %q", got, rec.ExternalRecordID)
	}
	rel := filepath.ToSlash(plan.Artifact.Path) // publication/content/hugo/content/<slug>.md
	afterHugo := strings.TrimPrefix(rel, "publication/content/hugo/")
	staged, err := os.ReadFile(filepath.Join(v.runDirAbs(runID), plan.Artifact.Path))
	if err != nil {
		t.Fatalf("reading staged content: %v", err)
	}
	committed, err := v.gitRaw("show", "HEAD:"+filepath.ToSlash(filepath.Join("content", strings.TrimPrefix(afterHugo, "content/"))))
	if err != nil {
		t.Fatalf("reading committed file: %v", err)
	}
	if !bytes.Equal([]byte(committed), staged) {
		t.Fatalf("committed bytes differ from the approved staged bytes:\ncommitted: %q\nstaged:   %q", committed, staged)
	}

	// 4) Persistence: the plan document on disk and the manifest summary.
	doc = v.readPlanDoc(t, runID, plan.ID)
	if doc.Status != "succeeded" || doc.Targets[0].Status != "succeeded" {
		t.Fatalf("plan document state = %s/%s, want succeeded/succeeded", doc.Status, doc.Targets[0].Status)
	}
	m = v.loadManifest(runID)
	if m.PublicationState == nil || m.PublicationState.Status != "succeeded" || m.PublicationState.PlanID != string(plan.ID) {
		t.Fatalf("manifest publication state = %+v, want succeeded plan %s", m.PublicationState, plan.ID)
	}
}

// readPlanDoc reads the persisted plan document for a run.
func (v *v6Env) readPlanDoc(t *testing.T, runID, planID string) *v6PlanDoc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(v.runDirAbs(runID), "publication", "plans", planID+".json"))
	if err != nil {
		t.Fatalf("reading plan document %s: %v", planID, err)
	}
	var doc v6PlanDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing plan document: %v\n%s", err, data)
	}
	return &doc
}

// ---------------------------------------------------------------------------
// Scenario 2: Bluesky single-post publication
// ---------------------------------------------------------------------------

func TestE2E_v06_BlueskySinglePost(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeThread, v6Posts[0]))

	code, out := v.run(t, "thread", "thread-only")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "thread-only", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	if len(rep.Plans) != 1 || rep.Plans[0].Status != "planned" {
		t.Fatalf("preview plan = %+v, want one planned", rep.Plans)
	}
	if v.at.SessionCount() != 0 || v.at.RecordCount() != 0 {
		t.Fatalf("dry run used the network (sessions=%d records=%d)", v.at.SessionCount(), v.at.RecordCount())
	}
	digest := rep.Plans[0].Artifact.Digest

	code, out = v.publishExplicit(runID, "thread-only", "--approve", "--digest", digest, "--format", "json")
	if code != 0 {
		t.Fatalf("publish exit = %d, want 0:\n%s", code, out)
	}
	rep = parseV6Report(t, out)
	plan := rep.Plans[0]
	if plan.Status != "succeeded" || plan.Targets[0].Status != "succeeded" {
		t.Fatalf("plan/target = %s/%s, want succeeded/succeeded (error: %s)", plan.Status, plan.Targets[0].Status, plan.Targets[0].Error)
	}
	rec := decodeV6Receipt(t, plan.Targets[0].Receipt)
	if rec.Provider != "bluesky" {
		t.Fatalf("receipt provider = %q, want bluesky", rec.Provider)
	}
	if rec.ContentDigest != digest {
		t.Fatalf("receipt content digest = %q, want %q", rec.ContentDigest, digest)
	}
	if rec.ExternalRecordID != v.at.fakePostURI(1) {
		t.Fatalf("receipt record id = %q, want %q", rec.ExternalRecordID, v.at.fakePostURI(1))
	}
	if got := rec.Metadata["did_sha"]; got != v6didSHA(v6Did) {
		t.Fatalf("receipt did_sha = %q, want %q", got, v6didSHA(v6Did))
	}

	// The fake PDS saw exactly one post, with the exact approved text and no
	// reply references (a single post is a root).
	if got := v.at.RecordCount(); got != 1 {
		t.Fatalf("createRecord calls = %d, want 1", got)
	}
	if got := v.at.SessionCount(); got != 1 {
		t.Fatalf("createSession calls = %d, want 1", got)
	}
	recs := v.at.Records()
	if recs[0].Text != v6Posts[0] {
		t.Fatalf("post text = %q, want %q", recs[0].Text, v6Posts[0])
	}
	if recs[0].Root != "" || recs[0].Parent != "" {
		t.Fatalf("single post carries reply refs (root=%q parent=%q), want none", recs[0].Root, recs[0].Parent)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3: Multi-post thread with reply references
// ---------------------------------------------------------------------------

func TestE2E_v06_BlueskyThread(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeThread, v6Posts...))

	code, out := v.run(t, "thread", "thread-only")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "thread-only", "--approve", "--format", "json")
	if code != 5 {
		t.Fatalf("publish without digest exit = %d, want 5 (approval required, non-interactive):\n%s", code, out)
	}
	code, out = v.publishExplicit(runID, "thread-only", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	digest := parseV6Report(t, out).Plans[0].Artifact.Digest

	code, out = v.publishExplicit(runID, "thread-only", "--approve", "--digest", digest, "--format", "json")
	if code != 0 {
		t.Fatalf("publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	plan := rep.Plans[0]
	if plan.Status != "succeeded" {
		t.Fatalf("plan status = %q, want succeeded (error: %s)", plan.Status, plan.Targets[0].Error)
	}

	// The fake PDS saw the full thread in order: a root post, then replies
	// whose root and parent reference the published records.
	if got := v.at.RecordCount(); got != len(v6Posts) {
		t.Fatalf("createRecord calls = %d, want %d", got, len(v6Posts))
	}
	recs := v.at.Records()
	for i, want := range v6Posts {
		if recs[i].Text != want {
			t.Fatalf("post %d text = %q, want %q", i+1, recs[i].Text, want)
		}
	}
	if recs[0].Root != "" || recs[0].Parent != "" {
		t.Fatalf("root post carries reply refs: root=%q parent=%q", recs[0].Root, recs[0].Parent)
	}
	if recs[1].Root != v.at.fakePostURI(1) || recs[1].Parent != v.at.fakePostURI(1) {
		t.Fatalf("post 2 reply = %q/%q, want root+parent = %q", recs[1].Root, recs[1].Parent, v.at.fakePostURI(1))
	}
	if recs[2].Root != v.at.fakePostURI(1) || recs[2].Parent != v.at.fakePostURI(2) {
		t.Fatalf("post 3 reply = %q/%q, want root %q parent %q", recs[2].Root, recs[2].Parent, v.at.fakePostURI(1), v.at.fakePostURI(2))
	}

	// The receipt proves every post of the thread (resumable state).
	rec := decodeV6Receipt(t, plan.Targets[0].Receipt)
	if len(rec.Metadata) != len(v6Posts)+1 {
		t.Fatalf("receipt metadata = %v, want %d post refs + did_sha", rec.Metadata, len(v6Posts)+1)
	}
	for i := range v6Posts {
		if got, ok := rec.Metadata[fmt.Sprintf("post_%d", i+1)]; !ok || got != v.at.fakePostURI(i+1)+"|bafyreibfake"+fmt.Sprintf("3q8fake%03d", i+1) {
			t.Fatalf("receipt post_%d = %q (present=%v), want the fake uri|cid", i+1, got, ok)
		}
	}
	if rec.Metadata["did_sha"] != v6didSHA(v6Did) {
		t.Fatalf("receipt did_sha = %q, want %q", rec.Metadata["did_sha"], v6didSHA(v6Did))
	}
}

// ---------------------------------------------------------------------------
// Scenario 4: Multi-target publication (Hugo + Bluesky, one run)
// ---------------------------------------------------------------------------

func TestE2E_v06_MultiTarget(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeHybrid, v6Posts...))

	code, out := v.run(t, "hybrid", "dual")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "dual", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	if len(rep.Plans) != 2 {
		t.Fatalf("preview plans = %d, want 2 (one per destination)", len(rep.Plans))
	}
	// Canonical order: Hugo first, then Bluesky.
	if !strings.Contains(rep.Plans[0].Artifact.Path, "hugo") || !strings.Contains(rep.Plans[1].Artifact.Path, "bluesky") {
		t.Fatalf("plan order = %q, %q; want hugo then bluesky", rep.Plans[0].Artifact.Path, rep.Plans[1].Artifact.Path)
	}
	d0, d1 := rep.Plans[0].Artifact.Digest, rep.Plans[1].Artifact.Digest
	if d0 == d1 {
		t.Fatalf("both plans baseline to the same digest %q; they must differ", d0)
	}
	branch := "publication/" + rep.Plans[0].Targets[0].ID

	code, out = v.publishExplicit(runID, "dual", "--approve", "--digest", d0, "--digest", d1, "--format", "json")
	if code != 0 {
		t.Fatalf("publish exit = %d, want 0:\n%s", code, out)
	}
	rep = parseV6Report(t, out)
	if len(rep.Plans) != 2 {
		t.Fatalf("publish plans = %d, want 2", len(rep.Plans))
	}
	if rep.Plans[0].Status != "succeeded" || rep.Plans[1].Status != "succeeded" {
		t.Fatalf("plan statuses = %s/%s, want succeeded/succeeded (hugo error: %s, bluesky error: %s)",
			rep.Plans[0].Status, rep.Plans[1].Status, rep.Plans[0].Targets[0].Error, rep.Plans[1].Targets[0].Error)
	}
	if got := v.branchExists(branch); !got {
		t.Fatalf("git branch %q missing after multi-target publish", branch)
	}
	if got := v.at.RecordCount(); got != len(v6Posts) {
		t.Fatalf("createRecord calls = %d, want %d", got, len(v6Posts))
	}
	if got := v.at.SessionCount(); got != 1 {
		t.Fatalf("createSession calls = %d, want 1", got)
	}
	// Both receipts are bound to their own plan digests.
	hrec := decodeV6Receipt(t, rep.Plans[0].Targets[0].Receipt)
	brec := decodeV6Receipt(t, rep.Plans[1].Targets[0].Receipt)
	if hrec.ContentDigest != d0 || brec.ContentDigest != d1 {
		t.Fatalf("receipt digests = %q/%q, want %q/%q", hrec.ContentDigest, brec.ContentDigest, d0, d1)
	}
}

// ---------------------------------------------------------------------------
// Scenario 5: Publish blocked when the run is not READY
// ---------------------------------------------------------------------------

func TestE2E_v06_NotReadyBlocksPublish(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeArticle))
	v.b.llm.SetResponse(bMarkerCheck, v6checkJSON(contracts.FactualCheckStatusFail, "the budget figure conflicts with the source"))

	code, out := v.run(t, "article", "site-only")
	if code == 0 {
		t.Fatalf("run with a failing final check exited 0:\n%s", out)
	}
	runID := v.b.waitRunDir()
	m := v.loadManifest(runID)
	if m.Status != run.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", m.Status)
	}

	code, out = v.publishExplicit(runID, "site-only", "--format", "json")
	if code != 3 {
		t.Fatalf("publish on a failed run exit = %d, want 3 (not READY):\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	if rep.Ready == nil || rep.Ready.Ready {
		t.Fatalf("report ready = %+v, want explicitly not ready", rep.Ready)
	}
	if len(rep.Plans) != 0 {
		t.Fatalf("report has %d plans; a not-ready run must plan nothing", len(rep.Plans))
	}

	// No publication side effects of any kind.
	if _, err := os.Stat(filepath.Join(v.runDirAbs(runID), "publication")); !os.IsNotExist(err) {
		t.Fatalf("a publication directory exists for a not-ready run")
	}
	if v.at.RecordCount() != 0 || v.at.SessionCount() != 0 {
		t.Fatalf("not-ready publish used the network (sessions=%d records=%d)", v.at.SessionCount(), v.at.RecordCount())
	}
}

// ---------------------------------------------------------------------------
// Scenario 6: Publish blocked without approval
// ---------------------------------------------------------------------------

func TestE2E_v06_ApprovalRequired(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeArticle))

	code, out := v.run(t, "article", "site-only")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "site-only", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	digest := rep.Plans[0].Artifact.Digest
	branch := "publication/" + rep.Plans[0].Targets[0].ID

	// --approve without the exact digest (non-interactive) is refused before
	// any external effect.
	code, out = v.publishExplicit(runID, "site-only", "--approve", "--format", "json")
	if code != 5 {
		t.Fatalf("approve without digest exit = %d, want 5:\n%s", code, out)
	}
	if v.branchExists(branch) {
		t.Fatalf("a refused approval still created branch %q", branch)
	}
	if v.at.RecordCount() != 0 {
		t.Fatalf("a refused approval still published %d records", v.at.RecordCount())
	}

	// The right digest unblocks.
	code, out = v.publishExplicit(runID, "site-only", "--approve", "--digest", digest, "--format", "json")
	if code != 0 {
		t.Fatalf("publish with digest exit = %d, want 0:\n%s", code, out)
	}
	if got := parseV6Report(t, out).Plans[0].Status; got != "succeeded" {
		t.Fatalf("plan status = %q, want succeeded", got)
	}
}

// ---------------------------------------------------------------------------
// Scenario 7: Stale approval (the content changed after the approval)
// ---------------------------------------------------------------------------

func TestE2E_v06_StaleApproval(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeHybrid, v6Posts...))

	code, out := v.run(t, "hybrid", "dual")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "dual", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	d0, d1 := rep.Plans[0].Artifact.Digest, rep.Plans[1].Artifact.Digest

	// Approve both plans (bound to the current digests) and publish; the
	// injected failure on the LAST post leaves the bluesky plan
	// NON-terminal (partially succeeded), so a later retry re-verifies the
	// content.
	v.at.FailAt(len(v6Posts))
	code, out = v.publishExplicit(runID, "dual", "--approve", "--digest", d0, "--digest", d1, "--format", "json")
	if code != 6 {
		t.Fatalf("publish with injected failure exit = %d, want 6:\n%s", code, out)
	}

	// Now tamper with the baselined content after the fact: the bytes on
	// disk no longer match what the plan (and its approval) bound to.
	v.at.FailNext(0)
	threadFile := filepath.Join(v.runDirAbs(runID), "publication", "content", "bluesky", "thread.txt")
	orig, err := os.ReadFile(threadFile)
	if err != nil {
		t.Fatalf("reading baselined thread content: %v", err)
	}
	if err := os.WriteFile(threadFile, append(append([]byte{}, orig...), '.'), 0o644); err != nil {
		t.Fatalf("tampering thread content: %v", err)
	}
	defer func() {
		if err := os.WriteFile(threadFile, orig, 0o644); err != nil {
			t.Errorf("restoring thread content: %v", err)
		}
	}() // restore for cleanup

	commitBefore := v.gitMust("rev-parse", "HEAD")
	recordsBefore := v.at.RecordCount()

	code, out = v.publish(runID, "--resume", "--format", "json")
	if code != 5 {
		t.Fatalf("resume with tampered content exit = %d, want 5 (stale approval / changed artifact):\n%s", code, out)
	}
	if !strings.Contains(out, "stale") && !strings.Contains(out, "re-baseline") && !strings.Contains(out, "changed") {
		t.Fatalf("stale-content error does not explain the staleness:\n%s", out)
	}
	if got := v.gitMust("rev-parse", "HEAD"); got != commitBefore {
		t.Fatalf("stale resume moved the checkout HEAD")
	}
	if got := v.at.RecordCount(); got != recordsBefore {
		t.Fatalf("stale resume published %d new records", got-recordsBefore)
	}
}

// ---------------------------------------------------------------------------
// Scenario 8: Partial failure, resume, no duplicates
// ---------------------------------------------------------------------------

func TestE2E_v06_PartialFailureResume(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeHybrid, v6Posts...))

	code, out := v.run(t, "hybrid", "dual")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "dual", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	d0, d1 := rep.Plans[0].Artifact.Digest, rep.Plans[1].Artifact.Digest

	// Inject a failure on the LAST post: the thread is partially published.
	v.at.FailAt(len(v6Posts))
	code, out = v.publishExplicit(runID, "dual", "--approve", "--digest", d0, "--digest", d1, "--format", "json")
	if code != 6 {
		t.Fatalf("publish with injected failure exit = %d, want 6 (failed destination):\n%s", code, out)
	}
	rep = parseV6Report(t, out)
	if rep.Plans[0].Status != "succeeded" {
		t.Fatalf("hugo plan = %s, want succeeded (error: %s)", rep.Plans[0].Status, rep.Plans[0].Targets[0].Error)
	}
	if rep.Plans[1].Status != "failed" {
		t.Fatalf("bluesky plan = %s, want failed (a partially_succeeded target counts as not-succeeded in the plan aggregate) (error: %s)", rep.Plans[1].Status, rep.Plans[1].Targets[0].Error)
	}
	if got := rep.Plans[1].Targets[0].Status; got != "partially_succeeded" {
		t.Fatalf("bluesky target = %s, want partially_succeeded (error: %s)", got, rep.Plans[1].Targets[0].Error)
	}
	if got := v.at.RecordCount(); got != len(v6Posts)-1 {
		t.Fatalf("createRecord calls = %d, want %d (last post failed)", got, len(v6Posts)-1)
	}
	// The partial receipt proved the posts that really went out.
	rec := decodeV6Receipt(t, rep.Plans[1].Targets[0].Receipt)
	if got := rec.Metadata["did_sha"]; got != v6didSHA(v6Did) {
		t.Fatalf("partial receipt did_sha = %q, want %q", got, v6didSHA(v6Did))
	}
	if got := len(rec.Metadata) - 1; got != len(v6Posts)-1 {
		t.Fatalf("partial receipt proves %d posts, want %d", got, len(v6Posts)-1)
	}
	commitAfterFirst := v.gitMust("rev-parse", "HEAD")

	// Resume: the failed thread publishes ONLY the remaining post. The resume
	// report re-reports the resumed (latest) plan — the bluesky plan — as
	// plans[0]; the hugo plan is terminal and not re-listed.
	v.at.FailNext(0)
	code, out = v.publish(runID, "--resume", "--format", "json")
	if code != 0 {
		t.Fatalf("resume exit = %d, want 0:\n%s", code, out)
	}
	rep = parseV6Report(t, out)
	if len(rep.Plans) != 1 {
		t.Fatalf("resume report lists %d plans, want 1 (the resumed plan): %s", len(rep.Plans), out)
	}
	if rep.Plans[0].Status != "succeeded" {
		t.Fatalf("post-resume bluesky plan = %s, want succeeded (error: %s)", rep.Plans[0].Status, rep.Plans[0].Targets[0].Error)
	}
	if got := v.at.RecordCount(); got != len(v6Posts) {
		t.Fatalf("createRecord calls after resume = %d, want %d (the %d published posts must not be re-posted)", got, len(v6Posts), len(v6Posts)-1)
	}
	// No new git work and no session churn beyond the resumed process.
	if got := v.gitMust("rev-parse", "HEAD"); got != commitAfterFirst {
		t.Fatalf("resume created a new git commit")
	}
	if got := v.at.SessionCount(); got != 2 {
		t.Fatalf("createSession calls = %d, want 2 (one per publish process)", got)
	}
}

// ---------------------------------------------------------------------------
// Scenario 9: Git dirty tree fails safely, then resumes clean
// ---------------------------------------------------------------------------

func TestE2E_v06_GitDirtyTree(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeArticle))

	code, out := v.run(t, "article", "site-only")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "site-only", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	digest := rep.Plans[0].Artifact.Digest
	branch := "publication/" + rep.Plans[0].Targets[0].ID
	commitBefore := v.gitMust("rev-parse", "HEAD")

	// Dirty the checkout with an untracked file inside the target dir.
	wip := filepath.Join(v.site, "content", "wip.md")
	if err := os.WriteFile(wip, []byte("draft\n"), 0o644); err != nil {
		t.Fatalf("writing wip file: %v", err)
	}
	defer func() {
		if err := os.Remove(wip); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("removing wip file: %v", err)
		}
	}()

	code, out = v.publishExplicit(runID, "site-only", "--approve", "--digest", digest, "--format", "json")
	if code != 6 {
		t.Fatalf("publish on a dirty tree exit = %d, want 6:\n%s", code, out)
	}
	if !strings.Contains(strings.ToLower(out), "dirty") {
		t.Fatalf("dirty-tree failure does not mention the dirty tree:\n%s", out)
	}
	// The failure happens before any branch/commit side effect.
	if v.branchExists(branch) {
		t.Fatalf("a dirty checkout still produced branch %q", branch)
	}
	if got := v.gitMust("rev-parse", "HEAD"); got != commitBefore {
		t.Fatalf("a dirty checkout still moved HEAD")
	}
	if got := v.at.RecordCount(); got != 0 {
		t.Fatalf("dirty publish used the network (%d records)", got)
	}

	// Clean the tree, then resume: exactly one commit, content intact.
	if err := os.Remove(wip); err != nil {
		t.Fatalf("removing wip file: %v", err)
	}
	code, out = v.publish(runID, "--resume", "--format", "json")
	if code != 0 {
		t.Fatalf("resume on a clean tree exit = %d, want 0:\n%s", code, out)
	}
	if got := v.gitMust("rev-list", "--count", "main..HEAD"); got != "1" {
		t.Fatalf("commits ahead of main = %q, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Scenario 10: Repeated publish creates no duplicates
// ---------------------------------------------------------------------------

func TestE2E_v06_RepeatPublishNoDups(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeHybrid, v6Posts...))

	code, out := v.run(t, "hybrid", "dual")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "dual", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	d0, d1 := rep.Plans[0].Artifact.Digest, rep.Plans[1].Artifact.Digest

	code, out = v.publishExplicit(runID, "dual", "--approve", "--digest", d0, "--digest", d1, "--format", "json")
	if code != 0 {
		t.Fatalf("publish exit = %d, want 0:\n%s", code, out)
	}

	commit := v.gitMust("rev-parse", "HEAD")
	records := v.at.RecordCount()
	sessions := v.at.SessionCount()

	// A repeated publish (no approval flag) is an idempotent no-op.
	code, out = v.publish(runID, "--format", "json")
	if code != 0 {
		t.Fatalf("repeat publish exit = %d, want 0:\n%s", code, out)
	}
	rep = parseV6Report(t, out)
	if rep.Plans[0].Status != "succeeded" || rep.Plans[1].Status != "succeeded" {
		t.Fatalf("repeat publish reports statuses %s/%s, want succeeded/succeeded", rep.Plans[0].Status, rep.Plans[1].Status)
	}
	// A repeated resume is a no-op as well.
	code, out = v.publish(runID, "--resume")
	if code != 0 {
		t.Fatalf("repeat resume exit = %d, want 0:\n%s", code, out)
	}
	// Nothing external changed on either repeat.
	if got := v.gitMust("rev-parse", "HEAD"); got != commit {
		t.Fatalf("repeat publish created a new git commit")
	}
	if got := v.at.RecordCount(); got != records {
		t.Fatalf("repeat publish posted %d new records (duplicates!)", got-records)
	}
	if got := v.at.SessionCount(); got != sessions {
		t.Fatalf("repeat publish opened %d new sessions", got-sessions)
	}
}

// ---------------------------------------------------------------------------
// Scenario 11: Credentials never persisted
// ---------------------------------------------------------------------------

func TestE2E_v06_SecretSweep(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeHybrid, v6Posts...))

	code, out := v.run(t, "hybrid", "dual")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()

	code, out = v.publishExplicit(runID, "dual", "--format", "json")
	if code != 0 {
		t.Fatalf("preview publish exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	d0, d1 := rep.Plans[0].Artifact.Digest, rep.Plans[1].Artifact.Digest

	code, out = v.publishExplicit(runID, "dual", "--approve", "--digest", d0, "--digest", d1, "--format", "json")
	if code != 0 {
		t.Fatalf("publish exit = %d, want 0:\n%s", code, out)
	}
	if strings.Contains(out, v6Password) {
		t.Fatalf("the publish output contains the app password")
	}

	// Every persisted file of the run and of the bundle is free of the
	// credential (the env var name is fine; its value is not).
	sweep := func(root string) {
		if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			if bytes.Contains(data, []byte(v6Password)) {
				t.Errorf("persisted file %s contains the app password", p)
			}
			return nil
		}); err != nil {
			t.Fatalf("sweeping %s: %v", root, err)
		}
	}
	sweep(v.runDirAbs(runID))
	sweep(v.bundle)
}

// ---------------------------------------------------------------------------
// Scenario 12: External bundle resolution
// ---------------------------------------------------------------------------

func TestE2E_v06_ExternalBundle(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeArticle))

	code, out := v.run(t, "article", "site-only")
	if code != 0 {
		t.Fatalf("run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()
	m := v.loadManifest(runID)
	if m.Publication == nil || m.Publication.ID != "site-only" {
		t.Fatalf("run did not record the preset: %+v", m.Publication)
	}
	if m.Publication.BundlePath != v.bundle {
		t.Fatalf("recorded bundle path = %q, want %q", m.Publication.BundlePath, v.bundle)
	}
	if len(m.Publication.Hash) < 8 {
		t.Fatalf("recorded preset hash = %q (too short)", m.Publication.Hash)
	}

	// A publish WITHOUT the bundle flags resolves the preset from the
	// manifest's recorded bundle.
	code, out = v.publish(runID, "--format", "json")
	if code != 0 {
		t.Fatalf("publish (manifest-resolved preset) exit = %d, want 0:\n%s", code, out)
	}
	rep := parseV6Report(t, out)
	if rep.Preset == nil || rep.Preset.Name != "site-only" || rep.Preset.BundleRoot != v.bundle {
		t.Fatalf("report preset = %+v, want site-only from %s", rep.Preset, v.bundle)
	}

	// An unknown preset fails cleanly at planning time (no run created).
	code, out = v.run(t, "article", "does-not-exist")
	if code == 0 {
		t.Fatalf("run with an unknown preset exited 0:\n%s", out)
	}
	if !strings.Contains(out, "publication") {
		t.Fatalf("unknown-preset error does not name the publication preset:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Scenario 13: v0.5 run workflow unaffected
// ---------------------------------------------------------------------------

func TestE2E_v06_V05RunUnaffected(t *testing.T) {
	v := newV6Env(t)
	v.b.llm.SetResponse(bMarkerWrite, v6writeJSON(contracts.ArtifactTypeArticle))

	// No publication flags at all: the plain v0.5 flow.
	code, out := v.run(t, "article", "")
	if code != 0 {
		t.Fatalf("v0.5-style run exit = %d, want 0:\n%s", code, out)
	}
	runID := v.b.waitRunDir()
	m := v.loadManifest(runID)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", m.Status)
	}
	if m.Publication != nil {
		t.Fatalf("run without a preset recorded a publication: %+v", m.Publication)
	}

	code, out = v.publish(runID)
	if code != 4 {
		t.Fatalf("publish on a preset-less run exit = %d, want 4:\n%s", code, out)
	}
	if !strings.Contains(out, "nothing to publish") {
		t.Fatalf("exit 4 output does not explain there is nothing to publish:\n%s", out)
	}
}
