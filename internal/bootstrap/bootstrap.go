// Package bootstrap implements `ksuite-mail init`: it prepares the local
// filesystem and service boundary for the daemon without ever exposing
// credentials to the invoking shell (UC-008, NFR-OPS-000, ARCH-RUN-006).
//
// The work is expressed against injected ports (UserProvisioner, Chowner,
// Prompter) so the whole flow runs hermetically in tests against a temporary
// root, while the production wiring talks to the real OS.
package bootstrap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/layout"
	"github.com/dothackerman/ksuite-mail/internal/systemd"
)

// UnitAction selects what init does with the rendered systemd units.
type UnitAction int

const (
	// UnitsPrint writes the rendered units to the output stream for the
	// operator to install manually. This is the safe default.
	UnitsPrint UnitAction = iota
	// UnitsInstall writes the units into the systemd unit directory.
	UnitsInstall
)

// AccountSeed optionally adds and credentials one account during init. When
// nil, init only prepares the boundary and seeds an empty config/secrets pair.
type AccountSeed struct {
	ID       string
	Email    string
	Host     string
	Port     int
	TLS      bool
	Username string
	Policy   string
	Domains  []string
	Folders  []string
}

// Options configures a bootstrap run.
type Options struct {
	// Root prefixes every canonical path. Empty (or "/") targets the real
	// filesystem; tests pass a temporary directory.
	Root string
	// AccessGroup is the socket access group. When empty it is derived from
	// InvokingUser's primary group (NFR-OPS-002).
	AccessGroup string
	// InvokingUser is the human who ran `sudo ksuite-mail init` (typically
	// $SUDO_USER), used to derive the default access group.
	InvokingUser string
	// Units selects install vs print behavior.
	Units UnitAction
	// Account, when set, is added to the config and credentialed via the TTY
	// prompt.
	Account *AccountSeed
	// Out receives human-readable progress. Secrets are never written here.
	Out io.Writer
}

// Result summarizes what a run did. It deliberately contains no secret values.
type Result struct {
	AccessGroup        string
	ServiceUserCreated bool
	ConfigCreated      bool
	SecretsCreated     bool
	AccountAdded       string
	UnitsInstalled     bool
	UnitPaths          []string
}

type runner struct {
	opts Options
	deps Deps
	out  io.Writer
	res  Result
}

func (r *runner) path(p string) string { return filepath.Join(r.opts.Root, p) }

func (r *runner) logf(format string, args ...any) {
	if r.out == nil {
		return
	}
	// Progress output is best-effort; a failing writer must not abort setup.
	_, _ = fmt.Fprintf(r.out, format+"\n", args...)
}

// Run executes the bootstrap. It is idempotent: re-running against an already
// prepared host validates and normalizes rather than clobbering.
func Run(opts Options, deps Deps) (*Result, error) {
	if deps.Users == nil || deps.Chown == nil {
		return nil, errors.New("bootstrap: Users and Chown dependencies are required")
	}
	r := &runner{opts: opts, deps: deps, out: opts.Out}

	accessGroup, err := r.resolveAccessGroup()
	if err != nil {
		return nil, err
	}
	r.res.AccessGroup = accessGroup
	r.logf("Using socket access group: %s", accessGroup)

	if err := r.ensureServiceUser(); err != nil {
		return nil, err
	}
	if err := r.createDirectories(); err != nil {
		return nil, err
	}

	account, err := r.buildAccount()
	if err != nil {
		return nil, err
	}
	if account == nil {
		if err := r.ensureBoundaryConfig(); err != nil {
			return nil, err
		}
		if err := r.ensureBoundarySecrets(); err != nil {
			return nil, err
		}
	} else if err := r.ensureAccount(account); err != nil {
		return nil, err
	}
	if err := r.handleUnits(accessGroup); err != nil {
		return nil, err
	}

	r.logf("init complete: local boundary prepared without exposing credentials")
	return &r.res, nil
}

