package bootstrap

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/layout"
)

// --- test doubles ---------------------------------------------------------

type fakeUsers struct {
	users        map[string]bool
	groups       map[string]bool
	primaryGroup map[string]string
	created      []string
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{
		users:        map[string]bool{},
		groups:       map[string]bool{"oriol": true, "ksuite-mail": true},
		primaryGroup: map[string]string{"oriol": "oriol"},
	}
}

func (f *fakeUsers) UserExists(name string) (bool, error)  { return f.users[name], nil }
func (f *fakeUsers) GroupExists(name string) (bool, error) { return f.groups[name], nil }
func (f *fakeUsers) EnsureSystemUser(name, _, _ string) error {
	f.users[name] = true
	f.groups[name] = true
	f.created = append(f.created, name)
	return nil
}
func (f *fakeUsers) PrimaryGroupName(user string) (string, error) { return f.primaryGroup[user], nil }

type fakeChowner struct {
	intents map[string]layout.Owner // canonical path -> owner
	root    string
}

func (c *fakeChowner) Chown(path string, owner layout.Owner) error {
	// Record against the canonical (un-rooted) path for stable assertions.
	rel := path
	if c.root != "" {
		rel = "/" + strings.TrimPrefix(strings.TrimPrefix(path, c.root), "/")
	}
	c.intents[filepath.Clean(rel)] = owner
	return nil
}

type fakePrompter struct {
	secret string
	calls  int
}

func (p *fakePrompter) PromptSecret(string) ([]byte, error) {
	p.calls++
	return []byte(p.secret), nil
}

func newDeps(root string, prompter Prompter) (Deps, *fakeChowner, *fakeUsers) {
	users := newFakeUsers()
	chown := &fakeChowner{intents: map[string]layout.Owner{}, root: root}
	return Deps{Users: users, Chown: chown, Prompt: prompter}, chown, users
}

// --- tests ----------------------------------------------------------------

