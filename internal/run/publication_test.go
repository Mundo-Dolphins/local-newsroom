package run

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublicationValidate covers the publication reference rules: identity
// shape, path safety, and hash shape.
func TestPublicationValidate(t *testing.T) {
	valid := Publication{
		ID:         "mundodolphins",
		BundlePath: "/srv/editorial-bundle",
		Hash:       strings.Repeat("a", 64),
	}
	assert.NoError(t, valid.Validate())

	// ID-only (no bundle path, no hash) is valid: the fields are optional
	// provenance.
	assert.NoError(t, (Publication{ID: "mundodolphins"}).Validate())

	cases := []struct {
		name string
		p    Publication
	}{
		{"empty id", Publication{BundlePath: "/x", Hash: "abc"}},
		{"space in id", Publication{ID: "bad name"}},
		{"dotdot id", Publication{ID: ".."}},
		{"leading dot id", Publication{ID: ".hidden"}},
		{"oversized id", Publication{ID: strings.Repeat("a", 65)}},
		{"traversal bundle path", Publication{ID: "p", BundlePath: "../outside"}},
		{"uppercase hash", Publication{ID: "p", Hash: strings.Repeat("A", 64)}},
		{"short hash", Publication{ID: "p", Hash: "abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			require.Error(t, err, "publication %+v must be invalid", tc.p)
		})
	}
}

// TestManifestPublicationRoundTrip: a manifest with a publication survives
// JSON round-trips, and a manifest without one omits the key.
func TestManifestPublicationRoundTrip(t *testing.T) {
	m, err := NewManifest(NewManifestParams{
		Workspace:   Workspace{Name: "mundodolphins", Path: "workspace/mundodolphins"},
		Topic:       "EU AI Act enters into force",
		SourceMode:  SourceModeExplicit,
		Profile:     &Profile{ID: "javi", Hash: strings.Repeat("1", 64)},
		Publication: &Publication{ID: "mundodolphins", BundlePath: "/srv/editorial", Hash: strings.Repeat("2", 64)},
		Model:       NewModelMetadata("omlx", "llama3.1:8b", "http://localhost:8000/v1"),
		Config:      &ConfigSnapshot{},
		Stages:      CanonicalStages(),
		StartedAt:   baseTime,
	})
	require.NoError(t, err)
	require.NoError(t, m.Validate())

	data, err := json.Marshal(m)
	require.NoError(t, err)

	var back Manifest
	require.NoError(t, json.Unmarshal(data, &back))
	require.NotNil(t, back.Publication)
	assert.Equal(t, "mundodolphins", back.Publication.ID)
	assert.Equal(t, "/srv/editorial", back.Publication.BundlePath)
	assert.Equal(t, strings.Repeat("2", 64), back.Publication.Hash)
	assert.NoError(t, back.Validate())

	// Without a publication the JSON must not carry the key at all: the
	// public manifest schema stays recognizable to old consumers.
	m2, err := NewManifest(NewManifestParams{
		Workspace:  Workspace{Name: "w", Path: "workspace/w"},
		Topic:      "topic",
		SourceMode: SourceModeExplicit,
		Config:     &ConfigSnapshot{},
		Stages:     CanonicalStages(),
		StartedAt:  baseTime,
	})
	require.NoError(t, err)
	data2, err := json.Marshal(m2)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(data2), `"publication"`), "publication key must be omitted when unset")
}

// TestManifestRejectsUnsafePublication: the manifest constructor validates
// the publication reference, so an unsafe one (traversal path,
// secret-looking path, malformed hash) can never exist in a manifest built
// through the API.
func TestManifestRejectsUnsafePublication(t *testing.T) {
	mk := func(pub *Publication) error {
		_, err := NewManifest(NewManifestParams{
			Workspace:   Workspace{Name: "w", Path: "workspace/w"},
			Topic:       "topic",
			SourceMode:  SourceModeExplicit,
			Publication: pub,
			Config:      &ConfigSnapshot{},
			Stages:      CanonicalStages(),
			StartedAt:   baseTime,
		})
		return err
	}

	assert.ErrorContains(t, mk(&Publication{ID: "p", BundlePath: "../etc"}), "..")
	assert.Error(t, mk(&Publication{ID: "p", Hash: "XYZ"}))
	assert.NoError(t, mk(&Publication{ID: "p", BundlePath: "/srv/editorial", Hash: strings.Repeat("a", 64)}))
}
