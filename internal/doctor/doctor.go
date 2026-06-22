// Package doctor runs the daemon-side setup diagnostics behind POST /v1/doctor
// (UC-007, ARCH-RUN-004). Every check resolves config and credentials
// daemon-side and returns only safe, content-free outcomes: it confirms a
// credential is present without ever reading or echoing its value
// (NFR-SEC-002, NFR-SEC-005, NFR-ERR-002).
//
// This slice performs no network I/O. Live IMAP connectivity is verified by the
// fixed provider probe in a later slice, so the connectivity check is reported
// as skipped rather than dialed; this keeps doctor hermetic for CI and tests
// (AGENTS.md security rules).
package doctor

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/config"
	"github.com/dothackerman/ksuite-mail/internal/secrets"
)

// Options points doctor at a concrete deployment. Defaults come from the layout
// package; tests override them with temp paths.
type Options struct {
	ConfigPath  string
	SecretsPath string
	StateDir    string
}

// Run executes every check and returns an aggregate report. The report is OK
// only when no check failed; skipped and warning checks do not fail it.
func Run(opts Options) api.DoctorReport {
	var checks []api.Check
	add := func(c api.Check) { checks = append(checks, c) }

	cfg, cfgOK := checkConfig(opts.ConfigPath, add)
	store, storeOK := checkSecrets(opts.SecretsPath, add)
	checkCredentials(cfg, cfgOK, store, storeOK, add)
	checkCache(opts.StateDir, add)
	add(api.Check{
		Name:   "imap_connectivity",
		Status: api.CheckSkip,
		Code:   "deferred_to_probe",
		Detail: "live IMAP connectivity is verified by the provider probe in a later slice",
	})

	ok := true
	for _, c := range checks {
		if c.Status == api.CheckFail {
			ok = false
			break
		}
	}
	return api.DoctorReport{OK: ok, Checks: checks}
}

// checkConfig parses and validates config.toml, emitting config_parse and
// config_validate checks. It returns the parsed config and whether parsing
// succeeded so credential checks know if account definitions are trustworthy.
func checkConfig(path string, add func(api.Check)) (*config.Config, bool) {
	f, err := os.Open(path) //nolint:gosec // path is the fixed config location
	if err != nil {
		code := "config_unreadable"
		if errors.Is(err, os.ErrNotExist) {
			code = "config_missing"
		}
		add(api.Check{Name: "config_parse", Status: api.CheckFail, Code: code, Detail: "could not open config.toml"})
		add(api.Check{Name: "config_validate", Status: api.CheckSkip, Code: "prerequisite_failed"})
		return nil, false
	}
	defer func() { _ = f.Close() }()

	cfg, err := config.Load(f)
	if err != nil {
		add(api.Check{Name: "config_parse", Status: api.CheckFail, Code: "config_parse_error", Detail: err.Error()})
		add(api.Check{Name: "config_validate", Status: api.CheckSkip, Code: "prerequisite_failed"})
		return nil, false
	}
	add(api.Check{Name: "config_parse", Status: api.CheckPass})

	if err := config.Validate(cfg); err != nil {
		add(api.Check{Name: "config_validate", Status: api.CheckFail, Code: "config_invalid", Detail: err.Error()})
		return cfg, false
	}
	add(api.Check{Name: "config_validate", Status: api.CheckPass})
	return cfg, true
}

// checkSecrets loads the credential file daemon-side and maps any failure to a
// stable, content-free code.
func checkSecrets(path string, add func(api.Check)) (*secrets.Store, bool) {
	store, err := secrets.Load(path)
	if err != nil {
		add(api.Check{Name: "secrets_file", Status: api.CheckFail, Code: secretsErrorCode(err), Detail: "secrets file is unusable"})
		return nil, false
	}
	add(api.Check{Name: "secrets_file", Status: api.CheckPass})
	return store, true
}

func secretsErrorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "secrets_missing"
	case errors.Is(err, secrets.ErrUnsafePermissions):
		return "secrets_unsafe_permissions"
	case errors.Is(err, secrets.ErrUnsupportedVersion):
		return "secrets_bad_version"
	case errors.Is(err, secrets.ErrMalformed):
		return "secrets_malformed"
	default:
		return "secrets_unreadable"
	}
}

// checkCredentials confirms each configured account resolves to a stored secret
// daemon-side, by id only. It never reads the secret value.
func checkCredentials(cfg *config.Config, cfgOK bool, store *secrets.Store, storeOK bool, add func(api.Check)) {
	if !cfgOK || !storeOK {
		add(api.Check{Name: "credentials", Status: api.CheckSkip, Code: "prerequisite_failed"})
		return
	}
	if len(cfg.Mail.Accounts) == 0 {
		add(api.Check{Name: "credentials", Status: api.CheckSkip, Code: "no_accounts", Detail: "no accounts configured yet"})
		return
	}
	for i := range cfg.Mail.Accounts {
		a := &cfg.Mail.Accounts[i]
		if _, ok := store.Resolve(a.PasswordRef.ID); ok {
			add(api.Check{Name: "credential:" + a.ID, Status: api.CheckPass})
			continue
		}
		add(api.Check{
			Name:   "credential:" + a.ID,
			Status: api.CheckFail,
			Code:   "credential_missing",
			Detail: "no secret stored for this account's password_ref.id",
		})
	}
}

// checkCache confirms the daemon's state directory exists and is writable. The
// SQLite cache itself arrives in a later slice; this verifies its home is
// usable without leaking any path content beyond the operational fact.
func checkCache(stateDir string, add func(api.Check)) {
	info, err := os.Stat(stateDir)
	if err != nil {
		code := "cache_unavailable"
		if errors.Is(err, os.ErrNotExist) {
			code = "cache_dir_missing"
		}
		add(api.Check{Name: "cache", Status: api.CheckFail, Code: code, Detail: "state directory is unavailable"})
		return
	}
	if !info.IsDir() {
		add(api.Check{Name: "cache", Status: api.CheckFail, Code: "cache_not_a_directory", Detail: "state path is not a directory"})
		return
	}
	probe := filepath.Join(stateDir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte{}, 0o600); err != nil {
		add(api.Check{Name: "cache", Status: api.CheckFail, Code: "cache_not_writable", Detail: "state directory is not writable"})
		return
	}
	_ = os.Remove(probe)
	add(api.Check{Name: "cache", Status: api.CheckPass})
}
