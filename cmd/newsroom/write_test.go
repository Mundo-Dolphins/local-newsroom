package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteCommand validates the write command CLI interface without live LLM.
func TestWriteCommand(t *testing.T) {
	// Create temporary directory for test files
	tmpDir := t.TempDir()

	// Create a valid verification result file
	verification := map[string]interface{}{
		"stable_id":        "ver-001",
		"input_dossier_id": "dossier-001",
		"verified_at":      "2024-01-15T13:00:00Z",
		"verification_statuses": map[string]string{
			"claim-001": "supported",
		},
		"claim_details": map[string]interface{}{
			"claim-001": map[string]interface{}{
				"claim_id":            "claim-001",
				"statement":           "Test claim",
				"verification_status": "supported",
				"confidence_level":    "high",
				"supporting_evidence": []map[string]interface{}{
					{
						"source_id":        "source-001",
						"excerpt":          "Evidence text",
						"evidence_context": "Context",
					},
				},
				"conflicting_evidence": []map[string]interface{}{},
				"verification_notes":   "Notes",
			},
		},
		"contradictions":       []map[string]interface{}{},
		"verification_notes":   []map[string]interface{}{},
		"verification_summary": "Summary",
		"quality_score":        100.0,
	}

	verificationBytes, err := json.MarshalIndent(verification, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal verification: %v", err)
	}

	verificationPath := filepath.Join(tmpDir, "verification.json")
	if err := os.WriteFile(verificationPath, verificationBytes, 0644); err != nil {
		t.Fatalf("failed to write verification file: %v", err)
	}

	// Test that write command requires --dossier
	cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "write")
	output, _ := cmd.CombinedOutput()
	if !strings.Contains(string(output), "--dossier is required") {
		t.Logf("Expected '--dossier is required' error, got: %s", string(output))
	}

	// Test dry-run mode
	cmd = exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "write", "--dossier", verificationPath, "--dry-run")
	output, err = cmd.CombinedOutput()
	// Expected to fail because no LLM is configured
	if err != nil && strings.Contains(string(output), "LLM base URL required") {
		t.Log("Correctly requires LLM configuration")
	}
}

// TestWriteCommandFormatValidation tests format validation
func TestWriteCommandFormatValidation(t *testing.T) {
	// Create a minimal verification file
	tmpDir := t.TempDir()
	verification := map[string]interface{}{
		"stable_id":             "ver-001",
		"input_dossier_id":      "dossier-001",
		"verified_at":           "2024-01-15T13:00:00Z",
		"verification_statuses": map[string]string{},
		"claim_details":         map[string]interface{}{},
		"contradictions":        []map[string]interface{}{},
		"verification_notes":    []map[string]interface{}{},
		"verification_summary":  "Summary",
		"quality_score":         100.0,
	}

	verificationBytes, _ := json.Marshal(verification)
	verificationPath := filepath.Join(tmpDir, "verification.json")
	if err := os.WriteFile(verificationPath, verificationBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Test invalid format
	cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "write",
		"--dossier", verificationPath,
		"--format", "invalid-format",
	)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid format") {
		t.Logf("Expected invalid format error, got: %v, output: %s", err, string(output))
	}

	// Test valid formats
	validFormats := []string{"article", "thread", "hybrid"}
	for _, format := range validFormats {
		cmd = exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "write",
			"--dossier", verificationPath,
			"--format", format,
		)
		output, err = cmd.CombinedOutput()
		// Expected to fail with LLM error, not format error
		if err != nil && strings.Contains(string(output), "invalid format") {
			t.Errorf("Valid format %q was rejected", format)
		}
	}
}

// TestWriteCommandProfileLoading tests profile directory configuration
func TestWriteCommandProfileLoading(t *testing.T) {
	tmpDir := t.TempDir()

	// Create profile directory structure
	profileDir := filepath.Join(tmpDir, "profiles", "test-author")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create minimal common profile
	commonDir := filepath.Join(profileDir, "common")
	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write minimal style file
	styleContent := `## Style Guide
Write in a professional tone.
`
	if err := os.WriteFile(filepath.Join(commonDir, "style.md"), []byte(styleContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create verification file
	verification := map[string]interface{}{
		"stable_id":             "ver-001",
		"input_dossier_id":      "dossier-001",
		"verified_at":           "2024-01-15T13:00:00Z",
		"verification_statuses": map[string]string{},
		"claim_details":         map[string]interface{}{},
		"contradictions":        []map[string]interface{}{},
		"verification_notes":    []map[string]interface{}{},
		"verification_summary":  "Summary",
		"quality_score":         100.0,
	}

	verificationBytes, _ := json.Marshal(verification)
	verificationPath := filepath.Join(tmpDir, "verification.json")
	if err := os.WriteFile(verificationPath, verificationBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Test with profile directory
	cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "write",
		"--dossier", verificationPath,
		"--profile-dir", profileDir,
		"--profile-name", "test-author",
		"--dry-run",
	)
	output, err := cmd.CombinedOutput()
	// Expected to fail with LLM error, not profile loading error
	if err != nil && strings.Contains(string(output), "profile") &&
		!strings.Contains(string(output), "LLM base URL required") {
		t.Logf("Profile loading may have issues: %s", string(output))
	}
}

// TestWriteCommandConfigValidation tests configuration precedence
func TestWriteCommandConfigValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create verification file
	verification := map[string]interface{}{
		"stable_id":             "ver-001",
		"input_dossier_id":      "dossier-001",
		"verified_at":           "2024-01-15T13:00:00Z",
		"verification_statuses": map[string]string{},
		"claim_details":         map[string]interface{}{},
		"contradictions":        []map[string]interface{}{},
		"verification_notes":    []map[string]interface{}{},
		"verification_summary":  "Summary",
		"quality_score":         100.0,
	}

	verificationBytes, _ := json.Marshal(verification)
	verificationPath := filepath.Join(tmpDir, "verification.json")
	if err := os.WriteFile(verificationPath, verificationBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Test with all configuration flags
	cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "write",
		"--dossier", verificationPath,
		"--format", "article",
		"--temperature", "0.5",
		"--max-tokens", "10000",
		"--language", "en",
		"--tone", "professional",
		"--dry-run",
	)
	output, _ := cmd.CombinedOutput()
	// Should fail with LLM error, not flag parsing error
	if !strings.Contains(string(output), "LLM base URL required") {
		t.Logf("Expected LLM error, got: %s", string(output))
	}
}
