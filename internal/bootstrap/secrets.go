package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// secretStoreVersion is the on-disk schema version of the secrets file.
const secretStoreVersion = 1

// secretStore is the daemon-readable credential file (NFR-SEC-001/003). It maps
// a logical password_ref id (for example "/ksuite-mail/rs_info/password") to
// its secret value. Only the daemon user can read this file; the CLI writes it
// once during init and never reads it back for display.
type secretStore struct {
	Version int               `json:"version"`
	Secrets map[string]string `json:"secrets"`
}

func newSecretStore() *secretStore {
	return &secretStore{Version: secretStoreVersion, Secrets: map[string]string{}}
}

// loadSecretStore reads and parses an existing secrets file.
func loadSecretStore(path string) (*secretStore, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the fixed secrets location
	if err != nil {
		return nil, err
	}
	var s secretStore
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse secrets file: %w", err)
	}
	if s.Version != secretStoreVersion {
		return nil, fmt.Errorf("unsupported secrets file version %d (want %d)", s.Version, secretStoreVersion)
	}
	if s.Secrets == nil {
		s.Secrets = map[string]string{}
	}
	return &s, nil
}

// writeSecretStore writes the secrets file atomically with the given mode. The
// temp file is created with restrictive permissions and chmod-ed explicitly so
// the secret is never briefly world-readable, even under a permissive umask.
func writeSecretStore(path string, s *secretStore, mode os.FileMode) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, mode)
}

// writeFileAtomic writes data to path via a temp file in the same directory,
// fixes its mode, then renames into place.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp file into place: %w", err)
	}
	return nil
}

// secretKey returns the canonical password_ref id for an account id.
func secretKey(accountID string) string {
	return fmt.Sprintf("/ksuite-mail/%s/password", accountID)
}
