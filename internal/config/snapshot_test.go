package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSnapshotIntoManifest proves the full acceptance path: a config file
// (with an env-referenced secret) resolves into the effective configuration
// snapshot, which is recorded in a run manifest, and the raw credential
// never appears in the serialized manifest.
func TestSnapshotIntoManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newsroom.yaml")
	content := `
schema_version: 1

llm:
  base_url: http://10.0.0.5:11434/v1
  model: file-model
  api_key:
    env: TEST_SNAPSHOT_KEY

search:
  max_queries: 3
  language: fr
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	f, err := Load(path)
	require.NoError(t, err)

	// A credential-looking value must never survive into the manifest.
	t.Setenv("TEST_SNAPSHOT_KEY", "supersecret-credential-123")
	t.Setenv("OMLX_MODEL", "env-model")

	app := New(f, path)
	snap, err := app.Snapshot(nil)
	require.NoError(t, err)

	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "ws"},
		Topic:      "test topic",
		SourceMode: run.SourceModeUnknown,
		Config:     snap,
	})
	require.NoError(t, err)
	require.NoError(t, m.Validate())

	data, err := json.Marshal(m)
	require.NoError(t, err)

	out := string(data)
	assert.NotContains(t, out, "supersecret-credential-123", "raw credential must not reach the manifest")
	assert.Contains(t, out, "env-model", "environment must beat the file in the snapshot")
	assert.Contains(t, out, `"search.language":"fr"`, "file value must survive where no env var exists")
	assert.Contains(t, out, `"search.max_queries":"3"`)
	assert.Contains(t, out, run.RedactedPlaceholder)
}

// TestSnapshotRedactsEnvSourcedSecrets verifies that a secret resolved from
// the environment layer (the primary OMLX_API_KEY path) is also redacted in
// the snapshot.
func TestSnapshotRedactsEnvSourcedSecrets(t *testing.T) {
	t.Setenv("OMLX_API_KEY", "sk-looks-like-a-token-abcdef0123456789")
	t.Setenv("OMLX_BASE_URL", "http://localhost:11434/v1")

	app := New(nil, "")
	snap, err := app.Snapshot(nil)
	require.NoError(t, err)
	require.NoError(t, snap.Validate())

	v, ok := snap.Get("llm.api_key")
	assert.True(t, ok)
	assert.Equal(t, run.RedactedPlaceholder, v)
	assert.False(t, strings.Contains(v, "sk-"), "snapshot must not contain the raw token")
}
