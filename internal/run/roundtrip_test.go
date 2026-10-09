package run

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifestRoundTripSuccess marshals a fully completed manifest and proves
// the JSON round trip is lossless.
func TestManifestRoundTripSuccess(t *testing.T) {
	m := newTestManifest(t)
	// Drive the manifest to a successful terminal state.
	require.NoError(t, m.Start(at(1)))
	for _, s := range m.Stages {
		require.NoError(t, m.SkipStage(s.Name, at(2), "round-trip test"))
	}
	require.NoError(t, m.Succeed(at(3)))
	require.NoError(t, m.Validate())

	data, err := MarshalManifest(m)
	require.NoError(t, err)

	// The encoding must be stable: marshaling twice yields identical bytes.
	again, err := MarshalManifest(m)
	require.NoError(t, err)
	assert.Equal(t, data, again, "marshaling must be deterministic")

	back, err := UnmarshalManifest(data)
	require.NoError(t, err)
	assert.Equal(t, *m, *back, "unmarshal(marshal(m)) must equal m")

	// Re-marshal of the unmarshaled copy must be byte-identical.
	remarshaled, err := MarshalManifest(back)
	require.NoError(t, err)
	assert.Equal(t, data, remarshaled, "round trip must be byte-stable")
}

// TestManifestRoundTripFailure covers a failed run with warnings, errors,
// retry attempts, and redacted failure text.
func TestManifestRoundTripFailure(t *testing.T) {
	m := newTestManifest(t)
	require.NoError(t, m.Start(at(1)))
	require.NoError(t, m.SkipStage(StageDiscovery, at(2), "explicit URLs provided"))
	require.NoError(t, m.StartStage(StageFetch, at(3)))
	require.NoError(t, m.FailStage(StageFetch, at(8), "fetch_error", "GET failed: Bearer abc123def456 expired"))
	require.NoError(t, m.AddWarning("retrying", "retrying with backoff", at(9)))
	require.NoError(t, m.StartStage(StageFetch, at(10)))
	require.NoError(t, m.SucceedStage(StageFetch, at(12)))
	require.NoError(t, m.StartStage(StageResearch, at(13)))
	require.NoError(t, m.FailStage(StageResearch, at(20), "llm_error", "model request timed out"))
	require.NoError(t, m.Fail(at(21), "run_failed", "research could not be completed"))
	require.NoError(t, m.Validate())

	data, err := MarshalManifest(m)
	require.NoError(t, err)

	// No raw credentials may appear anywhere in the document.
	assert.NotContains(t, string(data), "abc123def456")

	back, err := UnmarshalManifest(data)
	require.NoError(t, err)
	assert.Equal(t, *m, *back)
}

func TestUnmarshalManifestRejectsOtherMajorVersion(t *testing.T) {
	future := newTestManifest(t)
	future.SchemaVersion = "1.2.3"
	data, err := MarshalManifest(future)
	require.NoError(t, err)

	_, err = UnmarshalManifest(data)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupportedSchemaVersion), "want ErrUnsupportedSchemaVersion, got %v", err)
}

func TestUnmarshalManifestRejectsInvalidDocuments(t *testing.T) {
	// Not JSON at all.
	_, err := UnmarshalManifest([]byte(`{not json`))
	assert.Error(t, err)

	// Missing schema version.
	_, err = UnmarshalManifest([]byte(`{"id":"run-abc","workspace":{"name":"w"},"status":"pending","topic":"t"}`))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupportedSchemaVersion))

	// Unknown run status.
	m := newTestManifest(t)
	m.Status = "exploded"
	data, err := MarshalManifest(m)
	require.NoError(t, err)
	_, err = UnmarshalManifest(data)
	assert.Error(t, err)

	// Unknown stage status.
	m = newTestManifest(t)
	m.Stages[0].Status = "done"
	data, err = MarshalManifest(m)
	require.NoError(t, err)
	_, err = UnmarshalManifest(data)
	assert.Error(t, err)
}

// TestUnmarshalManifestIgnoresUnknownFields checks forward compatibility:
// fields added by a future 0.x patch are ignored.
func TestUnmarshalManifestIgnoresUnknownFields(t *testing.T) {
	m := newTestManifest(t)
	data, err := MarshalManifest(m)
	require.NoError(t, err)

	withExtra := append([]byte(`{"experimental": true,`), data[1:]...)
	back, err := UnmarshalManifest(withExtra)
	require.NoError(t, err, "unknown fields must be ignored")
	assert.Equal(t, m.ID, back.ID)
}

func TestExampleManifestFile(t *testing.T) {
	// The checked-in example must parse and validate.
	path := filepath.Join("..", "..", "schemas", "examples", "run-manifest-example.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the example manifest must exist")

	m, err := UnmarshalManifest(data)
	require.NoError(t, err)
	assert.Equal(t, SchemaVersion, m.SchemaVersion)
	assert.Equal(t, RunStatusSucceeded, m.Status)
	assert.Equal(t, OutcomeSucceeded, m.Result.Outcome)
	assert.NotEmpty(t, m.Topic)
	assert.NotEmpty(t, m.Stages)
	assert.NotEmpty(t, m.Artifacts)
}
