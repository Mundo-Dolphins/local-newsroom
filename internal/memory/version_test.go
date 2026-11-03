package memory

import (
	"testing"
)

func TestSchemaMajor(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    int
		wantErr bool
	}{
		{"current version", "0.7.0", 0, false},
		{"future major", "1.0.0", 1, false},
		{"minor bump accepted", "0.8.2", 0, false},
		{"garbage", "garbage", 0, true},
		{"two components", "0.7", 0, true},
		{"four components", "0.7.0.1", 0, true},
		{"non-numeric", "a.b.c", 0, true},
		{"empty component", "0..0", 0, true},
		{"empty", "", 0, true},
		{"negative", "-1.0.0", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SchemaMajor(tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SchemaMajor(%q) = %d, want error", tc.version, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SchemaMajor(%q): %v", tc.version, err)
			}
			if got != tc.want {
				t.Errorf("SchemaMajor(%q) = %d, want %d", tc.version, got, tc.want)
			}
		})
	}
}

func TestSchemaVersionIsSemantic(t *testing.T) {
	major, err := SchemaMajor(SchemaVersion)
	if err != nil {
		t.Fatalf("SchemaMajor(SchemaVersion): %v", err)
	}
	if major != SchemaMajorCurrent {
		t.Errorf("SchemaMajor(SchemaVersion) = %d, want %d", major, SchemaMajorCurrent)
	}
}
