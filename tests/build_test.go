package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// productName is shared with the CLI so tests can assert on its help output.
const productName = "newsroom"

// cmdPath returns the absolute path to this module root, so the build and
// CLI commands in this package stay hermetic. It looks for go.mod upward
// from the current directory so it works whether the test is run from the
// repo root or from any subdirectory.
func cmdPath() string {
	dir, err := os.Getwd()
	if err != nil {
		tLog("working directory unavailable:", err)
		return ""
	}

	for cur := dir; ; {
		mod := filepath.Join(cur, "go.mod")
		if _, err := os.Stat(mod); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			tLog("no go.mod found above", dir)
			return ""
		}
		cur = parent
	}
}

func tLog(args ...interface{}) {
	for _, a := range args {
		s, ok := a.(string)
		if !ok {
			s = fmt.Sprintf("%v", a)
		}
		_, _ = os.Stderr.WriteString(s)
	}
	_, _ = os.Stderr.WriteString("\n")
}

// TestBuild proves the project compiles.
func TestBuild(t *testing.T) {
	root := cmdPath()
	if root == "" {
		t.Skip("working directory unavailable")
	}

	out := filepath.Join(t.TempDir(), productName)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/newsroom")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		tLog("go build failed:", err)
		t.FailNow()
	}
}

// TestRootHelp proves the CLI's help output is wired up.
func TestRootHelp(t *testing.T) {
	root := cmdPath()
	if root == "" {
		t.Skip("working directory unavailable")
	}

	out := filepath.Join(t.TempDir(), productName)
	cmd := exec.Command("go", "build", "-o", out, "./cmd/newsroom")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		tLog("go build failed:", err)
		t.FailNow()
	}

	run := func(args ...string) string {
		cmd := exec.Command(out, args...)
		cmd.Dir = root
		b, err := cmd.CombinedOutput()
		if err != nil {
			tLog("CLI failed with", args, err)
			t.FailNow()
		}
		return string(b)
	}

	var help string
	for _, name := range []string{"--help", "-h"} {
		help = run(name)
		if strings.TrimSpace(help) == "" {
			tLog("expected non-empty help output for", name)
			t.FailNow()
		}
	}
	if help == "" {
		t.FailNow()
	}

	for _, expected := range []string{
		productName,
		"version",
	} {
		if !strings.Contains(strings.ToLower(help), expected) {
			tLog("expected help output to mention", expected)
			t.FailNow()
		}
	}
	if !strings.Contains(help, "Help about any command") {
		tLog("expected help output to offer command help")
		t.FailNow()
	}

	helpOf := run("help")
	if !strings.Contains(helpOf, "pipeline") {
		tLog("expected 'newsroom help' to mention the pipeline")
		t.FailNow()
	}
}
