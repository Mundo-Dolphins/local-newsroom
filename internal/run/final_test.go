package run

// Tests for the final-state record (v0.7, issue #146): the manifest's
// first-class generated vs human-edited artifact pair.

import (
	"errors"
	"testing"
	"time"
)

// finalTestTime is a fixed time for final-state fixtures.
var finalTestTime = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

// validFinalState builds a structurally valid FinalState (distinct paths,
// canonical digests, a safe editor).
func validFinalState() *FinalState {
	return &FinalState{
		GeneratedPath: FinalArtifactsDir + "/generated/article.md",
		GeneratedHash: "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111",
		FinalPath:     FinalArtifactsDir + "/article.md",
		FinalHash:     "sha256:" + "2222222222222222222222222222222222222222222222222222222222222222",
		Editor:        "editor",
		EditedAt:      NewTimestamp(finalTestTime),
	}
}

// TestFinalStateValidate verifies the structural rules of the pair.
func TestFinalStateValidate(t *testing.T) {
	if err := validFinalState().Validate(); err != nil {
		t.Fatalf("a valid final state must validate: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*FinalState)
	}{
		{"nil", func(*FinalState) { /* caller-checked */ }},
		{"missing generated path", func(f *FinalState) { f.GeneratedPath = "" }},
		{"missing final path", func(f *FinalState) { f.FinalPath = "" }},
		{"identical paths", func(f *FinalState) { f.FinalPath = f.GeneratedPath }},
		{"bad generated hash", func(f *FinalState) { f.GeneratedHash = "sha256:zz" }},
		{"missing final hash", func(f *FinalState) { f.FinalHash = "" }},
		{"empty editor", func(f *FinalState) { f.Editor = "   " }},
		{"redacted editor", func(f *FinalState) { f.Editor = RedactedPlaceholder }},
		{"secret in editor", func(f *FinalState) { f.Editor = "editor " + "aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f" }},
		{"bad uri", func(f *FinalState) { f.PublishedURI = "not a uri" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := validFinalState()
			if tc.name == "nil" {
				// validate on a zero value must refuse.
				var zero FinalState
				if err := zero.Validate(); err == nil {
					t.Fatal("a zero final state must not validate")
				}
				return
			}
			tc.mut(fs)
			if err := fs.Validate(); err == nil {
				t.Fatal("must refuse: " + tc.name)
			}
		})
	}
}

// TestManifestRecordFinalState verifies the terminal-only mutation rule, the
// run-level note, the replacement on re-record, and redaction round-trips
// (an editor or URI that carries a secret is redacted and re-validated).
func TestManifestRecordFinalState(t *testing.T) {
	m, err := NewManifest(NewManifestParams{
		Workspace:  Workspace{Name: "w"},
		Topic:      "t",
		SourceMode: SourceModeExplicit,
		StartedAt:  finalTestTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}

	// A non-terminal run refuses the record.
	if err := m.RecordFinalState(validFinalState(), finalTestTime); err == nil {
		t.Fatal("a pending run must not record a final state")
	}

	if err := m.Start(finalTestTime); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A failed run is terminal: the final state is a post-run record, valid
	// for any terminal run (the publish engine gates readiness separately).
	if err := m.Fail(finalTestTime, "fetch-failed", "all sources failed"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	warningsBefore := len(m.Warnings)
	if err := m.RecordFinalState(validFinalState(), finalTestTime); err != nil {
		t.Fatalf("RecordFinalState: %v", err)
	}
	if m.FinalState == nil {
		t.Fatal("final state not recorded")
	}
	if len(m.Warnings) != warningsBefore+1 {
		t.Errorf("warnings: %d -> %d, want one new note", warningsBefore, len(m.Warnings))
	}
	if !errors.Is(m.Validate(), nil) && m.Validate() != nil {
		t.Errorf("manifest with a final state must validate: %v", m.Validate())
	}

	// Re-recording replaces and advances the clock.
	fs2 := validFinalState()
	fs2.Editor = "editor2"
	before := m.UpdatedAt
	if err := m.RecordFinalState(fs2, finalTestTime.Add(time.Hour)); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if m.FinalState.Editor != "editor2" {
		t.Errorf("re-record must replace the state (editor %q)", m.FinalState.Editor)
	}
	if !m.UpdatedAt.Time().After(before.Time()) {
		t.Error("re-recording must advance updated_at")
	}

	// Redaction round-trip: an editor that carried a secret is redacted to
	// the placeholder by the workspace boundary before validation; the
	// placeholder editor must then be accepted (the identity is unknown but
	// the pair is still learnable).
	// A redacted editor is not a valid identifier: RecordFinalState rejects
	// it, so a placeholder editor is never recorded.
	fs3 := validFinalState()
	fs3.Editor = RedactedPlaceholder
	if err := m.RecordFinalState(fs3, finalTestTime.Add(2*time.Hour)); err == nil {
		t.Error("a placeholder editor must not validate (redaction failure)")
	}

	// An editor that only consists of whitespace is not a valid identifier.
	fs4 := validFinalState()
	fs4.Editor = "   "
	if err := fs4.Validate(); err == nil {
		t.Error("a whitespace-only editor must not validate")
	}

	// A redacted published URI is accepted (the reference is unknown but the
	// pair is still learnable).
	fs6 := validFinalState()
	fs6.PublishedURI = "https://example.com/[REDACTED]"
	if err := fs6.Validate(); err != nil {
		t.Errorf("a redacted published_uri must validate: %v", err)
	}
	if err := m.RecordFinalState(fs6, finalTestTime.Add(3*time.Hour)); err != nil {
		t.Errorf("RecordFinalState(redacted uri): %v", err)
	}
}

// TestManifestFinalStateJSON verifies the wire round-trip: an omitted final
// state stays omitted, a recorded one survives marshal/unmarshal.
func TestManifestFinalStateJSON(t *testing.T) {
	m, err := NewManifest(NewManifestParams{
		Workspace:  Workspace{Name: "w"},
		Topic:      "t",
		SourceMode: SourceModeExplicit,
		StartedAt:  finalTestTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	if err := m.Start(finalTestTime); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Fail(finalTestTime, "fetch-failed", "all sources failed"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := m.RecordFinalState(validFinalState(), finalTestTime); err != nil {
		t.Fatalf("RecordFinalState: %v", err)
	}

	data, err := MarshalManifest(m)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	back, err := UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("UnmarshalManifest: %v", err)
	}
	if back.FinalState == nil {
		t.Fatal("final state did not survive the round-trip")
	}
	if back.FinalState.Editor != "editor" {
		t.Errorf("editor = %q, want editor", back.FinalState.Editor)
	}
	if err := back.Validate(); err != nil {
		t.Errorf("round-tripped manifest must validate: %v", err)
	}

	// A manifest without a final state must marshal without the field.
	m2, err := NewManifest(NewManifestParams{
		Workspace:  Workspace{Name: "w"},
		Topic:      "u",
		SourceMode: SourceModeExplicit,
		StartedAt:  finalTestTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	data2, err := MarshalManifest(m2)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	back2, err := UnmarshalManifest(data2)
	if err != nil {
		t.Fatalf("UnmarshalManifest: %v", err)
	}
	if back2.FinalState != nil {
		t.Error("final state must be omitted when nil")
	}
}