func (r *runner) resolveAccessGroup() (string, error) {
	if g := strings.TrimSpace(r.opts.AccessGroup); g != "" {
		return g, nil
	}
	if u := strings.TrimSpace(r.opts.InvokingUser); u != "" {
		g, err := r.deps.Users.PrimaryGroupName(u)
		if err != nil {
			return "", fmt.Errorf("derive access group from user %q: %w", u, err)
		}
		// Deriving "root" means init was run directly as root rather than via
		// sudo as a human; silently using it would yield a root-only socket that
		// no normal user (or agent) can reach. Refuse and make the operator
		// choose. An explicit --access-group is honored above, even if "root".
		if g == "root" {
			return "", fmt.Errorf("refusing to derive a root socket access group from user %q: run via sudo as a normal user, or pass --access-group explicitly", u)
		}
		if g != "" {
			return g, nil
		}
	}
	return "", errors.New("no socket access group: pass --access-group or run via sudo so the invoking user's primary group can be used")
}

func (r *runner) ensureServiceUser() error {
	exists, err := r.deps.Users.UserExists(layout.ServiceUser)
	if err != nil {
		return fmt.Errorf("check service user: %w", err)
	}
	if exists {
		if err := r.verifyDedicatedServiceUser(); err != nil {
			return err
		}
		r.logf("Service user %q already present", layout.ServiceUser)
		return nil
	}
	if err := r.deps.Users.EnsureSystemUser(layout.ServiceUser, layout.ServiceHome, layout.ServiceShell); err != nil {
		return fmt.Errorf("create service user: %w", err)
	}
	r.res.ServiceUserCreated = true
	r.logf("Created system user %q", layout.ServiceUser)
	return nil
}

// systemUIDMax is the upper bound (exclusive) of the conventional system-account
// uid range on Debian/Ubuntu (SYS_UID_MAX = 999). An account at or above it is a
// regular interactive user, not a dedicated service account.
const systemUIDMax = 1000

// verifyDedicatedServiceUser refuses to adopt a pre-existing "ksuite-mail"
// account that is not a dedicated system user, because init would otherwise
// chown the credential file and cache to an interactive account that could then
// read daemon secrets and cached mail (PR #7 review, Codex P2).
func (r *runner) verifyDedicatedServiceUser() error {
	info, err := r.deps.Users.LookupUser(layout.ServiceUser)
	if err != nil {
		return fmt.Errorf("inspect existing service user %q: %w", layout.ServiceUser, err)
	}
	if info.UID >= systemUIDMax {
		return fmt.Errorf("existing user %q (uid %d) is not a dedicated system account; refusing to grant it the credential boundary. Remove or rename it, or use a different service user", layout.ServiceUser, info.UID)
	}
	if info.HomeDir != "" && info.HomeDir != layout.ServiceHome {
		return fmt.Errorf("existing user %q has home %q, not the dedicated service home %q; refusing to grant it the credential boundary", layout.ServiceUser, info.HomeDir, layout.ServiceHome)
	}
	return nil
}

func (r *runner) createDirectories() error {
	for _, d := range layout.Dirs() {
		p := r.path(d.Path)
		if err := os.MkdirAll(p, d.Mode); err != nil {
			return fmt.Errorf("create dir %s: %w", d.Path, err)
		}
		// MkdirAll honors umask; set the exact mode explicitly.
		if err := os.Chmod(p, d.Mode); err != nil {
			return fmt.Errorf("chmod dir %s: %w", d.Path, err)
		}
		if err := r.deps.Chown.Chown(p, d.Owner); err != nil {
			return fmt.Errorf("chown dir %s: %w", d.Path, err)
		}
		r.logf("Directory %s mode %#o owner %s:%s", d.Path, d.Mode, d.Owner.User, d.Owner.Group)
	}
	return nil
}

// buildAccount validates the optional seed before any prompting so a bad
// account fails fast without touching the terminal.
func (r *runner) buildAccount() (*config.Account, error) {
	if r.opts.Account == nil {
		return nil, nil
	}
	s := r.opts.Account
	tls := s.TLS
	acct := config.Account{
		ID:          s.ID,
		Email:       s.Email,
		Host:        s.Host,
		Port:        s.Port,
		TLS:         &tls,
		Username:    s.Username,
		PasswordRef: config.PasswordRef{Source: "file", Provider: "local", ID: secretKey(s.ID)},
		Policy:      s.Policy,
		Domains:     s.Domains,
		Folders:     s.Folders,
	}
	probe := &config.Config{Mail: config.Mail{Accounts: []config.Account{acct}}}
	if err := config.Validate(probe); err != nil {
		return nil, fmt.Errorf("invalid account seed: %w", err)
	}
	return &acct, nil
}

