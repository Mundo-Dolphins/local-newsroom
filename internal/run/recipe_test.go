package run

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validRecipeRef() *Recipe {
	return &Recipe{
		ID:         "afc-east-weekly",
		Version:    "1.2.0",
		Author:     "javi",
		BundlePath: "/tmp/base",
		Hash:       strings.Repeat("a", 64),
		Inputs:     map[string]string{"week": "7"},
	}
}

func TestRecipeRefValidate(t *testing.T) {
	require.NoError(t, validRecipeRef().Validate())
}

func TestRecipeRefValidateFailures(t *testing.T) {
	t.Run("missing id", func(t *testing.T) {
		r := validRecipeRef()
		r.ID = ""
		require.Error(t, r.Validate())
	})
	t.Run("non-kebab id", func(t *testing.T) {
		r := validRecipeRef()
		r.ID = "Bad_ID"
		require.Error(t, r.Validate())
	})
	t.Run("missing hash", func(t *testing.T) {
		r := validRecipeRef()
		r.Hash = ""
		require.Error(t, r.Validate())
	})
	t.Run("uppercase hash", func(t *testing.T) {
		r := validRecipeRef()
		r.Hash = strings.ToUpper("a") + strings.Repeat("a", 63)
		require.Error(t, r.Validate())
	})
	t.Run("traversal bundle path", func(t *testing.T) {
		r := validRecipeRef()
		r.BundlePath = "../escape"
		require.Error(t, r.Validate())
	})
	t.Run("bad input name", func(t *testing.T) {
		r := validRecipeRef()
		r.Inputs = map[string]string{"bad name!": "x"}
		require.Error(t, r.Validate())
	})
	t.Run("oversized input value", func(t *testing.T) {
		r := validRecipeRef()
		r.Inputs = map[string]string{"week": strings.Repeat("w", 8193)}
		require.Error(t, r.Validate())
	})
	t.Run("too many inputs", func(t *testing.T) {
		r := validRecipeRef()
		tooMany := make(map[string]string)
		for i := 0; i < 33; i++ {
			tooMany[string(rune('a'+i%26))+string(rune('0'+i/26))+"x"] = "v"
		}
		r.Inputs = tooMany
		require.Error(t, r.Validate())
	})
}

func TestManifestRecipeOptional(t *testing.T) {
	m := newTestManifest(t)
	require.Nil(t, m.Recipe)

	// Marshal without a recipe: the key must be absent (omitempty).
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"recipe"`)

	m.Recipe = validRecipeRef()
	data, err = json.Marshal(m)
	require.NoError(t, err)
	require.Contains(t, string(data), `"recipe"`)

	var back Manifest
	require.NoError(t, json.Unmarshal(data, &back))
	require.NotNil(t, back.Recipe)
	require.Equal(t, "afc-east-weekly", back.Recipe.ID)
	require.Equal(t, m.Recipe.Hash, back.Recipe.Hash)
}

func TestManifestValidateRejectsBadRecipe(t *testing.T) {
	m := newTestManifest(t)
	m.Recipe = &Recipe{ID: "x", Hash: ""}
	require.Error(t, m.Validate())
}

func TestNewManifestCarriesRecipe(t *testing.T) {
	config := &ConfigSnapshot{}
	m, err := NewManifest(NewManifestParams{
		Workspace:  Workspace{Name: "ws", Path: "/tmp/ws"},
		Topic:      "topic",
		SourceMode: SourceModeDiscovered,
		Recipe:     validRecipeRef(),
		Config:     config,
		StartedAt:  baseTime,
	})
	require.NoError(t, err)
	require.NotNil(t, m.Recipe)
	require.Equal(t, "afc-east-weekly", m.Recipe.ID)
	require.NoError(t, m.Validate())
}
