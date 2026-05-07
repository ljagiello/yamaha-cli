//go:build integration

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIntegration_StatusJSON drives the compiled binary via os/exec
// against a live receiver and asserts that `status --output json` exits
// 0 and produces a parseable JSON payload with the expected keys.
//
// Skips if the build fails or if -yamaha-host is empty.
func TestIntegration_StatusJSON(t *testing.T) {
	if yamahaHostFlag == nil || *yamahaHostFlag == "" {
		t.Skip("-yamaha-host not set; skipping integration test")
	}
	host := *yamahaHostFlag

	// Locate the repo root by walking up from this file's directory and
	// looking for go.mod. Build a fresh binary into t.TempDir.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("cannot find repo root: %v", err)
	}

	binDir := t.TempDir()
	binName := "yamaha"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(binDir, binName)

	build := exec.Command("go", "build", "-o", binPath, "./cmd/yamaha")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Run `yamaha --host <host> status --output json`. Use an empty
	// XDG_CONFIG_HOME so we don't accidentally pick up the developer's
	// config — we want a fully anonymous --host run.
	tmpHome := t.TempDir()
	cmd := exec.Command(binPath, "--host", host, "status", "--output", "json")
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+tmpHome,
		"NO_COLOR=1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("status: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON: %v\nstdout: %s", err, stdout.String())
	}
	for _, key := range []string{"zone", "power", "volume", "mute", "input"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("status JSON missing key %q; got %v", key, payload)
		}
	}
	if zone, _ := payload["zone"].(string); zone == "" {
		t.Errorf("zone is empty: %v", payload)
	}
	switch p, _ := payload["power"].(string); p {
	case "on", "standby":
	default:
		t.Errorf("unexpected power %q", p)
	}
}

// findRepoRoot walks up from the current working directory looking for
// go.mod. Used by the integration test to locate the binary source.
func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}
