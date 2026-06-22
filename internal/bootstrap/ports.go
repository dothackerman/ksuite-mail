package bootstrap

import "github.com/dothackerman/ksuite-mail/internal/layout"

// UserInfo carries the identity properties init needs to confirm an existing
// account is a dedicated service user before trusting it with credentials.
type UserInfo struct {
	UID     int
	HomeDir string
}

// UserProvisioner abstracts OS user and group management so init can be tested
// hermetically without creating real system accounts.
type UserProvisioner interface {
	// UserExists reports whether a user account already exists.
	UserExists(name string) (bool, error)
	// GroupExists reports whether a group already exists.
	GroupExists(name string) (bool, error)
	// EnsureSystemUser creates a dedicated system user (and its primary group)
	// with the given home and login shell if it does not already exist. It must
	// be idempotent.
	EnsureSystemUser(name, home, shell string) error
	// PrimaryGroupName returns the name of a user's primary group. It is used to
	// derive the default socket access group from the invoking human user.
	PrimaryGroupName(user string) (string, error)
	// LookupUser returns identity details for an existing account, used to verify
	// a pre-existing service user really is a dedicated system account.
	LookupUser(name string) (UserInfo, error)
}

// Chowner applies intended ownership to a path. Implementations map owner names
// to ids; the test implementation records intent instead of touching the OS.
type Chowner interface {
	Chown(path string, owner layout.Owner) error
}

// Prompter reads a secret interactively from the controlling terminal without
// echoing it. It must never read the secret from arguments, environment, or
// any non-TTY stream, and must never print the secret (NFR-SEC-005,
// NFR-OPS-000).
type Prompter interface {
	PromptSecret(label string) ([]byte, error)
}

// Deps bundles the injected collaborators for a bootstrap run.
type Deps struct {
	Users  UserProvisioner
	Chown  Chowner
	Prompt Prompter
}