// ensureBoundaryConfig seeds or validates config.toml when no account is being
// added. A re-run that merely validates an existing config must not clobber
// operator comments or formatting, so it is left byte-for-byte untouched.
func (r *runner) ensureBoundaryConfig() error {
	spec := layout.ConfigFileSpec()
	p := r.path(spec.Path)

	_, existed, err := r.loadOrSeedConfig(p)
	if err != nil {
		return err
	}
	if !existed {
		// Pristine starter document with the documented commented example.
		data, err := config.StarterDocument()
		if err != nil {
			return err
		}
		if err := writeFileAtomic(p, data, spec.Mode); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
	}
	if err := r.normalizePerms(spec); err != nil {
		return err
	}
	r.res.ConfigCreated = !existed
	r.logf("Config %s mode %#o owner %s:%s (%s)", spec.Path, spec.Mode, spec.Owner.User, spec.Owner.Group, createdOrValidated(existed))
	return nil
}

// ensureBoundarySecrets initializes or validates the (possibly empty) secrets
// store when no account is being added.
func (r *runner) ensureBoundarySecrets() error {
	spec := layout.SecretsFileSpec()
	p := r.path(spec.Path)

	store, existed, err := r.loadOrInitSecrets(p)
	if err != nil {
		return err
	}
	if !existed {
		if err := writeSecretStore(p, store, spec.Mode); err != nil {
			return fmt.Errorf("write secrets: %w", err)
		}
	}
	if err := r.normalizePerms(spec); err != nil {
		return err
	}
	r.res.SecretsCreated = !existed
	r.logf("Secrets %s mode %#o owner %s:%s (%s)", spec.Path, spec.Mode, spec.Owner.User, spec.Owner.Group, createdOrValidated(existed))
	return nil
}

// ensureAccount adds one account and its credential as a single transactional
// unit. The credential is acquired BEFORE anything is persisted, so a failed or
// cancelled prompt never leaves a configured account whose secret is missing.
// The secret is then written before the config entry, which makes a retry after
// a partial previous run recover the missing half instead of dead-locking on an
// "already present" check (PR #7 review, P2).
func (r *runner) ensureAccount(account *config.Account) error {
	cfgSpec := layout.ConfigFileSpec()
	secSpec := layout.SecretsFileSpec()
	cfgPath := r.path(cfgSpec.Path)
	secPath := r.path(secSpec.Path)

	cfg, cfgExisted, err := r.loadOrSeedConfig(cfgPath)
	if err != nil {
		return err
	}
	store, secExisted, err := r.loadOrInitSecrets(secPath)
	if err != nil {
		return err
	}

	key := account.PasswordRef.ID
	accountInConfig := false
	for i := range cfg.Mail.Accounts {
		if cfg.Mail.Accounts[i].ID == account.ID {
			accountInConfig = true
			break
		}
	}
	_, secretInStore := store.Secrets[key]
	if accountInConfig && secretInStore {
		return fmt.Errorf("account %q already present", account.ID)
	}

	if r.deps.Prompt == nil {
		return errors.New("an account was provided but no interactive prompter is available")
	}
	secret, err := r.deps.Prompt.PromptSecret(fmt.Sprintf("Mailbox password for %s", account.Email))
	if err != nil {
		return fmt.Errorf("read credential: %w", err)
	}
	if len(secret) == 0 {
		return errors.New("empty password: refusing to store a credential that cannot authenticate")
	}
	store.Secrets[key] = string(secret)
	// Best-effort scrub of the prompt buffer. The string copy above still lives
	// on the heap until garbage-collected; Go offers no way to pin and wipe it,
	// so the file mode (0600, service-owned) remains the real protection.
	for i := range secret {
		secret[i] = 0
	}

	// Persist the secret first (recovery ordering, see doc comment).
	if err := writeSecretStore(secPath, store, secSpec.Mode); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	if err := r.normalizePerms(secSpec); err != nil {
		return err
	}
	r.res.SecretsCreated = !secExisted

	if !accountInConfig {
		cfg.Mail.Accounts = append(cfg.Mail.Accounts, *account)
		if err := config.Validate(cfg); err != nil {
			return fmt.Errorf("config invalid after adding account: %w", err)
		}
	}
	data, err := config.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(cfgPath, data, cfgSpec.Mode); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := r.normalizePerms(cfgSpec); err != nil {
		return err
	}
	r.res.ConfigCreated = !cfgExisted
	r.res.AccountAdded = account.ID

	r.logf("Config %s mode %#o owner %s:%s (account %q added)", cfgSpec.Path, cfgSpec.Mode, cfgSpec.Owner.User, cfgSpec.Owner.Group, account.ID)
	r.logf("Secrets %s mode %#o owner %s:%s (credential stored)", secSpec.Path, secSpec.Mode, secSpec.Owner.User, secSpec.Owner.Group)
	return nil
}

