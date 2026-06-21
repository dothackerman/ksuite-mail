//go:build e2e

// Package e2e contains hermetic end-to-end tests. They build the real
// ksuite-mail binary and exercise it against a temporary root, with no real
// Infomaniak credentials or network access (AGENTS.md: keep e2e hermetic).
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// moduleRoot returns the repository root by walking up from this test file
// until it finds go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine caller location")
	}
	dir := filepath.Dir(here)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestInitPreparesBoundaryEndToEnd builds the CLI and runs `init` against a
// staging root, verifying the deployment boundary is created with the expected
// modes and that the boundary-only run prompts for no credentials.
func TestInitPreparesBoundaryEndToEnd(t *testing.T) {
	root := moduleRoot(t)
	work := t.TempDir()

	bin := filepath.Join(work, "ksuite-mail")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ksuite-mail")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}

	staging := filepath.Join(work, "staging")
	// --access-group avoids depending on the test runner's user/group database.
	cmd := exec.Command(bin, "init", "--root", staging, "--access-group", "testgroup")
	cmd.Env = append(os.Environ(), "SUDO_USER=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run init: %v\n%s", err, out)
	}

	// The boundary files must exist with hardened modes.
	checks := map[string]os.FileMode{
		filepath.Join(staging, "etc/ksuite-mail"):              0o750,
		filepath.Join(staging, "var/lib/ksuite-mail"):          0o700,
		filepath.Join(staging, "run/ksuite-mail"):              0o750,
		filepath.Join(staging, "etc/ksuite-mail/config.toml"):  0o640,
		filepath.Join(staging, "etc/ksuite-mail/secrets.json"): 0o600,
	}
	for path, want := range checks {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Errorf("stat %s: %v", path, statErr)
			continue
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %#o, want %#o", path, got, want)
		}
	}

	// The printed systemd units must carry the hardening boundary.
	for _, want := range []string{"User=ksuite-mail", "SocketGroup=testgroup", "NoNewPrivileges=true"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("init output missing %q", want)
		}
	}
}
