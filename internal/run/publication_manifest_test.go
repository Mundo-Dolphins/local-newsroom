package run

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The v0.6 manifest schema adds an optional publication_state block. These
// tests pin the backward-compatibility contract:
//
//   - a 0.5.0 manifest without publication_state loads and validates, with a
//     nil PublicationState field,
//   - a 0.6.0 manifest with publication_state round-trips losslessly,
//   - a malformed publication_state fails validation (fail closed), never
//     "best effort repair".

// TestManifestV05WithoutPublicationStateLoads downgrades a real example
// manifest to 0.5.0 (dropping the new field) and proves it still loads.
func TestManifestV05WithoutPublicationStateLoads(t *testing.T) {
	path := filepath.Join("..", "..", "schemas", "examples", "run-manifest-example.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	m, err := UnmarshalManifest(data)
	require.NoError(t, err)

	// Demote to the v0.5 schema and drop the v0.6 field.
	m.SchemaVersion = "0.5.0"
	m.PublicationState = nil
	data, err = MarshalManifest(m)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "publication_state", "a v0.5 document must not carry the v0.6 field")

	back, err := UnmarshalManifest(data)
	require.NoError(t, err, "a 0.5.0 manifest must load in a v0.6 binary")
	assert.Equal(t, "0.5.0", back.SchemaVersion)
	assert.Nil(t, back.PublicationState)
	require.NoError(t, back.Validate())
}

// TestManifestPublicationStateRoundTrip proves the v0.6 field survives a
// marshal/unmarshal cycle unchanged.
func TestManifestPublicationStateRoundTrip(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	for _, s := range m.Stages {
		require.NoError(t, m.SkipStage(s.Name, at(2), "round-trip test"))
	}
	require.NoError(t, m.Succeed(at(3)))

	planHash := "sha256:" + string("ab3f9c81d2b4e6f708192a3b4c5d6e7f8a3f9c81d2b4e6f708192a3b4c5d6e7f")[:64]
	updated := at(3)
	m.PublicationState = &PublicationState{
		PlanID:    "pub-20260714T120300Z-1b2c3d4e5f60",
		PlanPath:  "publication/plans/pub-20260714T120300Z-1b2c3d4e5f60.json",
		PlanHash:  planHash,
		Status:    PublicationStatusSucceeded,
		Targets:   2,
		Succeeded: 2,
		Failed:    0,
		UpdatedAt: NewTimestamp(updated),
	}
	require.NoError(t, m.Validate())

	data, err := MarshalManifest(m)
	require.NoError(t, err)
	back, err := UnmarshalManifest(data)
	require.NoError(t, err)
	require.NotNil(t, back.PublicationState)
	assert.Equal(t, *m.PublicationState, *back.PublicationState)
	assert.NoError(t, back.Validate())
}

// TestManifestPublicationStateValidation pins the fail-closed validation
// rules for the v0.6 block.
func TestManifestPublicationStateValidation(t *testing.T) {
	newState := func() *PublicationState {
		return &PublicationState{
			PlanID:    "pub-20260714T120300Z-1b2c3d4e5f60",
			PlanPath:  "publication/plans/pub-20260714T120300Z-1b2c3d4e5f60.json",
			PlanHash:  "sha256:" + string("ab3f9c81d2b4e6f708192a3b4c5d6e7f8a3f9c81d2b4e6f708192a3b4c5d6e7f")[:64],
			Status:    PublicationStatusPublishing,
			Targets:   2,
			Succeeded: 1,
			Failed:    0,
			UpdatedAt: NewTimestamp(at(3)),
		}
	}

	cases := []struct {
		name    string
		mutate  func(ps *PublicationState)
		wantErr bool
	}{
		{"valid in-flight summary", func(ps *PublicationState) {}, false},
		{"empty plan id", func(ps *PublicationState) { ps.PlanID = "" }, true},
		{"malformed plan id", func(ps *PublicationState) { ps.PlanID = "not-a-plan-id" }, true},
		{"empty plan path", func(ps *PublicationState) { ps.PlanPath = "" }, true},
		{"plan path escaping the run root", func(ps *PublicationState) { ps.PlanPath = "../plans/x.json" }, true},
		{"plan path in the wrong directory", func(ps *PublicationState) { ps.PlanPath = "output/plans/x.json" }, true},
		{"missing plan hash", func(ps *PublicationState) { ps.PlanHash = "" }, true},
		{"plan hash with wrong scheme", func(ps *PublicationState) { ps.PlanHash = "md5:0123" }, true},
		{"plan hash the wrong length", func(ps *PublicationState) { ps.PlanHash = "sha256:abc" }, true},
		{"unknown status", func(ps *PublicationState) { ps.Status = "exploded" }, true},
		{"succeeded with failed targets", func(ps *PublicationState) {
			ps.Status = PublicationStatusSucceeded
			ps.Failed = 1
		}, true},
		{"failed with only cancelled remainder (failed=0 is consistent)", func(ps *PublicationState) {
			ps.Status = PublicationStatusFailed
			ps.Failed = 0
		}, false},
		{"failed while every target succeeded", func(ps *PublicationState) {
			ps.Status = PublicationStatusFailed
			ps.Succeeded = 2
		}, true},
		{"negative target count", func(ps *PublicationState) { ps.Targets = -1 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManifest(t)
			require.NoError(t, m.Start(at(1)))
			for _, s := range m.Stages {
				require.NoError(t, m.SkipStage(s.Name, at(2), "round-trip test"))
			}
			require.NoError(t, m.Succeed(at(3)))
			m.PublicationState = newState()
			tc.mutate(m.PublicationState)
			err := m.Validate()
			if tc.wantErr {
				assert.Error(t, err, "invalid publication_state must fail validation")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestValidSHA256Digest pins the digest-format rules used by both the
// manifest summary and the plan contract.
func TestValidSHA256Digest(t *testing.T) {
	assert.False(t, ValidSHA256Digest(""))
	assert.False(t, ValidSHA256Digest("abc123"))
	assert.False(t, ValidSHA256Digest("md5:0123456789abcdef0123456789abcdef"))
	assert.False(t, ValidSHA256Digest("sha256:XYZ"))
	assert.False(t, ValidSHA256Digest("sha256:"+string("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde")[:63]))
	assert.True(t, ValidSHA256Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"))
}

// TestValidPublicationStatus pins the mirrored status vocabulary.
func TestValidPublicationStatus(t *testing.T) {
	for _, s := range AllPublicationStatuses {
		assert.True(t, ValidPublicationStatus(s), "%q must be a valid publication status", s)
	}
	assert.False(t, ValidPublicationStatus(""))
	assert.False(t, ValidPublicationStatus("SUCCEEDED"))
	assert.False(t, ValidPublicationStatus("published"))
}

// TestPublicationStatusTerminal pins which mirrored statuses are terminal.
func TestPublicationStatusTerminal(t *testing.T) {
	terminal := map[string]bool{
		PublicationStatusSucceeded:          true,
		PublicationStatusFailed:             true,
		PublicationStatusPartiallySucceeded: true,
		PublicationStatusCancelled:          true,
		PublicationStatusSkipped:            true,
		PublicationStatusPlanned:            false,
		PublicationStatusApproved:           false,
		PublicationStatusPublishing:         false,
	}
	for s, want := range terminal {
		assert.Equal(t, want, PublicationStatusTerminal(s), "terminal(%q)", s)
	}
	assert.False(t, PublicationStatusTerminal("not-a-status"))

}