func TestRunPreparesBoundaryWithHardenedModes(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	deps, chown, users := newDeps(root, nil)

	res, err := Run(Options{Root: root, InvokingUser: "oriol", Out: &out}, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !res.ServiceUserCreated || len(users.created) != 1 || users.created[0] != layout.ServiceUser {
		t.Fatalf("service user not created as expected: %+v", users.created)
	}
	if res.AccessGroup != "oriol" {
		t.Fatalf("access group = %q, want oriol", res.AccessGroup)
	}

	// Exact directory modes (umask must not relax them). Under Shape A the
	// runtime socket dir is no longer created by init: the socket lives directly
	// in /run, created by systemd from the .socket unit.
	assertMode(t, filepath.Join(root, layout.ConfigDir), 0o750)
	assertMode(t, filepath.Join(root, layout.StateDir), 0o700)
	if _, err := os.Stat(filepath.Join(root, "run", "ksuite-mail")); !os.IsNotExist(err) {
		t.Errorf("init must not create a /run/ksuite-mail directory under Shape A (err=%v)", err)
	}

	// Exact file modes: secrets readable only by owner, config group-readable.
	assertMode(t, filepath.Join(root, layout.ConfigFile), 0o640)
	assertMode(t, filepath.Join(root, layout.SecretsFile), 0o600)

	// Ownership intents must match the documented boundary (ARCH-DEP-001).
	wantOwners := map[string]layout.Owner{
		layout.ConfigDir:   {User: "root", Group: "ksuite-mail"},
		layout.StateDir:    {User: "ksuite-mail", Group: "ksuite-mail"},
		layout.ConfigFile:  {User: "root", Group: "ksuite-mail"},
		layout.SecretsFile: {User: "ksuite-mail", Group: "root"},
	}
	for path, want := range wantOwners {
		got, ok := chown.intents[path]
		if !ok {
			t.Errorf("no chown recorded for %s", path)
			continue
		}
		if got != want {
			t.Errorf("owner of %s = %s:%s, want %s:%s", path, got.User, got.Group, want.User, want.Group)
		}
	}

	// Cache directory and credential file must not be readable by the socket
	// access group: neither is group-owned by it, and neither grants group read
	// to a non-service group.
	if g := chown.intents[layout.SecretsFile].Group; g == "oriol" {
		t.Errorf("secrets file must not be group-owned by the access group")
	}
	if g := chown.intents[layout.StateDir].Group; g == "oriol" {
		t.Errorf("cache directory must not be group-owned by the access group")
	}
}

func TestRunSeedsAccountAndKeepsSecretOutOfOutput(t *testing.T) {
	root := t.TempDir()
	const secret = "sup3r-s3cr3t-app-pw-Z9x"
	var out bytes.Buffer
	deps, _, _ := newDeps(root, &fakePrompter{secret: secret})

	res, err := Run(Options{
		Root:         root,
		InvokingUser: "oriol",
		Out:          &out,
		Account: &AccountSeed{
			ID:       "rs_info",
			Email:    "info@regenerativ.ch",
			Host:     "mail.infomaniak.com",
			Port:     993,
			TLS:      true,
			Username: "info@regenerativ.ch",
			Policy:   "full",
			Folders:  []string{"INBOX", "Sent"},
		},
	}, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AccountAdded != "rs_info" {
		t.Fatalf("AccountAdded = %q", res.AccountAdded)
	}

	// The secret must live only in the protected secrets file.
	key := secretKey("rs_info")
	store, err := loadSecretStore(filepath.Join(root, layout.SecretsFile))
	if err != nil {
		t.Fatalf("loadSecretStore: %v", err)
	}
	if store.Secrets[key] != secret {
		t.Fatalf("secret not stored under %q", key)
	}
	assertMode(t, filepath.Join(root, layout.SecretsFile), 0o600)

	// Redaction: the secret must never appear in human-facing output.
	if strings.Contains(out.String(), secret) {
		t.Fatalf("secret leaked into init output:\n%s", out.String())
	}

	// The config must reference the secret indirectly, never inline.
	cfgData, err := os.ReadFile(filepath.Join(root, layout.ConfigFile))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(cfgData), secret) {
		t.Fatalf("secret leaked into config file")
	}
	if !strings.Contains(string(cfgData), key) {
		t.Fatalf("config does not contain the expected password_ref id %q", key)
	}
}

func accountSeed() *AccountSeed {
	return &AccountSeed{
		ID:       "rs_info",
		Email:    "info@regenerativ.ch",
		Host:     "mail.infomaniak.com",
		Port:     993,
		TLS:      true,
		Username: "info@regenerativ.ch",
		Policy:   "full",
		Folders:  []string{"INBOX", "Sent"},
	}
}

// An empty credential must be rejected before anything is persisted, and the
// rejection must not leave a half-configured account that blocks a retry with a
// correct password (PR #7 review, P2).
func TestRunRejectsEmptyPasswordAndStaysRetryable(t *testing.T) {
	root := t.TempDir()

	deps, _, _ := newDeps(root, &fakePrompter{secret: ""})
	if _, err := Run(Options{Root: root, InvokingUser: "oriol", Account: accountSeed()}, deps); err == nil {
		t.Fatal("expected error for empty password")
	}

	// The empty entry must not have been written.
	store, err := loadSecretStore(filepath.Join(root, layout.SecretsFile))
	if err == nil {
		if _, ok := store.Secrets[secretKey("rs_info")]; ok {
			t.Fatal("empty password must not be stored")
		}
	}

	// A retry with a real password must succeed.
	deps2, _, _ := newDeps(root, &fakePrompter{secret: "real-password-123"})
	res, err := Run(Options{Root: root, InvokingUser: "oriol", Account: accountSeed()}, deps2)
	if err != nil {
		t.Fatalf("retry after empty password failed: %v", err)
	}
	if res.AccountAdded != "rs_info" {
		t.Fatalf("AccountAdded = %q after retry", res.AccountAdded)
	}
	store, err = loadSecretStore(filepath.Join(root, layout.SecretsFile))
	if err != nil {
		t.Fatalf("loadSecretStore: %v", err)
	}
	if store.Secrets[secretKey("rs_info")] != "real-password-123" {
		t.Fatal("retry did not store the credential")
	}
}

// A retry after a partial run that wrote the account to config but not the
// secret (e.g. an interrupted previous run) must complete the missing secret,
// not dead-lock on an "already present" check (PR #7 review, P2).
func TestRunRecoversFromMissingSecret(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, &fakePrompter{secret: "first-pw"})
	if _, err := Run(Options{Root: root, InvokingUser: "oriol", Account: accountSeed()}, deps); err != nil {
		t.Fatalf("initial Run: %v", err)
	}

	// Simulate a partial state: the account is in config, but its secret is gone.
	if err := writeSecretStore(filepath.Join(root, layout.SecretsFile), newSecretStore(), 0o600); err != nil {
		t.Fatalf("reset secrets: %v", err)
	}

	deps2, _, _ := newDeps(root, &fakePrompter{secret: "recovered-pw"})
	if _, err := Run(Options{Root: root, InvokingUser: "oriol", Account: accountSeed()}, deps2); err != nil {
		t.Fatalf("recovery Run failed: %v", err)
	}
	store, err := loadSecretStore(filepath.Join(root, layout.SecretsFile))
	if err != nil {
		t.Fatalf("loadSecretStore: %v", err)
	}
	if store.Secrets[secretKey("rs_info")] != "recovered-pw" {
		t.Fatal("recovery did not store the credential")
	}
}

