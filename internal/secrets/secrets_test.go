package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/secrets"
)

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	// WriteFile is subject to umask; force the exact mode we want to test.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return p
}

func TestLoadResolvesSecretByID(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secrets.json",
		`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"hunter2"}}`, 0o600)

	store, err := secrets.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	val, ok := store.Resolve("/ksuite-mail/rs_info/password")
	if !ok || val != "hunter2" {
		t.Fatalf("Resolve = (%q, %v), want (hunter2, true)", val, ok)
	}
	if _, ok := store.Resolve("/missing"); ok {
		t.Fatalf("Resolve of unknown id should report not found")
	}
}

func TestLoadRejectsGroupOrWorldReadablePermissions(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secrets.json",
		`{"version":1,"secrets":{}}`, 0o640)

	_, err := secrets.Load(p)
	if !errors.Is(err, secrets.ErrUnsafePermissions) {
		t.Fatalf("Load with 0640 = %v, want ErrUnsafePermissions", err)
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secrets.json",
		`{"version":2,"secrets":{}}`, 0o600)

	_, err := secrets.Load(p)
	if !errors.Is(err, secrets.ErrUnsupportedVersion) {
		t.Fatalf("Load with version 2 = %v, want ErrUnsupportedVersion", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "secrets.json", `{not json`, 0o600)

	_, err := secrets.Load(p)
	if !errors.Is(err, secrets.ErrMalformed) {
		t.Fatalf("Load with malformed json = %v, want ErrMalformed", err)
	}
}

func TestLoadMissingFileReportsNotExist(t *testing.T) {
	_, err := secrets.Load(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load of missing file = %v, want os.ErrNotExist", err)
	}
}
