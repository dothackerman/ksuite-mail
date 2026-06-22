package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"

	"github.com/dothackerman/ksuite-mail/internal/layout"
	"golang.org/x/term"
)

// RealUsers provisions accounts through the host's user database and useradd.
type RealUsers struct{}

func (RealUsers) UserExists(name string) (bool, error) {
	_, err := user.Lookup(name)
	if err == nil {
		return true, nil
	}
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return false, nil
	}
	return false, err
}

func (RealUsers) GroupExists(name string) (bool, error) {
	_, err := user.LookupGroup(name)
	if err == nil {
		return true, nil
	}
	var unknown user.UnknownGroupError
	if errors.As(err, &unknown) {
		return false, nil
	}
	return false, err
}

func (RealUsers) EnsureSystemUser(name, home, shell string) error {
	if _, err := user.Lookup(name); err == nil {
		return nil
	}
	// --system creates a system account with its own primary group; --home and
	// --shell match ARCH-DEP-002. Requires root.
	cmd := exec.Command("useradd", "--system", "--home-dir", home, "--shell", shell, name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("useradd %s: %w", name, err)
	}
	return nil
}

func (RealUsers) PrimaryGroupName(username string) (string, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return "", err
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return "", err
	}
	return g.Name, nil
}

func (RealUsers) LookupUser(name string) (UserInfo, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return UserInfo{}, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return UserInfo{}, fmt.Errorf("parse uid for %q: %w", name, err)
	}
	return UserInfo{UID: uid, HomeDir: u.HomeDir}, nil
}

// RealChowner resolves owner names to numeric ids and applies them with chown.
type RealChowner struct{}

func (RealChowner) Chown(path string, owner layout.Owner) error {
	uid := -1
	gid := -1
	if owner.User != "" {
		id, err := lookupUID(owner.User)
		if err != nil {
			return err
		}
		uid = id
	}
	if owner.Group != "" {
		id, err := lookupGID(owner.Group)
		if err != nil {
			return err
		}
		gid = id
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s to %s:%s: %w", path, owner.User, owner.Group, err)
	}
	return nil
}

func lookupUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

func lookupGID(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

// TTYPrompter reads secrets from the controlling terminal with echo disabled.
// It opens /dev/tty directly so credentials cannot be piped in from a file,
// argument, or environment variable (NFR-OPS-000, NFR-SEC-005).
type TTYPrompter struct{}

func (TTYPrompter) PromptSecret(label string) ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("no interactive terminal available for credential entry: %w", err)
	}
	defer func() { _ = tty.Close() }()

	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("credential entry requires an interactive terminal")
	}

	_, _ = fmt.Fprintf(tty, "%s: ", label)
	secret, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(tty)
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	return secret, nil
}

// RealDeps returns the production wiring for a bootstrap run.
func RealDeps() Deps {
	return Deps{
		Users:  RealUsers{},
		Chown:  RealChowner{},
		Prompt: TTYPrompter{},
	}
}

// noopUsers and noopChowner back staging runs (a non-real --root prefix), where
// OS user creation and ownership changes are neither possible (unprivileged)
// nor meaningful. Staging prepares and validates the filesystem boundary only;
// ownership is exercised by the unit tests and applied for real under root.
type noopUsers struct{}

func (noopUsers) UserExists(string) (bool, error)         { return true, nil }
func (noopUsers) GroupExists(string) (bool, error)        { return true, nil }
func (noopUsers) EnsureSystemUser(_, _, _ string) error   { return nil }
func (noopUsers) PrimaryGroupName(string) (string, error) { return "", nil }

// LookupUser reports a dedicated system account so the staging boundary check
// passes; real identity verification only matters under root on the real host.
func (noopUsers) LookupUser(string) (UserInfo, error) {
	return UserInfo{UID: 1, HomeDir: layout.ServiceHome}, nil
}

type noopChowner struct{}

func (noopChowner) Chown(string, layout.Owner) error { return nil }

// StagingDeps returns wiring for a non-privileged staging run under a --root
// prefix. It still uses the real TTY prompter so credential entry behaves
// identically to production.
func StagingDeps() Deps {
	return Deps{
		Users:  noopUsers{},
		Chown:  noopChowner{},
		Prompt: TTYPrompter{},
	}
}
