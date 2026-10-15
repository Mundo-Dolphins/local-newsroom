package main

// Unit tests for the run planning layer (run_plan.go), focused on the
// resume semantics: a resumed run is pinned to the run's recorded config
// snapshot, so a resumed writer is configured for the format the run
// recorded — not the format the current configuration provides (issue #125).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// planUnitSnapshot builds a config snapshot from the given pairs (empty
// values remove the key, mirroring ConfigSnapshot.Set).
func planUnitSnapshot(t *testing.T, pairs ...[2]string) *run.ConfigSnapshot {
	t.Helper()
	snap := &run.ConfigSnapshot{}
	for _, p := range pairs {
		if err := snap.Set(p[0], p[1]); err != nil {
			t.Fatalf("setting snapshot %q: %v", p[0], err)
		}
	}
	return snap
}

// TestFormatForResume pins the resume format resolution: the snapshot value
// wins when present; the current configuration is the fallback.
func TestFormatForResume(t *testing.T) {
	mkManifest := func(snap *run.ConfigSnapshot) *run.Manifest {
		m, err := run.NewManifest(run.NewManifestParams{
			Workspace:  run.Workspace{Name: "default"},
			Topic:      "format resolution unit test",
			SourceMode: run.SourceModeExplicit,
			Config:     snap,
			Stages:     run.CanonicalStages(),
		})
		if err != nil {
			t.Fatalf("creating manifest: %v", err)
		}
		return m
	}

	cases := []struct {
		name     string
		snapshot *run.ConfigSnapshot
		current  string
		want     string
	}{
		{
			name:     "snapshot thread beats current article",
			snapshot: planUnitSnapshot(t, [2]string{orchestrator.SettingRunFormat, "thread"}),
			current:  "article",
			want:     "thread",
		},
		{
			name:     "snapshot article beats current thread",
			snapshot: planUnitSnapshot(t, [2]string{orchestrator.SettingRunFormat, "article"}),
			current:  "thread",
			want:     "article",
		},
		{
			name:     "snapshot hybrid beats current article",
			snapshot: planUnitSnapshot(t, [2]string{orchestrator.SettingRunFormat, "hybrid"}),
			current:  "article",
			want:     "hybrid",
		},
		{
			name:     "no snapshot value falls back to the current configuration",
			snapshot: planUnitSnapshot(t, [2]string{orchestrator.SettingRunFollowUp, "false"}),
			current:  "thread",
			want:     "thread",
		},
		{
			name:     "empty snapshot falls back to the current configuration",
			snapshot: &run.ConfigSnapshot{},
			current:  "article",
			want:     "article",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mkManifest(tc.snapshot)
			if got := formatForResume(m, tc.current); got != tc.want {
				t.Errorf("formatForResume = %q, want %q", got, tc.want)
			}
		})
	}
}

// planUnitThreadArtifactJSON is a valid thread artifact referencing the
// single claim of planUnitVerification.
func planUnitThreadArtifactJSON() string {
	now, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
	artifact := contracts.EditorialArtifact{
		StableID:     "artifact-unit-001",
		ArtifactType: contracts.ArtifactTypeThread,
		Title:        "Unit test story",
		Posts: []contracts.Post{
			{Order: 0, Body: "The council approved the budget on January 10, 2024. (1/1)", ClaimIDs: []string{"claim-1"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"fixture-source-1": {SourceID: "fixture-source-1", UsageCount: 1, CitationText: "Fixture source (unit test)"},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verification-unit-001",
			GeneratedAt:         now,
		},
	}
	b, _ := json.Marshal(artifact)
	return string(b)
}

// planUnitArticleArtifactJSON is the article-shaped twin of the thread
// fixture.
func planUnitArticleArtifactJSON() string {
	now, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
	artifact := contracts.EditorialArtifact{
		StableID:     "artifact-unit-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Unit test story",
		Body:         "The council approved the budget on January 10, 2024.",
		Sections: []contracts.Section{
			{Order: 0, Title: "The budget", Body: "The council approved the budget on January 10, 2024.", ClaimIDs: []string{"claim-1"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-1": {ClaimID: "claim-1", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"fixture-source-1": {SourceID: "fixture-source-1", UsageCount: 1, CitationText: "Fixture source (unit test)"},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verification-unit-001",
			GeneratedAt:         now,
		},
	}
	b, _ := json.Marshal(artifact)
	return string(b)
}

// planUnitVerification is a minimal valid verification result for the writer
// fixtures.
func planUnitVerification() *contracts.VerificationResult {
	now, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
	return &contracts.VerificationResult{
		StableID:       "verification-unit-001",
		InputDossierID: "dossier-unit-001",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {
				ClaimID:            "claim-1",
				Statement:          "The council approved the budget on January 10, 2024.",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
				VerificationNotes:  "Directly supported by the fixture source.",
			},
		},
		Contradictions:      []contracts.VerifiedContradiction{},
		VerificationSummary: "The single claim is directly supported.",
		QualityScore:        90.0,
	}
}

// newPlanUnitLLMServer serves an OpenAI-compatible endpoint that records the
// user prompt of every request and honors the artifact shape the writer
// requested in it (a real LLM obeys the "artifact_type:" instruction, so the
// fake does too). The returned function yields the recorded prompts.
func newPlanUnitLLMServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var userPrompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":{"message":"unreadable body","type":"fake"}}`, http.StatusBadRequest)
			return
		}
		var req struct {
			Messages []v05ChatMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":{"message":"invalid request body","type":"fake"}}`, http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				userPrompts = append(userPrompts, msg.Content)
			}
		}
		mu.Unlock()

		content := planUnitThreadArtifactJSON()
		if writerRequestedType(req.Messages) == orchestrator.FormatArticle {
			content = planUnitArticleArtifactJSON()
		}
		out, _ := json.Marshal(map[string]interface{}{
			"id":      "chatcmpl-unit",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "unit-model",
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"message":       map[string]string{"role": "assistant", "content": content},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(userPrompts))
		copy(out, userPrompts)
		return out
	}
}