// A fully-configured account (both config entry and secret present) must be
// rejected on re-add so an operator cannot silently overwrite a credential.
func TestRunRejectsFullyConfiguredAccount(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, &fakePrompter{secret: "pw"})
	if _, err := Run(Options{Root: root, InvokingUser: "oriol", Account: accountSeed()}, deps); err != nil {
		t.Fatalf("initial Run: %v", err)
	}
	deps2, _, _ := newDeps(root, &fakePrompter{secret: "pw"})
	_, err := Run(Options{Root: root, InvokingUser: "oriol", Account: accountSeed()}, deps2)
	if err == nil || !strings.Contains(err.Error(), "already present") {
		t.Fatalf("expected 'already present' error, got %v", err)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	if _, err := Run(Options{Root: root, InvokingUser: "oriol"}, deps); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	cfgPath := filepath.Join(root, layout.ConfigFile)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	// Second run validates the existing boundary instead of failing.
	deps2, _, _ := newDeps(root, nil)
	res, err := Run(Options{Root: root, InvokingUser: "oriol"}, deps2)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.ConfigCreated || res.SecretsCreated {
		t.Fatalf("second run should validate, not recreate: %+v", res)
	}

	// A validating re-run must not clobber the existing config (e.g. operator
	// comments in the starter document).
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("validating re-run modified config.toml")
	}
}

func TestRunInstallsUnits(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	res, err := Run(Options{Root: root, InvokingUser: "oriol", Units: UnitsInstall}, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.UnitsInstalled || len(res.UnitPaths) != 2 {
		t.Fatalf("units not installed: %+v", res)
	}
	svc, err := os.ReadFile(filepath.Join(root, layout.SystemdUnitDir, layout.ServiceUnit))
	if err != nil {
		t.Fatalf("read service unit: %v", err)
	}
	if !strings.Contains(string(svc), "User=ksuite-mail") {
		t.Fatalf("installed service unit missing hardened User directive")
	}
}

func TestRunRequiresAccessGroupResolution(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	// No AccessGroup and no InvokingUser -> cannot resolve.
	if _, err := Run(Options{Root: root}, deps); err == nil {
		t.Fatal("expected error when access group cannot be resolved")
	}
}

// Running outside sudo (e.g. directly in a root shell) makes the invoking user
// "root", whose primary group is "root". Deriving the socket access group from
// that would silently produce a root-only socket that no normal user can reach.
// init must refuse and tell the operator to pass --access-group (PR #7, B1).
func TestRunRejectsRootDerivedAccessGroup(t *testing.T) {
	root := t.TempDir()
	users := newFakeUsers()
	users.primaryGroup["root"] = "root"
	deps := Deps{Users: users, Chown: &fakeChowner{intents: map[string]layout.Owner{}, root: root}}

	_, err := Run(Options{Root: root, InvokingUser: "root"}, deps)
	if err == nil || !strings.Contains(err.Error(), "access group") {
		t.Fatalf("expected refusal to derive root access group, got %v", err)
	}
}

// An operator who explicitly opts into a root access group is taken at their
// word (the derivation guard only fires on the implicit default).
func TestRunAllowsExplicitRootAccessGroup(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	res, err := Run(Options{Root: root, AccessGroup: "root", InvokingUser: "oriol"}, deps)
	if err != nil {
		t.Fatalf("explicit --access-group root should be allowed: %v", err)
	}
	if res.AccessGroup != "root" {
		t.Fatalf("AccessGroup = %q, want root", res.AccessGroup)
	}
}

func TestRunRejectsInvalidAccountBeforePrompting(t *testing.T) {
	root := t.TempDir()
	prompter := &fakePrompter{secret: "x"}
	deps, _, _ := newDeps(root, prompter)
	_, err := Run(Options{
		Root:         root,
		InvokingUser: "oriol",
		Account:      &AccountSeed{ID: "bad", Policy: "domain"}, // missing fields + no domains
	}, deps)
	if err == nil {
		t.Fatal("expected validation error for bad account seed")
	}
	if prompter.calls != 0 {
		t.Fatalf("prompter must not be called for an invalid account; calls=%d", prompter.calls)
	}
}

// --- helpers --------------------------------------------------------------

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %#o, want %#o", path, got, want)
	}
}