// normalizePerms applies the exact mode and ownership for a boundary file. It is
// always run, even on a validating re-run, so drift is corrected.
func (r *runner) normalizePerms(spec layout.FileSpec) error {
	p := r.path(spec.Path)
	if err := os.Chmod(p, spec.Mode); err != nil {
		return fmt.Errorf("chmod %s: %w", spec.Path, err)
	}
	if err := r.deps.Chown.Chown(p, spec.Owner); err != nil {
		return fmt.Errorf("chown %s: %w", spec.Path, err)
	}
	return nil
}

func (r *runner) loadOrSeedConfig(p string) (cfg *config.Config, existed bool, err error) {
	f, err := os.Open(p) //nolint:gosec // p is the fixed config location
	switch {
	case err == nil:
		defer func() { _ = f.Close() }()
		cfg, err = config.Load(f)
		if err != nil {
			return nil, true, fmt.Errorf("existing config is invalid: %w", err)
		}
		if err = config.Validate(cfg); err != nil {
			return nil, true, fmt.Errorf("existing config failed validation: %w", err)
		}
		return cfg, true, nil
	case errors.Is(err, os.ErrNotExist):
		return config.Starter(), false, nil
	default:
		return nil, false, fmt.Errorf("open config: %w", err)
	}
}

func (r *runner) loadOrInitSecrets(p string) (store *secretStore, existed bool, err error) {
	store, err = loadSecretStore(p)
	switch {
	case err == nil:
		return store, true, nil
	case errors.Is(err, os.ErrNotExist):
		return newSecretStore(), false, nil
	default:
		return nil, false, err
	}
}

func (r *runner) handleUnits(accessGroup string) error {
	units, err := systemd.Render(accessGroup)
	if err != nil {
		return err
	}

	if r.opts.Units == UnitsInstall {
		dir := r.path(layout.SystemdUnitDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create unit dir: %w", err)
		}
		servicePath := filepath.Join(dir, layout.ServiceUnit)
		socketPath := filepath.Join(dir, layout.SocketUnit)
		if err := writeFileAtomic(servicePath, []byte(units.Service), 0o644); err != nil {
			return fmt.Errorf("write service unit: %w", err)
		}
		if err := writeFileAtomic(socketPath, []byte(units.Socket), 0o644); err != nil {
			return fmt.Errorf("write socket unit: %w", err)
		}
		r.res.UnitsInstalled = true
		r.res.UnitPaths = []string{layout.SystemdUnitDir + "/" + layout.ServiceUnit, layout.SystemdUnitDir + "/" + layout.SocketUnit}
		r.logf("Installed systemd units to %s", layout.SystemdUnitDir)
		r.logf("Enable with: systemctl daemon-reload && systemctl enable --now %s", layout.SocketUnit)
		return nil
	}

	r.logf("\n# --- %s ---\n%s", layout.ServiceUnit, units.Service)
	r.logf("# --- %s ---\n%s", layout.SocketUnit, units.Socket)
	return nil
}

func createdOrValidated(existed bool) string {
	if existed {
		return "validated"
	}
	return "created"
}
