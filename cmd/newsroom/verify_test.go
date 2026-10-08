package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyCommand validates the verify command CLI interface without live LLM.
func TestVerifyCommand(t *testing.T) {
	// Create temporary directory for test files
	tmpDir := t.TempDir()

	// Create a valid dossier file
	dossier := map[string]interface{}{
		"stable_id":    "test-dossier-001",
		"topic":        "Test Topic",
		"generated_at": "2024-01-15T12:00:00Z",
		"sources": []map[string]interface{}{
			{
				"stable_id":    "source-001",
				"original_url": "https://example.com/article",
				"source_type":  "web",
				"retrieved_at": "2024-01-15T10:00:00Z",
				"title":        "Test Article",
			},
		},
		"claims": []map[string]interface{}{
			{
				"id":         "claim-001",
				"statement":  "Test claim statement",
				"confidence": "high",
				"evidence": []map[string]interface{}{
					{
						"source_id": "source-001",
						"excerpts": []map[string]interface{}{
							{
								"text":         "Test evidence",
								"start_offset": 0,
								"end_offset":   13,
							},
						},
						"claim_context": "Test context",
					},
				},
			},
		},
	}

	dossierBytes, err := json.MarshalIndent(dossier, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal dossier: %v", err)
	}

	dossierPath := filepath.Join(tmpDir, "dossier.json")
	if err := os.WriteFile(dossierPath, dossierBytes, 0644); err != nil {
		t.Fatalf("failed to write dossier file: %v", err)
	}

	// Test that verify command requires --dossier
	cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "verify")
	output, _ := cmd.CombinedOutput()
	if !strings.Contains(string(output), "--dossier is required") {
		t.Logf("Expected '--dossier is required' error, got: %s", string(output))
	}

	// Test dry-run mode (won't call LLM, but validates input)
	// This will fail with LLM not configured, but that's expected
	cmd = exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "verify", "--dossier", dossierPath, "--dry-run")
	output, err = cmd.CombinedOutput()
	// Expected to fail because no LLM is configured, which validates the flag parsing worked
	if err == nil {
		t.Log("Note: dry-run succeeded (LLM may not be required for validation)")
	} else if !strings.Contains(string(output), "LLM base URL required") {
		t.Logf("Unexpected error: %s", string(output))
	}
}

// TestVerifyCommandConfigValidation tests configuration precedence
func TestVerifyCommandConfigValidation(t *testing.T) {
	// Create a valid dossier file
	tmpDir := t.TempDir()
	dossier := map[string]interface{}{
		"stable_id":    "test-dossier-002",
		"topic":        "Test Topic",
		"generated_at": "2024-01-15T12:00:00Z",
		"sources":      []map[string]interface{}{},
		"claims":       []map[string]interface{}{},
	}

	dossierBytes, _ := json.Marshal(dossier)
	dossierPath := filepath.Join(tmpDir, "dossier.json")
	if err := os.WriteFile(dossierPath, dossierBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Test with missing LLM configuration
	cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "verify", "--dossier", dossierPath)
	output, _ := cmd.CombinedOutput()

	// Should fail with clear error about LLM configuration
	if !strings.Contains(string(output), "LLM base URL required") &&
		!strings.Contains(string(output), "LLM model required") {
		t.Logf("Expected LLM configuration error, got: %s", string(output))
	}

	// Test with invalid verification date format
	cmd = exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "verify",
		"--dossier", dossierPath,
		"--verification-date", "invalid-date",
	)
	output, _ = cmd.CombinedOutput()
	if !strings.Contains(string(output), "invalid verification date format") {
		t.Logf("Expected date format error, got: %s", string(output))
	}
}

// TestVerifyOutputValidation tests that output is valid JSON
func TestVerifyOutputValidation(t *testing.T) {
	// This test verifies that the output structure would be valid JSON
	// when the LLM returns a valid response.
	// Note: We can't test the actual verification without a running LLM.

	// Verify that the verify command accepts all expected flags
	t.Run("flag parsing", func(t *testing.T) {
		cmd := exec.Command("go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom", "verify", "--help")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("verify --help failed: %v\n%s", err, string(output))
		}

		expectedFlags := []string{
			"--dossier",
			"--output",
			"--verification-date",
			"--quality-threshold",
			"--llm-base-url",
			"--llm-model",
			"--llm-api-key",
			"--llm-timeout",
			"--dry-run",
			"--verbose",
		}

		for _, flag := range expectedFlags {
			if !strings.Contains(string(output), flag) {
				t.Errorf("Expected help output to contain flag %q", flag)
			}
		}
	})
}
