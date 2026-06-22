// Package layout is the single source of truth for the ksuite-mail local
// deployment boundary: canonical filesystem paths, the dedicated service
// user/group, and the hardened ownership and permission model.
//
// It encodes the deployment table documented in ARCH-DEP-001 so that the
// init bootstrap, systemd unit rendering, and tests all agree on one model.
package layout

import "io/fs"

// Identity names for the dedicated service boundary (ARCH-DEP-002, NFR-SEC-003).
const (
	// ServiceUser owns the daemon process, the credential file, and the cache.
	ServiceUser = "ksuite-mail"
	// ServiceGroup is the primary group of the service user.
	ServiceGroup = "ksuite-mail"
	// ServiceHome is the service user's home, also the state/cache directory.
	ServiceHome = "/var/lib/ksuite-mail"
	// ServiceShell is a non-login shell; the service user must not log in.
	ServiceShell = "/usr/sbin/nologin"
	// OptionalAgentsGroup is an optional dedicated socket-access group for
	// multi-user deployments. It is never created by default (NFR-OPS-002).
	OptionalAgentsGroup = "mailagents"
)

// Canonical absolute paths for a hardened Linux deployment (NFR-OPS-001,
// ARCH-DEP-001). These are logical locations; tests prefix them with a
// temporary root.
const (
	ConfigDir   = "/etc/ksuite-mail"
	ConfigFile  = "/etc/ksuite-mail/config.toml"
	SecretsFile = "/etc/ksuite-mail/secrets.json"
	StateDir    = "/var/lib/ksuite-mail"
	DBFile      = "/var/lib/ksuite-mail/mail.db"
	// SocketPath sits directly in /run (Shape A): /run is world-traversable, so
	// there is no enclosing directory whose ownership could drift on reboot. The
	// socket is created by systemd from the .socket unit, which gates access via
	// SocketGroup/SocketMode — not by a separate init-created directory.
	SocketPath    = "/run/ksuite-mail.sock"
	InstalledCLI  = "/usr/local/bin/ksuite-mail"
	InstalledDaem = "/usr/local/bin/ksuite-maild"
	// SystemdUnitDir is where service/socket units are installed.
	SystemdUnitDir = "/etc/systemd/system"
	ServiceUnit    = "ksuite-mail.service"
	SocketUnit     = "ksuite-mail.socket"
)

// Owner expresses intended ownership by name. Names (not numeric ids) keep the
// model readable and let the resolver map them to ids at apply time.
type Owner struct {
	User  string
	Group string
}

// DirSpec is a directory the init bootstrap must create with an exact mode and
// owner.
type DirSpec struct {
	Path  string
	Mode  fs.FileMode
	Owner Owner
}

// FileSpec is a file the init bootstrap must create or normalize to an exact
// mode and owner.
type FileSpec struct {
	Path  string
	Mode  fs.FileMode
	Owner Owner
}

// Dirs returns the persistent directories init must create, in creation order.
//
// Rationale for the modes:
//   - ConfigDir 0750 root:ksuite-mail — the service group must traverse it to
//     read config.toml (0640 group-read) and secrets.json, but other users
//     must not list it.
//   - StateDir 0700 ksuite-mail:ksuite-mail — only the daemon user may read the
//     cache directory (acceptance: cache not readable by the socket group).
//
// The runtime socket directory is intentionally absent: under Shape A the
// socket lives directly in volatile /run and is managed entirely by systemd.
func Dirs() []DirSpec {
	return []DirSpec{
		{Path: ConfigDir, Mode: 0o750, Owner: Owner{User: "root", Group: ServiceGroup}},
		{Path: StateDir, Mode: 0o700, Owner: Owner{User: ServiceUser, Group: ServiceGroup}},
	}
}

// ConfigFileSpec is the spec for /etc/ksuite-mail/config.toml: root-owned,
// group-readable by the service so the daemon can read it, never world
// readable (NFR-CFG-002, ARCH-DEP-001).
func ConfigFileSpec() FileSpec {
	return FileSpec{Path: ConfigFile, Mode: 0o640, Owner: Owner{User: "root", Group: ServiceGroup}}
}

// SecretsFileSpec is the spec for /etc/ksuite-mail/secrets.json: readable and
// writable only by the service user, owned root as group so the access group
// can never read it (NFR-SEC-003, ARCH-CON-002).
func SecretsFileSpec() FileSpec {
	return FileSpec{Path: SecretsFile, Mode: 0o600, Owner: Owner{User: ServiceUser, Group: "root"}}
}

// SocketSpec documents the runtime socket boundary (NFR-OPS-002). The socket
// itself is created at runtime by systemd; init only records the intended
// model and renders it into the socket unit.
func SocketSpec(accessGroup string) FileSpec {
	return FileSpec{Path: SocketPath, Mode: 0o660, Owner: Owner{User: ServiceUser, Group: accessGroup}}
}
