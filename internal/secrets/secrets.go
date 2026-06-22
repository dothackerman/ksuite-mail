// Package secrets is the daemon-side reader for the file-backed credential
// store written by `ksuite-mail init` (NFR-SEC-001/003). Only ksuite-maild
// loads this file; the CLI never reads it (ARCH-CON-002, NFR-SEC-002).
//
// Load is defensive: it refuses a file whose permissions would let any group or
// other user read it, refuses unknown on-disk versions, and refuses malformed
// content, returning typed errors so the daemon can map them to safe,
// content-free diagnostics rather than surfacing a raw failure.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Version is the only on-disk schema version this build understands. It matches
// the version written by the init bootstrap.
const Version = 1

// Typed load failures, distinguished with errors.Is by callers that need to
// assign a stable diagnostic code.
var (
	ErrUnsafePermissions  = errors.New("secrets file is readable beyond its owner")
	ErrUnsupportedVersion = errors.New("unsupported secrets file version")
	ErrMalformed          = errors.New("secrets file is not valid JSON")
)

// Store is the parsed credential file: a map from a password_ref id to its
// secret value.
type Store struct {
	Version int               `json:"version"`
	Secrets map[string]string `json:"secrets"`
}

// Load reads and validates the secrets file at path.
func Load(path string) (*Store, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err // preserves os.ErrNotExist for callers
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%w: mode %o", ErrUnsafePermissions, perm)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is the fixed, daemon-owned secrets location
	if err != nil {
		return nil, err
	}

	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if s.Version != Version {
		return nil, fmt.Errorf("%w %d (want %d)", ErrUnsupportedVersion, s.Version, Version)
	}
	if s.Secrets == nil {
		s.Secrets = map[string]string{}
	}
	return &s, nil
}

// Resolve returns the secret value for id and whether it was present.
func (s *Store) Resolve(id string) (string, bool) {
	v, ok := s.Secrets[id]
	return v, ok
}