// TestPlanRunResumeWriterUsesRecordedFormat asserts the service wiring of
// planRunResume: when the run's snapshot records a different format than the
// current configuration, the writer the plan builds is configured for the
// RECORDED format (its prompt asks for it and the artifact it produces has
// that shape); when the snapshot records no format, the writer falls back to
// the current configuration.
func TestPlanRunResumeWriterUsesRecordedFormat(t *testing.T) {
	for name, wantFormat := range map[string]string{
		"recorded thread beats current article":            "thread",
		"no recorded format falls back to current article": "article",
	} {
		t.Run(name, func(t *testing.T) {
			var pairs [][2]string
			if wantFormat == "thread" {
				pairs = [][2]string{{orchestrator.SettingRunFormat, "thread"}}
			}

			srv, prompts := newPlanUnitLLMServer(t)

			root := t.TempDir()
			store, err := workspace.NewStore(root)
			if err != nil {
				t.Fatalf("creating workspace store: %v", err)
			}
			m, err := run.NewManifest(run.NewManifestParams{
				Workspace:  run.Workspace{Name: "default", Path: root},
				Topic:      "plan resume writer unit test",
				SourceMode: run.SourceModeExplicit,
				Model:      run.NewModelMetadata("omlx", "unit-model", srv.URL),
				Config:     planUnitSnapshot(t, pairs...),
				Stages:     run.CanonicalStages(),
			})
			if err != nil {
				t.Fatalf("creating manifest: %v", err)
			}
			if _, err := store.CreateRun(m); err != nil {
				t.Fatalf("creating run: %v", err)
			}
			// The write stage reads the verification artifact from the run
			// directory; persist the fixture where runWrite loads it from.
			data, err := json.Marshal(planUnitVerification())
			if err != nil {
				t.Fatalf("marshaling verification fixture: %v", err)
			}
			ref := run.ArtifactRef{
				Kind:       run.ArtifactKindVerification,
				ID:         "unit-verification",
				Stage:      run.StageVerify,
				Path:       run.ArtifactPathVerification,
				ProducedAt: run.NewTimestamp(time.Now().UTC()),
			}
			if _, err := store.SaveArtifact(m.ID, ref, data); err != nil {
				t.Fatalf("saving verification artifact: %v", err)
			}

			// The current configuration says article — the snapshot (when
			// present) must win for the writer.
			r := &runResolved{
				llmBaseURL:  srv.URL,
				llmModel:    "unit-model",
				format:      "article",
				temperature: 0.2,
				maxTokens:   1000,
			}
			plan, err := planRunResume(context.Background(), r, runIntent{
				ResumeID:  m.ID,
				Workspace: run.Workspace{Path: root},
			}, store)
			if err != nil {
				t.Fatalf("planRunResume: %v", err)
			}

			// Drive the write stage the plan built (via the public stage
			// plan, with the stored verification as input).
			var writeFn runner.StageFunc
			for _, s := range plan.Orch.BuildStages(m) {
				if s.Name == run.StageWrite {
					writeFn = s.Fn
				}
			}
			if writeFn == nil {
				t.Fatal("no write stage in the resumed plan")
			}
			out, err := writeFn(context.Background(), &runner.StageInput{
				RunID:    m.ID,
				Stage:    run.StageWrite,
				Manifest: m,
				Store:    store,
			})
			if err != nil {
				t.Fatalf("executing write stage: %v", err)
			}
			if len(out.Files) != 1 {
				t.Fatalf("write stage produced %d files, want 1", len(out.Files))
			}
			var artifact contracts.EditorialArtifact
			if err := json.Unmarshal(out.Files[0].Data, &artifact); err != nil {
				t.Fatalf("parsing write stage output: %v", err)
			}

			// The writer prompt must request the format the run should use.
			got := prompts()
			if len(got) != 1 {
				t.Fatalf("expected exactly one LLM request, got %d", len(got))
			}
			wantLine := "artifact_type: " + wantFormat
			if !strings.Contains(got[0], wantLine) {
				t.Errorf("writer prompt does not request %q; the writer was configured for a different format\nprompt:\n%s", wantLine, got[0])
			}

			// And the produced artifact carries that shape (the fake honors
			// the requested type, like a real LLM would).
			if string(artifact.ArtifactType) != wantFormat {
				t.Errorf("artifact type = %q, want %q", artifact.ArtifactType, wantFormat)
			}
		})
	}
}
